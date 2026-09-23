package netmap

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/trace"
)

// Measured ways: GET /api/net/trace?to=IP — the way from this server to an address, hop by hop
// (internal/trace), with the network, the exchange point and the place of every hop. The route of
// /api/net/route is what the policies of the networks make likely; this is what the packets did.

// Tracer measures the way from this server to an address: trace.Run, in the service.
type Tracer func(ctx context.Context, to netip.Addr) (*trace.Result, error)

// Resolver names addresses (reverse DNS): net.DefaultResolver.LookupAddr, in the service.
type Resolver func(ctx context.Context, addr string) ([]string, error)

const (
	tracesPerMinute = 3 // per visitor: every trace sends some hundred packets
	tracesAtOnce    = 2 // for the whole server
	traceTimeout    = 25 * time.Second
	traceTTL        = 10 * time.Minute // a measurement from this server is the same for everyone
	namesTimeout    = 2 * time.Second
)

var (
	errTraceFailed = problem{http.StatusBadGateway, "trace_failed"}
	errTracing     = problem{http.StatusTooManyRequests, "busy"}
)

// TraceJSON is a measured way as the page reads it.
type TraceJSON struct {
	From    EndpointJSON   `json:"from"` // this server
	To      EndpointJSON   `json:"to"`
	Hops    []TraceHopJSON `json:"hops"`
	Reached bool           `json:"reached"`           // the address itself answered the probes
	Connect *ConnectJSON   `json:"connect,omitempty"` // TCP handshakes with the address
	At      time.Time      `json:"at"`
}

type TraceHopJSON struct {
	TTL     int       `json:"ttl"`
	IP      string    `json:"ip,omitempty"` // empty: nobody answered
	Others  []string  `json:"others,omitempty"`
	Host    string    `json:"host,omitempty"` // reverse DNS
	ASN     uint32    `json:"asn,omitempty"`
	Name    string    `json:"name,omitempty"`
	Country string    `json:"country,omitempty"`
	IX      string    `json:"ix,omitempty"` // the exchange point whose peering LAN the address is in
	Lat     *float32  `json:"lat"`
	Lon     *float32  `json:"lon"`
	Sent    int       `json:"sent"`
	RTTs    []float64 `json:"rtts"` // ms, of the probes that were answered
	Reached bool      `json:"reached,omitempty"`
	Refused string    `json:"refused,omitempty"`
}

type ConnectJSON struct {
	Port int       `json:"port"`
	Sent int       `json:"sent"`
	RTTs []float64 `json:"rtts"`
}

