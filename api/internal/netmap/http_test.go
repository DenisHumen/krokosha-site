package netmap

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingLimiter allows `limit` requests per key.
type countingLimiter struct {
	mu    sync.Mutex
	seen  map[string]int
	limit int
}

func (l *countingLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[key]++
	return l.seen[key] <= l.limit
}

type client struct {
	t       *testing.T
	handler http.Handler
}

func (c client) get(path string) (int, http.Header, map[string]any) {
	c.t.Helper()
	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		c.t.Fatalf("%s: not JSON: %q", path, recorder.Body.String())
	}
	return recorder.Code, recorder.Header(), body
}

func newTestService(t *testing.T, visitor string, limit int) (*Service, client) {
	t.Helper()
	geo := fakeGeo{
		netip.MustParsePrefix("192.0.2.0/24"):    kyiv,
		netip.MustParsePrefix("198.51.100.0/24"): paris,
	}
	service := NewService(ServiceOptions{
		Geo:      geo,
		Limit:    &countingLimiter{seen: map[string]int{}, limit: limit},
		ClientIP: func(context.Context) net.IP { return net.ParseIP(visitor) },
	})
	mux := http.NewServeMux()
	service.Register(mux)
	return service, client{t: t, handler: mux}
}

func TestServiceBeforeTheFirstMap(t *testing.T) {
	_, c := newTestService(t, "203.0.113.9", 100)
	for _, path := range []string{"/api/net/route?from=1.1.1.1&to=8.8.8.8", "/api/net/as/13335", "/api/net/search?q=x", "/api/net/me"} {
		if status, _, body := c.get(path); status != http.StatusServiceUnavailable || body["error"] != "not_ready" {
			t.Errorf("%s: %d %v", path, status, body)
		}
	}
}

// The test world's addresses are documentation ranges, which the service refuses from visitors:
// the checks here go through the handler with a world that has real-looking addresses.
func TestServiceRoute(t *testing.T) {
	service, c := newTestService(t, "203.0.113.9", 100)
	m := testWorld(t)
	// Addresses a visitor may type: the same networks behind public prefixes.
	for _, r := range []struct {
		prefix string
		asn    uint32
	}{{"81.0.0.0/24", 6000}, {"82.0.0.0/24", 7000}} {
		prefix := netip.MustParsePrefix(r.prefix)
		last := prefix.Addr().As4()
		last[3] = 255
		m.Prefixes.add(prefix.Addr(), netip.AddrFrom4(last), r.asn)
	}
	m.Prefixes.sort()
	service.Use(m)

	status, header, body := c.get("/api/net/route?from=81.0.0.10&to=82.0.0.20")
	if status != http.StatusOK || !strings.Contains(header.Get("Cache-Control"), "max-age") {
		t.Fatalf("route: %d %v", status, body)
	}
	hops, _ := body["hops"].([]any)
	if len(hops) != 4 || body["kind"] != "inferred" {
		t.Fatalf("hops: %v", body)
	}
	third, _ := hops[2].(map[string]any)
	meet, _ := third["meet"].(map[string]any)
	if meet["kind"] != "ix" || meet["name"] != "TEST-IX" || third["rel"] != "customer" {
		t.Errorf("the third hop: %v", third)
	}
	if from, _ := body["from"].(map[string]any); from["asn"] != float64(6000) || from["name"] != "SIX" {
		t.Errorf("from: %v", from)
	}

	for path, code := range map[string]string{
		"/api/net/route?from=81.0.0.10&to=nonsense":     "bad_address",
		"/api/net/route?from=81.0.0.10":                 "bad_address",
		"/api/net/route?from=10.0.0.1&to=82.0.0.20":     "private_address",
		"/api/net/route?from=81.0.0.10&to=100.64.0.1":   "private_address", // carrier NAT
		"/api/net/route?from=81.0.0.10&to=192.0.2.1":    "private_address", // documentation
		"/api/net/route?from=81.0.0.10&to=127.0.0.1":    "private_address",
		"/api/net/route?from=81.0.0.10&to=224.0.0.5":    "private_address",
		"/api/net/route?from=81.0.0.10&to=9.9.9.9":      "not_routed", // nobody in the test world announces it
		"/api/net/route?from=81.0.0.10&to=fe80::1%eth0": "bad_address",
	} {
		if status, _, body := c.get(path); status < 400 || body["error"] != code {
			t.Errorf("%s: %d %v, want %s", path, status, body, code)
		}
	}
}

func TestServiceNetworkAndSearch(t *testing.T) {
	service, c := newTestService(t, "203.0.113.9", 100)
	service.Use(testWorld(t))

	status, _, body := c.get("/api/net/as/AS3000")
	if status != http.StatusOK || body["name"] != "Three" || body["cone"] != float64(3) {
		t.Fatalf("AS3000: %d %v", status, body)
	}
	neighbours, _ := body["neighbours"].(map[string]any)
	providers, _ := neighbours["providers"].([]any)
	peers, _ := neighbours["peers"].([]any)
	customers, _ := neighbours["customers"].([]any)
	if len(providers) != 1 || len(peers) != 1 || len(customers) != 1 {
		t.Errorf("neighbours of 3000: %v", neighbours)
	}
	exchanges, _ := body["exchanges"].([]any)
	if len(exchanges) != 1 {
		t.Errorf("exchanges of 3000: %v", exchanges)
	}
	if status, _, _ := c.get("/api/net/as/64999"); status != http.StatusNotFound {
		t.Errorf("an unknown network: %d", status)
	}

	if _, _, body := c.get("/api/net/search?q=se"); len(body["results"].([]any)) != 1 {
		t.Errorf("search by name: %v", body)
	}
	if _, _, body := c.get("/api/net/search?q=AS6000"); len(body["results"].([]any)) != 1 {
		t.Errorf("search by number: %v", body)
	}
	if _, _, body := c.get("/api/net/search?q=x"); len(body["results"].([]any)) != 0 {
		t.Errorf("a query too short: %v", body)
	}
}

func TestServiceMeAndLimits(t *testing.T) {
	service, c := newTestService(t, "192.0.2.77", 2)
	service.Use(testWorld(t))

	status, header, body := c.get("/api/net/me")
	if status != http.StatusOK || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("me: %d %v", status, header)
	}
	if body["ip"] != "192.0.2.77" || body["asn"] != float64(6000) || body["lat"] != kyiv[0] {
		t.Errorf("me: %v", body)
	}
	c.get("/api/net/me")
	if status, _, body := c.get("/api/net/me"); status != http.StatusTooManyRequests || body["error"] != "busy" {
		t.Errorf("the third request of the minute: %d %v", status, body)
	}
}
