package netmap

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

// fakeGeo places addresses from a table: the prefix they fall into.
type fakeGeo map[netip.Prefix][2]float64

func (g fakeGeo) Where(addr netip.Addr) (float64, float64, bool) {
	for prefix, place := range g {
		if prefix.Contains(addr) {
			return place[0], place[1], true
		}
	}
	return 0, 0, false
}

// Places of the test world.
var (
	kyiv      = [2]float64{50.45, 30.52}
	paris     = [2]float64{48.85, 2.35}
	frankfurt = [2]float64{50.11, 8.68}
	newYork   = [2]float64{40.71, -74.0}
)

func ptr(value float64) *float64 { return &value }

// testWorld is a small internet:
//
//	1000 ━━ peer ━━ 2000            two providers of everything («tier 1»)
//	 │  └─ 8000      │  └─ 7000 ─ peer ─ 3000
//	3000            4000
//	   └── 5000 ────┘                5000 has two providers
//	        │
//	       6000                      a customer of a customer
//
// and a chain 9100 → 9101 → 9102 → 9103 → 9200 of «customer» links that a leak made up, next to a
// plain peering 9100 ━ 9200.
func testWorld(t *testing.T) *Model {
	t.Helper()
	links := []Link{
		NewLink(1000, 2000, RelPeer, SourceBGP),
		NewLink(1000, 3000, RelProvider, SourceBGP),
		NewLink(1000, 8000, RelProvider, SourceBGP),
		NewLink(2000, 4000, RelProvider, SourceBGP),
		NewLink(2000, 7000, RelProvider, SourceBGP),
		NewLink(3000, 7000, RelPeer, SourceMLP),
		NewLink(3000, 5000, RelProvider, SourceBGP),
		NewLink(4000, 5000, RelProvider, SourceBGP),
		NewLink(5000, 6000, RelProvider, SourceBGP),
		NewLink(9100, 9200, RelPeer, SourceBGP),
		NewLink(9100, 9101, RelProvider, SourceBGP),
		NewLink(9101, 9102, RelProvider, SourceBGP),
		NewLink(9102, 9103, RelProvider, SourceBGP),
		NewLink(9103, 9200, RelProvider, SourceBGP),
		// Seen in a table today, and by CAIDA as a customer link: CAIDA's relation wins.
		NewLink(3000, 5000, RelUnknown, SourceRIB),
		// Seen in a table only.
		NewLink(8000, 4000, RelUnknown, SourceRIB),
	}
	prefixes := &Prefixes{}
	for _, r := range []struct {
		prefix string
		asn    uint32
	}{
		{"192.0.2.0/24", 6000}, {"198.51.100.0/24", 7000}, {"1.1.1.0/24", 7000}, {"203.0.113.0/24", 8000},
	} {
		prefix := netip.MustParsePrefix(r.prefix)
		last := prefix.Addr().As4()
		last[3] = 255
		prefixes.add(prefix.Addr(), netip.AddrFrom4(last), r.asn)
	}
	prefixes.sort()
	pdb := &PeeringDB{
		Networks: []PDBNetwork{{ASN: 7000, Name: "Seven", Type: "Content"}, {ASN: 3000, Name: "Three", Type: "NSP"}},
		IXs:      []PDBIX{{ID: 1, Name: "TEST-IX", City: "Frankfurt", Country: "DE"}, {ID: 2, Name: "EMPTY-IX", City: "Nowhere"}},
		Facilities: []PDBFacility{
			{ID: 10, Name: "FRA1", City: "Frankfurt", Country: "DE", Lat: ptr(frankfurt[0]), Lon: ptr(frankfurt[1])},
			{ID: 20, Name: "NYC1", City: "New York", Country: "US", Lat: ptr(newYork[0]), Lon: ptr(newYork[1])},
			{ID: 30, Name: "No coordinates", City: "Somewhere"},
		},
		Ports: []PDBPort{
			{ASN: 3000, IX: 1, LAN: 1, Speed: 100000, IPv4: "80.81.192.3", Operational: true},
			{ASN: 7000, IX: 1, LAN: 1, Speed: 100000, IPv4: "80.81.192.7", Operational: true},
			{ASN: 7000, IX: 1, LAN: 1, Speed: 100000, IPv4: "80.81.192.8", Operational: true},
			{ASN: 4000, IX: 1, LAN: 1, Speed: 10000, Operational: false}, // not in service: not counted
		},
		Presence:   []PDBPresence{{ASN: 3000, Facility: 20}, {ASN: 5000, Facility: 20}, {ASN: 1000, Facility: 30}},
		IXFacility: []PDBIXFacility{{IX: 1, Facility: 10}},
		LANs:       []PDBLAN{{ID: 1, IX: 1}},
		LANPrefix:  []PDBPrefix{{LAN: 1, Prefix: "80.81.192.0/21"}},
	}
	geo := fakeGeo{
		netip.MustParsePrefix("192.0.2.0/24"):    kyiv,
		netip.MustParsePrefix("198.51.100.0/24"): paris,
		netip.MustParsePrefix("1.1.1.0/24"):      {-33.87, 151.21}, // an anycast address «in Sydney»
	}
	return Build(Sources{
		Links: links, Prefixes: prefixes, PeeringDB: pdb, Geo: geo,
		Names: map[uint32]AS{6000: {Name: "SIX", Country: "UA"}, 7000: {Name: "SEVEN", Country: "FR"}},
		Now:   func() time.Time { return time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC) },
	})
}

