package netmap

import (
	"encoding/binary"
	"math"
	"net/netip"

	"github.com/oschwald/maxminddb-golang/v2"
)

// GeoIP answers «where is this address?» from a MaxMind DB file with coordinates — the DB-IP City
// Lite the site already keeps for its statistics, or GeoLite2-City.
type GeoIP struct {
	db *maxminddb.Reader
}

// OpenGeoIP opens such a file.
func OpenGeoIP(path string) (*GeoIP, error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return &GeoIP{db: db}, nil
}

// Where gives the coordinates of the city of an address.
func (g *GeoIP) Where(addr netip.Addr) (lat, lon float64, ok bool) {
	var record struct {
		Location struct {
			Latitude  float64 `maxminddb:"latitude"`
			Longitude float64 `maxminddb:"longitude"`
		} `maxminddb:"location"`
	}
	if err := g.db.Lookup(addr.Unmap()).Decode(&record); err != nil {
		return 0, 0, false
	}
	lat, lon = record.Location.Latitude, record.Location.Longitude
	return lat, lon, lat != 0 || lon != 0
}

// Close releases the file.
func (g *GeoIP) Close() error { return g.db.Close() }

// place puts every network on the map: where most of the addresses it announces are; failing
// that, in the city of most of its data centres; failing that, next to its largest provider or
// neighbour. A few stay nowhere: a network with no addresses, no data centre and no placed
// neighbour is not drawn.
func (m *Model) place(geo Locator) {
	if geo != nil {
		m.placeByAddresses(geo)
	}
	m.placeByFacilities()
	for range 3 { // each round reaches one link further
		if !m.placeByNeighbours() {
			break
		}
	}
}

// vote is the weight of one place for one network, and the middle of what fell into it.
type vote struct {
	weight, lat, lon float64
}

// placeByAddresses looks up the first and the middle address of every announced range. Each look
// is a vote for a cell of half a degree, weighed by the addresses the range holds; the cell with
// the most weight wins, and the network stands in the middle of what fell into it.
func (m *Model) placeByAddresses(geo Locator) {
	type place struct {
		node int32
		cell uint32
	}
	votes := make(map[place]*vote, 1<<19)
	m.Prefixes.Each(func(r Range) {
		node, ok := m.index[r.ASN]
		if !ok {
			return
		}
		weight := rangeWeight(r)
		samples := []netip.Addr{r.First}
		if middle := midpoint(r); middle != r.First {
			samples = append(samples, middle)
		}
		for _, addr := range samples {
			lat, lon, ok := geo.Where(addr)
			if !ok {
				continue
			}
			key := place{node, cellOf(lat, lon)}
			v := votes[key]
			if v == nil {
				v = &vote{}
				votes[key] = v
			}
			share := weight / float64(len(samples))
			v.weight += share
			v.lat += lat * share
			v.lon += lon * share
		}
	})
	type winner struct {
		cell uint32
		vote *vote
	}
	best := make(map[int32]winner, len(m.Nodes))
	for key, v := range votes {
		// Equal weights: the lower cell, so that a network stands in the same place every day.
		if current, ok := best[key.node]; !ok || v.weight > current.vote.weight || v.weight == current.vote.weight && key.cell < current.cell {
			best[key.node] = winner{cell: key.cell, vote: v}
		}
	}
	for node, w := range best {
		v := w.vote
		m.Nodes[node].Lat = float32(v.lat / v.weight)
		m.Nodes[node].Lon = float32(v.lon / v.weight)
		m.Nodes[node].Placed = PlacedByAddresses
	}
}

// rangeWeight is how many addresses a range holds: IPv4 addresses, or /48 networks of IPv6 — the
// size of a site. A single range never outweighs a /8.
func rangeWeight(r Range) float64 {
	const most = 1 << 24
	if r.First.Is4() {
		first, last := r.First.As4(), r.Last.As4()
		return math.Min(float64(binary.BigEndian.Uint32(last[:])-binary.BigEndian.Uint32(first[:]))+1, most)
	}
	first, last := r.First.As16(), r.Last.As16()
	sites := (binary.BigEndian.Uint64(last[:8]) >> 16) - (binary.BigEndian.Uint64(first[:8]) >> 16) + 1
	return math.Min(float64(sites), most)
}

