// Package indexnow tells search engines which pages of a fresh release changed (brief B7).
// IndexNow (https://www.indexnow.org) is one request shared by Bing — and through its index
// DuckDuckGo and others — Yandex, Naver and Seznam. Google is not among them: it is told by
// the sitemap and Search Console. The site proves it is the sender with a key it serves as
// /<key>.txt; the key is not a secret, only a proof of control.
//
// Only pages that changed are submitted: the build is reproducible, so a page whose HTML is
// byte for byte the release before is the same page.
package indexnow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultEndpoint is shared by every engine that takes part; whoever receives a submission
// passes it on to the others.
const DefaultEndpoint = "https://api.indexnow.org/indexnow"

var reKey = regexp.MustCompile(`^[A-Za-z0-9-]{8,128}$`)

// ValidKey reports whether key may serve as an IndexNow key: 8 to 128 letters, digits and dashes.
func ValidKey(key string) bool { return reKey.MatchString(key) }

// Options say what to submit.
type Options struct {
	Site     string // the public origin, https://krokosha.xyz
	Key      string // INDEXNOW_KEY, served as Site/<Key>.txt
	Endpoint string // DefaultEndpoint unless a test says otherwise
	Release  string // the directory of the new release: sitemap.xml and the pages
	Previous string // the directory of the release before it; empty — every page counts as changed
	All      bool   // submit every page of the sitemap whether it changed or not
	Client   *http.Client
}

// Result is what was found and what was sent.
type Result struct {
	Pages   []string // every page of the sitemap
	Changed []string // the ones submitted
}

// Pages lists the <loc> of a sitemap.
func Pages(sitemap string) ([]string, error) {
	raw, err := os.ReadFile(sitemap)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%s: %w", sitemap, err)
	}
	pages := make([]string, 0, len(parsed.URLs))
	for _, item := range parsed.URLs {
		if loc := strings.TrimSpace(item.Loc); loc != "" {
			pages = append(pages, loc)
		}
	}
	return pages, nil
}

// file is where a page of the release lives: /uk/ → uk/index.html.
func file(release, page string) (string, error) {
	parsed, err := url.Parse(page)
	if err != nil {
		return "", err
	}
	clean := path.Clean("/" + parsed.Path)
	if strings.HasSuffix(parsed.Path, "/") || clean == "/" {
		clean = path.Join(clean, "index.html")
	}
	return filepath.Join(release, filepath.FromSlash(strings.TrimPrefix(clean, "/"))), nil
}

func digest(name string) ([32]byte, error) {
	raw, err := os.ReadFile(name)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

// Changed picks the pages whose HTML differs from the previous release. A page that is not in
// the new release is skipped: there is nothing to index. Without a previous release every page
// is new.
func Changed(pages []string, release, previous string) ([]string, error) {
	var changed []string
	for _, page := range pages {
		current, err := file(release, page)
		if err != nil {
			return nil, err
		}
		now, err := digest(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if previous != "" {
			old, err := file(previous, page)
			if err != nil {
				return nil, err
			}
			if before, err := digest(old); err == nil && before == now {
				continue
			}
		}
		changed = append(changed, page)
	}
	return changed, nil
}

// Submit tells the engines about the pages that changed. No page changed: nothing is sent.
func Submit(ctx context.Context, opts Options) (Result, error) {
	var out Result
	if !ValidKey(opts.Key) {
		return out, errors.New("the key must be 8 to 128 letters, digits and dashes")
	}
	site, err := url.Parse(opts.Site)
	if err != nil || site.Host == "" || (site.Scheme != "https" && site.Scheme != "http") {
		return out, fmt.Errorf("the site must be an origin like https://example.com, not %q", opts.Site)
	}
	if out.Pages, err = Pages(filepath.Join(opts.Release, "sitemap.xml")); err != nil {
		return out, err
	}
	previous := opts.Previous
	if opts.All {
		previous = ""
	}
	if out.Changed, err = Changed(out.Pages, opts.Release, previous); err != nil {
		return out, err
	}
	if len(out.Changed) == 0 {
		return out, nil
	}

	body, err := json.Marshal(map[string]any{
		"host":        site.Host,
		"key":         opts.Key,
		"keyLocation": strings.TrimRight(opts.Site, "/") + "/" + opts.Key + ".txt",
		"urlList":     out.Changed,
	})
	if err != nil {
		return out, err
	}
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("User-Agent", "krokosha-site (+"+opts.Site+")")
	response, err := client.Do(request)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	switch response.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		return out, nil
	case http.StatusForbidden:
		return out, fmt.Errorf("the key was not accepted: is %s/%s.txt served with the key in it? (%s)", opts.Site, opts.Key, strings.TrimSpace(string(answer)))
	case http.StatusUnprocessableEntity:
		return out, fmt.Errorf("the pages do not belong to %s (%s)", site.Host, strings.TrimSpace(string(answer)))
	case http.StatusTooManyRequests:
		return out, errors.New("too many submissions; the next release tries again")
	default:
		return out, fmt.Errorf("%s answered %s: %s", endpoint, response.Status, strings.TrimSpace(string(answer)))
	}
}
