package netmap

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// AS is what is known about one autonomous system.
type AS struct {
	Name    string // «CLOUDFLARENET», as the registry has it
	Country string // ISO 3166-1 alpha-2 of the registration, «US»
}

// Prefixes answer «which AS announces this address?». IPv4 and IPv6 ranges are kept apart, in
// order: an address is found by a binary search, about 20 steps.
type Prefixes struct {
	v4 []span4
	v6 []span6
}

type span4 struct {
	lo, hi, asn uint32
}

type span6 struct {
	loHi, loLo, hiHi, hiLo uint64
	asn                    uint32
}

// Lookup finds the AS that announces an address. ok is false for an address nobody announces.
func (p *Prefixes) Lookup(addr netip.Addr) (asn uint32, ok bool) {
	if p == nil || !addr.IsValid() {
		return 0, false
	}
	addr = addr.Unmap()
	if addr.Is4() {
		ip := binary.BigEndian.Uint32(addr.AsSlice())
		i := sort.Search(len(p.v4), func(i int) bool { return p.v4[i].lo > ip }) - 1
		if i >= 0 && ip <= p.v4[i].hi {
			return p.v4[i].asn, true
		}
		return 0, false
	}
	raw := addr.As16()
	hi, lo := binary.BigEndian.Uint64(raw[:8]), binary.BigEndian.Uint64(raw[8:])
	i := sort.Search(len(p.v6), func(i int) bool {
		s := p.v6[i]
		return s.loHi > hi || (s.loHi == hi && s.loLo > lo)
	}) - 1
	if i >= 0 {
		s := p.v6[i]
		if hi < s.hiHi || (hi == s.hiHi && lo <= s.hiLo) {
			return s.asn, true
		}
	}
	return 0, false
}

// Len is how many ranges are known: IPv4, IPv6.
func (p *Prefixes) Len() (v4, v6 int) { return len(p.v4), len(p.v6) }

// Range is one announced range of addresses and its AS.
type Range struct {
	First, Last netip.Addr
	ASN         uint32
}

// Each calls fn for every announced range, IPv4 first.
func (p *Prefixes) Each(fn func(Range)) {
	for _, s := range p.v4 {
		var first, last [4]byte
		binary.BigEndian.PutUint32(first[:], s.lo)
		binary.BigEndian.PutUint32(last[:], s.hi)
		fn(Range{First: netip.AddrFrom4(first), Last: netip.AddrFrom4(last), ASN: s.asn})
	}
	for _, s := range p.v6 {
		var first, last [16]byte
		binary.BigEndian.PutUint64(first[:8], s.loHi)
		binary.BigEndian.PutUint64(first[8:], s.loLo)
		binary.BigEndian.PutUint64(last[:8], s.hiHi)
		binary.BigEndian.PutUint64(last[8:], s.hiLo)
		fn(Range{First: netip.AddrFrom16(first), Last: netip.AddrFrom16(last), ASN: s.asn})
	}
}

// maxNameBytes is the width of the name column.
const maxNameBytes = 128

// ParseIPtoASN reads the table of iptoasn.com (ip2asn-combined.tsv, ip2asn-v4.tsv, ip2asn-v6.tsv):
//
//	<first address>\t<last address>\t<AS number>\t<country>\t<AS name>
//
// It gives the ranges every AS announces and the name and country of each AS. Ranges of AS 0 —
// «not routed» — and of private AS numbers are not kept.
func ParseIPtoASN(r io.Reader) (*Prefixes, map[uint32]AS, error) {
	prefixes := &Prefixes{}
	names := make(map[uint32]AS, 1<<17)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for number := 1; scanner.Scan(); number++ {
		fields := strings.SplitN(scanner.Text(), "\t", 5)
		if len(fields) < 3 {
			continue
		}
		value, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return nil, nil, fmt.Errorf("line %d: AS number %q", number, fields[2])
		}
		asn := uint32(value)
		if !PublicASN(asn) {
			continue
		}
		first, errFirst := netip.ParseAddr(fields[0])
		last, errLast := netip.ParseAddr(fields[1])
		if errFirst != nil || errLast != nil || first.Is4() != last.Is4() || last.Less(first) {
			return nil, nil, fmt.Errorf("line %d: range %q – %q", number, fields[0], fields[1])
		}
		prefixes.add(first, last, asn)
		if _, known := names[asn]; !known {
			var as AS
			if len(fields) > 3 && len(fields[3]) == 2 && fields[3] != "None" {
				as.Country = fields[3]
			}
			if len(fields) > 4 && fields[4] != "Not routed" {
				as.Name = cut(strings.TrimSpace(fields[4]))
			}
			names[asn] = as
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	prefixes.sort()
	return prefixes, names, nil
}

func (p *Prefixes) add(first, last netip.Addr, asn uint32) {
	if first.Is4() {
		lo, hi := first.As4(), last.As4()
		p.v4 = append(p.v4, span4{lo: binary.BigEndian.Uint32(lo[:]), hi: binary.BigEndian.Uint32(hi[:]), asn: asn})
		return
	}
	lo, hi := first.As16(), last.As16()
	p.v6 = append(p.v6, span6{
		loHi: binary.BigEndian.Uint64(lo[:8]), loLo: binary.BigEndian.Uint64(lo[8:]),
		hiHi: binary.BigEndian.Uint64(hi[:8]), hiLo: binary.BigEndian.Uint64(hi[8:]),
		asn: asn,
	})
}

// sort orders the ranges by their first address, as Lookup needs; the source is usually in order already.
func (p *Prefixes) sort() {
	sort.Slice(p.v4, func(i, j int) bool { return p.v4[i].lo < p.v4[j].lo })
	sort.Slice(p.v6, func(i, j int) bool {
		a, b := p.v6[i], p.v6[j]
		return a.loHi < b.loHi || (a.loHi == b.loHi && a.loLo < b.loLo)
	})
}

// cut shortens a name to at most maxNameBytes without breaking a character.
func cut(text string) string { return cutTo(text, maxNameBytes) }

// cutTo shortens text to at most limit bytes without breaking a character.
func cutTo(text string, limit int) string {
	for len(text) > limit {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	return text
}
