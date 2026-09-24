package achievements

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

var (
	quiet  = slog.New(slog.DiscardHandler)
	secret = []byte("0123456789abcdef0123456789abcdef")
	noon   = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
)

// The eggs here are the eggs of the page: design/components/eggs/eggs.js → ALL, in its order.
func TestEggsMatchThePage(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "design", "components", "eggs", "eggs.js"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`const ALL = \[([^\]]*)\]`).FindSubmatch(source)
	if match == nil {
		t.Fatal("eggs.js: const ALL = [...] not found")
	}
	var page []string
	for _, quoted := range regexp.MustCompile(`'([a-z0-9_]+)'`).FindAllSubmatch(match[1], -1) {
		page = append(page, string(quoted[1]))
	}
	if strings.Join(page, ",") != strings.Join(Eggs, ",") {
		t.Errorf("eggs.js has %v, the API knows %v", page, Eggs)
	}
}

func TestReceipts(t *testing.T) {
	receipt := Sign(secret, "konami", noon)
	got, err := Verify(secret, receipt)
	if err != nil || got.ID != "konami" || !got.At.Equal(noon) {
		t.Fatalf("Verify(%q) = %+v, %v", receipt, got, err)
	}
	all := SignAll(secret, noon, 3*time.Hour+7*time.Second)
	if got, err := Verify(secret, all); err != nil || got.ID != All || got.Span != 3*time.Hour+7*time.Second {
		t.Fatalf("Verify(%q) = %+v, %v", all, got, err)
	}

	for name, bad := range map[string]string{
		"another secret":   Sign([]byte("another secret, just as long ok!"), "konami", noon),
		"another egg":      strings.Replace(receipt, "konami", "sudo", 1),
		"moved in time":    strings.Replace(receipt, ".", ".1", 1),
		"all without span": "all." + strings.SplitN(receipt, ".", 2)[1],
		"unknown egg":      Sign(secret, "players", noon),
		"empty":            "",
		"garbage":          "a.b.c.d.e",
		"too long":         strings.Repeat("x", 200),
	} {
		if _, err := Verify(secret, bad); err == nil {
			t.Errorf("%s: %q accepted", name, bad)
		}
	}
}

func TestComplete(t *testing.T) {
	var receipts []string
	for i, egg := range Eggs {
		receipts = append(receipts, Sign(secret, egg, noon.Add(time.Duration(i)*time.Minute)))
	}
	first, last, err := Complete(secret, receipts)
	if err != nil || !first.Equal(noon) || !last.Equal(noon.Add(7*time.Minute)) {
		t.Fatalf("Complete = %v, %v, %v", first, last, err)
	}

	twice := append(append([]string{}, receipts[:7]...), receipts[0])
	missing := receipts[:7]
	withAll := append(append([]string{}, receipts[:7]...), SignAll(secret, noon, time.Minute))
	stranger := append(append([]string{}, receipts[:7]...), Sign([]byte("another secret, just as long ok!"), Eggs[7], noon))
	for name, set := range map[string][]string{"an egg twice": twice, "an egg missing": missing, "all among eggs": withAll, "a stranger's receipt": stranger} {
		if _, _, err := Complete(secret, set); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestShare(t *testing.T) {
	for _, tc := range []struct {
		found, players int
		want           float64
	}{{0, 0, 0}, {3, 0, 0}, {1, 3, 33.33}, {2, 3, 66.67}, {5, 4, 100}, {1, 1000, 0.1}} {
		if got := share(tc.found, tc.players); got != tc.want {
			t.Errorf("share(%d, %d) = %v, want %v", tc.found, tc.players, got, tc.want)
		}
	}
}

// --- the API against a real MySQL -----------------------------------------------------------------

type fixture struct {
	t       *testing.T
	db      *sql.DB
	service *Service
	handler http.Handler
	now     time.Time
}

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
	store, err := cache.New(ctx, "", quiet)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: pool, now: noon}
	clock := func() time.Time { return f.now }
	store.SetClock(clock)
	kyiv, _ := time.LoadLocation("Europe/Kyiv")
	f.service = New(Options{DB: pool, Cache: store, Log: quiet, Secret: secret, Location: kyiv, IgnoreCookie: "__Host-ks", Now: clock})
	srv := server.New(server.Deps{Env: &config.Env{Listen: "127.0.0.1:0"}, DB: pool, Cache: store, Log: quiet, Started: time.Now()})
	f.service.Register(srv.Mux())
	f.handler = srv.Handler()
	return f
}

