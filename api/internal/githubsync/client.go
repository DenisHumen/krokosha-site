package githubsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

const (
	defaultBaseURL = "https://api.github.com"
	userAgent      = "krokosha-site-sync (+https://github.com/DenisHumen/krokosha-site)"
	maxBodyBytes   = 10 << 20
	maxAttempts    = 3
)

var errNotFound = errors.New("not found")

// rateLimitError means GitHub refused the request because the quota is used up.
// Without a token the quota is 60 requests per hour per IP.
type rateLimitError struct {
	reset time.Time
}

func (e *rateLimitError) Error() string {
	if e.reset.IsZero() {
		return "GitHub API rate limit exceeded"
	}
	return "GitHub API rate limit exceeded, resets at " + e.reset.UTC().Format(time.RFC3339)
}

type statusError struct {
	status int
	url    string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("GET %s: HTTP %d", e.url, e.status)
}

type response struct {
	body []byte
	link string // Link header: pagination
}

// client is a minimal GitHub REST client: authentication, conditional requests with an on-disk
// ETag cache (brief B3), retries of transient failures, rate-limit detection.
type client struct {
	http     *http.Client
	baseURL  string
	token    string
	cacheDir string
	log      *slog.Logger
}

// retrySleep waits between attempts; tests replace it to skip the back-off.
var retrySleep = sleepContext

type cachedResponse struct {
	URL  string `json:"url"`
	ETag string `json:"etag"`
	Link string `json:"link"`
	Body []byte `json:"body"`
}

func (c *client) cachePath(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(c.cacheDir, "http", hex.EncodeToString(sum[:16])+".json")
}

func (c *client) loadCached(rawURL string) *cachedResponse {
	data, err := os.ReadFile(c.cachePath(rawURL))
	if err != nil {
		return nil
	}
	var cached cachedResponse
	if json.Unmarshal(data, &cached) != nil || cached.URL != rawURL || cached.ETag == "" {
		return nil
	}
	return &cached
}

// get performs a GET. With conditional=true the ETag of the previous answer is sent, and
// a "304 Not Modified" is served from the disk cache.
func (c *client) get(ctx context.Context, rawURL string, conditional bool) (*response, error) {
	var cached *cachedResponse
	if conditional {
		cached = c.loadCached(rawURL)
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := retrySleep(ctx, time.Duration(attempt-1)*2*time.Second); err != nil {
				return nil, err
			}
		}
		result, retry, err := c.do(ctx, rawURL, cached, conditional)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
		c.log.Warn("GitHub request failed, will retry", "url", rawURL, "attempt", attempt, "error", err)
	}
	return nil, lastErr
}

func (c *client) do(ctx context.Context, rawURL string, cached *cachedResponse, store bool) (result *response, retry bool, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	if cached != nil {
		request.Header.Set("If-None-Match", cached.ETag)
	}

	reply, err := c.http.Do(request)
	if err != nil {
		return nil, ctx.Err() == nil, err
	}
	defer reply.Body.Close()

	switch {
	case reply.StatusCode == http.StatusNotModified && cached != nil:
		return &response{body: cached.Body, link: cached.Link}, false, nil
	case reply.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(reply.Body, maxBodyBytes+1))
		if err != nil {
			return nil, true, err
		}
		if len(body) > maxBodyBytes {
			return nil, false, fmt.Errorf("GET %s: response is larger than %d bytes", rawURL, maxBodyBytes)
		}
		result := &response{body: body, link: reply.Header.Get("Link")}
		if etag := reply.Header.Get("ETag"); store && etag != "" {
			c.store(cachedResponse{URL: rawURL, ETag: etag, Link: result.link, Body: body})
		}
		return result, false, nil
	case reply.StatusCode == http.StatusNotFound:
		return nil, false, errNotFound
	case isRateLimited(reply):
		return nil, false, &rateLimitError{reset: rateLimitReset(reply)}
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(reply.Body, 1<<16))
		return nil, reply.StatusCode >= 500, &statusError{status: reply.StatusCode, url: rawURL}
	}
}

func (c *client) store(entry cachedResponse) {
	data, err := json.Marshal(entry)
	if err == nil {
		err = writeFileAtomic(c.cachePath(entry.URL), data)
	}
	if err != nil {
		c.log.Warn("cannot write the HTTP cache", "error", err)
	}
}

func isRateLimited(reply *http.Response) bool {
	if reply.StatusCode != http.StatusForbidden && reply.StatusCode != http.StatusTooManyRequests {
		return false
	}
	return reply.Header.Get("X-RateLimit-Remaining") == "0" || reply.Header.Get("Retry-After") != ""
}

func rateLimitReset(reply *http.Response) time.Time {
	if seconds, err := strconv.ParseInt(reply.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		return time.Unix(seconds, 0)
	}
	if seconds, err := strconv.Atoi(reply.Header.Get("Retry-After")); err == nil {
		return time.Now().Add(time.Duration(seconds) * time.Second)
	}
	return time.Time{}
}

var reLastLink = regexp.MustCompile(`<([^>]+)>;\s*rel="last"`)

// lastPage reads the number of the last page from a Link header; 0 if there is no pagination.
// With per_page=1 this is the total number of items — the cheapest way to count commits and releases.
func lastPage(link string) int {
	match := reLastLink.FindStringSubmatch(link)
	if match == nil {
		return 0
	}
	parsed, err := url.Parse(match[1])
	if err != nil {
		return 0
	}
	page, err := strconv.Atoi(parsed.Query().Get("page"))
	if err != nil || page < 0 {
		return 0
	}
	return page
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// writeFileAtomic writes next to the target and renames, so readers never see a half-written file.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
