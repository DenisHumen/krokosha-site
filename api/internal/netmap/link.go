package netmap

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Rel says what the first AS of a link is to the second.
type Rel int8

const (
	RelPeer     Rel = 0  // settlement-free peers: each carries the other's own traffic and its customers'
	RelProvider Rel = -1 // the first sells transit to the second (CAIDA writes a provider-customer link as -1)
	RelCustomer Rel = 1  // the first buys transit from the second
	RelUnknown  Rel = 2  // seen in a path, not classified yet (routed like a peering)
)

// Reverse is the same link seen from the other end.
func (r Rel) Reverse() Rel {
	switch r {
	case RelProvider:
		return RelCustomer
	case RelCustomer:
		return RelProvider
	}
	return r
}

// Source bits say where a link was seen.
const (
	SourceBGP uint8 = 1 << iota // CAIDA: inferred from BGP paths
	SourceMLP                   // CAIDA: multilateral peering at an exchange point's route server
	SourceRIB                   // a RouteViews snapshot of today
)

// Link is one link between two ASes, the smaller number first.
type Link struct {
	A, B    uint32
	Rel     Rel // what A is to B
	Sources uint8
}

// NewLink puts the smaller number first and turns the relation around when it has to.
func NewLink(a, b uint32, rel Rel, sources uint8) Link {
	if a > b {
		return Link{A: b, B: a, Rel: rel.Reverse(), Sources: sources}
	}
	return Link{A: a, B: b, Rel: rel, Sources: sources}
}

// Key identifies a link regardless of its direction.
func (l Link) Key() uint64 { return uint64(l.A)<<32 | uint64(l.B) }

// ParseRelationships reads a CAIDA AS Relationships file (serial-1 or serial-2):
//
//	# comments
//	<provider>|<customer>|-1[|<source>]
//	<peer>|<peer>|0[|<source>]
//
// Links with a private or reserved AS number are left out.
func ParseRelationships(r io.Reader) ([]Link, error) {
	var links []Link
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for number := 1; scanner.Scan(); number++ {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 3 {
			return nil, fmt.Errorf("line %d: %q is not <as>|<as>|<relation>", number, line)
		}
		a, errA := strconv.ParseUint(fields[0], 10, 32)
		b, errB := strconv.ParseUint(fields[1], 10, 32)
		kind, errK := strconv.Atoi(fields[2])
		if errA != nil || errB != nil || errK != nil || (kind != 0 && kind != -1) {
			return nil, fmt.Errorf("line %d: %q is not <as>|<as>|<relation>", number, line)
		}
		if a == b || !PublicASN(uint32(a)) || !PublicASN(uint32(b)) {
			continue
		}
		var sources uint8
		if len(fields) > 3 {
			for _, source := range strings.Split(fields[3], ",") {
				switch source {
				case "bgp":
					sources |= SourceBGP
				case "mlp":
					sources |= SourceMLP
				}
			}
		}
		if sources == 0 {
			sources = SourceBGP // serial-1 has no source column: every link there comes from BGP paths
		}
		rel := RelPeer
		if kind == -1 {
			rel = RelProvider // <provider>|<customer>|-1
		}
		links = append(links, NewLink(uint32(a), uint32(b), rel, sources))
	}
	return links, scanner.Err()
}
