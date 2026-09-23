//go:build linux

package trace

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"time"

	"golang.org/x/sys/unix"
)

func isRefused(err error) bool { return errors.Is(err, unix.ECONNREFUSED) }

// basePort is where the probes go, a port of its own for every probe (as traceroute does it):
// nothing listens there, so the address itself answers «port unreachable», and the port an
// answer is about tells which probe it answers — many routers quote the UDP header of a probe
// but not a byte of what it carried.
const basePort = 33434

// roundGap is the pause between two rounds of probes.
const roundGap = 120 * time.Millisecond

// probeHops sends Probes datagrams for every TTL up to MaxHops, each TTL from a socket of its
// own, in rounds, and reads the answers the kernel puts into the sockets' error queues.
func probeHops(ctx context.Context, to netip.Addr, opt Options) ([]Hop, netip.Addr, error) {
	sockets := make([]int, opt.MaxHops)
	for i := range sockets {
		sockets[i] = -1
	}
	defer func() {
		for _, fd := range sockets {
			if fd >= 0 {
				_ = unix.Close(fd)
			}
		}
	}()
	for i := range sockets {
		fd, err := openProbe(to, i+1)
		if err != nil {
			return nil, netip.Addr{}, err
		}
		sockets[i] = fd
	}

	p := &prober{to: to, probes: opt.Probes, sockets: sockets, hops: make([]Hop, opt.MaxHops), sent: make([][]time.Time, opt.MaxHops)}
	for i := range p.hops {
		p.hops[i].TTL = i + 1
		p.sent[i] = make([]time.Time, opt.Probes)
	}
	for round := range opt.Probes {
		for i := range sockets {
			p.send(i, round)
		}
		if err := p.collect(ctx, time.Now().Add(roundGap)); err != nil {
			return nil, netip.Addr{}, err
		}
	}
	if err := p.collect(ctx, time.Now().Add(opt.Wait)); err != nil {
		return nil, netip.Addr{}, err
	}
	return trim(p.hops), source(to), nil
}

