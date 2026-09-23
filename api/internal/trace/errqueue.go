package trace

import (
	"encoding/binary"
	"net/netip"
)

// What the kernel says about an ICMP answer to a probe (struct sock_extended_err of Linux, then
// the address of whoever sent it), read without the help of a system package so that it can be
// tested anywhere.
type icmpAnswer struct {
	origin   uint8 // 2: ICMP, 3: ICMPv6
	kind     uint8 // the ICMP type
	code     uint8
	offender netip.Addr
}

const (
	originICMP  = 2
	originICMP6 = 3
	// Address families of Linux.
	familyInet  = 2
	familyInet6 = 10
)

// parseExtendedError reads a control message of type IP_RECVERR / IPV6_RECVERR.
func parseExtendedError(data []byte) (icmpAnswer, bool) {
	const header = 16 // errno u32, origin, type, code, pad u8, info u32, data u32
	if len(data) < header+2 {
		return icmpAnswer{}, false
	}
	answer := icmpAnswer{origin: data[4], kind: data[5], code: data[6]}
	if answer.origin != originICMP && answer.origin != originICMP6 {
		return icmpAnswer{}, false
	}
	offender := data[header:]
	switch binary.NativeEndian.Uint16(offender) {
	case familyInet:
		if len(offender) < 8 {
			return icmpAnswer{}, false
		}
		answer.offender = netip.AddrFrom4([4]byte(offender[4:8]))
	case familyInet6:
		if len(offender) < 24 {
			return icmpAnswer{}, false
		}
		answer.offender = netip.AddrFrom16([16]byte(offender[8:24])).Unmap()
	default:
		return icmpAnswer{}, false
	}
	return answer, true
}

// What an answer means for the probe it answers.
type verdict int

const (
	passedOn verdict = iota // time exceeded: a router on the way
	arrived                 // port unreachable: the address itself
	stopped                 // another «unreachable»: a router will not pass it on
)

func (a icmpAnswer) verdict() verdict {
	switch {
	case a.origin == originICMP && a.kind == 11, a.origin == originICMP6 && a.kind == 3:
		return passedOn
	case a.origin == originICMP && a.kind == 3 && a.code == 3, a.origin == originICMP6 && a.kind == 1 && a.code == 4:
		return arrived
	default:
		return stopped
	}
}

// refusal names why a router stopped the probes.
func (a icmpAnswer) refusal() string {
	if a.origin == originICMP {
		switch a.code {
		case 0, 6:
			return "network unreachable"
		case 1, 7:
			return "host unreachable"
		case 9, 10, 13:
			return "prohibited"
		}
	} else {
		switch a.code {
		case 1:
			return "prohibited"
		case 3:
			return "host unreachable"
		case 0:
			return "network unreachable"
		}
	}
	return "unreachable"
}