func node(t *testing.T, m *Model, asn uint32) *Node {
	t.Helper()
	id, ok := m.Node(asn)
	if !ok {
		t.Fatalf("AS%d is not in the map", asn)
	}
	return &m.Nodes[id]
}

func TestModel(t *testing.T) {
	m := testWorld(t)
	if len(m.Nodes) != 13 || m.Links() != 15 {
		t.Fatalf("%d nodes, %d links", len(m.Nodes), m.Links())
	}
	if n := node(t, m, 6000); n.Name != "SIX" || n.Country != "UA" {
		t.Errorf("names: %+v", n)
	}
	if n := node(t, m, 7000); n.Name != "SEVEN" || n.Type != "Content" { // iptoasn's name wins, PeeringDB gives the type
		t.Errorf("types: %+v", n)
	}
	if n := node(t, m, 3000); n.Name != "Three" { // no name in iptoasn: PeeringDB's
		t.Errorf("PeeringDB name: %+v", n)
	}

	// Links: CAIDA's relation wins over «seen in a table», and the sources add up.
	three, _ := m.Node(3000)
	m.Neighbours(three, func(v int32, rel Rel, sources uint8) {
		if m.Nodes[v].ASN == 5000 && (rel != RelProvider || sources != SourceBGP|SourceRIB) {
			t.Errorf("3000–5000: %v, sources %b", rel, sources)
		}
	})
	if n := node(t, m, 5000); n.Providers != 2 || n.Customers != 1 || n.Peers != 0 {
		t.Errorf("relations of 5000: %+v", n)
	}
	if n := node(t, m, 8000); n.Providers != 1 || n.Peers != 1 { // the link seen in a table only counts as a peering
		t.Errorf("relations of 8000: %+v", n)
	}

	// Cones: 1000 reaches 3000, 8000, 5000, 6000; 5000 counts once although two roads lead to it.
	for asn, want := range map[uint32]uint32{1000: 5, 2000: 5, 3000: 3, 5000: 2, 6000: 1, 9100: 5} {
		if got := node(t, m, asn).Cone; got != want {
			t.Errorf("cone of %d: %d, want %d", asn, got, want)
		}
	}
	ranks := map[uint32]bool{}
	for _, n := range m.Nodes {
		ranks[n.Rank] = true
	}
	if len(ranks) != len(m.Nodes) || !ranks[1] || !ranks[uint32(len(m.Nodes))] {
		t.Errorf("ranks are not 1…%d: %v", len(m.Nodes), ranks)
	}

	// Places: by addresses, by data centres, next to a provider.
	for asn, want := range map[uint32]Placement{6000: PlacedByAddresses, 7000: PlacedByAddresses,
		3000: PlacedByFacilities, 5000: PlacedByFacilities, 4000: PlacedByNeighbours, 1000: PlacedByNeighbours} {
		if got := node(t, m, asn).Placed; got != want {
			t.Errorf("AS%d placed %d, want %d", asn, got, want)
		}
	}
	if n := node(t, m, 6000); n.Lat != float32(kyiv[0]) || n.Lon != float32(kyiv[1]) {
		t.Errorf("6000 is at %v, %v", n.Lat, n.Lon)
	}
	if n := node(t, m, 9100); n.Placed != PlacedNowhere {
		t.Errorf("a network with nothing to go by was placed: %+v", n)
	}

	// Exchange points: a place from their data centres, members and capacity from the ports.
	ix := m.IXs[0]
	if !ix.Placed || ix.Lat != float32(frankfurt[0]) || ix.Members != 2 || ix.Capacity != 300000 {
		t.Errorf("TEST-IX: %+v", ix)
	}
	if m.IXs[1].Placed {
		t.Errorf("an exchange point without data centres was placed: %+v", m.IXs[1])
	}
	seven, _ := m.Node(7000)
	if ports := m.Ports(seven); len(ports) != 1 || ports[0].Speed != 200000 || ports[0].Count != 2 {
		t.Errorf("ports of 7000: %+v", ports)
	}
	if x, ok := m.ExchangeOf(netip.MustParseAddr("80.81.193.12")); !ok || m.IXs[x].Name != "TEST-IX" {
		t.Errorf("a port address was not recognised: %d, %v", x, ok)
	}
	if _, ok := m.ExchangeOf(netip.MustParseAddr("8.8.8.8")); ok {
		t.Error("an ordinary address was taken for a port of an exchange point")
	}
}

