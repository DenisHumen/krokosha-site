package netmap

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// pretendSources serves what the real sources serve, a little of it: CAIDA has only last month's
// file, RouteViews only yesterday's snapshot — the first days of a month, early in the morning.
type pretendSources struct {
	*httptest.Server
	mu       sync.Mutex
	requests map[string]int
	agents   map[string]bool
}

func newPretendSources(t *testing.T) *pretendSources {
	t.Helper()
	caida, err := os.ReadFile("testdata/as-rel2.txt.bz2")
	if err != nil {
		t.Fatal(err)
	}
	rib, err := os.ReadFile("testdata/rib.mrt.bz2")
	if err != nil {
		t.Fatal(err)
	}
	var table bytes.Buffer
	zw := gzip.NewWriter(&table)
	fmt.Fprint(zw, "192.0.2.0\t192.0.2.255\t6000\tUA\tSIX\n198.51.100.0\t198.51.100.255\t7000\tFR\tSEVEN\n")
	_ = zw.Close()
	peeringdb := map[string]string{
		"net":      `[{"asn":7000,"name":"Seven","info_type":"Content"}]`,
		"ix":       `[{"id":1,"name":"TEST-IX","city":"Frankfurt","country":"DE"}]`,
		"fac":      `[{"id":10,"name":"FRA1","city":"Frankfurt","country":"DE","latitude":50.11,"longitude":8.68}]`,
		"netixlan": `[{"asn":3000,"ix_id":1,"ixlan_id":1,"speed":100000,"operational":true},{"asn":7000,"ix_id":1,"ixlan_id":1,"speed":400000,"operational":true}]`,
		"netfac":   `[{"local_asn":3000,"fac_id":10}]`,
		"ixfac":    `[{"ix_id":1,"fac_id":10}]`,
		"ixlan":    `[{"id":1,"ix_id":1}]`,
		"ixpfx":    `[{"ixlan_id":1,"prefix":"80.81.192.0/21"}]`,
	}
	p := &pretendSources{requests: map[string]int{}, agents: map[string]bool{}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests[r.URL.Path]++
		p.agents[r.UserAgent()] = true
		p.mu.Unlock()
		switch {
		case r.URL.Path == "/caida/20260801.as-rel2.txt.bz2":
			_, _ = w.Write(caida)
		case r.URL.Path == "/iptoasn.tsv.gz":
			_, _ = w.Write(table.Bytes())
		case r.URL.Path == "/routeviews/route-views2/bgpdata/2026.08/RIBS/rib.20260831.0000.bz2":
			_, _ = w.Write(rib)
		case strings.HasPrefix(r.URL.Path, "/pdb/"):
			object := strings.TrimPrefix(r.URL.Path, "/pdb/")
			if data, ok := peeringdb[object]; ok && r.URL.Query().Get("limit") == "0" {
				_, _ = fmt.Fprintf(w, `{"meta":{},"data":%s}`, data)
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *pretendSources) count(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[path]
}

func TestFetchLoadBuild(t *testing.T) {
	sources := newPretendSources(t)
	dir := t.TempDir()
	fetcher := &Fetcher{
		Places: Places{
			CAIDA: sources.URL + "/caida", IPtoASN: sources.URL + "/iptoasn.tsv.gz",
			RouteViews: sources.URL + "/routeviews", Collectors: []string{"route-views2"}, PeeringDB: sources.URL + "/pdb",
		},
		Client:    sources.Client(),
		UserAgent: "krokosha-site-netmap/test",
		Now:       func() time.Time { return time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC) },
	}
	if err := fetcher.FetchAll(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	// This month's CAIDA file is not out yet: last month's; today's snapshot neither: yesterday's.
	if stamp, _ := os.ReadFile(filepath.Join(dir, fileCAIDA+".month")); strings.TrimSpace(string(stamp)) != "20260801" {
		t.Errorf("CAIDA month: %q", stamp)
	}
	if sources.count("/caida/20260901.as-rel2.txt.bz2") != 1 || sources.count("/routeviews/route-views2/bgpdata/2026.09/RIBS/rib.20260901.0000.bz2") != 1 {
		t.Errorf("the newest files were not asked for first: %v", sources.requests)
	}
	if len(sources.agents) != 1 || !sources.agents["krokosha-site-netmap/test"] {
		t.Errorf("user agents: %v", sources.agents)
	}

	// The next day: CAIDA's file of the month is here already, nothing is downloaded again.
	if err := fetcher.FetchAll(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if n := sources.count("/caida/20260801.as-rel2.txt.bz2"); n != 1 {
		t.Errorf("last month's CAIDA file was downloaded %d times", n)
	}

	loaded, err := LoadSources(dir, []string{"route-views2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Report) != 4 || !strings.Contains(loaded.Report[2], "2 peers, 4 routes, 6 new pairs") {
		t.Errorf("report: %q", loaded.Report)
	}
	model := Build(loaded.Sources)
	// 6 links of CAIDA and 2 that only today's table has (1299–8000, 8000–6000).
	if len(model.Nodes) != 6 || model.Links() != 8 {
		t.Fatalf("%d networks, %d links", len(model.Nodes), model.Links())
	}
	if n := node(t, model, 7000); n.Type != "Content" || n.Name != "SEVEN" {
		t.Errorf("7000: %+v", n)
	}
	var overview bytes.Buffer
	if err := model.WriteOverview(&overview, DefaultOverview); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(overview.Bytes(), []byte("KNM1")) {
		t.Errorf("overview starts with %q", overview.Bytes()[:4])
	}

	// A source that fails keeps its old file and says so; the others still come.
	fetcher.Places.IPtoASN = sources.URL + "/gone.tsv.gz"
	err = fetcher.FetchAll(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "iptoasn") || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("a failing source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, fileIPtoASN)); err != nil {
		t.Errorf("the old table was lost: %v", err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".*")); len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestLoadSourcesNeedsCAIDA(t *testing.T) {
	if _, err := LoadSources(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "caida") {
		t.Errorf("an empty data directory: %v", err)
	}
}

// testdata/world is a data directory as the sync leaves it, a small world instead of the internet:
// the installer's test builds the map from it without downloading anything (deploy/ci/test-install.sh).
func TestTheSampleWorld(t *testing.T) {
	loaded, err := LoadSources("testdata/world", DefaultPlaces.Collectors)
	if err != nil {
		t.Fatal(err)
	}
	m := Build(loaded.Sources)
	if len(m.Nodes) != 6 || m.Links() != 8 || len(m.IXs) != 1 || len(m.Facilities) != 3 {
		t.Fatalf("%d networks, %d links, %d exchange points, %d data centres", len(m.Nodes), m.Links(), len(m.IXs), len(m.Facilities))
	}
	// What the test asks the API: addresses it takes for the internet's, with a way between them.
	from, to := netip.MustParseAddr("81.0.0.10"), netip.MustParseAddr("82.0.0.20")
	for _, addr := range []netip.Addr{from, to} {
		if _, bad := publicAddress(addr.String()); bad != nil {
			t.Errorf("%s is refused: %s", addr, bad.code)
		}
	}
	route, err := m.Route(from, to, nil)
	if err != nil || route.Hops[0].ASN != 6000 || route.Hops[len(route.Hops)-1].ASN != 7000 {
		t.Fatalf("81.0.0.10 → 82.0.0.20: %+v, %v", route, err)
	}
}
