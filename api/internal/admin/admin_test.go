package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base32"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/auth"
	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/nginxlog"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
	"github.com/DenisHumen/krokosha-site/api/internal/sysstatus"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

var quiet = slog.New(slog.DiscardHandler)

const (
	prefix   = "/_test1234"
	password = "correct horse battery staple"
)

type site struct {
	t       *testing.T
	handler http.Handler
	cookie  string // value of the session cookie, once signed in
	db      *sql.DB
	feed    chan analytics.Live // what the «analytics service» publishes to the live feed
	state   string              // the server's state directory: build report, rebuild requests
	leads   *leads.Store
	bot     telegram.Status // what the bot reports; the zero value — no token, no bot
}

// The dashboards are tested on a fixed day, so that the numbers on the page are known.
var reportDay = time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)

func newSite(t *testing.T) *site {
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
	accounts := auth.New(pool, store, quiet)
	if err := accounts.CreateUser(ctx, "denis", password); err != nil {
		t.Fatal(err)
	}

	s := &site{t: t, db: pool, feed: make(chan analytics.Live, 4), state: t.TempDir(), leads: leads.NewStore(pool, nil)}
	if err := os.MkdirAll(filepath.Join(s.state, "requests"), 0o755); err != nil {
		t.Fatal(err)
	}
	system := sysstatus.New(sysstatus.Options{StateDir: s.state, WWWDir: t.TempDir(), ContentDir: t.TempDir(), DB: pool, Cache: store,
		Version: "test", Started: time.Now(), Now: func() time.Time { return reportDay }})
	srv := server.New(server.Deps{Env: &config.Env{Listen: "127.0.0.1:0"}, DB: pool, Cache: store, Log: quiet, Started: time.Now()})
	panel, err := New(Options{
		Prefix: prefix, SiteHost: "krokosha.xyz", Auth: accounts, Log: quiet, Version: "test",
		Reports: analytics.NewReports(pool, time.UTC, func() time.Time { return reportDay }),
		Feed:    func() (<-chan analytics.Live, func()) { return s.feed, func() {} },
		Active:  func(context.Context, time.Duration) int { return 3 },
		Traffic: nginxlog.NewReports(pool, time.UTC), System: system,
		LogPolled: func() time.Time { return time.Now().Add(-7 * time.Second) },
		Leads:     s.leads, Form: testForm,
		BotAccess: telegram.NewAccess(pool, nil),
		BotStatus: func() (telegram.Status, bool) { return s.bot, s.bot.Mode != "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	panel.Register(srv.Mux())
	s.handler = srv.Handler()
	return s
}

type reply struct {
	status   int
	header   http.Header
	body     string
	location string
}

func (s *site) do(method, path string, form url.Values, headers map[string]string) reply {
	s.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request := httptest.NewRequest(method, path, body)
	request.Host = "krokosha.xyz"
	request.RemoteAddr = "127.0.0.1:40000"
	request.Header.Set("X-Real-IP", "203.0.113.7")
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", "https://krokosha.xyz")
	}
	if s.cookie != "" {
		request.AddCookie(&http.Cookie{Name: cookieName, Value: s.cookie})
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, request)
	result := recorder.Result()
	defer result.Body.Close()
	raw, _ := io.ReadAll(result.Body)
	for _, cookie := range result.Cookies() {
		if cookie.Name == cookieName {
			s.cookie = cookie.Value
		}
	}
	return reply{status: result.StatusCode, header: result.Header, body: string(raw), location: result.Header.Get("Location")}
}

var reCSRF = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (s *site) csrf() string {
	s.t.Helper()
	match := reCSRF.FindStringSubmatch(s.do(http.MethodGet, prefix+"/account", nil, nil).body)
	if match == nil {
		s.t.Fatal("no CSRF token on the account page")
	}
	return match[1]
}

func (s *site) signIn() {
	s.t.Helper()
	got := s.do(http.MethodPost, prefix+"/login", url.Values{"login": {"denis"}, "password": {password}}, nil)
	if got.status != http.StatusSeeOther || s.cookie == "" {
		s.t.Fatalf("sign-in: status %d, cookie %q", got.status, s.cookie)
	}
}

func TestAnonymousVisitorsSeeOnlyTheLoginForm(t *testing.T) {
	s := newSite(t)

	for _, path := range []string{prefix + "/", prefix + "/account", prefix + "/account/totp/qr.png"} {
		if got := s.do(http.MethodGet, path, nil, nil); got.status != http.StatusSeeOther || got.location != prefix+"/login" {
			t.Errorf("GET %s: %d → %q, want a redirect to the login form", path, got.status, got.location)
		}
	}
	if got := s.do(http.MethodPost, prefix+"/account/password", url.Values{"csrf": {"x"}}, nil); got.status != http.StatusUnauthorized {
		t.Errorf("POST without a session: %d, want 401", got.status)
	}
	// A guessed prefix reveals nothing: it is just another 404.
	if got := s.do(http.MethodGet, "/_admin/login", nil, nil); got.status != http.StatusNotFound {
		t.Errorf("wrong prefix: %d, want 404", got.status)
	}

	page := s.do(http.MethodGet, prefix+"/login", nil, nil)
	if page.status != http.StatusOK || !strings.Contains(page.body, `name="password"`) {
		t.Fatalf("login page: %d", page.status)
	}
	if page.header.Get("Cache-Control") != "no-store" {
		t.Error("admin pages must not be cached")
	}
	csp := page.header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'", "form-action 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(page.body, "<script>") || strings.Contains(page.body, "style=") {
		t.Error("the admin area must work without inline scripts and styles")
	}
	if got := s.do(http.MethodGet, prefix+"/static/admin.css", nil, nil); got.status != http.StatusOK || !strings.Contains(got.header.Get("Content-Type"), "text/css") {
		t.Errorf("stylesheet: %d %s", got.status, got.header.Get("Content-Type"))
	}
}

func TestSignInAndOut(t *testing.T) {
	s := newSite(t)

	bad := s.do(http.MethodPost, prefix+"/login", url.Values{"login": {`"><script>alert(1)</script>`}, "password": {"nope nope nope"}}, nil)
	if bad.status != http.StatusUnauthorized || !strings.Contains(bad.body, "Неверный логин или пароль") {
		t.Errorf("wrong password: %d", bad.status)
	}
	if strings.Contains(bad.body, "<script>alert") {
		t.Error("the login typed by the visitor came back unescaped")
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, prefix+"/login", strings.NewReader(url.Values{"login": {"Denis"}, "password": {password}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.RemoteAddr = "127.0.0.1:40000"
	s.handler.ServeHTTP(recorder, request)
	result := recorder.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in: %d", result.StatusCode)
	}
	cookies := result.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies: %v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != "__Host-ks" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Errorf("session cookie is not locked down: %+v", cookie)
	}
	s.cookie = cookie.Value

	if got := s.do(http.MethodGet, prefix+"/", nil, nil); got.status != http.StatusOK || !strings.Contains(got.body, "denis") {
		t.Errorf("overview after sign-in: %d", got.status)
	}
	if got := s.do(http.MethodGet, prefix+"/login", nil, nil); got.status != http.StatusSeeOther {
		t.Errorf("login page while signed in: %d, want a redirect to the overview", got.status)
	}

	old := s.cookie
	if got := s.do(http.MethodPost, prefix+"/logout", url.Values{"csrf": {s.csrf()}}, nil); got.status != http.StatusSeeOther {
		t.Fatalf("logout: %d", got.status)
	}
	s.cookie = old // a thief replaying the cookie after the owner signed out
	if got := s.do(http.MethodGet, prefix+"/", nil, nil); got.status != http.StatusSeeOther {
		t.Errorf("the session survived the logout: %d", got.status)
	}
}

func TestForgedRequestsAreRefused(t *testing.T) {
	s := newSite(t)
	s.signIn()
	form := func(csrf string) url.Values {
		return url.Values{"csrf": {csrf}, "current": {password}, "new": {"another long password 1"}, "repeat": {"another long password 1"}}
	}

	cases := []struct {
		name    string
		form    url.Values
		headers map[string]string
	}{
		{"no CSRF token", form(""), nil},
		{"wrong CSRF token", form("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), nil},
		{"another site's form", form(s.csrf()), map[string]string{"Origin": "https://evil.example"}},
		{"cross-site fetch", form(s.csrf()), map[string]string{"Sec-Fetch-Site": "cross-site"}},
	}
	for _, tc := range cases {
		if got := s.do(http.MethodPost, prefix+"/account/password", tc.form, tc.headers); got.status != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", tc.name, got.status)
		}
	}
	// None of them changed the password.
	if got := s.do(http.MethodPost, prefix+"/account/password", form(s.csrf()), nil); got.status != http.StatusSeeOther || !strings.Contains(got.location, "ok=password") {
		t.Errorf("a genuine request: %d → %q", got.status, got.location)
	}
}

func TestTwoFactorEnrolmentAndLogin(t *testing.T) {
	s := newSite(t)
	s.signIn()

	if got := s.do(http.MethodPost, prefix+"/account/totp/begin", url.Values{"csrf": {s.csrf()}}, nil); got.status != http.StatusSeeOther {
		t.Fatalf("begin: %d", got.status)
	}
	page := s.do(http.MethodGet, prefix+"/account", nil, nil).body
	match := regexp.MustCompile(`class="mono secret">([A-Z2-7 ]+)<`).FindStringSubmatch(page)
	if match == nil {
		t.Fatal("the secret is not shown during enrolment")
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ReplaceAll(match[1], " ", ""))
	if err != nil {
		t.Fatal(err)
	}

	qr := s.do(http.MethodGet, prefix+"/account/totp/qr.png", nil, nil)
	if qr.status != http.StatusOK || qr.header.Get("Content-Type") != "image/png" {
		t.Fatalf("QR code: %d %s", qr.status, qr.header.Get("Content-Type"))
	}
	if _, err := png.Decode(bytes.NewReader([]byte(qr.body))); err != nil {
		t.Errorf("the QR code is not a PNG: %v", err)
	}

	if got := s.do(http.MethodPost, prefix+"/account/totp/confirm", url.Values{"csrf": {s.csrf()}, "code": {"000000"}}, nil); got.status != http.StatusBadRequest {
		t.Errorf("confirming with a wrong code: %d", got.status)
	}
	if got := s.do(http.MethodPost, prefix+"/account/totp/confirm", url.Values{"csrf": {s.csrf()}, "code": {auth.TOTPCode(secret, time.Now())}}, nil); got.status != http.StatusSeeOther {
		t.Fatalf("confirm: %d", got.status)
	}
	if got := s.do(http.MethodGet, prefix+"/account/totp/qr.png", nil, nil); got.status != http.StatusNotFound {
		t.Errorf("the QR code is still served after enrolment: %d", got.status)
	}

	// A new browser: password first, then the code — and the password never comes back in the page.
	fresh := &site{t: t, handler: s.handler}
	step1 := fresh.do(http.MethodPost, prefix+"/login", url.Values{"login": {"denis"}, "password": {password}}, nil)
	if step1.status != http.StatusOK || fresh.cookie != "" || !strings.Contains(step1.body, `name="code"`) {
		t.Fatalf("step one: %d, cookie %q", step1.status, fresh.cookie)
	}
	if strings.Contains(step1.body, password) {
		t.Error("the password was sent back to the browser")
	}
	pending := regexp.MustCompile(`name="pending" value="([^"]+)"`).FindStringSubmatch(step1.body)
	if pending == nil {
		t.Fatal("no ticket for the second step")
	}
	if got := fresh.do(http.MethodPost, prefix+"/login/code", url.Values{"pending": {pending[1]}, "code": {"123456"}}, nil); got.status != http.StatusUnauthorized || !strings.Contains(got.body, `name="code"`) {
		t.Errorf("wrong code: %d, want the code form again", got.status)
	}
	// The code that confirmed the enrolment is spent; the next 30-second step works.
	next := auth.TOTPCode(secret, time.Now().Add(30*time.Second))
	if got := fresh.do(http.MethodPost, prefix+"/login/code", url.Values{"pending": {pending[1]}, "code": {next}}, nil); got.status != http.StatusSeeOther || fresh.cookie == "" {
		t.Errorf("right code: %d, cookie %q", got.status, fresh.cookie)
	}
}

func TestSessionsCanBeRevoked(t *testing.T) {
	s := newSite(t)
	s.signIn()
	other := &site{t: t, handler: s.handler}
	other.signIn()

	page := s.do(http.MethodGet, prefix+"/account", nil, nil).body
	ids := regexp.MustCompile(`name="id" value="([0-9a-f]{16})"`).FindAllStringSubmatch(page, -1)
	if len(ids) != 1 || !strings.Contains(page, "этот сеанс") || !strings.Contains(page, "203.0.113.0/24") {
		t.Fatalf("session list: %d revocable, page mentions the truncated address: %v", len(ids), strings.Contains(page, "203.0.113.0/24"))
	}
	if strings.Contains(page, "203.0.113.7") {
		t.Error("the full address is shown")
	}
	if got := s.do(http.MethodPost, prefix+"/account/sessions/revoke", url.Values{"csrf": {s.csrf()}, "id": {ids[0][1]}}, nil); got.status != http.StatusSeeOther {
		t.Fatalf("revoke: %d", got.status)
	}
	if got := other.do(http.MethodGet, prefix+"/", nil, nil); got.status != http.StatusSeeOther {
		t.Errorf("the revoked browser is still signed in: %d", got.status)
	}
}
