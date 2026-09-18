package analytics

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

var quiet = slog.New(slog.DiscardHandler)

const visitorIP = "203.0.113.77"

type fixture struct {
	t       *testing.T
	db      *sql.DB
	redis   *miniredis.Miniredis
	service *Service
	handler http.Handler
	now     time.Time
	mu      sync.Mutex
}

// newFixture is the real thing end to end: MySQL with the real migrations, the HTTP middleware
// stack of the server, the batching writer. Only Redis is an in-memory stand-in.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := testenv.MySQL(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, cfg, migrations.Files, quiet); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, cfg, 10*time.Second, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	redis := miniredis.RunT(t)
	store, err := cache.New(ctx, "redis://"+redis.Addr()+"/0", quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	f := &fixture{t: t, db: pool, redis: redis, now: time.Date(2026, 9, 18, 21, 30, 0, 0, time.UTC)}
	store.SetClock(func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now })
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	f.service = New(Options{
		DB: pool, Cache: store, Log: quiet, SiteHost: "krokosha.xyz", Location: kyiv,
		Now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
	})
	f.service.writer.flushEvery = 20 * time.Millisecond

	srv := server.New(server.Deps{Env: &config.Env{Listen: "127.0.0.1:0"}, DB: pool, Cache: store, Log: quiet, Started: time.Now()})
	f.service.Register(srv.Mux())
	f.handler = srv.Handler()

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); f.service.Run(runCtx) }()
	t.Cleanup(func() { stop(); <-done })
	return f
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
	f.redis.FastForward(d)
}

type request struct {
	body    string
	ip      string
	ua      string
	headers map[string]string
}

func (f *fixture) post(r request) int {
	f.t.Helper()
	if r.ip == "" {
		r.ip = visitorIP
	}
	if r.ua == "" {
		r.ua = uaChromeWindows
	}
	req := httptest.NewRequest(http.MethodPost, "/api/e", strings.NewReader(r.body))
	req.RemoteAddr = "127.0.0.1:40000" // nginx
	req.Host = "krokosha.xyz"
	req.Header.Set("X-Real-IP", r.ip)
	req.Header.Set("User-Agent", r.ua)
	req.Header.Set("Content-Type", "application/json")
	for key, value := range r.headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	return recorder.Code
}

// wait polls until the query returns want, so tests do not depend on the writer's timing.
func (f *fixture) wait(query string, want int) {
	f.t.Helper()
	var got int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := f.db.QueryRow(query).Scan(&got); err != nil {
			f.t.Fatal(err)
		}
		if got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("%s = %d, want %d", query, got, want)
}

func batch(id string, events string) string {
	return fmt.Sprintf(`{"v":1,"id":"%s","p":"/uk/","l":"uk","r":"www.google.com","u":{"s":"google","m":"cpc","c":"mikrotik"},"e":[%s]}`, id, events)
}

func TestPageviewIsStoredWithoutAnythingPersonal(t *testing.T) {
	f := newFixture(t)

	code := f.post(request{body: batch("00112233aabbccdd",
		`{"t":"pageview","o":0},{"t":"section","x":"hero","o":300},{"t":"click","x":"cta-telegram","o":900},{"t":"scroll","v":50,"o":950}`)})
	if code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", code)
	}
	f.wait(`SELECT COUNT(*) FROM analytics_pageviews`, 1)
	f.wait(`SELECT COUNT(*) FROM analytics_events`, 2) // section + click; scroll is a property of the page view

	var (
		path, lang, refHost, refKind, source, prefix, device, browser, osName, day string
		isAd                                                                       bool
		scroll                                                                     int
		started                                                                    time.Time
	)
	err := f.db.QueryRow(`SELECT path, lang, referrer_host, referrer_kind, utm_source, is_ad, ip_prefix,
		device, browser, os, max_scroll, DATE_FORMAT(day, '%Y-%m-%d'), started_at FROM analytics_pageviews`).
		Scan(&path, &lang, &refHost, &refKind, &source, &isAd, &prefix, &device, &browser, &osName, &scroll, &day, &started)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/uk/" || lang != "uk" || refHost != "google.com" || refKind != ReferrerSearch || source != "google" || !isAd {
		t.Errorf("page view: %s %s %s %s %s ad=%v", path, lang, refHost, refKind, source, isAd)
	}
	if prefix != "203.0.113.0/24" || device != "desktop" || browser != "Chrome" || osName != "Windows" || scroll != 50 {
		t.Errorf("client: %s %s %s %s scroll=%d", prefix, device, browser, osName, scroll)
	}
	// 21:30 UTC on the 18th is already the 19th in Kyiv: reports follow the owner's calendar.
	if day != "2026-09-19" {
		t.Errorf("day = %s, want 2026-09-19 (Europe/Kyiv)", day)
	}
	if started.After(f.now) || f.now.Sub(started) > 2*time.Second {
		t.Errorf("started_at = %v, want just before %v", started, f.now)
	}

	// The promise of the privacy page, checked the blunt way: the address and the browser string
	// appear nowhere — not in the database, not in Redis.
	for _, table := range []string{"analytics_pageviews", "analytics_events", "analytics_salts"} {
		rows, err := f.db.Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			values := make([]sql.RawBytes, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			for i, value := range values {
				if strings.Contains(string(value), visitorIP) || strings.Contains(string(value), "Mozilla/") {
					t.Errorf("%s.%s holds personal data: %q", table, columns[i], value)
				}
			}
		}
		_ = rows.Close()
	}
	for _, key := range f.redis.Keys() {
		if strings.Contains(key, visitorIP) {
			t.Errorf("Redis key holds the address: %s", key)
		}
	}
}

