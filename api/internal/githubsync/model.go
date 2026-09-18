// Package githubsync pulls the public repositories and the avatar of one GitHub user and writes
// content/generated/github.json — the input of the site build (docs/architecture.md §3).
//
// The work is split in two so that the site never depends on GitHub being reachable:
//
//	fetch — network; its result is cached on disk (state.json)
//	rank  — a pure function over the cached data and content/projects.yaml (docs/contract.md §5)
//
// When GitHub is down or rate-limited, ranking still runs on the cached data.
package githubsync

import "time"

// Repo is what is kept from one item of `GET /users/{user}/repos`.
type Repo struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Fork          bool     `json:"fork"`
	Private       bool     `json:"private"`
	Archived      bool     `json:"archived"`
	Language      string   `json:"language"`
	Topics        []string `json:"topics"`
	Stars         int      `json:"stars"`
	PushedAt      string   `json:"pushedAt"`
	UpdatedAt     string   `json:"updatedAt"`
	Homepage      string   `json:"homepage"`
	URL           string   `json:"url"`
	License       string   `json:"license"`
	DefaultBranch string   `json:"defaultBranch"`
}

// Metrics are the signals of how developed a repository is; they cost extra requests,
// so they are cached until the repository changes.
type Metrics struct {
	ReadmeSize    int    `json:"readmeSize"`
	ReadmeExcerpt string `json:"readmeExcerpt"`
	Commits       int    `json:"commits"`
	Releases      int    `json:"releases"`
	// Revision identifies the repository state the metrics were taken for (pushed_at + updated_at).
	Revision  string    `json:"revision"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// Project is one item of github.json → public; field order and names follow docs/contract.md §2.3.
type Project struct {
	Name          string   `json:"name"`
	Description   *string  `json:"description"`
	Language      *string  `json:"language"`
	LanguageColor *string  `json:"languageColor"`
	Topics        []string `json:"topics"`
	Stars         int      `json:"stars"`
	UpdatedAt     string   `json:"updatedAt"`
	URL           string   `json:"url"`
	Homepage      *string  `json:"homepage"`
	Pinned        bool     `json:"pinned"`
	Tier          string   `json:"tier"`
	Archived      bool     `json:"archived"`
}

// Output is content/generated/github.json.
type Output struct {
	Public   []Project `json:"public"`
	SyncedAt string    `json:"syncedAt"`
	Stale    bool      `json:"stale"`
}

// Tiers of docs/contract.md §5.
const (
	TierFeatured = "featured"
	TierStandard = "standard"
	TierCompact  = "compact"
)
