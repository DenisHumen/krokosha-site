// Package trace measures the way from this server to an address, the way tracepath does it and
// without any privileges: UDP probes with a growing TTL, whose ICMP answers the kernel hands back
// to the socket (IP_RECVERR) — the routers on the way, with the time each took to answer — and
// TCP handshakes with a port the address serves, for the time to the address itself (many
// hosts do not answer UDP probes, while a web server answers a handshake).
//
// It is a measurement of the path from this server, not from a visitor. Routers answer probes
// at their leisure: a hop that did not answer some of them is not proof of loss — the answers of
// the address itself are.
package trace

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// ErrUnsupported: probes with a TTL are sent on Linux only.
var ErrUnsupported = errors.New("trace: probing hop by hop is not supported on this system")

// Options shape a trace. The zero value is what the site uses.
type Options struct {
	MaxHops int           // default 30
	Probes  int           // per hop; default 3
	Wait    time.Duration // for answers after the last probe; default 2 s
	Ports   []int         // TCP ports tried for the time to the address; default 443, 80
	Connect int           // handshakes per port; default 5
}

func (o Options) withDefaults() Options {
	if o.MaxHops <= 0 {
		o.MaxHops = 30
	}
	o.MaxHops = min(o.MaxHops, 64) // a socket for every hop
	if o.Probes <= 0 {
		o.Probes = 3
	}
	o.Probes = min(o.Probes, 10)
	if o.Wait <= 0 {
		o.Wait = 2 * time.Second
	}
	if len(o.Ports) == 0 {
		o.Ports = []int{443, 80}
	}
	if o.Connect <= 0 {
		o.Connect = 5
	}
	return o
}

// Hop is one step of the way: who answered the probes sent with this TTL.
type Hop struct {
	TTL     int
	Addr    netip.Addr      // zero: nobody answered
	Others  []netip.Addr    // more routers that answered the same TTL (load balancing)
	RTTs    []time.Duration // of the probes that were answered
	Sent    int
	Reached bool   // the address itself answered: the way ends here
	Refused string // a router refused to pass the probes on («host unreachable», «prohibited»)
}

// Connect is the time to the address itself: TCP handshakes with a port it serves.
type Connect struct {
	Port int
	RTTs []time.Duration // of the handshakes that were answered (a refusal is an answer too)
	Sent int
}

// Result is a whole measurement.
type Result struct {
	From    netip.Addr // this server, as the address saw it leave
	To      netip.Addr
	Hops    []Hop
	Reached bool
	Connect *Connect // nil: no port answered
	Took    time.Duration
}

// Run traces the way to an address.
func Run(ctx context.Context, to netip.Addr, opt Options) (*Result, error) {
	opt = opt.withDefaults()
	started := time.Now()
	result := &Result{To: to.Unmap()}
	hops, from, err := probeHops(ctx, result.To, opt)
	if err != nil && !errors.Is(err, ErrUnsupported) {
		return nil, err
	}
	result.Hops, result.From = hops, from
	for _, hop := range hops {
		result.Reached = result.Reached || hop.Reached
	}
	result.Connect = connect(ctx, result.To, opt)
	result.Took = time.Since(started)
	return result, err
}

// connect times TCP handshakes with the first port that answers.
func connect(ctx context.Context, to netip.Addr, opt Options) *Connect {
	dialer := net.Dialer{Timeout: 1500 * time.Millisecond}
	for _, port := range opt.Ports {
		result := &Connect{Port: port}
		address := net.JoinHostPort(to.String(), strconv.Itoa(port))
		for range opt.Connect {
			if ctx.Err() != nil {
				return nil
			}
			result.Sent++
			started := time.Now()
			conn, err := dialer.DialContext(ctx, "tcp", address)
			took := time.Since(started)
			switch {
			case err == nil:
				_ = conn.Close()
				result.RTTs = append(result.RTTs, took)
			case refused(err):
				result.RTTs = append(result.RTTs, took) // a refusal comes back from the address itself
			}
			if len(result.RTTs) == 0 {
				break // a port that keeps silent the first time is not asked four times more
			}
			time.Sleep(50 * time.Millisecond)
		}
		if len(result.RTTs) > 0 {
			return result
		}
	}
	return nil
}

// refused tells a refused connection (a reset from the address) from silence.
func refused(err error) bool {
	var op *net.OpError
	if !errors.As(err, &op) || op.Timeout() {
		return false
	}
	return isRefused(op.Err)
}
