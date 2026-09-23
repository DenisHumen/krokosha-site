package netmap

import (
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"
)

// Model is the map at one moment: every AS with its links, the addresses it announces, the
// exchange points and data centres where networks meet.
type Model struct {
	Built time.Time

	Nodes []Node           // ordered by AS number; the index of a node is its id
	index map[uint32]int32 // AS number → id

	// The links of node i are adj[start[i]:start[i+1]], with what i is to each neighbour in rel.
	start []int32
	adj   []int32
	rel   []Rel
	src   []uint8 // where each link was seen (Source bits), per direction

	Prefixes   *Prefixes
	IXs        []IX
	Facilities []Facility
	ports      [][]Port              // per node: its ports at exchange points, one per IX, ordered by IX
	sites      [][]int32             // per node: the data centres it is present in, ordered
	lans       []lan                 // peering LANs of exchange points, for the hops of a traceroute
	ixAddrs    map[netip.Addr]ixPort // the address of a network's port at an exchange point
}

// ixPort is whose port an address in a peering LAN is, and at which exchange point.
type ixPort struct {
	node int32
	ix   int32
}

// Node is one AS.
type Node struct {
	ASN      uint32
	Name     string
	Country  string // of the registration
	Type     string // PeeringDB: «NSP», «Content», «Cable/DSL/ISP», «Enterprise»…
	Lat, Lon float32
	Placed   Placement
	// Cone is how many ASes reach the internet through this one — its customers, their customers
	// and so on, itself included. The size of a network in the map is its cone.
	Cone      uint32
	Rank      uint32 // 1 is the largest cone
	Customers uint32
	Providers uint32
	Peers     uint32
}

// Placement says how a node found its place on the map.
type Placement uint8

const (
	PlacedNowhere      Placement = iota
	PlacedByAddresses            // where most of the addresses it announces are (GeoIP)
	PlacedByFacilities           // the data centres it is present in (PeeringDB)
	PlacedByNeighbours           // the middle of the networks it is linked to
)

// IX is an internet exchange point.
type IX struct {
	ID            int
	Name          string
	City, Country string
	Lat, Lon      float32
	Placed        bool
	Members       uint32 // networks with a port there
	Capacity      int64  // Mbit/s, all ports together
}

// Facility is a data centre where networks meet.
type Facility struct {
	ID            int
	Name          string
	City, Country string
	Lat, Lon      float32
	Placed        bool
}

// Port is all ports of a network at one exchange point.
type Port struct {
	IX    int32 // index in Model.IXs
	Speed int64 // Mbit/s together
	Count uint16
}

type lan struct {
	prefix netip.Prefix
	ix     int32
}

// Node finds an AS by its number.
func (m *Model) Node(asn uint32) (int32, bool) {
	id, ok := m.index[asn]
	return id, ok
}

// Neighbours calls fn for every link of a node.
func (m *Model) Neighbours(node int32, fn func(neighbour int32, rel Rel, sources uint8)) {
	for i := m.start[node]; i < m.start[node+1]; i++ {
		fn(m.adj[i], m.rel[i], m.src[i])
	}
}

// Links is how many links there are.
func (m *Model) Links() int { return len(m.adj) / 2 }

// Ports are the ports of a network at exchange points.
func (m *Model) Ports(node int32) []Port { return m.ports[node] }

// Sites are the data centres a network is present in.
func (m *Model) Sites(node int32) []int32 { return m.sites[node] }

// PortOf finds the network whose port at an exchange point has this address: a hop of a
// traceroute that answers from a peering LAN is the router of that network.
func (m *Model) PortOf(addr netip.Addr) (node, ix int32, ok bool) {
	port, ok := m.ixAddrs[addr.Unmap()]
	return port.node, port.ix, ok
}

// ExchangeOf finds the exchange point whose peering LAN an address belongs to.
func (m *Model) ExchangeOf(addr netip.Addr) (int32, bool) {
	addr = addr.Unmap()
	for _, l := range m.lans {
		if l.prefix.Contains(addr) {
			return l.ix, true
		}
	}
	return -1, false
}

