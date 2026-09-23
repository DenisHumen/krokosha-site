package netmap

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/trace"
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

// mapCache is a cache in a map, with what was asked of it.
type mapCache struct {
	mu     sync.Mutex
	values map[string]string
	hits   int
}

func (c *mapCache) Get(_ context.Context, key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	if ok {
		c.hits++
	}
	return value, ok
}

func (c *mapCache) Set(_ context.Context, key, data string, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = data
}

func TestServiceKeepsRoutes(t *testing.T) {
	store := &mapCache{values: map[string]string{}}
	service := NewService(ServiceOptions{
		Cache:    store,
		ClientIP: func(context.Context) net.IP { return net.ParseIP("203.0.113.9") },
	})
	mux := http.NewServeMux()
	service.Register(mux)
	m := testWorld(t)
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

	get := func() string {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/net/route?from=81.0.0.10&to=82.0.0.20", nil))
		if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != routeCaching {
			t.Fatalf("route: %d %v", recorder.Code, recorder.Header())
		}
		return recorder.Body.String()
	}
	first := get()
	if len(store.values) != 1 || store.hits != 0 {
		t.Fatalf("after the first route: %d kept, %d hits", len(store.values), store.hits)
	}
	for key, value := range store.values {
		if !strings.HasPrefix(key, "net:route:") || !strings.HasSuffix(key, ":81.0.0.10:82.0.0.20") || value+"\n" != first {
			t.Errorf("kept: %q = %q", key, value)
		}
	}
	if second := get(); second != first || store.hits != 1 {
		t.Errorf("the second route: %d hits, the same answer %v", store.hits, second == first)
	}
	// A new map is a new key: yesterday's routes are not given for today's map.
	next := *m
	next.Built = m.Built.Add(24 * time.Hour)
	service.Use(&next)
	get()
	if len(store.values) != 2 {
		t.Errorf("%d routes kept after a new map", len(store.values))
	}
}

func TestServiceTraces(t *testing.T) {
	store := &mapCache{values: map[string]string{}}
	var calls atomic.Int32
	release := make(chan struct{})
	service := NewService(ServiceOptions{
		Cache:    store,
		Geo:      fakeGeo{netip.MustParsePrefix("81.0.0.0/24"): kyiv},
		ClientIP: func(context.Context) net.IP { return net.ParseIP("203.0.113.9") },
		Resolve: func(_ context.Context, addr string) ([]string, error) {
			if addr == "81.0.0.1" {
				return []string{"gw.six.example."}, nil
			}
			return nil, errors.New("no name")
		},
		Trace: func(ctx context.Context, to netip.Addr) (*trace.Result, error) {
			calls.Add(1)
			if to == netip.MustParseAddr("82.0.0.99") { // a slow one, to fill the places
				<-release
			}
			ms := func(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }
			return &trace.Result{
				From: netip.MustParseAddr("81.0.0.10"), To: to, Reached: true,
				Hops: []trace.Hop{
					{TTL: 1, Addr: netip.MustParseAddr("81.0.0.1"), Sent: 3, RTTs: []time.Duration{ms(0.8), ms(0.7), ms(0.75)}},
					{TTL: 2, Sent: 3},
					{TTL: 3, Addr: netip.MustParseAddr("80.81.192.7"), Others: []netip.Addr{netip.MustParseAddr("80.81.192.8")}, Sent: 3, RTTs: []time.Duration{ms(21.34)}},
					{TTL: 4, Addr: to, Sent: 3, RTTs: []time.Duration{ms(30), ms(31), ms(30.5)}, Reached: true},
				},
				Connect: &trace.Connect{Port: 443, Sent: 5, RTTs: []time.Duration{ms(30.2)}},
			}, nil
		},
	})
	mux := http.NewServeMux()
	service.Register(mux)
	m := testWorld(t)
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
	c := client{t: t, handler: mux}
	if status, _, body := c.get("/api/net/trace?to=82.0.0.20"); status != http.StatusServiceUnavailable || body["error"] != "not_ready" {
		t.Errorf("before the map: %d %v", status, body)
	}
	service.Use(m)

	status, header, body := c.get("/api/net/trace?to=82.0.0.20")
	if status != http.StatusOK || header.Get("Cache-Control") != traceCaching {
		t.Fatalf("trace: %d %v", status, body)
	}
	from, _ := body["from"].(map[string]any)
	if from["ip"] != "81.0.0.10" || from["asn"] != float64(6000) || from["lat"] != kyiv[0] {
		t.Errorf("from: %v", from)
	}
	hops, _ := body["hops"].([]any)
	if len(hops) != 4 || body["reached"] != true {
		t.Fatalf("hops: %v", body)
	}
	first, second, third := hops[0].(map[string]any), hops[1].(map[string]any), hops[2].(map[string]any)
	if first["host"] != "gw.six.example" || first["name"] != "SIX" || first["asn"] != float64(6000) || len(first["rtts"].([]any)) != 3 {
		t.Errorf("the first hop: %v", first)
	}
	if second["ip"] != nil || second["sent"] != float64(3) || len(second["rtts"].([]any)) != 0 {
		t.Errorf("a silent hop: %v", second)
	}
	// A port at an exchange point is placed at the exchange point.
	if third["ix"] != "TEST-IX" || third["lat"] != frankfurt[0] || third["others"].([]any)[0] != "80.81.192.8" || third["rtts"].([]any)[0] != 21.3 {
		t.Errorf("the hop at the exchange point: %v", third)
	}
	// …whose address is the port of 7000 there, as PeeringDB lists it.
	if third["asn"] != float64(7000) || third["name"] != "SEVEN" {
		t.Errorf("whose port: %v", third)
	}
	if connect, _ := body["connect"].(map[string]any); connect["port"] != float64(443) || connect["sent"] != float64(5) {
		t.Errorf("connect: %v", connect)
	}

	// The same measurement again comes from the cache: nothing is sent.
	if status, _, _ := c.get("/api/net/trace?to=82.0.0.20"); status != http.StatusOK || calls.Load() != 1 {
		t.Errorf("the second time: %d, %d traces", status, calls.Load())
	}
	// Addresses that are not on the internet are not traced.
	if status, _, body := c.get("/api/net/trace?to=10.1.2.3"); status < 400 || body["error"] != "private_address" {
		t.Errorf("a private address: %d %v", status, body)
	}

	// Two traces at once at most: a third waits for nobody, it is told to try again.
	done := make(chan int, 2)
	for range 2 {
		go func() {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/net/trace?to=82.0.0.99", nil))
			done <- recorder.Code
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(service.tracing) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if status, _, body := c.get("/api/net/trace?to=82.0.0.30"); status != http.StatusTooManyRequests || body["error"] != "busy" {
		t.Errorf("a third trace at once: %d %v", status, body)
	}
	close(release)
	for range 2 {
		if code := <-done; code != http.StatusOK {
			t.Errorf("a trace that waited: %d", code)
		}
	}
}
