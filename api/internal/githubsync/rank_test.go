package githubsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

type snapshot struct {
	Now     time.Time          `json:"now"`
	Repos   []Repo             `json:"repos"`
	Metrics map[string]Metrics `json:"metrics"`
}

func loadSnapshot(t *testing.T) snapshot {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "snapshot-2026-09-18.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func repoRoot(t *testing.T) string {
	t.Helper()
	contentDir, err := config.FindContentDir(".")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(contentDir)
}

// The contract between Go and the rest of the project: for the snapshot the mocks were made from,
// Rank must produce exactly mock/en/projects.json → public (docs/contract.md §5).
func TestRankMatchesContractMock(t *testing.T) {
	snap := loadSnapshot(t)
	root := repoRoot(t)
	content, err := config.LoadContent(filepath.Join(root, "content"))
	if err != nil {
		t.Fatal(err)
	}
	if len(content.Projects.Pinned) > 0 || len(content.Projects.Overrides) > 0 {
		t.Skip("content/projects.yaml now has pins or overrides; the mock snapshot was ranked without them")
	}

	got := Rank(RankInput{Repos: snap.Repos, Metrics: snap.Metrics, Config: content.Projects, Now: snap.Now})

	data, err := os.ReadFile(filepath.Join(root, "mock", "en", "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mock struct {
		Public []Project `json:"public"`
	}
	if err := json.Unmarshal(data, &mock); err != nil {
		t.Fatal(err)
	}

	if len(got) != len(mock.Public) {
		t.Fatalf("got %d projects, mock has %d", len(got), len(mock.Public))
	}
	for i := range got {
		if !reflect.DeepEqual(got[i], mock.Public[i]) {
			gotJSON, _ := json.Marshal(got[i])
			wantJSON, _ := json.Marshal(mock.Public[i])
			t.Errorf("project #%d differs\n got: %s\nwant: %s", i, gotJSON, wantJSON)
		}
	}
}

var testNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func repo(name string, mutate ...func(*Repo)) Repo {
	r := Repo{
		Name:     name,
		URL:      "https://github.com/DenisHumen/" + name,
		PushedAt: "2026-09-01T10:00:00Z",
	}
	for _, m := range mutate {
		m(&r)
	}
	return r
}

// strong is a repository that scores 30+20+10+10 = 70: featured material.
var strong = Metrics{ReadmeSize: 12_000, Commits: 250}

func describe(r *Repo) { r.Description = "A meaningful description" }

func tiers(projects []Project) map[string]string {
	result := map[string]string{}
	for _, p := range projects {
		result[p.Name] = p.Tier
	}
	return result
}

func names(projects []Project) []string {
	result := make([]string, len(projects))
	for i, p := range projects {
		result[i] = p.Name
	}
	return result
}

func TestScoreThresholds(t *testing.T) {
	fresh := repo("x")
	cases := []struct {
		name    string
		repo    Repo
		metrics Metrics
		desc    bool
		home    bool
		want    int
	}{
		{"recent push only", fresh, Metrics{}, false, false, 10},
		{"readme 499 B", fresh, Metrics{ReadmeSize: 499}, false, false, 10},
		{"readme 500 B", fresh, Metrics{ReadmeSize: 500}, false, false, 18},
		{"readme 2 KB", fresh, Metrics{ReadmeSize: 2_000}, false, false, 25},
		{"readme 5 KB", fresh, Metrics{ReadmeSize: 5_000}, false, false, 32},
		{"readme 10 KB", fresh, Metrics{ReadmeSize: 10_000}, false, false, 40},
		{"commits 4", fresh, Metrics{Commits: 4}, false, false, 10},
		{"commits 5", fresh, Metrics{Commits: 5}, false, false, 15},
		{"commits 20", fresh, Metrics{Commits: 20}, false, false, 20},
		{"commits 50", fresh, Metrics{Commits: 50}, false, false, 25},
		{"commits 200", fresh, Metrics{Commits: 200}, false, false, 30},
		{"one release", fresh, Metrics{Releases: 1}, false, false, 18},
		{"five releases", fresh, Metrics{Releases: 5}, false, false, 25},
		{"description and homepage", fresh, Metrics{}, true, true, 25},
		{"license", repo("x", func(r *Repo) { r.License = "MIT" }), Metrics{}, false, false, 15},
		{"topics", repo("x", func(r *Repo) { r.Topics = []string{"networking"} }), Metrics{}, false, false, 15},
		{"stars are capped at 5", repo("x", func(r *Repo) { r.Stars = 40 }), Metrics{}, false, false, 20},
		{"3 stars", repo("x", func(r *Repo) { r.Stars = 3 }), Metrics{}, false, false, 16},
		{"pushed 180 days ago", repo("x", func(r *Repo) { r.PushedAt = "2026-03-22T12:00:00Z" }), Metrics{}, false, false, 10},
		{"pushed 181 days ago", repo("x", func(r *Repo) { r.PushedAt = "2026-03-21T11:00:00Z" }), Metrics{}, false, false, 5},
		{"pushed 366 days ago", repo("x", func(r *Repo) { r.PushedAt = "2025-09-17T11:00:00Z" }), Metrics{}, false, false, 0},
		{"archived", repo("x", func(r *Repo) { r.Archived = true }), Metrics{}, false, false, -10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Score(tc.repo, tc.metrics, tc.desc, tc.home, testNow); got != tc.want {
				t.Errorf("Score = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRankFiltersWhatMustNotBeShown(t *testing.T) {
	repos := []Repo{
		repo("visible", describe),
		repo("DenisHumen", describe), // profile repository, excluded in a different letter case
		repo("a-fork", describe, func(r *Repo) { r.Fork = true }),
		repo("secret", describe, func(r *Repo) { r.Private = true }),
		repo("old", describe, func(r *Repo) { r.Archived = true }),
		repo("no-date", describe, func(r *Repo) { r.PushedAt = "" }),
		repo("bad-url", describe, func(r *Repo) { r.URL = "javascript:alert(1)" }),
	}
	cfg := config.Projects{Exclude: []string{"denishumen"}, ShowArchived: false}

	got := names(Rank(RankInput{Repos: repos, Config: cfg, Now: testNow}))
	if want := []string{"visible"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	cfg.ShowArchived = true
	projects := Rank(RankInput{Repos: repos, Config: cfg, Now: testNow})
	if got := tiers(projects)["old"]; got != TierCompact {
		t.Errorf("archived repository tier = %q, want compact", got)
	}
}

func TestRankPinnedComeFirstInConfigOrder(t *testing.T) {
	repos := []Repo{repo("alpha", describe), repo("beta", describe), repo("gamma", describe)}
	metrics := map[string]Metrics{"alpha": strong}
	cfg := config.Projects{Pinned: []string{"Gamma", "beta"}}

	projects := Rank(RankInput{Repos: repos, Metrics: metrics, Config: cfg, Now: testNow})

	if got, want := names(projects), []string{"gamma", "beta", "alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for _, p := range projects[:2] {
		if !p.Pinned || p.Tier != TierFeatured {
			t.Errorf("%s: pinned=%v tier=%s, want pinned and featured", p.Name, p.Pinned, p.Tier)
		}
	}
	if projects[2].Pinned {
		t.Error("alpha must not be pinned")
	}
}

func TestRankFeaturedSlotsIncludePinned(t *testing.T) {
	var repos []Repo
	metrics := map[string]Metrics{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		repos = append(repos, repo(name, describe))
		metrics[name] = strong
	}
	cfg := config.Projects{Pinned: []string{"h", "g"}}

	got := tiers(Rank(RankInput{Repos: repos, Metrics: metrics, Config: cfg, Now: testNow}))

	featured := 0
	for _, tier := range got {
		if tier == TierFeatured {
			featured++
		}
	}
	if featured != featuredSlots {
		t.Errorf("featured = %d, want %d (pinned included)", featured, featuredSlots)
	}
	// Equal scores and dates: the rest goes by name, so e and f are the ones left out.
	if got["e"] != TierStandard || got["f"] != TierStandard {
		t.Errorf("e=%s f=%s, want standard", got["e"], got["f"])
	}
}

func TestRankOverridesWin(t *testing.T) {
	repos := []Repo{repo("big", describe), repo("small")}
	metrics := map[string]Metrics{"big": strong}
	cfg := config.Projects{Overrides: map[string]config.ProjectOverride{
		"Big":   {Tier: TierCompact},
		"small": {Tier: TierFeatured},
	}}

	got := tiers(Rank(RankInput{Repos: repos, Metrics: metrics, Config: cfg, Now: testNow}))
	if got["big"] != TierCompact || got["small"] != TierFeatured {
		t.Errorf("tiers = %v", got)
	}
}

func TestRankDescriptionFallsBackToReadme(t *testing.T) {
	repos := []Repo{
		repo("named", func(r *Repo) { r.Description = "  NAMED " }), // equals the name: not a description
		repo("empty"),
		repo("nothing"),
		repo("fine", describe),
	}
	metrics := map[string]Metrics{
		"named": {ReadmeExcerpt: "From the README of named"},
		"empty": {ReadmeExcerpt: "From the README of empty"},
	}
	projects := Rank(RankInput{Repos: repos, Metrics: metrics, Now: testNow})

	byName := map[string]Project{}
	for _, p := range projects {
		byName[p.Name] = p
	}
	if got := *byName["named"].Description; got != "From the README of named" {
		t.Errorf("named: %q", got)
	}
	if got := *byName["empty"].Description; got != "From the README of empty" {
		t.Errorf("empty: %q", got)
	}
	if byName["nothing"].Description != nil {
		t.Errorf("nothing: want null, got %q", *byName["nothing"].Description)
	}
	if got := *byName["fine"].Description; got != "A meaningful description" {
		t.Errorf("fine: %q", got)
	}
}

func TestNormalizeHomepage(t *testing.T) {
	ignored := lowerSet([]string{"t.me", "telegram.me", "github.com"})
	cases := map[string]string{
		"":                               "",
		"   ":                            "",
		"https://t.me/DenisHumen":        "",
		"http://www.T.me/DenisHumen":     "",
		"https://github.com/DenisHumen":  "",
		"https://example.com/docs":       "https://example.com/docs",
		"example.com":                    "https://example.com",
		"www.example.com/path":           "https://www.example.com/path",
		"http://example.com":             "http://example.com",
		"javascript:alert(1)":            "",
		"ftp://example.com":              "",
		"https://":                       "",
		"localhost":                      "",
		"https://exa mple.com":           "",
		"https://docs.example.com:8443/": "https://docs.example.com:8443/",
	}
	for input, want := range cases {
		if got := normalizeHomepage(input, ignored); got != want {
			t.Errorf("normalizeHomepage(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRankOutputIsSafeForTheSiteSchema(t *testing.T) {
	projects := Rank(RankInput{
		Repos: []Repo{repo("x", func(r *Repo) {
			r.Topics = []string{" ", "networking"}
			r.Language = "Klingon" // no colour known
			r.Stars = -3
		})},
		Now: testNow,
	})
	data, err := json.Marshal(projects)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"x","description":null,"language":"Klingon","languageColor":null,"topics":["networking"],` +
		`"stars":0,"updatedAt":"2026-09-01","url":"https://github.com/DenisHumen/x","homepage":null,` +
		`"pinned":false,"tier":"compact","archived":false}]`
	if string(data) != want {
		t.Errorf("got  %s\nwant %s", data, want)
	}

	empty, _ := json.Marshal(Rank(RankInput{Repos: []Repo{repo("y")}, Now: testNow})[0].Topics)
	if string(empty) != "[]" {
		t.Errorf("topics of a repository without topics = %s, want []", empty)
	}
}