// Sources is everything a model is built from. Only Links is required.
type Sources struct {
	Links     []Link           // CAIDA, RouteViews
	Prefixes  *Prefixes        // iptoasn
	Names     map[uint32]AS    // iptoasn
	PeeringDB *PeeringDB       // optional
	Geo       Locator          // optional: where an address is
	Now       func() time.Time // optional
}

// Locator tells where an address is.
type Locator interface {
	Where(addr netip.Addr) (lat, lon float64, ok bool)
}

// Build makes a model. Links seen by several sources are merged: a relation known from CAIDA wins
// over one seen in a path only.
func Build(sources Sources) *Model {
	now := time.Now
	if sources.Now != nil {
		now = sources.Now
	}
	m := &Model{Built: now().UTC(), Prefixes: sources.Prefixes}
	if m.Prefixes == nil {
		m.Prefixes = &Prefixes{}
	}
	links := mergeLinks(sources.Links)
	m.makeNodes(links, sources)
	m.link(links)
	if sources.PeeringDB != nil {
		m.addPeeringDB(sources.PeeringDB)
	} else {
		m.ports = make([][]Port, len(m.Nodes))
		m.sites = make([][]int32, len(m.Nodes))
		m.ixAddrs = map[netip.Addr]ixPort{}
	}
	m.place(sources.Geo)
	return m
}

