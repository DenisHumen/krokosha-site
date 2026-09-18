package githubsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

// fakeGitHub imitates the few endpoints of api.github.com the sync uses, plus the avatar host.
type fakeGitHub struct {
	t      *testing.T
	server *httptest.Server

	mu         sync.Mutex
	repos      []map[string]any
	readmes    map[string]string // repository → markdown
	commits    map[string]int
	releases   map[string]int
	avatar     []byte
	avatarETag string
	down       bool           // every request fails with 503
	limitAfter int            // > 0: rate-limit API requests after this many
	apiCalls   int            // API requests since the last reset
	hits       map[string]int // path → requests
	tokens     map[string]bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	fake := &fakeGitHub{
		t:        t,
		readmes:  map[string]string{},
		commits:  map[string]int{},
		releases: map[string]int{},
		hits:     map[string]int{},
		tokens:   map[string]bool{},
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeGitHub) addRepo(name string, fields map[string]any) {
	item := map[string]any{
		"name":             name,
		"description":      nil,
		"fork":             false,
		"private":          false,
		"archived":         false,
		"language":         "Go",
		"topics":           []string{},
		"stargazers_count": 0,
		"pushed_at":        "2026-09-01T10:00:00Z",
		"updated_at":       "2026-09-01T10:00:00Z",
		"homepage":         nil,
		"html_url":         "https://github.com/someone/" + name,
		"default_branch":   "main",
		"license":          nil,
	}
	for key, value := range fields {
		item[key] = value
	}
	f.repos = append(f.repos, item)
}

func (f *fakeGitHub) resetCounters() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits = map[string]int{}
	f.apiCalls = 0
}

func (f *fakeGitHub) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for path, n := range f.hits {
		if strings.HasPrefix(path, prefix) {
			total += n
		}
	}
	return total
}

