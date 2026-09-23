package netmap

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Service answers the questions of the /map page:
//
//	GET /api/net/route?from=IP&to=IP   the likely path between two addresses
//	GET /api/net/as/{asn}              one network: its size, neighbours, exchange points
//	GET /api/net/search?q=…            networks by number or name
//	GET /api/net/me                    the visitor's own address, to start a path from — not kept
type Service struct {
	model atomic.Pointer[Model]
	opts  ServiceOptions
}

// ServiceOptions are what the service works with. Only ClientIP is required.
type ServiceOptions struct {
	Geo      Locator                          // where an address is
	Limit    Limiter                          // requests per visitor; nil — no limit
	Cache    Cache                            // answers kept for a while; nil — none
	ClientIP func(ctx context.Context) net.IP // the visitor, as nginx tells it
	Log      *slog.Logger
}

// Limiter counts requests (cache.Cache is one).
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, per time.Duration) bool
}

// Cache keeps answers for a while (cache.Cache is one: Redis).
type Cache interface {
	Get(ctx context.Context, key string) (string, bool)
	Set(ctx context.Context, key, data string, ttl time.Duration)
}

// routeTTL is how long a route is kept: the map changes once a day, and its time is in the key.
const routeTTL = 10 * time.Minute

// How often a visitor may ask, per minute.
const (
	routesPerMinute   = 30
	lookupsPerMinute  = 60
	whoAmIPerMinute   = 30
	maxSearchResults  = 12
	maxNeighbourShown = 16
)

// NewService makes a service without a map: it answers «not ready» until Use gives it one.
func NewService(opts ServiceOptions) *Service {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Service{opts: opts}
}

// Use puts a fresh map in service; requests under way finish with the old one.
func (s *Service) Use(m *Model) { s.model.Store(m) }

// Model is the map in service, nil before the first one.
func (s *Service) Model() *Model { return s.model.Load() }

// Register adds the routes to a mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/net/route", s.route)
	mux.HandleFunc("GET /api/net/as/{asn}", s.as)
	mux.HandleFunc("GET /api/net/search", s.search)
	mux.HandleFunc("GET /api/net/me", s.me)
}

// Problems a visitor can cause, with the code the page shows a message for.
var (
	errBadAddress     = problem{http.StatusBadRequest, "bad_address"}
	errPrivateAddress = problem{http.StatusUnprocessableEntity, "private_address"}
	errNotRouted      = problem{http.StatusUnprocessableEntity, "not_routed"}
	errNoPath         = problem{http.StatusUnprocessableEntity, "no_path"}
	errUnknownAS      = problem{http.StatusNotFound, "unknown_as"}
	errBusy           = problem{http.StatusTooManyRequests, "busy"}
	errNotReady       = problem{http.StatusServiceUnavailable, "not_ready"}
)

type problem struct {
	status int
	code   string
}

func (s *Service) fail(w http.ResponseWriter, p problem) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.status)
	_, _ = w.Write([]byte(`{"error":"` + p.code + `"}` + "\n"))
}

