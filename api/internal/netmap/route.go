package netmap

import (
	"errors"
	"math"
	"net/netip"
	"sync"
)

// How a network chooses among the routes it has to a destination. The Gao–Rexford model: a route
// learnt from a customer earns money and beats a route from a peer, which costs nothing and beats a
// route from a provider, which costs; a network passes a route from a customer on to everybody, a
// route from a peer or a provider only to its own customers — nobody carries traffic between two
// parties that do not pay for it.
//
// Taken literally, the model believes every link the data calls «customer»: one wrongly inferred
// link — a leaked route makes a small network look like the provider of a giant — would pull whole
// continents through it on the strength of «a customer route always wins». So the kind of a route
// weighs half a hop here: a customer route beats a peer route of the same length and a provider
// route one hop shorter, a peer route beats a provider route of the same length — but a much shorter
// route wins whatever its kind. Real networks behave so too, mostly: they prefer what pays, within
// reason.
const (
	routeNone     uint8 = iota
	routeProvider       // learnt from a provider
	routePeer           // learnt from a peer
	routeCustomer       // learnt from a customer
	routeSelf           // the destination itself
)

// penalty is what the kind of a route adds to its length, in half hops.
var penalty = [...]uint16{routeProvider: 2, routePeer: 1, routeCustomer: 0, routeSelf: 0}

// tree is the route every network chooses towards one destination.
type tree struct {
	kind   []uint8
	length []uint16
	score  []uint16 // 2 × length + penalty of the kind
	next   []int32
	done   []bool
	bucket [][]int32
}

var trees = sync.Pool{New: func() any { return &tree{} }}

// ErrNoPath means the two networks are not connected in the map.
var ErrNoPath = errors.New("no path between these networks")

// PathBetween is the AS path the routing policies make likely from one network to another, both
// ends included.
func (m *Model) PathBetween(from, to int32) ([]int32, error) {
	if from == to {
		return []int32{from}, nil
	}
	t := trees.Get().(*tree)
	defer trees.Put(t)
	m.routesTo(to, t)
	if t.kind[from] == routeNone {
		return nil, ErrNoPath
	}
	path := []int32{from}
	for node := from; node != to; {
		node = t.next[node]
		path = append(path, node)
		if len(path) > 64 { // cannot happen: every step shortens the route
			return nil, ErrNoPath
		}
	}
	return path, nil
}

// better says whether a is a better next hop than b among routes of the same score: the larger
// network, then the smaller number — as good a guess as any about a router's tie-break.
func (m *Model) better(a, b int32) bool {
	if m.Nodes[a].Cone != m.Nodes[b].Cone {
		return m.Nodes[a].Cone > m.Nodes[b].Cone
	}
	return m.Nodes[a].ASN < m.Nodes[b].ASN
}

// routesTo fills t with the route every network chooses towards dst: the offers of the neighbours
// are taken best score first (a bucket queue — scores are small numbers), and a network settled on
// its route offers it on as the rules of export allow.
func (m *Model) routesTo(dst int32, t *tree) {
	n := len(m.Nodes)
	if cap(t.kind) < n {
		t.kind, t.length, t.score = make([]uint8, n), make([]uint16, n), make([]uint16, n)
		t.next, t.done = make([]int32, n), make([]bool, n)
	}
	t.kind, t.length, t.score, t.next, t.done = t.kind[:n], t.length[:n], t.score[:n], t.next[:n], t.done[:n]
	clear(t.kind)
	clear(t.done)
	for i := range t.bucket {
		t.bucket[i] = t.bucket[i][:0]
	}
	push := func(node int32, score uint16) {
		for int(score) >= len(t.bucket) {
			t.bucket = append(t.bucket, nil)
		}
		t.bucket[score] = append(t.bucket[score], node)
	}
	offer := func(from, to int32, kind uint8) {
		if t.done[to] {
			return
		}
		length := t.length[from] + 1
		score := 2*length + penalty[kind]
		switch {
		case t.kind[to] == routeNone, score < t.score[to],
			score == t.score[to] && (kind > t.kind[to] || kind == t.kind[to] && m.better(from, t.next[to])):
			t.kind[to], t.length[to], t.score[to], t.next[to] = kind, length, score, from
			push(to, score)
		}
	}

	t.kind[dst], t.length[dst], t.score[dst], t.next[dst] = routeSelf, 0, 0, dst
	push(dst, 0)
	for score := 0; score < len(t.bucket); score++ {
		for i := 0; i < len(t.bucket[score]); i++ { // the bucket may grow while it is walked
			u := t.bucket[score][i]
			if t.done[u] || int(t.score[u]) != score {
				continue // settled already, or an offer that a better one replaced
			}
			t.done[u] = true
			exportsUp := t.kind[u] == routeSelf || t.kind[u] == routeCustomer
			for k := m.start[u]; k < m.start[u+1]; k++ {
				v := m.adj[k]
				switch m.rel[k] {
				case RelCustomer: // u buys transit from v: v learns a customer route
					if exportsUp {
						offer(u, v, routeCustomer)
					}
				case RelPeer, RelUnknown:
					if exportsUp {
						offer(u, v, routePeer)
					}
				case RelProvider: // u sells transit to v: v learns a provider route
					offer(u, v, routeProvider)
				}
			}
		}
	}
}