// midpoint is the address in the middle of a range.
func midpoint(r Range) netip.Addr {
	if r.First.Is4() {
		first, last := r.First.As4(), r.Last.As4()
		lo, hi := binary.BigEndian.Uint32(first[:]), binary.BigEndian.Uint32(last[:])
		var mid [4]byte
		binary.BigEndian.PutUint32(mid[:], lo+(hi-lo)/2)
		return netip.AddrFrom4(mid)
	}
	first, last := r.First.As16(), r.Last.As16()
	loHi, hiHi := binary.BigEndian.Uint64(first[:8]), binary.BigEndian.Uint64(last[:8])
	var mid [16]byte
	binary.BigEndian.PutUint64(mid[:8], loHi+(hiHi-loHi)/2)
	return netip.AddrFrom16(mid)
}

// cellOf numbers the half-degree cell a point falls into.
func cellOf(lat, lon float64) uint32 {
	row := uint32(math.Floor((lat + 90) * 2))
	column := uint32(math.Floor((lon + 180) * 2))
	return row*1000 + column
}

// placeByFacilities puts a network that is still nowhere into the city where most of its data
// centres are.
func (m *Model) placeByFacilities() {
	for node := range m.Nodes {
		if m.Nodes[node].Placed != PlacedNowhere || len(m.sites[node]) == 0 {
			continue
		}
		votes := map[uint32]*vote{}
		var top *vote
		for _, f := range m.sites[node] {
			facility := m.Facilities[f]
			if !facility.Placed {
				continue
			}
			key := cellOf(float64(facility.Lat), float64(facility.Lon))
			v := votes[key]
			if v == nil {
				v = &vote{}
				votes[key] = v
			}
			v.weight++
			v.lat += float64(facility.Lat)
			v.lon += float64(facility.Lon)
			if top == nil || v.weight > top.weight {
				top = v
			}
		}
		if top != nil {
			m.Nodes[node].Lat = float32(top.lat / top.weight)
			m.Nodes[node].Lon = float32(top.lon / top.weight)
			m.Nodes[node].Placed = PlacedByFacilities
		}
	}
}

// placeByNeighbours puts a network that is still nowhere next to its largest placed provider — or,
// without one, its largest placed neighbour — a little aside, so that the two do not cover each
// other. It reports whether anything moved.
func (m *Model) placeByNeighbours() bool {
	type choice struct {
		node int32
		near int32
	}
	var chosen []choice
	for node := range m.Nodes {
		if m.Nodes[node].Placed != PlacedNowhere {
			continue
		}
		best, bestProvider := int32(-1), false
		m.Neighbours(int32(node), func(neighbour int32, rel Rel, _ uint8) {
			if m.Nodes[neighbour].Placed == PlacedNowhere {
				return
			}
			provider := rel == RelCustomer
			switch {
			case best < 0, provider && !bestProvider,
				provider == bestProvider && m.Nodes[neighbour].Cone > m.Nodes[best].Cone:
				best, bestProvider = neighbour, provider
			}
		})
		if best >= 0 {
			chosen = append(chosen, choice{node: int32(node), near: best})
		}
	}
	for _, c := range chosen {
		near := m.Nodes[c.near]
		dLat, dLon := jitter(m.Nodes[c.node].ASN)
		m.Nodes[c.node].Lat = clampLat(near.Lat + dLat)
		m.Nodes[c.node].Lon = wrapLon(near.Lon + dLon)
		m.Nodes[c.node].Placed = PlacedByNeighbours
	}
	return len(chosen) > 0
}

// jitter is a small offset, the same for an AS every day, up to 0.6 of a degree.
func jitter(asn uint32) (dLat, dLon float32) {
	h := asn*2654435761 + 0x9e3779b9
	h ^= h >> 15
	angle := float64(h%3600) / 3600 * 2 * math.Pi
	radius := 0.2 + float64((h>>12)%400)/1000
	return float32(radius * math.Sin(angle)), float32(radius * math.Cos(angle))
}

func clampLat(lat float32) float32 { return float32(math.Max(-89.5, math.Min(89.5, float64(lat)))) }

func wrapLon(lon float32) float32 {
	switch {
	case lon > 180:
		return lon - 360
	case lon < -180:
		return lon + 360
	}
	return lon
}