// mergeLinks joins the links of all sources: one entry per pair of ASes.
func mergeLinks(all []Link) []Link {
	merged := make(map[uint64]Link, len(all))
	for _, link := range all {
		key := link.Key()
		known, ok := merged[key]
		switch {
		case !ok:
			merged[key] = link
		case known.Rel == RelUnknown && link.Rel != RelUnknown:
			link.Sources |= known.Sources
			merged[key] = link
		default:
			known.Sources |= link.Sources
			merged[key] = known
		}
	}
	out := make([]Link, 0, len(merged))
	for _, link := range merged {
		out = append(out, link)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// makeNodes lists every AS that has a link or announces addresses.
func (m *Model) makeNodes(links []Link, sources Sources) {
	seen := make(map[uint32]struct{}, 1<<17)
	for _, link := range links {
		seen[link.A] = struct{}{}
		seen[link.B] = struct{}{}
	}
	m.Prefixes.Each(func(r Range) { seen[r.ASN] = struct{}{} })
	asns := make([]uint32, 0, len(seen))
	for asn := range seen {
		asns = append(asns, asn)
	}
	sort.Slice(asns, func(i, j int) bool { return asns[i] < asns[j] })
	types := map[uint32]PDBNetwork{}
	if sources.PeeringDB != nil {
		for _, network := range sources.PeeringDB.Networks {
			types[network.ASN] = network
		}
	}
	m.Nodes = make([]Node, len(asns))
	m.index = make(map[uint32]int32, len(asns))
	for i, asn := range asns {
		node := Node{ASN: asn}
		if as, ok := sources.Names[asn]; ok {
			node.Name, node.Country = as.Name, as.Country
		}
		if network, ok := types[asn]; ok {
			node.Type = network.Type
			if node.Name == "" {
				node.Name = cut(network.Name)
			}
		}
		m.Nodes[i] = node
		m.index[asn] = int32(i)
	}
}

// link lays the links out and counts what follows from them: relations, cones, ranks. Every
// end of every link must be a node; links are sorted by Key.
func (m *Model) link(links []Link) {
	m.makeGraph(links)
	m.countRelations()
	m.computeCones()
}

// makeGraph lays the links out as arrays: for each node, its neighbours one after another.
func (m *Model) makeGraph(links []Link) {
	degree := make([]int32, len(m.Nodes)+1)
	for _, link := range links {
		degree[m.index[link.A]]++
		degree[m.index[link.B]]++
	}
	m.start = make([]int32, len(m.Nodes)+1)
	for i := range m.Nodes {
		m.start[i+1] = m.start[i] + degree[i]
	}
	m.adj = make([]int32, 2*len(links))
	m.rel = make([]Rel, 2*len(links))
	m.src = make([]uint8, 2*len(links))
	fill := make([]int32, len(m.Nodes))
	copy(fill, m.start[:len(m.Nodes)])
	for _, link := range links {
		a, b := m.index[link.A], m.index[link.B]
		m.adj[fill[a]], m.rel[fill[a]], m.src[fill[a]] = b, link.Rel, link.Sources
		fill[a]++
		m.adj[fill[b]], m.rel[fill[b]], m.src[fill[b]] = a, link.Rel.Reverse(), link.Sources
		fill[b]++
	}
}

func (m *Model) countRelations() {
	for i := range m.Nodes {
		node := &m.Nodes[i]
		m.Neighbours(int32(i), func(_ int32, rel Rel, _ uint8) {
			switch rel {
			case RelProvider:
				node.Customers++
			case RelCustomer:
				node.Providers++
			default:
				node.Peers++
			}
		})
	}
}

// computeCones counts the customer cone of every network: a walk down its customers, their
// customers and so on. Most networks have no customers and a cone of one; the walk is only taken
// for the others, with a stamp per node instead of a set, so that the whole map takes a second.
func (m *Model) computeCones() {
	stamp := make([]int32, len(m.Nodes))
	stack := make([]int32, 0, 1024)
	for i := range m.Nodes {
		m.Nodes[i].Cone = 1
		if m.Nodes[i].Customers == 0 {
			continue
		}
		mark := int32(i) + 1
		stamp[i] = mark
		count := uint32(1)
		stack = append(stack[:0], int32(i))
		for len(stack) > 0 {
			u := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for k := m.start[u]; k < m.start[u+1]; k++ {
				if v := m.adj[k]; m.rel[k] == RelProvider && stamp[v] != mark {
					stamp[v] = mark
					count++
					stack = append(stack, v)
				}
			}
		}
		m.Nodes[i].Cone = count
	}
	order := make([]int32, len(m.Nodes))
	for i := range order {
		order[i] = int32(i)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := &m.Nodes[order[i]], &m.Nodes[order[j]]
		if a.Cone != b.Cone {
			return a.Cone > b.Cone
		}
		if da, db := a.Customers+a.Peers+a.Providers, b.Customers+b.Peers+b.Providers; da != db {
			return da > db
		}
		return a.ASN < b.ASN
	})
	for rank, id := range order {
		m.Nodes[id].Rank = uint32(rank + 1)
	}
}

// addPeeringDB attaches exchange points, data centres, ports and presence. Exchange points and
// data centres are kept in the order of their PeeringDB ids, as the stored map restores them.
func (m *Model) addPeeringDB(db *PeeringDB) {
	facilities := append([]PDBFacility(nil), db.Facilities...)
	sort.Slice(facilities, func(i, j int) bool { return facilities[i].ID < facilities[j].ID })
	facIndex := make(map[int]int32, len(facilities))
	for _, f := range facilities {
		if _, twice := facIndex[f.ID]; twice {
			continue
		}
		facility := Facility{ID: f.ID, Name: cut(f.Name), City: cut(f.City), Country: country(f.Country)}
		if f.Lat != nil && f.Lon != nil && (*f.Lat != 0 || *f.Lon != 0) {
			facility.Lat, facility.Lon, facility.Placed = float32(*f.Lat), float32(*f.Lon), true
		}
		facIndex[f.ID] = int32(len(m.Facilities)) //nolint:gosec // some 6 000 data centres
		m.Facilities = append(m.Facilities, facility)
	}
	exchanges := append([]PDBIX(nil), db.IXs...)
	sort.Slice(exchanges, func(i, j int) bool { return exchanges[i].ID < exchanges[j].ID })
	ixIndex := make(map[int]int32, len(exchanges))
	for _, x := range exchanges {
		if _, twice := ixIndex[x.ID]; twice {
			continue
		}
		ixIndex[x.ID] = int32(len(m.IXs)) //nolint:gosec // some 1 300 exchange points
		m.IXs = append(m.IXs, IX{ID: x.ID, Name: cut(x.Name), City: cut(x.City), Country: country(x.Country)})
	}
	// An exchange point is where its data centres are: their middle.
	sums := make([][3]float64, len(m.IXs))
	for _, link := range db.IXFacility {
		x, okX := ixIndex[link.IX]
		f, okF := facIndex[link.Facility]
		if okX && okF && m.Facilities[f].Placed {
			sums[x][0] += float64(m.Facilities[f].Lat)
			sums[x][1] += float64(m.Facilities[f].Lon)
			sums[x][2]++
		}
	}
	// Without data centres of its own, an exchange point is in the middle of the other data centres
	// of its city.
	cities := map[string]*[3]float64{}
	for _, facility := range m.Facilities {
		if !facility.Placed || facility.City == "" {
			continue
		}
		key := facility.Country + "|" + facility.City
		if cities[key] == nil {
			cities[key] = &[3]float64{}
		}
		cities[key][0] += float64(facility.Lat)
		cities[key][1] += float64(facility.Lon)
		cities[key][2]++
	}
	for i := range m.IXs {
		if sums[i][2] == 0 {
			if city := cities[m.IXs[i].Country+"|"+m.IXs[i].City]; city != nil {
				sums[i] = *city
			}
		}
		if sums[i][2] > 0 {
			m.IXs[i].Lat = float32(sums[i][0] / sums[i][2])
			m.IXs[i].Lon = float32(sums[i][1] / sums[i][2])
			m.IXs[i].Placed = true
		}
	}

	m.ports = make([][]Port, len(m.Nodes))
	members := make([]map[int32]struct{}, len(m.IXs))
	m.ixAddrs = map[netip.Addr]ixPort{}
	for _, p := range db.Ports {
		node, okN := m.index[p.ASN]
		x, okX := ixIndex[p.IX]
		if !okN || !okX || !p.Operational {
			continue
		}
		for _, text := range []string{p.IPv4, p.IPv6} {
			if addr, err := netip.ParseAddr(strings.TrimSpace(text)); err == nil {
				m.ixAddrs[addr.Unmap()] = ixPort{node: node, ix: x}
			}
		}
		m.IXs[x].Capacity += p.Speed
		if members[x] == nil {
			members[x] = map[int32]struct{}{}
		}
		members[x][node] = struct{}{}
		ports := m.ports[node]
		found := false
		for k := range ports {
			if ports[k].IX == x {
				ports[k].Speed += p.Speed
				ports[k].Count++
				found = true
				break
			}
		}
		if !found {
			m.ports[node] = append(ports, Port{IX: x, Speed: p.Speed, Count: 1})
		}
	}
	for i := range m.IXs {
		m.IXs[i].Members = uint32(len(members[i])) //nolint:gosec // networks at one exchange point
	}
	for i := range m.ports {
		sort.Slice(m.ports[i], func(a, b int) bool { return m.ports[i][a].IX < m.ports[i][b].IX })
	}

	m.sites = make([][]int32, len(m.Nodes))
	for _, p := range db.Presence {
		node, okN := m.index[p.ASN]
		f, okF := facIndex[p.Facility]
		if okN && okF {
			m.sites[node] = append(m.sites[node], f)
		}
	}
	for i := range m.sites {
		sort.Slice(m.sites[i], func(a, b int) bool { return m.sites[i][a] < m.sites[i][b] })
		m.sites[i] = slices.Compact(m.sites[i]) // PeeringDB may list a presence twice
	}

	for prefix, ix := range db.LANPrefixes() {
		if x, ok := ixIndex[ix]; ok {
			m.lans = append(m.lans, lan{prefix: prefix, ix: x})
		}
	}
	sort.Slice(m.lans, func(i, j int) bool { return m.lans[i].prefix.String() < m.lans[j].prefix.String() })
}

// country is a code of two letters, or nothing.
func country(code string) string {
	if len(code) != 2 {
		return ""
	}
	return code
}