func TestLaterBatchesUpdateTheSamePageview(t *testing.T) {
	f := newFixture(t)
	f.post(request{body: batch("00112233aabbccdd", `{"t":"pageview","o":0},{"t":"scroll","v":25,"o":500}`)})
	f.wait(`SELECT COUNT(*) FROM analytics_pageviews`, 1)

	f.advance(20 * time.Second)
	f.post(request{body: batch("00112233aabbccdd", `{"t":"scroll","v":100,"o":19000},{"t":"time","v":20000,"o":20000},{"t":"outbound","x":"t.me","o":19500}`)})
	f.wait(`SELECT max_scroll FROM analytics_pageviews`, 100)
	f.wait(`SELECT duration_ms FROM analytics_pageviews`, 20000)
	f.wait(`SELECT COUNT(*) FROM analytics_pageviews`, 1)
	f.wait(`SELECT COUNT(*) FROM analytics_events WHERE type = 'outbound' AND target = 't.me'`, 1)

	// A smaller value arriving late (the beacon of a hidden tab) never shrinks what is stored.
	f.post(request{body: batch("00112233aabbccdd", `{"t":"scroll","v":50,"o":21000},{"t":"time","v":1000,"o":21000}`)})
	time.Sleep(150 * time.Millisecond)
	f.wait(`SELECT max_scroll FROM analytics_pageviews`, 100)
	f.wait(`SELECT duration_ms FROM analytics_pageviews`, 20000)
}

func TestVisitorsWhoAskNotToBeTrackedAreNotRecorded(t *testing.T) {
	f := newFixture(t)
	body := batch("00112233aabbccdd", `{"t":"pageview","o":0}`)

	for name, headers := range map[string]map[string]string{
		"Do Not Track":           {"DNT": "1"},
		"Global Privacy Control": {"Sec-GPC": "1"},
	} {
		if code := f.post(request{body: body, headers: headers}); code != http.StatusNoContent {
			t.Errorf("%s: status %d, want a silent 204", name, code)
		}
	}
	if code := f.post(request{body: body, ua: "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"}); code != http.StatusNoContent {
		t.Errorf("bot: status %d, want a silent 204", code)
	}
	time.Sleep(200 * time.Millisecond)
	f.wait(`SELECT COUNT(*) FROM analytics_pageviews`, 0)
}

func TestTheOwnerIsNotAVisitor(t *testing.T) {
	f := newFixture(t)
	f.service.opts.IgnoreCookie = "__Host-ks"

	// Signed in to the admin area (even with a session that has expired since): not counted.
	if code := f.post(request{body: batch("00112233aabbcc10", `{"t":"pageview","o":0}`), headers: map[string]string{"Cookie": "__Host-ks=whatever"}}); code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", code)
	}
	// Any other cookie is none of our business.
	f.post(request{body: batch("00112233aabbcc11", `{"t":"pageview","o":0}`), headers: map[string]string{"Cookie": "theme=dark"}})
	f.wait(`SELECT COUNT(*) FROM analytics_pageviews`, 1)
	f.wait(`SELECT COUNT(*) FROM analytics_pageviews WHERE pageview_id = UNHEX('00112233aabbcc11')`, 1)
}