func path(t *testing.T, m *Model, from, to uint32) []uint32 {
	t.Helper()
	a, _ := m.Node(from)
	b, _ := m.Node(to)
	ids, err := m.PathBetween(a, b)
	if err != nil {
		t.Fatalf("%d → %d: %v", from, to, err)
	}
	out := make([]uint32, len(ids))
	for i, id := range ids {
		out[i] = m.Nodes[id].ASN
	}
	return out
}

func TestPaths(t *testing.T) {
	m := testWorld(t)
	for _, tc := range []struct {
		from, to uint32
		want     []uint32
		why      string
	}{
		{1000, 6000, []uint32{1000, 3000, 5000, 6000}, "down through customers"},
		{6000, 7000, []uint32{6000, 5000, 3000, 7000}, "up to 3000, then its peering with 7000"},
		{3000, 4000, []uint32{3000, 1000, 2000, 4000}, "not through 5000: nobody carries transit for its providers"},
		{5000, 2000, []uint32{5000, 4000, 2000}, "a provider's route: the shorter one"},
		{9100, 9200, []uint32{9100, 9200}, "the peering, not four «customer» links a leak made up"},
		{6000, 6000, []uint32{6000}, "the same network"},
	} {
		if got := path(t, m, tc.from, tc.to); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%d → %d: %v, want %v (%s)", tc.from, tc.to, got, tc.want, tc.why)
		}
	}
	a, _ := m.Node(6000)
	b, _ := m.Node(9200)
	if _, err := m.PathBetween(a, b); !errors.Is(err, ErrNoPath) {
		t.Errorf("between two unconnected parts: %v", err)
	}
}

func TestRoute(t *testing.T) {
	m := testWorld(t)
	geo := fakeGeo{
		netip.MustParsePrefix("192.0.2.0/24"):    kyiv,
		netip.MustParsePrefix("198.51.100.0/24"): paris,
		netip.MustParsePrefix("1.1.1.0/24"):      {-33.87, 151.21},
	}
	route, err := m.Route(netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("198.51.100.20"), geo)
	if err != nil {
		t.Fatal(err)
	}
	if route.From.ASN != 6000 || route.To.ASN != 7000 || !route.From.Located || route.To.Lat != float32(paris[0]) {
		t.Fatalf("ends: %+v → %+v", route.From, route.To)
	}
	if len(route.Hops) != 4 {
		t.Fatalf("hops: %+v", route.Hops)
	}
	// 6000 buys transit from 5000: they meet where 6000 is.
	first := route.Hops[0].Meeting
	if first.Kind != MeetGuess || first.Lat != float32(kyiv[0]) {
		t.Errorf("6000 → 5000: %+v", first)
	}
	// 5000 and 3000 are both in NYC1.
	if second := route.Hops[1].Meeting; second.Kind != MeetFacility || second.Name != "NYC1" {
		t.Errorf("5000 → 3000: %+v", second)
	}
	// 3000 and 7000 both have ports at TEST-IX: one of 100G, two of 100G.
	third := route.Hops[2].Meeting
	if third.Kind != MeetIX || third.Name != "TEST-IX" || third.Speeds != [2]int64{100000, 200000} || third.Ports != [2]uint16{1, 2} {
		t.Errorf("3000 → 7000: %+v", third)
	}
	if route.Hops[1].Rel != RelCustomer || route.Hops[3].Rel != RelPeer || route.Hops[3].Meeting != nil {
		t.Errorf("relations along the path: %+v", route.Hops)
	}
	// Kyiv → New York → Frankfurt → Paris: some 16 000 km, ~200 ms with the detours of the cables.
	if route.RTT < 150 || route.RTT > 260 || third.RTT <= first.RTT {
		t.Errorf("RTT %.1f ms, at the meetings %.1f → %.1f", route.RTT, first.RTT, third.RTT)
	}

	// An anycast address answers where the traffic reaches its network — in New York, where 3000 is,
	// not «in Sydney», and not across the ocean at the only exchange point PeeringDB knows for both.
	anycastRoute, err := m.Route(netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("1.1.1.1"), geo)
	if err != nil {
		t.Fatal(err)
	}
	last := anycastRoute.Hops[2].Meeting
	if !anycastRoute.To.Anycast || anycastRoute.To.Lat != float32(newYork[0]) || last.Kind != MeetGuess || anycastRoute.RTT > route.RTT {
		t.Errorf("anycast: %+v, last meeting %+v, RTT %.1f", anycastRoute.To, last, anycastRoute.RTT)
	}

	if _, err := m.Route(netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("10.0.0.1"), geo); !errors.Is(err, ErrNotRouted) {
		t.Errorf("to a private address: %v", err)
	}
	// A path seen in a real table, through a network the map does not know.
	seen := m.Along([]uint32{6000, 5000, 64999, 7000}, route.From, route.To)
	if len(seen.Hops) != 4 || seen.Hops[2].Known || seen.Hops[2].Rel != RelUnknown {
		t.Errorf("an unknown network in a real path: %+v", seen.Hops)
	}
}
