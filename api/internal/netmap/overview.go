package netmap

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"math/bits"
	"sort"
)

// The overview is what the /map page downloads to draw the whole internet: every network that has
// a place, the exchange points, the busiest bundles of links between places, and the links of the
// core — the largest networks — for the view «core». One binary file, little-endian, every section
// aligned to 4 bytes, so that the browser reads it into typed arrays without copying:
//
//	header      16 × u32: magic "KNM1", version, built (unix), nodes N, exchanges X, bundles B,
//	            core links C, core size K, named nodes M, links, ASes, prefixes v4, prefixes v6,
//	            text length T, 2 reserved
//	nodes       asn u32[N], lat i16[N], lon i16[N] (hundredths of a degree), size u8[N]
//	            (bits of the cone), degree u8[N] (bits of the number of links), type u8[N],
//	            country u8[N] (index in the text)            — ordered by rank: the largest first
//	exchanges   lat i16[X], lon i16[X], members u16[X], capacity u32[X] (Gbit/s)
//	bundles     lat1 i16[B], lon1 i16[B], lat2 i16[B], lon2 i16[B], links u32[B]
//	core        a u16[C], b u16[C] (indices among the first K nodes), rel i8[C]
//	text        T bytes of JSON: {"countries": […], "names": [names of the first M nodes],
//	            "exchanges": [[name, city, country], …], "types": […]}
const (
	overviewMagic   = "KNM1"
	OverviewVersion = 1
)

// OverviewOptions choose how much goes into the file.
type OverviewOptions struct {
	Core    int     // the largest networks whose links between each other are drawn in «core», ≤ 65535
	Named   int     // networks whose names come along (for labels and the search)
	Bundles int     // busiest bundles of links between cells
	Cell    float64 // size of a cell for the bundles, degrees
}

// DefaultOverview is what the site uses.
var DefaultOverview = OverviewOptions{Core: 1000, Named: 3000, Bundles: 5000, Cell: 2}

// nodeTypes numbers the kinds of networks PeeringDB knows; 0 — not told.
var nodeTypes = []string{"", "NSP", "Content", "Cable/DSL/ISP", "Enterprise", "Educational/Research",
	"Non-Profit", "Route Server", "Network Services", "Route Collector", "Government"}

// WriteOverview writes the overview.
func (m *Model) WriteOverview(w io.Writer, opt OverviewOptions) error {
	if opt.Core > math.MaxUint16 {
		opt.Core = math.MaxUint16
	}
	// Nodes with a place, largest first.
	order := make([]int32, 0, len(m.Nodes))
	for i := range m.Nodes {
		if m.Nodes[i].Placed != PlacedNowhere {
			order = append(order, int32(i))
		}
	}
	sort.Slice(order, func(i, j int) bool { return m.Nodes[order[i]].Rank < m.Nodes[order[j]].Rank })
	position := make([]int32, len(m.Nodes)) // node id → place in the file, -1 without one
	for i := range position {
		position[i] = -1
	}
	for at, id := range order {
		position[id] = int32(at)
	}
	n := len(order)
	core := min(opt.Core, n)
	named := min(opt.Named, n)

	countries, countryIndex := []string{""}, map[string]uint8{"": 0}
	typeIndex := map[string]uint8{}
	for i, name := range nodeTypes {
		typeIndex[name] = uint8(i)
	}

	var buf bytes.Buffer
	asn := make([]uint32, n)
	lat, lon := make([]int16, n), make([]int16, n)
	size, degree, kind, country := make([]uint8, n), make([]uint8, n), make([]uint8, n), make([]uint8, n)
	for at, id := range order {
		node := &m.Nodes[id]
		asn[at] = node.ASN
		lat[at], lon[at] = centi(node.Lat), centi(node.Lon)
		size[at] = uint8(bits.Len32(node.Cone))                                      //nolint:gosec // 0…32
		degree[at] = uint8(bits.Len32(node.Customers + node.Peers + node.Providers)) //nolint:gosec // 0…32
		kind[at] = typeIndex[node.Type]
		index, ok := countryIndex[node.Country]
		if !ok && len(countries) < 255 {
			index = uint8(len(countries)) //nolint:gosec // fewer than 255, checked above
			countryIndex[node.Country] = index
			countries = append(countries, node.Country)
		}
		country[at] = index
	}

	var exchanges []int32
	for i := range m.IXs {
		if m.IXs[i].Placed && m.IXs[i].Members > 0 {
			exchanges = append(exchanges, int32(i))
		}
	}
	sort.Slice(exchanges, func(i, j int) bool {
		a, b := &m.IXs[exchanges[i]], &m.IXs[exchanges[j]]
		if a.Capacity != b.Capacity {
			return a.Capacity > b.Capacity
		}
		return a.ID < b.ID
	})

	bundles := m.bundles(opt.Cell, opt.Bundles)

	var coreA, coreB []uint16
	var coreRel []int8
	for at := range core {
		u := order[at]
		m.Neighbours(u, func(v int32, rel Rel, _ uint8) {
			// Places in the core are below 65 536: opt.Core is cut to that above.
			if p := position[v]; p > int32(at) && p < int32(core) { //nolint:gosec // see above
				coreA, coreB, coreRel = append(coreA, uint16(at)), append(coreB, uint16(p)), append(coreRel, int8(rel)) //nolint:gosec // see above
			}
		})
	}

	names := make([]string, named)
	for at := range named {
		names[at] = m.Nodes[order[at]].Name
	}
	exchangeText := make([][3]string, len(exchanges))
	for i, x := range exchanges {
		exchangeText[i] = [3]string{m.IXs[x].Name, m.IXs[x].City, m.IXs[x].Country}
	}
	text, err := json.Marshal(map[string]any{
		"countries": countries, "names": names, "exchanges": exchangeText, "types": nodeTypes,
	})
	if err != nil {
		return err
	}
	v4, v6 := m.Prefixes.Len()

	// Every count of the map is far below 2^32, and seconds since 1970 fit in 32 bits until 2106.
	header := []uint32{
		binary.LittleEndian.Uint32([]byte(overviewMagic)), OverviewVersion, uint32(m.Built.Unix()), //nolint:gosec // see above
		uint32(n), uint32(len(exchanges)), uint32(len(bundles)), uint32(len(coreA)), uint32(core), uint32(named), //nolint:gosec // see above
		uint32(m.Links()), uint32(len(m.Nodes)), uint32(v4), uint32(v6), uint32(len(text)), 0, 0, //nolint:gosec // see above
	}
	put := func(values any) { _ = binary.Write(&buf, binary.LittleEndian, values) }
	align := func() {
		for buf.Len()%4 != 0 {
			buf.WriteByte(0)
		}
	}
	put(header)
	put(asn)
	put(lat)
	put(lon)
	put(size)
	put(degree)
	put(kind)
	put(country)
	align()

	xLat, xLon := make([]int16, len(exchanges)), make([]int16, len(exchanges))
	members, capacity := make([]uint16, len(exchanges)), make([]uint32, len(exchanges))
	for i, x := range exchanges {
		ix := &m.IXs[x]
		xLat[i], xLon[i] = centi(ix.Lat), centi(ix.Lon)
		members[i] = uint16(min(ix.Members, math.MaxUint16))
		capacity[i] = uint32(min(ix.Capacity/1000, math.MaxUint32)) //nolint:gosec // clamped
	}
	put(xLat)
	put(xLon)
	put(members)
	align()
	put(capacity)

	bLat1, bLon1 := make([]int16, len(bundles)), make([]int16, len(bundles))
	bLat2, bLon2 := make([]int16, len(bundles)), make([]int16, len(bundles))
	bLinks := make([]uint32, len(bundles))
	for i, b := range bundles {
		bLat1[i], bLon1[i], bLat2[i], bLon2[i] = centi(b.lat1), centi(b.lon1), centi(b.lat2), centi(b.lon2)
		bLinks[i] = b.links
	}
	put(bLat1)
	put(bLon1)
	put(bLat2)
	put(bLon2)
	align()
	put(bLinks)

	put(coreA)
	put(coreB)
	put(coreRel)
	align()
	buf.Write(text)

	_, err = w.Write(buf.Bytes())
	return err
}