// openProbe makes the socket the probes of one TTL are sent from.
func openProbe(to netip.Addr, ttl int) (int, error) {
	family, level, recverr, hops := unix.AF_INET, unix.IPPROTO_IP, unix.IP_RECVERR, unix.IP_TTL
	if to.Is6() {
		family, level, recverr, hops = unix.AF_INET6, unix.IPPROTO_IPV6, unix.IPV6_RECVERR, unix.IPV6_UNICAST_HOPS
	}
	fd, err := unix.Socket(family, unix.SOCK_DGRAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := errors.Join(unix.SetsockoptInt(fd, level, recverr, 1), unix.SetsockoptInt(fd, level, hops, ttl)); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// source is the address the probes leave from: what the kernel picks for a socket connected to
// the address (connecting a UDP socket sends nothing).
func source(to netip.Addr) netip.Addr {
	family := unix.AF_INET
	if to.Is6() {
		family = unix.AF_INET6
	}
	fd, err := unix.Socket(family, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return netip.Addr{}
	}
	defer func() { _ = unix.Close(fd) }()
	if unix.Connect(fd, sockaddr(to, basePort)) != nil {
		return netip.Addr{}
	}
	return localAddr(fd)
}

func sockaddr(to netip.Addr, port int) unix.Sockaddr {
	if to.Is4() {
		return &unix.SockaddrInet4{Port: port, Addr: to.As4()}
	}
	return &unix.SockaddrInet6{Port: port, Addr: to.As16()}
}

func localAddr(fd int) netip.Addr {
	switch sa := sockaddrOf(fd).(type) {
	case *unix.SockaddrInet4:
		return netip.AddrFrom4(sa.Addr)
	case *unix.SockaddrInet6:
		return netip.AddrFrom16(sa.Addr).Unmap()
	}
	return netip.Addr{}
}

func sockaddrOf(fd int) unix.Sockaddr {
	sa, err := unix.Getsockname(fd)
	if err != nil {
		return nil
	}
	return sa
}

type prober struct {
	to      netip.Addr
	probes  int
	sockets []int
	hops    []Hop
	sent    [][]time.Time // per TTL, per round: when the probe left
}

// port is where the probe of one round for one TTL goes.
func (p *prober) port(i, round int) int { return basePort + i*p.probes + round }

// send sends the probe of one round for one TTL. An answer to an earlier probe that has not been
// read yet may make the send fail once: it is read, and the probe goes again.
func (p *prober) send(i, round int) {
	payload := []byte{'K', 'T', byte(i + 1), byte(round)} //nolint:gosec // at most 64 hops and 10 rounds: withDefaults
	for attempt := 0; attempt < 2; attempt++ {
		p.sent[i][round] = time.Now()
		if err := unix.Sendto(p.sockets[i], payload, 0, sockaddr(p.to, p.port(i, round))); err == nil {
			p.hops[i].Sent++
			return
		}
		p.drain(i)
	}
}

// collect reads answers until the deadline.
func (p *prober) collect(ctx context.Context, deadline time.Time) error {
	fds := make([]unix.PollFd, len(p.sockets))
	for i, fd := range p.sockets {
		fds[i] = unix.PollFd{Fd: int32(fd)} //nolint:gosec // descriptors of this process
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil
		}
		// Only errors are waited for: an answer is an error of the socket.
		n, err := unix.Poll(fds, int(left.Milliseconds())+1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		for i := range fds {
			if fds[i].Revents&unix.POLLERR != 0 {
				p.drain(i)
			}
		}
	}
}

// drain reads the answers queued on the socket of one TTL.
func (p *prober) drain(i int) {
	buf := make([]byte, 64)
	oob := make([]byte, 256)
	for {
		n, oobn, _, to, err := unix.Recvmsg(p.sockets[i], buf, oob, unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
		if err != nil {
			return
		}
		received := time.Now()
		messages, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			continue
		}
		for _, message := range messages {
			if !answerOfICMP(message.Header) {
				continue
			}
			answer, ok := parseExtendedError(message.Data)
			if !ok {
				continue
			}
			p.record(i, answer, p.round(i, to, buf[:n]), received)
		}
	}
}

// answerOfICMP tells a control message that carries an ICMP answer from others.
func answerOfICMP(h unix.Cmsghdr) bool {
	return (h.Level == unix.IPPROTO_IP && h.Type == unix.IP_RECVERR) ||
		(h.Level == unix.IPPROTO_IPV6 && h.Type == unix.IPV6_RECVERR)
}

// round tells which probe an answer is about: by the port the probe went to — the kernel says
// where the answered packet was going — or, failing that, by what the probe carried.
func (p *prober) round(i int, to unix.Sockaddr, payload []byte) int {
	port := -1
	switch sa := to.(type) {
	case *unix.SockaddrInet4:
		port = sa.Port
	case *unix.SockaddrInet6:
		port = sa.Port
	}
	if round := port - p.port(i, 0); port > 0 && round >= 0 && round < p.probes {
		return round
	}
	if len(payload) >= 4 && payload[0] == 'K' && payload[1] == 'T' && int(payload[2]) == i+1 && int(payload[3]) < p.probes {
		return int(payload[3])
	}
	return -1
}

func (p *prober) record(i int, answer icmpAnswer, round int, received time.Time) {
	hop := &p.hops[i]
	switch {
	case !hop.Addr.IsValid():
		hop.Addr = answer.offender
	case answer.offender != hop.Addr && !slices.Contains(hop.Others, answer.offender):
		hop.Others = append(hop.Others, answer.offender)
	}
	if round >= 0 && !p.sent[i][round].IsZero() {
		hop.RTTs = append(hop.RTTs, received.Sub(p.sent[i][round]))
	}
	switch answer.verdict() {
	case arrived:
		hop.Reached = true
	case stopped:
		hop.Refused = answer.refusal()
	}
}

// trim ends the way at the first TTL the address itself answered, or a router refused to pass
// on; without either, after the last hop anybody answered (and one silent hop after it).
func trim(hops []Hop) []Hop {
	for i, hop := range hops {
		if hop.Reached || hop.Refused != "" {
			return hops[:i+1]
		}
	}
	last := -1
	for i, hop := range hops {
		if hop.Addr.IsValid() {
			last = i
		}
	}
	return hops[:min(last+2, len(hops))]
}