// Route is a path between two addresses, network by network.
type Route struct {
	From, To Endpoint
	Hops     []Hop
	RTT      float64 // estimated round trip, milliseconds
	Measured bool    // the AS path was seen in a real routing table rather than inferred
}

// Endpoint is one end of a route.
type Endpoint struct {
	Addr     netip.Addr
	ASN      uint32
	Lat, Lon float32
	Located  bool // Lat and Lon are known
	// Anycast: the address is served from many places at once — public DNS resolvers — and answers
	// from the one closest to the traffic. GeoIP puts it wherever its owner registered it.
	Anycast bool
}

// anycast are the addresses people test with most — public DNS resolvers — which are served from
// hundreds of places at once. There is no open list of every anycast network.
var anycast = []netip.Prefix{
	netip.MustParsePrefix("1.1.1.0/24"), netip.MustParsePrefix("1.0.0.0/24"), // Cloudflare
	netip.MustParsePrefix("2606:4700:4700::/48"),
	netip.MustParsePrefix("8.8.8.0/24"), netip.MustParsePrefix("8.8.4.0/24"), // Google
	netip.MustParsePrefix("2001:4860:4860::/48"),
	netip.MustParsePrefix("9.9.9.0/24"), netip.MustParsePrefix("149.112.112.0/24"), // Quad9
	netip.MustParsePrefix("2620:fe::/48"),
	netip.MustParsePrefix("208.67.222.0/24"), netip.MustParsePrefix("208.67.220.0/24"), // OpenDNS
	netip.MustParsePrefix("2620:119:35::/48"), netip.MustParsePrefix("2620:119:53::/48"),
}

