package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-sql-driver/mysql"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
)

var quiet = slog.New(slog.DiscardHandler)

// unreachableDB is a pool whose every ping fails — a database that is down.
func unreachableDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := mysql.NewConfig()
	cfg.Net, cfg.Addr, cfg.Timeout = "tcp", "127.0.0.1:1", time.Second
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool := sql.OpenDB(connector)
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func newServer(t *testing.T, pool *sql.DB, redisURL string) *Server {
	t.Helper()
	store, err := cache.New(context.Background(), redisURL, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(Deps{
		Env:     &config.Env{Listen: "127.0.0.1:0", AdminPath: "/_test1"},
		DB:      pool,
		Cache:   store,
		Log:     quiet,
		Version: "abc123",
		Started: time.Now().Add(-time.Minute),
	})
}

type reply struct {
	status int
	header http.Header
	body   map[string]any
}

func get(t *testing.T, s *Server, path string, headers map[string]string) reply {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = "127.0.0.1:50000"
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(response.Body).Decode(&body)
	return reply{status: response.StatusCode, header: response.Header, body: body}
}

func TestHealthWithDatabaseDown(t *testing.T) {
	s := newServer(t, unreachableDB(t), "")

	got := get(t, s, "/api/health", nil)
	if got.status != http.StatusServiceUnavailable || got.body["status"] != "down" {
		t.Errorf("status %d %v, want 503 down", got.status, got.body)
	}
	checks, _ := got.body["checks"].(map[string]any)
	if checks["mysql"] != "down" || checks["redis"] != "disabled" {
		t.Errorf("checks = %v", checks)
	}
}

func TestHealthHidesDetailsFromVisitors(t *testing.T) {
	s := newServer(t, unreachableDB(t), "")

	local := get(t, s, "/api/health", nil).body
	if local["version"] != "abc123" || local["checks"] == nil {
		t.Errorf("a direct call on the server must see the details: %v", local)
	}
	public := get(t, s, "/api/health", map[string]string{"X-Real-IP": "203.0.113.7"}).body
	if public["version"] != nil || public["checks"] != nil || public["uptime_s"] != nil {
		t.Errorf("a visitor must see the bare status only: %v", public)
	}
	if public["status"] == nil {
		t.Error("status is missing")
	}
}

func TestHealthOKAndDegraded(t *testing.T) {
	cfg := testenv.MySQL(t)
	pool, err := db.Open(context.Background(), cfg, 10*time.Second, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	redis := miniredis.RunT(t)
	s := newServer(t, pool, "redis://"+redis.Addr()+"/0")

	got := get(t, s, "/api/health", nil)
	if got.status != http.StatusOK || got.body["status"] != "ok" {
		t.Fatalf("healthy: %d %v", got.status, got.body)
	}

	// Redis is optional: without it the service is degraded but still answers 200,
	// so the installer and monitoring do not treat it as an outage.
	redis.Close()
	got = get(t, s, "/api/health", nil)
	if got.status != http.StatusOK || got.body["status"] != "degraded" {
		t.Errorf("without Redis: %d %v, want 200 degraded", got.status, got.body)
	}
}

func TestResponsesCarrySecurityHeadersAndRequestID(t *testing.T) {
	s := newServer(t, unreachableDB(t), "")
	got := get(t, s, "/api/nope", nil)

	if got.status != http.StatusNotFound {
		t.Errorf("unknown API path: %d, want 404", got.status)
	}
	if !strings.HasPrefix(got.header.Get("Content-Type"), "application/json") {
		t.Errorf("API errors are JSON, got %q", got.header.Get("Content-Type"))
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"X-Robots-Tag":           "noindex, nofollow",
	} {
		if value := got.header.Get(header); value != want {
			t.Errorf("%s = %q, want %q", header, value, want)
		}
	}
	if len(got.header.Get("X-Request-Id")) != 16 {
		t.Errorf("X-Request-Id = %q", got.header.Get("X-Request-Id"))
	}
}

func TestPanicBecomesA500(t *testing.T) {
	s := newServer(t, unreachableDB(t), "")
	s.Mux().HandleFunc("GET /api/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	got := get(t, s, "/api/boom", nil)
	if got.status != http.StatusInternalServerError || got.body["error"] != "internal error" {
		t.Errorf("%d %v", got.status, got.body)
	}
}

func TestClientIPTrustsTheProxyOnlyOnLoopback(t *testing.T) {
	cases := []struct {
		remote, realIP, want string
	}{
		{"127.0.0.1:40000", "203.0.113.7", "203.0.113.7"},
		{"[::1]:40000", "2001:db8::1", "2001:db8::1"},
		{"127.0.0.1:40000", "", "127.0.0.1"},
		{"127.0.0.1:40000", "not-an-ip", "127.0.0.1"},
		// Someone reaching the port directly cannot choose their own address.
		{"198.51.100.9:40000", "203.0.113.7", "198.51.100.9"},
	}
	for _, tc := range cases {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = tc.remote
		if tc.realIP != "" {
			request.Header.Set("X-Real-IP", tc.realIP)
		}
		if got := clientIP(request).String(); got != tc.want {
			t.Errorf("remote %s, X-Real-IP %q: got %s, want %s", tc.remote, tc.realIP, got, tc.want)
		}
	}
}

func TestLoggedPathHasNoSecrets(t *testing.T) {
	for path, want := range map[string]string{
		"/api/leads":                     "/api/leads",
		"/_k7f3a9":                       "/<admin>",
		"/_k7f3a9/leads/42":              "/<admin>/leads/42",
		"/_k7f3a9x/leads":                "/_k7f3a9x/leads", // another path that merely starts alike
		"/api/telegram/0123456789abcdef": "/api/telegram/<secret>",
		"/api/telegram/":                 "/api/telegram/",
		"/uk/account/":                   "/uk/account/",
	} {
		if got := loggedPath(path, "/_k7f3a9"); got != want {
			t.Errorf("%s → %s, want %s", path, got, want)
		}
	}
}