func (f *fakeGitHub) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits[r.URL.Path]++
	f.tokens[r.Header.Get("Authorization")] = true

	if f.down {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/avatar.png" {
		if f.avatarETag != "" && r.Header.Get("If-None-Match") == f.avatarETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", f.avatarETag)
		_, _ = w.Write(f.avatar)
		return
	}

	f.apiCalls++
	if f.limitAfter > 0 && f.apiCalls > f.limitAfter {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case len(parts) == 3 && parts[0] == "users" && parts[2] == "repos":
		body, _ := json.Marshal(f.repos)
		etag := fmt.Sprintf(`"%x"`, sha256.Sum256(body))
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = w.Write(body)
	case len(parts) == 4 && parts[0] == "repos" && parts[3] == "readme":
		markdown, ok := f.readmes[parts[2]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"size":     len(markdown),
			"encoding": "base64",
			"content":  base64.StdEncoding.EncodeToString([]byte(markdown)),
		})
	case len(parts) == 4 && parts[0] == "repos" && (parts[3] == "commits" || parts[3] == "releases"):
		total := f.commits[parts[2]]
		if parts[3] == "releases" {
			total = f.releases[parts[2]]
		}
		if total > 1 {
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?per_page=1&page=2>; rel="next", <%s%s?per_page=1&page=%d>; rel="last"`,
				f.server.URL, r.URL.Path, f.server.URL, r.URL.Path, total))
		}
		items := make([]map[string]any, min(total, 1))
		for i := range items {
			items[i] = map[string]any{"id": i}
		}
		_ = json.NewEncoder(w).Encode(items)
	default:
		f.t.Errorf("unexpected request: %s", r.URL)
		http.NotFound(w, r)
	}
}

func testPNG(t *testing.T, side int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type harness struct {
	fake *fakeGitHub
	opts Options
	now  time.Time
}

func newHarness(t *testing.T) *harness {
	fake := newFakeGitHub(t)
	fake.avatar = testPNG(t, 128)
	fake.avatarETag = `"avatar-1"`

	content := &config.Content{}
	content.Site.GitHub.User = "someone"
	// The https:// requirement is lifted for the test server below.
	content.Site.Profile.AvatarSource = ""
	content.Projects.ShowArchived = true
	content.Projects.HomepageIgnoreHosts = []string{"t.me", "github.com"}

	dir := t.TempDir()
	h := &harness{fake: fake, now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	h.opts = Options{
		Content:    content,
		OutDir:     filepath.Join(dir, "generated"),
		CacheDir:   filepath.Join(dir, "cache"),
		BaseURL:    fake.server.URL,
		HTTPClient: fake.server.Client(),
		Now:        func() time.Time { return h.now },
	}
	return h
}

func (h *harness) run(t *testing.T) (*Result, Output) {
	t.Helper()
	result, err := Run(context.Background(), h.opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result, h.output(t)
}

func (h *harness) output(t *testing.T) Output {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.opts.OutDir, outputFile))
	if err != nil {
		t.Fatal(err)
	}
	var output Output
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func TestRunWritesRankedProjects(t *testing.T) {
	h := newHarness(t)
	h.fake.addRepo("balancer", map[string]any{
		"description": "Traffic balancing across providers", "language": "Rust", "stargazers_count": 3,
		"topics": []string{"networking"}, "homepage": "https://balancer.example.com",
		"license": map[string]any{"spdx_id": "MIT", "key": "mit"},
	})
	h.fake.addRepo("notes", map[string]any{"language": nil})
	h.fake.addRepo("a-fork", map[string]any{"fork": true, "description": "Forked project"})
	h.fake.addRepo("Someone", map[string]any{"description": "Profile README"})
	h.opts.Content.Projects.Exclude = []string{"someone"}
	h.fake.readmes["balancer"] = "# balancer\n\n" + strings.Repeat("Documentation line.\n", 700)
	h.fake.readmes["notes"] = "# notes\n\nPersonal notes about network equipment configuration and troubleshooting.\n"
	h.fake.commits["balancer"] = 240
	h.fake.commits["notes"] = 1
	h.fake.releases["balancer"] = 6
	h.opts.Token = "secret-token"

	result, output := h.run(t)

	if !result.Fresh || result.Stale || result.Repos != 4 || result.Public != 2 || result.MetricsFetched != 2 {
		t.Errorf("result = %+v", result)
	}
	if output.SyncedAt != "2026-09-18T12:00:00Z" || output.Stale {
		t.Errorf("syncedAt=%q stale=%v", output.SyncedAt, output.Stale)
	}
	if got := names(output.Public); len(got) != 2 || got[0] != "balancer" || got[1] != "notes" {
		t.Fatalf("projects = %v", got)
	}

	balancer, notes := output.Public[0], output.Public[1]
	if balancer.Tier != TierFeatured || *balancer.LanguageColor != "#dea584" || *balancer.Homepage != "https://balancer.example.com" {
		t.Errorf("balancer = %+v", balancer)
	}
	if notes.Language != nil || notes.LanguageColor != nil {
		t.Errorf("notes language = %v / %v, want null", notes.Language, notes.LanguageColor)
	}
	if notes.Description == nil || !strings.HasPrefix(*notes.Description, "Personal notes about network equipment") {
		t.Errorf("notes description must come from the README, got %v", notes.Description)
	}

	if h.fake.count("/repos/someone/a-fork") != 0 || h.fake.count("/repos/someone/someone") != 0 {
		t.Error("metrics of a fork or an excluded repository were requested: they are never shown")
	}
	if !h.fake.tokens["Bearer secret-token"] {
		t.Error("the token was not sent")
	}
}

func TestRunUsesCacheWhenNothingChanged(t *testing.T) {
	h := newHarness(t)
	h.fake.addRepo("one", map[string]any{"description": "First repository"})
	h.fake.addRepo("two", map[string]any{"description": "Second repository"})
	h.run(t)

	h.fake.resetCounters()
	h.now = h.now.Add(6 * time.Hour)
	result, output := h.run(t)

	if result.MetricsFetched != 0 || h.fake.count("/repos/") != 0 {
		t.Errorf("unchanged repositories were fetched again: %d metric requests", h.fake.count("/repos/"))
	}
	if h.fake.count("/users/someone/repos") != 1 {
		t.Errorf("list requests = %d, want 1 (conditional)", h.fake.count("/users/someone/repos"))
	}
	// A 304 still proves GitHub is reachable: the data is fresh as of now.
	if !result.Fresh || output.SyncedAt != "2026-09-18T18:00:00Z" {
		t.Errorf("fresh=%v syncedAt=%q", result.Fresh, output.SyncedAt)
	}

	// A push changes the revision: only that repository is fetched again.
	h.fake.repos[1]["pushed_at"] = "2026-09-18T17:00:00Z"
	h.fake.resetCounters()
	result, _ = h.run(t)
	if result.MetricsFetched != 1 || h.fake.count("/repos/someone/two") != 3 || h.fake.count("/repos/someone/one") != 0 {
		t.Errorf("after a push: fetched=%d, requests two=%d one=%d", result.MetricsFetched,
			h.fake.count("/repos/someone/two"), h.fake.count("/repos/someone/one"))
	}

	// Metrics are refreshed from time to time even without a push (a release can be added without one).
	h.now = h.now.Add(8 * 24 * time.Hour)
	result, _ = h.run(t)
	if result.MetricsFetched != 2 {
		t.Errorf("after the metrics TTL: fetched=%d, want 2", result.MetricsFetched)
	}
}

func TestRunSurvivesGitHubOutage(t *testing.T) {
	h := newHarness(t)
	h.fake.addRepo("one", map[string]any{"description": "First repository"})
	h.run(t)

	h.fake.down = true
	h.opts.HTTPClient.Timeout = 5 * time.Second
	noSleep(t)

	h.now = h.now.Add(6 * time.Hour)
	result, output := h.run(t)
	if result.Fresh || result.Stale || output.Stale {
		t.Errorf("6 hours of outage: fresh=%v stale=%v, want cached and not yet stale", result.Fresh, output.Stale)
	}
	if output.SyncedAt != "2026-09-18T12:00:00Z" || len(output.Public) != 1 {
		t.Errorf("output = %+v", output)
	}

	h.now = h.now.Add(24 * time.Hour)
	_, output = h.run(t)
	if !output.Stale {
		t.Error("30 hours of outage: want stale=true so that the site can say so")
	}

	// Pins from content/projects.yaml apply even while GitHub is down: ranking needs no network.
	h.opts.Content.Projects.Pinned = []string{"one"}
	_, output = h.run(t)
	if !output.Public[0].Pinned || output.Public[0].Tier != TierFeatured {
		t.Errorf("pin was not applied offline: %+v", output.Public[0])
	}

	h.fake.down = false
	_, output = h.run(t)
	if output.Stale {
		t.Error("GitHub is back: stale must be cleared")
	}
}

func TestRunWithoutAnyDataFails(t *testing.T) {
	h := newHarness(t)
	h.fake.down = true
	noSleep(t)

	_, err := Run(context.Background(), h.opts)
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
	if _, statErr := os.Stat(filepath.Join(h.opts.OutDir, outputFile)); !os.IsNotExist(statErr) {
		t.Error("github.json must not be written without data: the site build falls back to its snapshot")
	}
}

func TestRunRateLimitKeepsPartialResult(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"a", "b", "c"} {
		h.fake.addRepo(name, map[string]any{"description": "Repository " + name})
		h.fake.commits[name] = 300
	}
	h.fake.limitAfter = 1 + 3 // the list and the metrics of exactly one repository

	result, output := h.run(t)
	if result.MetricsFetched != 1 || len(result.MetricsMissing) != 2 {
		t.Errorf("fetched=%d missing=%v, want 1 and 2", result.MetricsFetched, result.MetricsMissing)
	}
	if len(output.Public) != 3 {
		t.Errorf("every repository must still be listed, got %d", len(output.Public))
	}
	// After the limit hit, no more requests were wasted.
	if calls := h.fake.count("/repos/"); calls > 4 {
		t.Errorf("%d metric requests, want the sync to stop at the first refusal", calls)
	}

	h.fake.limitAfter = 0
	result, _ = h.run(t)
	if result.MetricsFetched != 2 || len(result.MetricsMissing) != 0 {
		t.Errorf("next run: fetched=%d missing=%v, want the rest to be completed", result.MetricsFetched, result.MetricsMissing)
	}
}

func TestSyncAvatar(t *testing.T) {
	h := newHarness(t)
	h.fake.addRepo("one", nil)
	s := &syncer{opts: h.opts, log: discardLogger(), client: &client{http: h.fake.server.Client()}}
	avatarURL := h.fake.server.URL + "/avatar.png"
	state := &State{}

	// Only https:// sources are accepted…
	s.opts.Content.Site.Profile.AvatarSource = "http://example.com/a.png"
	if _, err := s.syncAvatar(context.Background(), state); err == nil {
		t.Error("an http:// avatar source must be rejected")
	}
	// …so the test goes one level below the check.
	download := func() (bool, error) { return s.downloadAvatar(context.Background(), state, avatarURL) }

	updated, err := download()
	if err != nil || !updated || state.Avatar.File != "avatar.png" {
		t.Fatalf("first download: updated=%v err=%v state=%+v", updated, err, state.Avatar)
	}
	if !fileExists(filepath.Join(h.opts.OutDir, "avatar.png")) {
		t.Fatal("avatar.png was not written")
	}

	updated, err = download()
	if err != nil || updated {
		t.Errorf("unchanged avatar: updated=%v err=%v, want a 304 and no rewrite", updated, err)
	}

	// A broken download must never replace a good picture.
	h.fake.avatar, h.fake.avatarETag = []byte("<html>error page</html>"), `"avatar-2"`
	if _, err = download(); err == nil {
		t.Error("an HTML page was accepted as an image")
	}
	h.fake.avatar, h.fake.avatarETag = testPNG(t, 16), `"avatar-3"`
	if _, err = download(); err == nil {
		t.Error("a 16×16 image was accepted")
	}
	if state.Avatar.ETag != `"avatar-1"` {
		t.Errorf("state was changed by a failed download: %+v", state.Avatar)
	}

	// A new format replaces the old file: the site build must find exactly one avatar.*.
	if err := os.WriteFile(filepath.Join(h.opts.OutDir, "avatar.jpg"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.fake.avatar, h.fake.avatarETag = testPNG(t, 256), `"avatar-4"`
	if updated, err = download(); err != nil || !updated {
		t.Fatalf("new avatar: updated=%v err=%v", updated, err)
	}
	if fileExists(filepath.Join(h.opts.OutDir, "avatar.jpg")) {
		t.Error("the old avatar.jpg was left behind")
	}
}

func TestLastPage(t *testing.T) {
	cases := map[string]int{
		"": 0,
		`<https://api.github.com/repositories/1/commits?per_page=1&page=2>; rel="next", <https://api.github.com/repositories/1/commits?per_page=1&page=317>; rel="last"`: 317,
		`<https://api.github.com/x?page=2>; rel="next"`:    0,
		`<https://api.github.com/x?page=oops>; rel="last"`: 0,
	}
	for link, want := range cases {
		if got := lastPage(link); got != want {
			t.Errorf("lastPage(%q) = %d, want %d", link, got, want)
		}
	}
}
