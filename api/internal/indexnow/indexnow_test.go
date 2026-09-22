package indexnow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// release writes a directory that looks like a built site: a sitemap and the pages it names.
func release(t *testing.T, pages map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	var sitemap strings.Builder
	sitemap.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for page, html := range pages {
		sitemap.WriteString("<url><loc>https://krokosha.xyz" + page + "</loc><lastmod>2026-09-18</lastmod></url>")
		if html == "" {
			continue // in the sitemap, not in the release
		}
		name, err := file(dir, "https://krokosha.xyz"+page)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sitemap.WriteString("</urlset>")
	if err := os.WriteFile(filepath.Join(dir, "sitemap.xml"), []byte(sitemap.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

type submission struct {
	Host, Key, KeyLocation string
	URLList                []string
}

// engine stands in for api.indexnow.org: it keeps what it was told and answers as it is told to.
func engine(t *testing.T, status int) (*httptest.Server, *[]submission) {
	t.Helper()
	var got []submission
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "not a submission", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var one submission
		if err := json.Unmarshal(body, &one); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got = append(got, one)
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server, &got
}

func TestOnlyThePagesThatChangedAreSubmitted(t *testing.T) {
	before := release(t, map[string]string{"/": "<h1>Denis</h1>", "/uk/": "<h1>Денис</h1>", "/ru/": "<h1>Денис</h1>", "/privacy/": "<p>old</p>"})
	after := release(t, map[string]string{"/": "<h1>Denis</h1>", "/uk/": "<h1>Денис</h1>", "/ru/": "<h1>Денис!</h1>", "/privacy/": "<p>new</p>", "/en/new/": "<p>a page</p>", "/ghost/": ""})
	server, got := engine(t, http.StatusOK)
	ctx := context.Background()

	result, err := Submit(ctx, Options{Site: "https://krokosha.xyz", Key: "0123456789abcdef", Endpoint: server.URL, Release: after, Previous: before})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pages) != 6 {
		t.Errorf("pages of the sitemap: %v", result.Pages)
	}
	want := []string{"https://krokosha.xyz/ru/", "https://krokosha.xyz/privacy/", "https://krokosha.xyz/en/new/"}
	if len(*got) != 1 || !sameSet((*got)[0].URLList, want) || !sameSet(result.Changed, want) {
		t.Fatalf("submitted %+v, want %v", *got, want)
	}
	if one := (*got)[0]; one.Host != "krokosha.xyz" || one.Key != "0123456789abcdef" || one.KeyLocation != "https://krokosha.xyz/0123456789abcdef.txt" {
		t.Errorf("the submission: %+v", one)
	}

	// The same release again: nothing changed, nothing is sent.
	result, err = Submit(ctx, Options{Site: "https://krokosha.xyz", Key: "0123456789abcdef", Endpoint: server.URL, Release: after, Previous: after})
	if err != nil || len(result.Changed) != 0 || len(*got) != 1 {
		t.Errorf("an unchanged release: %+v, %v, %d submissions", result.Changed, err, len(*got))
	}
	// The first release ever, or --all: every page that exists.
	for _, opts := range []Options{
		{Site: "https://krokosha.xyz", Key: "0123456789abcdef", Endpoint: server.URL, Release: after},
		{Site: "https://krokosha.xyz", Key: "0123456789abcdef", Endpoint: server.URL, Release: after, Previous: after, All: true},
	} {
		result, err = Submit(ctx, opts)
		if err != nil || len(result.Changed) != 5 {
			t.Errorf("every page: %v, %v", result.Changed, err)
		}
	}
}

func TestAnswersOfTheEngine(t *testing.T) {
	dir := release(t, map[string]string{"/": "<h1>Denis</h1>"})
	for status, want := range map[int]string{
		http.StatusAccepted:            "",
		http.StatusForbidden:           "the key was not accepted",
		http.StatusUnprocessableEntity: "do not belong to krokosha.xyz",
		http.StatusTooManyRequests:     "too many submissions",
		http.StatusInternalServerError: "answered 500",
	} {
		server, _ := engine(t, status)
		_, err := Submit(context.Background(), Options{Site: "https://krokosha.xyz", Key: "0123456789abcdef", Endpoint: server.URL, Release: dir})
		switch {
		case want == "" && err != nil:
			t.Errorf("%d: %v", status, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%d: %v, want %q", status, err, want)
		}
	}
	for _, opts := range []Options{
		{Site: "https://krokosha.xyz", Key: "short", Release: dir},
		{Site: "krokosha.xyz", Key: "0123456789abcdef", Release: dir},
		{Site: "https://krokosha.xyz", Key: "0123456789abcdef", Release: t.TempDir()}, // no sitemap
	} {
		if _, err := Submit(context.Background(), opts); err == nil {
			t.Errorf("accepted %+v", opts)
		}
	}
	for key, valid := range map[string]bool{"0123456789abcdef": true, "a-b-c-d-1": true, "1234567": false, "with space": false, strings.Repeat("k", 129): false} {
		if ValidKey(key) != valid {
			t.Errorf("ValidKey(%q) = %v", key, !valid)
		}
	}
}

func sameSet(a, b []string) bool {
	set := map[string]bool{}
	for _, item := range a {
		set[item] = true
	}
	other := map[string]bool{}
	for _, item := range b {
		other[item] = true
	}
	return reflect.DeepEqual(set, other)
}
