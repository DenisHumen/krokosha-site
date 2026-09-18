package githubsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

const (
	outputFile   = "github.json"
	stateFile    = "state.json"
	stateVersion = 1
)

// Options configure one synchronisation run.
type Options struct {
	Content *config.Content
	// OutDir receives github.json and avatar.* (content/generated).
	OutDir string
	// CacheDir keeps the raw GitHub data between runs (state.json, HTTP cache).
	CacheDir string
	// Token is optional: a fine-grained token with read-only access to public repositories.
	// Without it GitHub allows 60 requests per hour, which the per-repository cache makes enough.
	Token string
	// StaleAfter: how old the last successful answer may be before the site admits that
	// GitHub is unavailable (github.json → stale). A single failed run is not worth a warning.
	StaleAfter time.Duration
	// MetricsTTL forces a refresh of repository metrics even if the repository did not change.
	MetricsTTL time.Duration

	BaseURL    string
	HTTPClient *http.Client
	Now        func() time.Time
	Logger     *slog.Logger
}

// State is the on-disk cache: the last successful answer of GitHub.
type State struct {
	Version   int                `json:"version"`
	User      string             `json:"user"`
	FetchedAt time.Time          `json:"fetchedAt"`
	Repos     []Repo             `json:"repos"`
	Metrics   map[string]Metrics `json:"metrics"`
	Avatar    avatarState        `json:"avatar"`
}

// Result summarises a run for the log and for the admin status page.
type Result struct {
	Repos          int
	Public         int
	Fresh          bool // the repository list came from GitHub in this run
	Stale          bool
	SyncedAt       time.Time
	MetricsFetched int
	MetricsMissing []string
	AvatarUpdated  bool
}

type syncer struct {
	opts   Options
	client *client
	log    *slog.Logger
	now    time.Time
}

// ErrNoData means GitHub could not be reached and there is no cached answer either.
var ErrNoData = errors.New("no GitHub data: the first synchronisation has not succeeded yet")

// Run fetches what changed, re-ranks and writes github.json. It fails only when there is
// nothing to write at all; every other problem degrades to the cached data.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.Content == nil || opts.OutDir == "" || opts.CacheDir == "" {
		return nil, errors.New("githubsync: Content, OutDir and CacheDir are required")
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = 24 * time.Hour
	}
	if opts.MetricsTTL <= 0 {
		opts.MetricsTTL = 7 * 24 * time.Hour
	}
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBaseURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}

	s := &syncer{
		opts: opts,
		log:  opts.Logger,
		now:  opts.Now().UTC(),
		client: &client{
			http:     opts.HTTPClient,
			baseURL:  opts.BaseURL,
			token:    opts.Token,
			cacheDir: opts.CacheDir,
			log:      opts.Logger,
		},
	}
	return s.run(ctx)
}

func (s *syncer) run(ctx context.Context) (*Result, error) {
	user := s.opts.Content.Site.GitHub.User
	state := s.loadState(user)
	result := &Result{}

	repos, err := s.client.listRepos(ctx, user)
	switch {
	case err == nil:
		state.Repos = repos
		state.FetchedAt = s.now
		result.Fresh = true
		result.MetricsFetched = s.refreshMetrics(ctx, user, state)
	case ctx.Err() != nil:
		return nil, ctx.Err()
	default:
		s.log.Warn("cannot fetch the repository list, using the cached one", "error", err)
	}

	if updated, err := s.syncAvatar(ctx, state); err != nil {
		s.log.Warn("cannot update the avatar, keeping the previous one", "error", err)
	} else {
		result.AvatarUpdated = updated
	}

	if state.FetchedAt.IsZero() {
		return nil, ErrNoData
	}
	if err := s.saveState(state); err != nil {
		s.log.Warn("cannot save the cache", "error", err)
	}

	projects := Rank(RankInput{
		Repos:   state.Repos,
		Metrics: state.Metrics,
		Config:  s.opts.Content.Projects,
		Now:     s.now,
	})
	output := Output{
		Public:   projects,
		SyncedAt: state.FetchedAt.UTC().Format(time.RFC3339),
		Stale:    s.now.Sub(state.FetchedAt) > s.opts.StaleAfter,
	}
	if err := writeJSON(filepath.Join(s.opts.OutDir, outputFile), output); err != nil {
		return nil, err
	}

	result.Repos = len(state.Repos)
	result.Public = len(projects)
	result.Stale = output.Stale
	result.SyncedAt = state.FetchedAt
	for _, project := range projects {
		if _, ok := state.Metrics[project.Name]; !ok {
			result.MetricsMissing = append(result.MetricsMissing, project.Name)
		}
	}
	return result, nil
}

// refreshMetrics fetches metrics of new and changed repositories and returns how many were fetched.
// When the rate limit hits, the rest keep their old metrics (or none) until the next run.
func (s *syncer) refreshMetrics(ctx context.Context, user string, state *State) int {
	if state.Metrics == nil {
		state.Metrics = map[string]Metrics{}
	}
	known := map[string]bool{}
	excluded := lowerSet(s.opts.Content.Projects.Exclude)
	fetched := 0
	limited := false

	for _, repo := range state.Repos {
		known[repo.Name] = true
		hidden := repo.Fork || repo.Private || excluded[strings.ToLower(repo.Name)] ||
			(repo.Archived && !s.opts.Content.Projects.ShowArchived)
		if hidden {
			continue // never shown, not worth three requests
		}
		revision := repo.PushedAt + "|" + repo.UpdatedAt
		cached, ok := state.Metrics[repo.Name]
		if ok && cached.Revision == revision && s.now.Sub(cached.FetchedAt) < s.opts.MetricsTTL {
			continue
		}
		if limited || ctx.Err() != nil {
			continue
		}

		metrics, err := s.client.fetchMetrics(ctx, user, repo)
		var rateLimit *rateLimitError
		switch {
		case err == nil:
			metrics.Revision = revision
			metrics.FetchedAt = s.now
			state.Metrics[repo.Name] = metrics
			fetched++
			if !ok && repo.Description == "" {
				s.log.Info("repository has no description, using the README", "repo", repo.Name, "found", metrics.ReadmeExcerpt != "")
			}
		case errors.As(err, &rateLimit):
			limited = true
			s.log.Warn("rate limit reached, the remaining repositories keep their previous metrics; "+
				"set GITHUB_TOKEN to raise the limit", "error", err)
		default:
			s.log.Warn("cannot fetch repository metrics", "repo", repo.Name, "error", err)
		}
	}

	for name := range state.Metrics {
		if !known[name] {
			delete(state.Metrics, name) // repository was deleted, renamed or made private
		}
	}
	return fetched
}

func (s *syncer) loadState(user string) *State {
	fresh := &State{Version: stateVersion, User: user, Metrics: map[string]Metrics{}}
	data, err := os.ReadFile(filepath.Join(s.opts.CacheDir, stateFile))
	if err != nil {
		return fresh
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil || state.Version != stateVersion || state.User != user {
		s.log.Warn("ignoring an unreadable or foreign cache", "file", stateFile)
		return fresh
	}
	if state.Metrics == nil {
		state.Metrics = map[string]Metrics{}
	}
	return &state
}

func (s *syncer) saveState(state *State) error {
	return writeJSON(filepath.Join(s.opts.CacheDir, stateFile), state)
}

func writeJSON(path string, value any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return writeFileAtomic(path, buffer.Bytes())
}