type answer struct {
	status int
	header http.Header
	body   map[string]any
}

// call sends a request as a browser on the site would; ip tells visitors apart, edit changes the request.
func (f *fixture) call(method, path, ip, body string, edit ...func(*http.Request)) answer {
	f.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Host = "krokosha.com"
	request.RemoteAddr = "127.0.0.1:40000"
	request.Header.Set("X-Real-IP", ip)
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36")
	if method == http.MethodPost {
		request.Header.Set("Origin", "https://krokosha.com")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.Header.Set("Content-Type", "application/json")
	}
	for _, change := range edit {
		change(request)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	out := answer{status: recorder.Code, header: recorder.Header()}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out.body)
	return out
}

func (f *fixture) daily(id string) int {
	f.t.Helper()
	var n int
	err := f.db.QueryRow(`SELECT COALESCE(SUM(n), 0) FROM achievement_daily WHERE id = ?`, id).Scan(&n)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestPlayersAndFindsAreCounted(t *testing.T) {
	f := newFixture(t)
	for i := range 12 {
		if got := f.call(http.MethodPost, "/api/eggs/hello", fmt.Sprintf("203.0.113.%d", 100+i), ""); got.status != http.StatusNoContent {
			t.Fatalf("hello: %d", got.status)
		}
	}
	got := f.call(http.MethodPost, "/api/eggs/konami", "203.0.113.10", "")
	receipt, _ := got.body["receipt"].(string)
	if got.status != http.StatusOK || !strings.HasPrefix(receipt, "konami.") {
		t.Fatalf("found: %d %v", got.status, got.body)
	}
	if _, err := Verify(secret, receipt); err != nil {
		t.Fatalf("the receipt does not verify: %v", err)
	}
	f.call(http.MethodPost, "/api/eggs/konami", "203.0.113.11", "")
	f.call(http.MethodPost, "/api/eggs/sudo", "203.0.113.12", "")
	if f.daily(players) != 12 || f.daily("konami") != 2 || f.daily("sudo") != 1 {
		t.Fatalf("counts: players %d, konami %d, sudo %d", f.daily(players), f.daily("konami"), f.daily("sudo"))
	}

	// The day is the owner's: 23:30 UTC is already tomorrow in Kyiv.
	f.now = time.Date(2026, 9, 24, 23, 30, 0, 0, time.UTC)
	f.call(http.MethodPost, "/api/eggs/cat", "203.0.113.13", "")
	var day string
	if err := f.db.QueryRow(`SELECT DATE_FORMAT(day, '%Y-%m-%d') FROM achievement_daily WHERE id = 'cat'`).Scan(&day); err != nil || day != "2026-09-25" {
		t.Errorf("the day of a find at 23:30 UTC: %q, %v", day, err)
	}

	if err := f.service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats, _, err := f.service.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != len(Eggs)+1 || stats[0].ID != "konami" || stats[0].Found != 2 || stats[0].Players != 12 || stats[0].Percent != 16.67 {
		t.Fatalf("stats: %+v", stats)
	}
	shares := f.call(http.MethodGet, "/api/eggs", "203.0.113.14", "")
	eggs, _ := shares.body["eggs"].(map[string]any)
	if shares.status != http.StatusOK || eggs["konami"] != 16.67 || eggs["croc"] != 0.0 || !strings.Contains(shares.header.Get("Cache-Control"), "max-age") {
		t.Errorf("shares: %d %v %s", shares.status, shares.body, shares.header.Get("Cache-Control"))
	}
}

func TestSharesWaitForEnoughPlayers(t *testing.T) {
	f := newFixture(t)
	for i := range MinPlayers - 1 {
		f.call(http.MethodPost, "/api/eggs/hello", fmt.Sprintf("198.51.100.%d", 1+i), "")
	}
	f.call(http.MethodPost, "/api/eggs/konami", "198.51.100.1", "")
	if err := f.service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	shares := f.call(http.MethodGet, "/api/eggs", "198.51.100.1", "")
	if eggs, _ := shares.body["eggs"].(map[string]any); len(eggs) != 0 {
		t.Errorf("with %d players the site shows shares: %v", MinPlayers-1, eggs)
	}
}

func TestAllNeedsEveryEgg(t *testing.T) {
	f := newFixture(t)
	var receipts []string
	for i, egg := range Eggs {
		f.now = noon.Add(time.Duration(i) * 10 * time.Minute)
		got := f.call(http.MethodPost, "/api/eggs/"+egg, "203.0.113.20", "")
		receipts = append(receipts, got.body["receipt"].(string))
	}
	body, _ := json.Marshal(foundBody{Receipts: receipts})
	got := f.call(http.MethodPost, "/api/eggs/all", "203.0.113.20", string(body))
	receipt, _ := got.body["receipt"].(string)
	all, err := Verify(secret, receipt)
	if got.status != http.StatusOK || err != nil || all.ID != All || all.Span != 70*time.Minute {
		t.Fatalf("all: %d %v → %+v %v", got.status, got.body, all, err)
	}
	if f.daily(All) != 1 {
		t.Errorf("«all» counted %d times", f.daily(All))
	}

	short, _ := json.Marshal(foundBody{Receipts: receipts[:7]})
	if got := f.call(http.MethodPost, "/api/eggs/all", "203.0.113.20", string(short)); got.status != http.StatusBadRequest {
		t.Errorf("seven receipts: %d", got.status)
	}
	forged := append(append([]string{}, receipts[:7]...), Sign([]byte("another secret, just as long ok!"), Eggs[7], noon))
	bad, _ := json.Marshal(foundBody{Receipts: forged})
	if got := f.call(http.MethodPost, "/api/eggs/all", "203.0.113.20", string(bad)); got.status != http.StatusUnprocessableEntity {
		t.Errorf("a forged receipt: %d", got.status)
	}
	if f.daily(All) != 1 {
		t.Errorf("refused requests were counted: %d", f.daily(All))
	}
}

func TestWhoIsNotCounted(t *testing.T) {
	f := newFixture(t)
	// The owner, signed in to the admin area, tries the eggs: receipts, but no statistics.
	owner := f.call(http.MethodPost, "/api/eggs/sudo", "203.0.113.30", "", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "__Host-ks", Value: "x"})
	})
	if owner.status != http.StatusOK || owner.body["receipt"] == nil {
		t.Errorf("owner: %d %v", owner.status, owner.body)
	}
	robot := f.call(http.MethodPost, "/api/eggs/sudo", "203.0.113.31", "", func(r *http.Request) {
		r.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
	})
	if robot.status != http.StatusNoContent || robot.body["receipt"] != nil {
		t.Errorf("robot: %d %v", robot.status, robot.body)
	}
	foreign := f.call(http.MethodPost, "/api/eggs/sudo", "203.0.113.32", "", func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example")
	})
	crossSite := f.call(http.MethodPost, "/api/eggs/hello", "203.0.113.33", "", func(r *http.Request) {
		r.Header.Del("Origin")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	if foreign.status != http.StatusForbidden || crossSite.status != http.StatusForbidden {
		t.Errorf("another site: %d, %d", foreign.status, crossSite.status)
	}
	if unknown := f.call(http.MethodPost, "/api/eggs/players", "203.0.113.34", ""); unknown.status != http.StatusNotFound {
		t.Errorf("a made-up achievement: %d", unknown.status)
	}
	// «Do Not Track» and Global Privacy Control: the receipt (the discount needs it), and no count.
	for i, header := range []string{"DNT", "Sec-GPC"} {
		ip := fmt.Sprintf("203.0.113.%d", 40+i)
		f.call(http.MethodPost, "/api/eggs/hello", ip, "", func(r *http.Request) { r.Header.Set(header, "1") })
		got := f.call(http.MethodPost, "/api/eggs/sudo", ip, "", func(r *http.Request) { r.Header.Set(header, "1") })
		if got.status != http.StatusOK || got.body["receipt"] == nil {
			t.Errorf("%s: %d %v", header, got.status, got.body)
		}
	}
	if f.daily("sudo") != 0 || f.daily(players) != 0 {
		t.Errorf("counted: sudo %d, players %d", f.daily("sudo"), f.daily(players))
	}
}

func TestRateLimits(t *testing.T) {
	f := newFixture(t)
	for range helloPerHour + 3 {
		f.call(http.MethodPost, "/api/eggs/hello", "203.0.113.40", "")
	}
	if f.daily(players) != helloPerHour {
		t.Errorf("one address counted %d players", f.daily(players))
	}
	limited := 0
	for range foundPerHour + 5 {
		if f.call(http.MethodPost, "/api/eggs/croc", "203.0.113.41", "").status == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 5 || f.daily("croc") != foundPerHour {
		t.Errorf("limited %d, counted %d", limited, f.daily("croc"))
	}
}