func TestRequestsThatAreRefused(t *testing.T) {
	f := newFixture(t)
	body := batch("00112233aabbccdd", `{"t":"pageview","o":0}`)

	if code := f.post(request{body: body, headers: map[string]string{"Origin": "https://evil.example"}}); code != http.StatusForbidden {
		t.Errorf("another site's page: status %d, want 403", code)
	}
	if code := f.post(request{body: body, headers: map[string]string{"Origin": "https://krokosha.xyz"}}); code != http.StatusNoContent {
		t.Errorf("own origin: status %d, want 204", code)
	}
	if code := f.post(request{body: `{"v":1,"id":"x"}`}); code != http.StatusBadRequest {
		t.Errorf("invalid payload: status %d, want 400", code)
	}
	if code := f.post(request{body: strings.Repeat("x", maxBodyBytes+10)}); code != http.StatusRequestEntityTooLarge {
		t.Errorf("huge body: status %d, want 413", code)
	}
}

func TestRateLimitPerAddress(t *testing.T) {
	f := newFixture(t)
	body := batch("00112233aabbccdd", `{"t":"time","v":1,"o":1}`)
	for i := 1; i <= batchesPerMin; i++ {
		if code := f.post(request{body: body, ip: "198.51.100.9"}); code != http.StatusNoContent {
			t.Fatalf("request %d: status %d", i, code)
		}
	}
	if code := f.post(request{body: body, ip: "198.51.100.9"}); code != http.StatusTooManyRequests {
		t.Errorf("request over the limit: status %d, want 429", code)
	}
	if code := f.post(request{body: body, ip: "198.51.100.10"}); code != http.StatusNoContent {
		t.Errorf("a neighbour's address was limited too: status %d", code)
	}
}

func TestSessionsAndDailyPseudonyms(t *testing.T) {
	f := newFixture(t)
	view := func(id string) { f.post(request{body: batch(id, `{"t":"pageview","o":0}`)}) }

	view("0000000000000001")
	f.advance(10 * time.Minute)
	view("0000000000000002")
	f.wait(`SELECT COUNT(*) FROM analytics_pageviews`, 2)
	f.wait(`SELECT COUNT(DISTINCT session_id) FROM analytics_pageviews`, 1)
	f.wait(`SELECT COUNT(DISTINCT visitor) FROM analytics_pageviews`, 1)

	// Half an hour of silence ends the visit.
	f.advance(31 * time.Minute)
	view("0000000000000003")
	f.wait(`SELECT COUNT(DISTINCT session_id) FROM analytics_pageviews`, 2)
	f.wait(`SELECT COUNT(DISTINCT visitor) FROM analytics_pageviews`, 1)

	// Another browser on the same address is another visitor.
	f.post(request{body: batch("0000000000000004", `{"t":"pageview","o":0}`), ua: uaFirefoxLinux})
	f.wait(`SELECT COUNT(DISTINCT visitor) FROM analytics_pageviews`, 2)

	// The next day the same person is a stranger: the salt changed.
	f.advance(24 * time.Hour)
	view("0000000000000005")
	f.wait(`SELECT COUNT(DISTINCT visitor) FROM analytics_pageviews`, 3)

	// And two days later the first salt is gone for good.
	f.advance(24 * time.Hour)
	view("0000000000000006")
	f.wait(`SELECT COUNT(*) FROM analytics_salts`, 2)
	f.wait(`SELECT COUNT(*) FROM analytics_salts WHERE day = '2026-09-18'`, 0)
}

func TestLiveFeedAndActiveVisitors(t *testing.T) {
	f := newFixture(t)
	feed, cancel := f.service.Subscribe()
	defer cancel()

	f.post(request{body: batch("00112233aabbccdd", `{"t":"pageview","o":0},{"t":"click","x":"cta-email","o":10},{"t":"time","v":10,"o":10}`)})
	f.post(request{body: batch("00112233aabbccde", `{"t":"pageview","o":0}`), ip: "198.51.100.20"})

	var got []Live
	for len(got) < 3 {
		select {
		case item := <-feed:
			got = append(got, item)
		case <-time.After(2 * time.Second):
			t.Fatalf("feed delivered %d items, want 3: %+v", len(got), got)
		}
	}
	if got[0].Type != TypePageview || got[1].Type != TypeClick || got[1].Target != "cta-email" || got[1].Path != "/uk/" {
		t.Errorf("feed: %+v", got)
	}
	if len(got[0].Visitor) != 8 || got[0].Visitor == got[2].Visitor {
		t.Errorf("visitors in the feed: %q %q", got[0].Visitor, got[2].Visitor)
	}

	if n := f.service.opts.Cache.CountActive(context.Background(), ActiveSet, 5*time.Minute); n != 2 {
		t.Errorf("active visitors = %d, want 2", n)
	}
	f.advance(6 * time.Minute)
	if n := f.service.opts.Cache.CountActive(context.Background(), ActiveSet, 5*time.Minute); n != 0 {
		t.Errorf("active visitors after 6 minutes = %d, want 0", n)
	}
}