func (s *Service) trace(w http.ResponseWriter, r *http.Request) {
	m := s.model.Load()
	if m == nil {
		s.fail(w, errNotReady)
		return
	}
	if !s.allowed(r, "trace", tracesPerMinute) {
		s.fail(w, errBusy)
		return
	}
	to, bad := publicAddress(r.URL.Query().Get("to"))
	if bad != nil {
		s.fail(w, *bad)
		return
	}
	key := "net:trace:" + to.String()
	if s.opts.Cache != nil {
		if body, ok := s.opts.Cache.Get(r.Context(), key); ok {
			s.write(w, []byte(body), traceCaching)
			return
		}
	}
	select {
	case s.tracing <- struct{}{}:
		defer func() { <-s.tracing }()
	default:
		s.fail(w, errTracing)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), traceTimeout)
	defer cancel()
	result, err := s.opts.Trace(ctx, to)
	if result == nil {
		s.opts.Log.Error("netmap: trace", "error", err)
		s.fail(w, errTraceFailed)
		return
	}
	body, err := json.Marshal(m.traceJSON(ctx, result, s.opts.Geo, s.resolver(), time.Now().UTC()))
	if err != nil {
		s.opts.Log.Error("netmap: an answer cannot be encoded", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if s.opts.Cache != nil {
		s.opts.Cache.Set(r.Context(), key, string(body), traceTTL)
	}
	s.write(w, body, traceCaching)
}

// traceCaching lets browsers keep a measurement as long as the service does.
const traceCaching = "public, max-age=600"

func (s *Service) resolver() Resolver {
	if s.opts.Resolve != nil {
		return s.opts.Resolve
	}
	return net.DefaultResolver.LookupAddr
}

func (m *Model) traceJSON(ctx context.Context, result *trace.Result, geo Locator, resolve Resolver, at time.Time) TraceJSON {
	out := TraceJSON{
		From: m.plainEndpointJSON(result.From, geo), To: m.plainEndpointJSON(result.To, geo),
		Reached: result.Reached, At: at, Hops: []TraceHopJSON{},
	}
	var addrs []netip.Addr
	for _, hop := range result.Hops {
		if hop.Addr.IsValid() {
			addrs = append(addrs, hop.Addr)
		}
	}
	hosts := names(ctx, addrs, resolve)
	for _, hop := range result.Hops {
		h := TraceHopJSON{TTL: hop.TTL, Sent: hop.Sent, Reached: hop.Reached, Refused: hop.Refused, RTTs: milliseconds(hop.RTTs)}
		if hop.Addr.IsValid() {
			h.IP, h.Host = hop.Addr.String(), hosts[hop.Addr]
			for _, other := range hop.Others {
				h.Others = append(h.Others, other.String())
			}
			if asn, ok := m.Prefixes.Lookup(hop.Addr); ok {
				h.ASN = asn
				if id, ok := m.Node(asn); ok {
					h.Name, h.Country = m.Nodes[id].Name, m.Nodes[id].Country
				}
			}
			// An address of a peering LAN is the port of a network there, as PeeringDB lists it:
			// that network's router answered, whoever announces the LAN.
			if node, _, ok := m.PortOf(hop.Addr); ok {
				h.ASN, h.Name, h.Country = m.Nodes[node].ASN, m.Nodes[node].Name, m.Nodes[node].Country
			}
			// A port at an exchange point is where the exchange point is — a better place than
			// what GeoIP says of an address in its peering LAN.
			if x, ok := m.ExchangeOf(hop.Addr); ok {
				ix := &m.IXs[x]
				h.IX = ix.Name
				if ix.Placed {
					lat, lon := ix.Lat, ix.Lon
					h.Lat, h.Lon = &lat, &lon
				}
			}
			if h.Lat == nil && geo != nil {
				if lat, lon, ok := geo.Where(hop.Addr); ok {
					la, lo := float32(lat), float32(lon)
					h.Lat, h.Lon = &la, &lo
				}
			}
		}
		out.Hops = append(out.Hops, h)
	}
	if c := result.Connect; c != nil {
		out.Connect = &ConnectJSON{Port: c.Port, Sent: c.Sent, RTTs: milliseconds(c.RTTs)}
	}
	return out
}

// plainEndpointJSON describes an address whether or not anybody announces it.
func (m *Model) plainEndpointJSON(addr netip.Addr, geo Locator) EndpointJSON {
	end := Endpoint{Addr: addr}
	if !addr.IsValid() {
		return EndpointJSON{}
	}
	if asn, ok := m.Prefixes.Lookup(addr); ok {
		end.ASN = asn
	}
	if geo != nil {
		if lat, lon, ok := geo.Where(addr); ok {
			end.Lat, end.Lon, end.Located = float32(lat), float32(lon), true
		}
	}
	return m.endpointJSON(end)
}

// names asks reverse DNS about every address at once, and waits a little: a name is a nicety.
func names(ctx context.Context, addrs []netip.Addr, resolve Resolver) map[netip.Addr]string {
	ctx, cancel := context.WithTimeout(ctx, namesTimeout)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := map[netip.Addr]string{}
	for _, addr := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			found, err := resolve(ctx, addr.String())
			if err != nil || len(found) == 0 {
				return
			}
			mu.Lock()
			out[addr] = cutTo(strings.TrimSuffix(found[0], "."), 253)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func milliseconds(durations []time.Duration) []float64 {
	out := make([]float64, len(durations))
	for i, d := range durations {
		out[i] = round1(float64(d.Microseconds()) / 1000)
	}
	return out
}
