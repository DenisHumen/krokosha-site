package githubsync

import (
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

// Ranking of public repositories — docs/contract.md §5. Denis's decision: show every repository,
// but make the well-developed ones more prominent. Coefficients are deliberately plain numbers
// in one place; change them together with the table in the contract.

const (
	featuredSlots    = 6  // featured cards in total, pinned included
	featuredMinScore = 55 // below this a repository is never featured automatically
	standardMinScore = 30
)

// RankInput is everything Rank depends on; there is no I/O and no clock inside.
type RankInput struct {
	Repos   []Repo
	Metrics map[string]Metrics
	Config  config.Projects
	Now     time.Time
}

// Score counts the points of one repository. descriptionOK and homepageOK are decided by the caller,
// because both depend on content/projects.yaml.
func Score(repo Repo, m Metrics, descriptionOK, homepageOK bool, now time.Time) int {
	points := 0

	switch {
	case m.ReadmeSize >= 10_000:
		points += 30
	case m.ReadmeSize >= 5_000:
		points += 22
	case m.ReadmeSize >= 2_000:
		points += 15
	case m.ReadmeSize >= 500:
		points += 8
	}

	switch {
	case m.Commits >= 200:
		points += 20
	case m.Commits >= 50:
		points += 15
	case m.Commits >= 20:
		points += 10
	case m.Commits >= 5:
		points += 5
	}

	switch {
	case m.Releases >= 5:
		points += 15
	case m.Releases >= 1:
		points += 8
	}

	if descriptionOK {
		points += 10
	}
	if repo.License != "" {
		points += 5
	}
	if homepageOK {
		points += 5
	}
	if len(repo.Topics) > 0 {
		points += 5
	}
	points += min(repo.Stars, 5) * 2

	if pushed, err := time.Parse(time.RFC3339, repo.PushedAt); err == nil {
		switch days := int(now.Sub(pushed).Hours() / 24); {
		case days <= 180:
			points += 10
		case days <= 365:
			points += 5
		}
	}

	if repo.Archived {
		points -= 20
	}
	return points
}

type ranked struct {
	project Project
	score   int
}

// Rank turns raw GitHub data into the ordered list for github.json:
// pinned → featured → standard → compact, by score inside a tier.
func Rank(in RankInput) []Project {
	excluded := lowerSet(in.Config.Exclude)
	ignoredHosts := lowerSet(in.Config.HomepageIgnoreHosts)
	pinOrder := map[string]int{}
	for i, name := range in.Config.Pinned {
		pinOrder[strings.ToLower(name)] = i
	}
	overrides := map[string]config.ProjectOverride{}
	for name, override := range in.Config.Overrides {
		overrides[strings.ToLower(name)] = override
	}

	var pinned, rest []ranked
	for _, repo := range in.Repos {
		key := strings.ToLower(repo.Name)
		if repo.Private || repo.Fork || excluded[key] || (repo.Archived && !in.Config.ShowArchived) {
			continue
		}
		// The site build validates github.json strictly; a repository it would reject is left out
		// here rather than allowed to break the whole build.
		updatedAt := dateOf(repo.PushedAt)
		if updatedAt == "" {
			updatedAt = dateOf(repo.UpdatedAt)
		}
		if repo.Name == "" || updatedAt == "" || !strings.HasPrefix(repo.URL, "https://") {
			continue
		}
		metrics := in.Metrics[repo.Name]
		override := overrides[key]

		description := strings.TrimSpace(repo.Description)
		descriptionOK := description != "" && !strings.EqualFold(description, repo.Name)
		if !descriptionOK {
			description = metrics.ReadmeExcerpt
		}
		homepage := normalizeHomepage(repo.Homepage, ignoredHosts)
		_, isPinned := pinOrder[key]

		item := ranked{
			score: Score(repo, metrics, descriptionOK || override.HasDescription(), homepage != "", in.Now),
			project: Project{
				Name:          repo.Name,
				Description:   optional(description),
				Language:      optional(repo.Language),
				LanguageColor: optional(languageColor(repo.Language)),
				Topics:        cleanTopics(repo.Topics),
				Stars:         max(repo.Stars, 0),
				UpdatedAt:     updatedAt,
				URL:           repo.URL,
				Homepage:      optional(homepage),
				Pinned:        isPinned,
				Archived:      repo.Archived,
			},
		}
		if isPinned {
			item.project.Tier = TierFeatured
			pinned = append(pinned, item)
		} else {
			rest = append(rest, item)
		}
	}

	sort.SliceStable(pinned, func(a, b int) bool {
		return pinOrder[strings.ToLower(pinned[a].project.Name)] < pinOrder[strings.ToLower(pinned[b].project.Name)]
	})
	// Best first; equal scores — the most recently updated first; then by name, to stay deterministic.
	sort.SliceStable(rest, func(a, b int) bool {
		x, y := rest[a], rest[b]
		if x.score != y.score {
			return x.score > y.score
		}
		if x.project.UpdatedAt != y.project.UpdatedAt {
			return x.project.UpdatedAt > y.project.UpdatedAt
		}
		return strings.ToLower(x.project.Name) < strings.ToLower(y.project.Name)
	})

	slots := max(0, featuredSlots-len(pinned))
	for i := range rest {
		item := &rest[i]
		switch override := overrides[strings.ToLower(item.project.Name)]; {
		case override.Tier != "":
			item.project.Tier = override.Tier
		case item.project.Archived:
			item.project.Tier = TierCompact
		case slots > 0 && item.score >= featuredMinScore:
			item.project.Tier = TierFeatured
			slots--
		case item.score >= standardMinScore:
			item.project.Tier = TierStandard
		default:
			item.project.Tier = TierCompact
		}
	}
	tierOrder := map[string]int{TierFeatured: 0, TierStandard: 1, TierCompact: 2}
	sort.SliceStable(rest, func(a, b int) bool {
		return tierOrder[rest[a].project.Tier] < tierOrder[rest[b].project.Tier]
	})

	projects := make([]Project, 0, len(pinned)+len(rest))
	for _, item := range append(pinned, rest...) {
		projects = append(projects, item.project)
	}
	return projects
}

// normalizeHomepage returns a web URL or "" when the homepage is empty, malformed or only a link
// to a messenger / GitHub itself. The site build rejects anything that is not http(s), so nothing
// questionable may get through here.
func normalizeHomepage(raw string, ignoredHosts map[string]bool) string {
	home := strings.TrimSpace(raw)
	if home == "" {
		return ""
	}
	if !strings.Contains(home, "://") {
		home = "https://" + home
	}
	parsed, err := url.Parse(home)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if ignoredHosts[host] || !strings.Contains(host, ".") {
		return ""
	}
	return parsed.String()
}

func cleanTopics(topics []string) []string {
	clean := make([]string, 0, len(topics))
	for _, topic := range topics {
		if topic = strings.TrimSpace(topic); topic != "" {
			clean = append(clean, topic)
		}
	}
	return clean
}

// dateOf cuts an RFC 3339 timestamp down to YYYY-MM-DD (UTC, as GitHub reports it).
func dateOf(timestamp string) string {
	if parsed, err := time.Parse(time.RFC3339, timestamp); err == nil {
		return parsed.UTC().Format(time.DateOnly)
	}
	return ""
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func lowerSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[strings.ToLower(strings.TrimSpace(value))] = true
	}
	return set
}
