package githubsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	reposPerPage = 100
	maxRepoPages = 10 // 1000 repositories is far beyond a personal account
)

type apiRepo struct {
	Name          string   `json:"name"`
	Description   *string  `json:"description"`
	Fork          bool     `json:"fork"`
	Private       bool     `json:"private"`
	Archived      bool     `json:"archived"`
	Language      *string  `json:"language"`
	Topics        []string `json:"topics"`
	Stars         int      `json:"stargazers_count"`
	PushedAt      *string  `json:"pushed_at"`
	UpdatedAt     *string  `json:"updated_at"`
	Homepage      *string  `json:"homepage"`
	HTMLURL       string   `json:"html_url"`
	DefaultBranch string   `json:"default_branch"`
	License       *struct {
		SPDX string `json:"spdx_id"`
		Key  string `json:"key"`
	} `json:"license"`
}

func (r apiRepo) toRepo() Repo {
	repo := Repo{
		Name:          r.Name,
		Description:   deref(r.Description),
		Fork:          r.Fork,
		Private:       r.Private,
		Archived:      r.Archived,
		Language:      deref(r.Language),
		Topics:        r.Topics,
		Stars:         r.Stars,
		PushedAt:      deref(r.PushedAt),
		UpdatedAt:     deref(r.UpdatedAt),
		Homepage:      deref(r.Homepage),
		URL:           r.HTMLURL,
		DefaultBranch: r.DefaultBranch,
	}
	if r.License != nil {
		repo.License = r.License.SPDX
		if repo.License == "" {
			repo.License = r.License.Key
		}
	}
	return repo
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// listRepos returns the public repositories owned by the user.
// `GET /users/{user}/repos` never includes private repositories, whatever the token can see.
func (c *client) listRepos(ctx context.Context, user string) ([]Repo, error) {
	var repos []Repo
	for page := 1; page <= maxRepoPages; page++ {
		query := url.Values{
			"type":     {"owner"},
			"sort":     {"pushed"},
			"per_page": {fmt.Sprint(reposPerPage)},
			"page":     {fmt.Sprint(page)},
		}
		endpoint := fmt.Sprintf("%s/users/%s/repos?%s", c.baseURL, url.PathEscape(user), query.Encode())
		reply, err := c.get(ctx, endpoint, true)
		if err != nil {
			return nil, err
		}
		var batch []apiRepo
		if err := json.Unmarshal(reply.body, &batch); err != nil {
			return nil, fmt.Errorf("repository list: %w", err)
		}
		for _, item := range batch {
			repos = append(repos, item.toRepo())
		}
		if len(batch) < reposPerPage {
			break
		}
	}
	return repos, nil
}

// fetchMetrics costs three requests per repository: README, commit count, release count.
func (c *client) fetchMetrics(ctx context.Context, user string, repo Repo) (Metrics, error) {
	base := fmt.Sprintf("%s/repos/%s/%s", c.baseURL, url.PathEscape(user), url.PathEscape(repo.Name))
	var metrics Metrics

	switch reply, err := c.get(ctx, base+"/readme", false); {
	case err == nil:
		var readme struct {
			Size     int    `json:"size"`
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.Unmarshal(reply.body, &readme); err != nil {
			return metrics, fmt.Errorf("%s readme: %w", repo.Name, err)
		}
		metrics.ReadmeSize = readme.Size
		if readme.Encoding == "base64" {
			// GitHub wraps base64 at 60 columns.
			if text, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(readme.Content, "\n", "")); err == nil {
				metrics.ReadmeExcerpt = ReadmeExcerpt(string(text))
			}
		}
	case errors.Is(err, errNotFound):
		// No README — zero points, nothing to quote.
	default:
		return metrics, err
	}

	commits, err := c.count(ctx, base+"/commits?per_page=1")
	if err != nil {
		return metrics, err
	}
	metrics.Commits = commits

	releases, err := c.count(ctx, base+"/releases?per_page=1")
	if err != nil {
		return metrics, err
	}
	metrics.Releases = releases
	return metrics, nil
}

// count returns the number of items of a list endpoint called with per_page=1.
func (c *client) count(ctx context.Context, endpoint string) (int, error) {
	reply, err := c.get(ctx, endpoint, false)
	var status *statusError
	switch {
	case errors.Is(err, errNotFound):
		return 0, nil
	case errors.As(err, &status) && status.status == 409:
		return 0, nil // "Git Repository is empty"
	case err != nil:
		return 0, err
	}
	if last := lastPage(reply.link); last > 0 {
		return last, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(reply.body, &items); err != nil {
		return 0, fmt.Errorf("GET %s: %w", endpoint, err)
	}
	return len(items), nil
}