// IsAnycast reports whether an address is one of the well-known anycast services.
func IsAnycast(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range anycast {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Hop is one network of a path.
type Hop struct {
	ASN     uint32
	Name    string
	Country string
	Type    string
	// Rel is what the previous network is to this one: RelCustomer — it buys transit here,
	// RelProvider — it sells transit to this one, RelPeer — the two exchange traffic for free.
	Rel     Rel
	Known   bool     // the network is in the map
	Meeting *Meeting // where this network hands the traffic on; nil for the last one
}

// Meeting is where two networks of a path meet.
type Meeting struct {
	Kind          MeetingKind
	Name          string // of the exchange point or the data centre
	City, Country string
	Lat, Lon      float32
	// Speeds are the ports of the two networks at the exchange point, Mbit/s (0 — not told), and
	// Ports how many there are.
	Speeds [2]int64
	Ports  [2]uint16
	RTT    float64 // estimated round trip from the source to here, ms
}

// MeetingKind says how a meeting point was found.
type MeetingKind uint8

const (
	MeetGuess    MeetingKind = iota // nothing is known: the home of the next network
	MeetIX                          // both networks have ports at this exchange point
	MeetFacility                    // both networks are present in this data centre
)

// Delays of light in fibre: 200 km in a millisecond, both ways, on cables about 30% longer than the
// straight line; and a little for every network the packets pass. maxDetourKm is how far off the
// way a known meeting place may lie.
const (
	rttPerKm    = 2 * 1.3 / 200.0
	rttPerHop   = 0.2
	maxDetourKm = 1500
)

// Route draws the likely path between two addresses. geo tells where the addresses are; without
// it, or when it does not know, the homes of their networks stand in.
func (m *Model) Route(from, to netip.Addr, geo Locator) (*Route, error) {
	src, errFrom := m.endpoint(from, geo)
	dst, errTo := m.endpoint(to, geo)
	if err := errors.Join(errFrom, errTo); err != nil {
		return nil, err
	}
	a, _ := m.Node(src.ASN)
	b, _ := m.Node(dst.ASN)
	ids, err := m.PathBetween(a, b)
	if err != nil {
		return nil, err
	}
	path := make([]uint32, len(ids))
	for i, id := range ids {
		path[i] = m.Nodes[id].ASN
	}
	return m.Along(path, src, dst), nil
}

// ErrNotRouted means nobody announces the address.
var ErrNotRouted = errors.New("nobody announces this address")

// endpoint finds the network of an address and where the address is.
func (m *Model) endpoint(addr netip.Addr, geo Locator) (Endpoint, error) {
	end := Endpoint{Addr: addr.Unmap()}
	asn, ok := m.Prefixes.Lookup(end.Addr)
	if !ok {
		return end, ErrNotRouted
	}
	end.ASN = asn
	end.Anycast = IsAnycast(end.Addr)
	if geo != nil {
		if lat, lon, ok := geo.Where(end.Addr); ok {
			end.Lat, end.Lon, end.Located = float32(lat), float32(lon), true
			return end, nil
		}
	}
	if id, ok := m.Node(asn); ok && m.Nodes[id].Placed != PlacedNowhere {
		end.Lat, end.Lon, end.Located = m.Nodes[id].Lat, m.Nodes[id].Lon, true
	}
	return end, nil
}

// Along draws an AS path — inferred or seen in a real table — between two ends: where each pair of
// networks meets and how long the packets are likely to take.
func (m *Model) Along(path []uint32, src, dst Endpoint) *Route {
	route := &Route{From: src, To: dst, Hops: make([]Hop, len(path))}
	here := point{float64(src.Lat), float64(src.Lon), src.Located}
	var previous uint32
	for i, asn := range path {
		hop := &route.Hops[i]
		hop.ASN = asn
		id, known := m.Node(asn)
		hop.Known = known
		if known {
			node := &m.Nodes[id]
			hop.Name, hop.Country, hop.Type = node.Name, node.Country, node.Type
		}
		if i > 0 {
			hop.Rel = m.relation(previous, asn)
		}
		previous = asn
		if i == len(path)-1 {
			break
		}
		towards := point{float64(dst.Lat), float64(dst.Lon), dst.Located}
		if dst.Anycast {
			towards = here // an anycast address is met where the traffic already is
		}
		if i+1 < len(path)-1 {
			if next, ok := m.Node(path[i+1]); ok && m.Nodes[next].Placed != PlacedNowhere {
				towards = point{float64(m.Nodes[next].Lat), float64(m.Nodes[next].Lon), true}
			}
		}
		// The last network of the path serves an anycast address from the place closest to the
		// traffic: they meet right here.
		stayHere := dst.Anycast && i+1 == len(path)-1
		meeting := m.meeting(asn, path[i+1], here, towards, stayHere)
		there := point{float64(meeting.Lat), float64(meeting.Lon), true}
		route.RTT += here.km(there)*rttPerKm + rttPerHop
		meeting.RTT = route.RTT
		hop.Meeting = &meeting
		here = there
	}
	if dst.Anycast && here.ok {
		// Served from the closest of its places: where the traffic reaches its network.
		route.To.Lat, route.To.Lon, route.To.Located = float32(here.lat), float32(here.lon), true
		dst = route.To
	}
	route.RTT += here.km(point{float64(dst.Lat), float64(dst.Lon), dst.Located}) * rttPerKm
	return route
}

// relation is what network a is to network b; RelUnknown when they are not linked in the map.
func (m *Model) relation(a, b uint32) Rel {
	x, okA := m.Node(a)
	y, okB := m.Node(b)
	if !okA || !okB {
		return RelUnknown
	}
	for k := m.start[x]; k < m.start[x+1]; k++ {
		if m.adj[k] == y {
			return m.rel[k]
		}
	}
	return RelUnknown
}

// meeting chooses where networks a and b hand traffic over: an exchange point or a data centre both
// are present at — the one that makes the shortest detour on the way from here towards the next
// place — or, knowing none, a guess. A common place far off the way does not count: two networks
// that meet only in Dubai according to PeeringDB surely have a private link somewhere nearer, which
// nobody publishes.
func (m *Model) meeting(a, b uint32, here, towards point, stayHere bool) Meeting {
	x, okA := m.Node(a)
	y, okB := m.Node(b)
	direct := here.km(towards)
	limit := direct + math.Max(maxDetourKm, direct/2)
	best, bestCost := Meeting{}, math.Inf(1)
	consider := func(candidate Meeting) {
		cost := here.km(point{float64(candidate.Lat), float64(candidate.Lon), true}) +
			point{float64(candidate.Lat), float64(candidate.Lon), true}.km(towards)
		if cost <= limit && cost < bestCost {
			best, bestCost = candidate, cost
		}
	}
	if okA && okB {
		portsA, portsB := m.ports[x], m.ports[y]
		for i, j := 0, 0; i < len(portsA) && j < len(portsB); {
			switch {
			case portsA[i].IX < portsB[j].IX:
				i++
			case portsA[i].IX > portsB[j].IX:
				j++
			default:
				if ix := &m.IXs[portsA[i].IX]; ix.Placed {
					consider(Meeting{
						Kind: MeetIX, Name: ix.Name, City: ix.City, Country: ix.Country, Lat: ix.Lat, Lon: ix.Lon,
						Speeds: [2]int64{portsA[i].Speed, portsB[j].Speed},
						Ports:  [2]uint16{portsA[i].Count, portsB[j].Count},
					})
				}
				i++
				j++
			}
		}
		sitesA, sitesB := m.sites[x], m.sites[y]
		for i, j := 0, 0; i < len(sitesA) && j < len(sitesB); {
			switch {
			case sitesA[i] < sitesB[j]:
				i++
			case sitesA[i] > sitesB[j]:
				j++
			default:
				if f := &m.Facilities[sitesA[i]]; f.Placed {
					consider(Meeting{Kind: MeetFacility, Name: f.Name, City: f.City, Country: f.Country, Lat: f.Lat, Lon: f.Lon})
				}
				i++
				j++
			}
		}
	}
	if !math.IsInf(bestCost, 1) {
		return best
	}
	// An anycast address answers from the place of its network closest to the traffic: the nearest
	// exchange point or data centre where the network says it is.
	if stayHere && okB && here.ok {
		if nearest, ok := m.nearestPresence(y, here); ok {
			return nearest
		}
	}
	// Nothing is known about where they meet. A customer connects to its provider close to itself —
	// a large provider is everywhere — so they meet here; a provider reaches its customer where the
	// customer is; peers meet here, unless the other one is a small network with a home of its own.
	guess := Meeting{Kind: MeetGuess, Lat: float32(here.lat), Lon: float32(here.lon)}
	if !here.ok {
		guess.Lat, guess.Lon = float32(towards.lat), float32(towards.lon)
	}
	if okB && m.Nodes[y].Placed != PlacedNowhere && (!stayHere || !here.ok) {
		rel := m.relation(a, b)
		if rel == RelProvider || (rel != RelCustomer && m.Nodes[y].Cone < 100) || !here.ok {
			guess.Lat, guess.Lon = m.Nodes[y].Lat, m.Nodes[y].Lon
		}
	}
	return guess
}

// maxAnycastKm is how far the nearest place of an anycast network may be to count.
const maxAnycastKm = 3000

// nearestPresence is the exchange point or data centre of a network closest to a place.
func (m *Model) nearestPresence(node int32, here point) (Meeting, bool) {
	best, bestKm := Meeting{}, float64(maxAnycastKm)
	found := false
	for _, port := range m.ports[node] {
		ix := &m.IXs[port.IX]
		if !ix.Placed {
			continue
		}
		if km := here.km(point{float64(ix.Lat), float64(ix.Lon), true}); km < bestKm {
			best, bestKm, found = Meeting{
				Kind: MeetIX, Name: ix.Name, City: ix.City, Country: ix.Country, Lat: ix.Lat, Lon: ix.Lon,
				Speeds: [2]int64{0, port.Speed}, Ports: [2]uint16{0, port.Count},
			}, km, true
		}
	}
	for _, f := range m.sites[node] {
		facility := &m.Facilities[f]
		if !facility.Placed {
			continue
		}
		if km := here.km(point{float64(facility.Lat), float64(facility.Lon), true}); km < bestKm {
			best, bestKm, found = Meeting{
				Kind: MeetFacility, Name: facility.Name, City: facility.City, Country: facility.Country,
				Lat: facility.Lat, Lon: facility.Lon,
			}, km, true
		}
	}
	return best, found
}

// point is a place on the globe; ok is false for a place that is not known.
type point struct {
	lat, lon float64
	ok       bool
}

// km is the distance along the surface of the Earth; 0 when either place is unknown.
func (p point) km(q point) float64 {
	if !p.ok || !q.ok {
		return 0
	}
	const radius = 6371.0
	lat1, lat2 := p.lat*math.Pi/180, q.lat*math.Pi/180
	dLat, dLon := lat2-lat1, (q.lon-p.lon)*math.Pi/180
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * radius * math.Asin(math.Min(1, math.Sqrt(h)))
}