func (s *Service) send(w http.ResponseWriter, value any, cache string) {
	body, err := json.Marshal(value)
	if err != nil {
		s.opts.Log.Error("netmap: an answer cannot be encoded", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.write(w, body, cache)
}

func (s *Service) write(w http.ResponseWriter, body []byte, cache string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", cache)
	_, _ = w.Write(append(body, '\n')) //nolint:gosec // JSON this service encoded, served as JSON: nothing is rendered
}

// allowed counts a request of the visitor against a limit.
func (s *Service) allowed(r *http.Request, kind string, limit int) bool {
	if s.opts.Limit == nil {
		return true
	}
	who := "unknown"
	if ip := s.opts.ClientIP(r.Context()); ip != nil {
		who = ip.String()
	}
	return s.opts.Limit.Allow(r.Context(), "net:"+kind+":"+who, limit, time.Minute)
}

// publicAddress reads an address a visitor typed. Addresses of private networks, loopback,
// multicast and the like are refused with their own message: they are not on the internet.
func publicAddress(text string) (netip.Addr, *problem) {
	addr, err := netip.ParseAddr(strings.TrimSpace(text))
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, &errBadAddress
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || isShared(addr) {
		return netip.Addr{}, &errPrivateAddress
	}
	return addr, nil
}

// shared are ranges that are not private in the sense of RFC 1918 but are not on the internet
// either: carrier-grade NAT, benchmarking, documentation.
var shared = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8::/32"),
}

func isShared(addr netip.Addr) bool {
	for _, prefix := range shared {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (s *Service) route(w http.ResponseWriter, r *http.Request) {
	m := s.model.Load()
	if m == nil {
		s.fail(w, errNotReady)
		return
	}
	if !s.allowed(r, "route", routesPerMinute) {
		s.fail(w, errBusy)
		return
	}
	from, bad := publicAddress(r.URL.Query().Get("from"))
	if bad != nil {
		s.fail(w, *bad)
		return
	}
	to, bad := publicAddress(r.URL.Query().Get("to"))
	if bad != nil {
		s.fail(w, *bad)
		return
	}
	// The same two addresses on the same map give the same route: the answer is kept a while.
	key := "net:route:" + strconv.FormatInt(m.Built.Unix(), 10) + ":" + from.String() + ":" + to.String()
	if s.opts.Cache != nil {
		if body, ok := s.opts.Cache.Get(r.Context(), key); ok {
			s.write(w, []byte(body), routeCaching)
			return
		}
	}
	route, err := m.Route(from, to, s.opts.Geo)
	switch {
	case errors.Is(err, ErrNotRouted):
		s.fail(w, errNotRouted)
		return
	case errors.Is(err, ErrNoPath):
		s.fail(w, errNoPath)
		return
	case err != nil:
		s.opts.Log.Error("netmap: route", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	body, err := json.Marshal(m.routeJSON(route))
	if err != nil {
		s.opts.Log.Error("netmap: an answer cannot be encoded", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if s.opts.Cache != nil {
		s.opts.Cache.Set(r.Context(), key, string(body), routeTTL)
	}
	s.write(w, body, routeCaching)
}

// routeCaching lets browsers keep a route as long as the service does.
const routeCaching = "public, max-age=600"

// RouteJSON is a route as the page reads it.
type RouteJSON struct {
	From  EndpointJSON `json:"from"`
	To    EndpointJSON `json:"to"`
	RTT   float64      `json:"rtt"` // estimated round trip, ms
	Kind  string       `json:"kind"`
	Hops  []HopJSON    `json:"hops"`
	Built time.Time    `json:"built"`
}

type EndpointJSON struct {
	IP      string   `json:"ip"`
	ASN     uint32   `json:"asn"`
	Name    string   `json:"name"`
	Country string   `json:"country"`
	Lat     *float32 `json:"lat"`
	Lon     *float32 `json:"lon"`
	Anycast bool     `json:"anycast,omitempty"`
}

type HopJSON struct {
	ASN     uint32       `json:"asn"`
	Name    string       `json:"name"`
	Country string       `json:"country"`
	Type    string       `json:"type,omitempty"`
	Rel     string       `json:"rel,omitempty"` // what the previous network is to this one
	Known   bool         `json:"known"`
	Meet    *MeetingJSON `json:"meet,omitempty"`
}

type MeetingJSON struct {
	Kind    string    `json:"kind"` // ix, facility, guess
	Name    string    `json:"name,omitempty"`
	City    string    `json:"city,omitempty"`
	Country string    `json:"country,omitempty"`
	Lat     float32   `json:"lat"`
	Lon     float32   `json:"lon"`
	Speeds  [2]int64  `json:"speeds"` // Mbit/s of the two networks' ports, 0 — not told
	Ports   [2]uint16 `json:"ports"`
	RTT     float64   `json:"rtt"`
}

var relNames = map[Rel]string{RelCustomer: "customer", RelProvider: "provider", RelPeer: "peer", RelUnknown: "unknown"}
var meetNames = map[MeetingKind]string{MeetIX: "ix", MeetFacility: "facility", MeetGuess: "guess"}

func (m *Model) endpointJSON(e Endpoint) EndpointJSON {
	out := EndpointJSON{IP: e.Addr.String(), ASN: e.ASN, Anycast: e.Anycast}
	if id, ok := m.Node(e.ASN); ok {
		out.Name, out.Country = m.Nodes[id].Name, m.Nodes[id].Country
	}
	if e.Located {
		lat, lon := e.Lat, e.Lon
		out.Lat, out.Lon = &lat, &lon
	}
	return out
}

func (m *Model) routeJSON(route *Route) RouteJSON {
	out := RouteJSON{
		From: m.endpointJSON(route.From), To: m.endpointJSON(route.To),
		RTT: round1(route.RTT), Kind: "inferred", Built: m.Built,
	}
	if route.Measured {
		out.Kind = "observed"
	}
	for i, hop := range route.Hops {
		h := HopJSON{ASN: hop.ASN, Name: hop.Name, Country: hop.Country, Type: hop.Type, Known: hop.Known}
		if i > 0 {
			h.Rel = relNames[hop.Rel]
		}
		if meeting := hop.Meeting; meeting != nil {
			h.Meet = &MeetingJSON{
				Kind: meetNames[meeting.Kind], Name: meeting.Name, City: meeting.City, Country: meeting.Country,
				Lat: meeting.Lat, Lon: meeting.Lon, Speeds: meeting.Speeds, Ports: meeting.Ports, RTT: round1(meeting.RTT),
			}
		}
		out.Hops = append(out.Hops, h)
	}
	return out
}

func round1(value float64) float64 { return float64(int64(value*10+0.5)) / 10 }

// ASJSON is one network as its card on the page shows it.
type ASJSON struct {
	ASN        uint32         `json:"asn"`
	Name       string         `json:"name"`
	Country    string         `json:"country"`
	Type       string         `json:"type,omitempty"`
	Cone       uint32         `json:"cone"`
	Rank       uint32         `json:"rank"`
	Customers  uint32         `json:"customers"`
	Providers  uint32         `json:"providers"`
	Peers      uint32         `json:"peers"`
	Lat        *float32       `json:"lat"`
	Lon        *float32       `json:"lon"`
	Neighbours NeighboursJSON `json:"neighbours"` // the largest of each kind
	Exchanges  []PortJSON     `json:"exchanges"`  // fastest first
}

type NeighboursJSON struct {
	Providers []BriefJSON `json:"providers"`
	Peers     []BriefJSON `json:"peers"`
	Customers []BriefJSON `json:"customers"`
}

type BriefJSON struct {
	ASN     uint32 `json:"asn"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Cone    uint32 `json:"cone"`
}

type PortJSON struct {
	Name    string `json:"name"`
	City    string `json:"city"`
	Country string `json:"country"`
	Speed   int64  `json:"speed"` // Mbit/s
	Ports   uint16 `json:"ports"`
}

func (s *Service) as(w http.ResponseWriter, r *http.Request) {
	m := s.model.Load()
	if m == nil {
		s.fail(w, errNotReady)
		return
	}
	if !s.allowed(r, "lookup", lookupsPerMinute) {
		s.fail(w, errBusy)
		return
	}
	asn, err := ParseASN(r.PathValue("asn"))
	id, known := m.Node(asn)
	if err != nil || !known {
		s.fail(w, errUnknownAS)
		return
	}
	node := &m.Nodes[id]
	out := ASJSON{ASN: node.ASN, Name: node.Name, Country: node.Country, Type: node.Type, Cone: node.Cone,
		Rank: node.Rank, Customers: node.Customers, Providers: node.Providers, Peers: node.Peers,
		Neighbours: NeighboursJSON{Providers: []BriefJSON{}, Peers: []BriefJSON{}, Customers: []BriefJSON{}},
		Exchanges:  []PortJSON{}}
	if node.Placed != PlacedNowhere {
		lat, lon := node.Lat, node.Lon
		out.Lat, out.Lon = &lat, &lon
	}
	m.Neighbours(id, func(v int32, rel Rel, _ uint8) {
		brief := BriefJSON{ASN: m.Nodes[v].ASN, Name: m.Nodes[v].Name, Country: m.Nodes[v].Country, Cone: m.Nodes[v].Cone}
		switch rel {
		case RelCustomer:
			out.Neighbours.Providers = append(out.Neighbours.Providers, brief)
		case RelProvider:
			out.Neighbours.Customers = append(out.Neighbours.Customers, brief)
		default:
			out.Neighbours.Peers = append(out.Neighbours.Peers, brief)
		}
	})
	for _, list := range []*[]BriefJSON{&out.Neighbours.Providers, &out.Neighbours.Peers, &out.Neighbours.Customers} {
		sort.Slice(*list, func(i, j int) bool {
			a, b := (*list)[i], (*list)[j]
			return a.Cone > b.Cone || a.Cone == b.Cone && a.ASN < b.ASN
		})
		if len(*list) > maxNeighbourShown {
			*list = (*list)[:maxNeighbourShown]
		}
	}
	for _, port := range m.Ports(id) {
		ix := &m.IXs[port.IX]
		out.Exchanges = append(out.Exchanges, PortJSON{Name: ix.Name, City: ix.City, Country: ix.Country, Speed: port.Speed, Ports: port.Count})
	}
	sort.Slice(out.Exchanges, func(i, j int) bool { return out.Exchanges[i].Speed > out.Exchanges[j].Speed })
	s.send(w, out, "public, max-age=3600")
}

func (s *Service) search(w http.ResponseWriter, r *http.Request) {
	m := s.model.Load()
	if m == nil {
		s.fail(w, errNotReady)
		return
	}
	if !s.allowed(r, "lookup", lookupsPerMinute) {
		s.fail(w, errBusy)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	found := []BriefJSON{}
	if asn, err := ParseASN(query); err == nil {
		if id, ok := m.Node(asn); ok {
			n := &m.Nodes[id]
			found = append(found, BriefJSON{ASN: n.ASN, Name: n.Name, Country: n.Country, Cone: n.Cone})
		}
	} else if len(query) >= 2 {
		needle := strings.ToLower(query)
		var ids []int32
		for i := range m.Nodes {
			if strings.Contains(strings.ToLower(m.Nodes[i].Name), needle) {
				ids = append(ids, int32(i))
			}
		}
		sort.Slice(ids, func(i, j int) bool { return m.Nodes[ids[i]].Rank < m.Nodes[ids[j]].Rank })
		for _, id := range ids[:min(len(ids), maxSearchResults)] {
			n := &m.Nodes[id]
			found = append(found, BriefJSON{ASN: n.ASN, Name: n.Name, Country: n.Country, Cone: n.Cone})
		}
	}
	s.send(w, map[string]any{"results": found}, "public, max-age=3600")
}

// me tells visitors their own address and network, so that a path can start from them. Nothing is
// kept: the answer is not even cached.
func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	m := s.model.Load()
	if m == nil {
		s.fail(w, errNotReady)
		return
	}
	if !s.allowed(r, "me", whoAmIPerMinute) {
		s.fail(w, errBusy)
		return
	}
	ip := s.opts.ClientIP(r.Context())
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		s.fail(w, errBadAddress)
		return
	}
	end := Endpoint{Addr: addr.Unmap()}
	if asn, ok := m.Prefixes.Lookup(end.Addr); ok {
		end.ASN = asn
	}
	if s.opts.Geo != nil {
		if lat, lon, ok := s.opts.Geo.Where(end.Addr); ok {
			end.Lat, end.Lon, end.Located = float32(lat), float32(lon), true
		}
	}
	s.send(w, m.endpointJSON(end), "no-store")
}