// bundle is the links between two cells of the map, drawn as one arc from the middle of the
// networks of one cell to the middle of the other's.
type bundle struct {
	lat1, lon1, lat2, lon2 float32
	links                  uint32
	cells                  [2]uint32 // ties are ordered by the cells, never by the order of a map
}

// bundles gathers links between cells of the given size and keeps the busiest.
func (m *Model) bundles(cell float64, most int) []bundle {
	type side struct{ lat, lon, count float64 }
	cellOf := func(node *Node) uint32 {
		return uint32((float64(node.Lat)+90)/cell)*10000 + uint32((float64(node.Lon)+180)/cell)
	}
	middles := map[uint32]*side{}
	for i := range m.Nodes {
		node := &m.Nodes[i]
		if node.Placed == PlacedNowhere {
			continue
		}
		c := cellOf(node)
		s := middles[c]
		if s == nil {
			s = &side{}
			middles[c] = s
		}
		s.lat += float64(node.Lat)
		s.lon += float64(node.Lon)
		s.count++
	}
	counts := map[[2]uint32]uint32{}
	for u := int32(0); int(u) < len(m.Nodes); u++ {
		if m.Nodes[u].Placed == PlacedNowhere {
			continue
		}
		a := cellOf(&m.Nodes[u])
		m.Neighbours(u, func(v int32, _ Rel, _ uint8) {
			if u >= v || m.Nodes[v].Placed == PlacedNowhere {
				return
			}
			b := cellOf(&m.Nodes[v])
			if a == b {
				return
			}
			counts[[2]uint32{min(a, b), max(a, b)}]++
		})
	}
	out := make([]bundle, 0, len(counts))
	for cells, links := range counts {
		a, b := middles[cells[0]], middles[cells[1]]
		out = append(out, bundle{
			lat1: float32(a.lat / a.count), lon1: float32(a.lon / a.count),
			lat2: float32(b.lat / b.count), lon2: float32(b.lon / b.count),
			links: links, cells: cells,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := &out[i], &out[j]
		if a.links != b.links {
			return a.links > b.links
		}
		return a.cells[0] < b.cells[0] || a.cells[0] == b.cells[0] && a.cells[1] < b.cells[1]
	})
	if len(out) > most {
		out = out[:most]
	}
	return out
}

// centi is a coordinate in hundredths of a degree.
func centi(degrees float32) int16 {
	return int16(math.Round(float64(degrees) * 100))
}
