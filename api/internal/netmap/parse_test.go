package netmap

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestPublicASN(t *testing.T) {
	for asn, want := range map[uint32]bool{
		13335: true, 1: true, 64495: true, 131072: true, 4199999999: true,
		0: false, 23456: false, 64496: false, 64512: false, 65534: false, 65535: false,
		65536: false, 65551: false, 70000: false, 4200000000: false, 4294967295: false,
	} {
		if got := PublicASN(asn); got != want {
			t.Errorf("PublicASN(%d) = %v", asn, got)
		}
	}
	for _, tc := range []struct {
		text string
		want uint32
	}{{"13335", 13335}, {"AS13335", 13335}, {" as15169 ", 15169}} {
		if got, err := ParseASN(tc.text); err != nil || got != tc.want {
			t.Errorf("ParseASN(%q) = %d, %v", tc.text, got, err)
		}
	}
	if _, err := ParseASN("ASX"); err == nil {
		t.Error("ParseASN accepted a word")
	}
}

func TestParseRelationships(t *testing.T) {
	links, err := ParseRelationships(strings.NewReader(`# source:topology|BGP|20260901|routeviews|eqix
# a comment
3356|15169|-1|bgp
13335|6939|0|bgp,mlp
2914|64512|-1|bgp
1299|3356|0
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Link{
		{A: 3356, B: 15169, Rel: RelProvider, Sources: SourceBGP},
		{A: 6939, B: 13335, Rel: RelPeer, Sources: SourceBGP | SourceMLP}, // the smaller number first
		{A: 1299, B: 3356, Rel: RelPeer, Sources: SourceBGP},              // serial-1: no source column
	}
	if !reflect.DeepEqual(links, want) {
		t.Errorf("links: %+v", links)
	}
	// A provider written second: the relation turns around with the order.
	if link := NewLink(15169, 3356, RelCustomer, 0); link.A != 3356 || link.Rel != RelProvider {
		t.Errorf("NewLink: %+v", link)
	}
	if _, err := ParseRelationships(strings.NewReader("3356|15169\n")); err == nil {
		t.Error("a line without a relation was accepted")
	}
}

func TestIPtoASN(t *testing.T) {
	prefixes, names, err := ParseIPtoASN(strings.NewReader(strings.Join([]string{
		"1.0.0.0\t1.0.0.255\t13335\tUS\tCLOUDFLARENET",
		"1.0.1.0\t1.0.3.255\t0\tNone\tNot routed",
		"8.8.8.0\t8.8.8.255\t15169\tUS\tGOOGLE",
		"10.0.0.0\t10.255.255.255\t64512\tNone\tprivate",
		"46.175.144.0\t46.175.151.255\t50673\tNL\tSERVERIUS-AS",
		"2606:4700::\t2606:4700:ffff:ffff:ffff:ffff:ffff:ffff\t13335\tUS\tCLOUDFLARENET",
		"2a00:1450::\t2a00:1450:4fff:ffff:ffff:ffff:ffff:ffff\t15169\tUS\tGOOGLE",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if v4, v6 := prefixes.Len(); v4 != 3 || v6 != 2 {
		t.Errorf("ranges: %d IPv4, %d IPv6", v4, v6)
	}
	for addr, want := range map[string]uint32{
		"1.0.0.1": 13335, "1.0.0.255": 13335, "1.0.1.1": 0, "8.8.8.8": 15169, "8.8.9.1": 0,
		"::ffff:8.8.8.8": 15169, "10.1.2.3": 0, "46.175.147.165": 50673,
		"2606:4700:4700::1111": 13335, "2a00:1450:4001:80b::200e": 15169, "2a00:1451::1": 0, "::1": 0,
	} {
		got, ok := prefixes.Lookup(netip.MustParseAddr(addr))
		if got != want || ok != (want != 0) {
			t.Errorf("Lookup(%s) = %d, %v; want %d", addr, got, ok, want)
		}
	}
	if names[50673] != (AS{Name: "SERVERIUS-AS", Country: "NL"}) || len(names) != 3 {
		t.Errorf("names: %+v", names)
	}
	var ranges int
	prefixes.Each(func(Range) { ranges++ })
	if ranges != 5 {
		t.Errorf("Each walked %d ranges", ranges)
	}
}

// mrtWriter builds MRT snapshots for the tests, the way a collector writes them.
type mrtWriter struct{ bytes.Buffer }

func (w *mrtWriter) record(subtype uint16, body []byte) {
	var header [12]byte
	binary.BigEndian.PutUint32(header[0:], 1790000000)
	binary.BigEndian.PutUint16(header[4:], mrtTableDumpV2)
	binary.BigEndian.PutUint16(header[6:], subtype)
	binary.BigEndian.PutUint32(header[8:], uint32(len(body)))
	w.Write(header[:])
	w.Write(body)
}

func (w *mrtWriter) peers(asns ...uint32) {
	body := []byte{192, 0, 2, 1, 0, 0} // collector id, an empty view name
	body = binary.BigEndian.AppendUint16(body, uint16(len(asns)))
	for i, asn := range asns {
		body = append(body, 0x02, 10, 0, 0, byte(i), 192, 0, 2, byte(10+i)) // AS4, IPv4 address
		body = binary.BigEndian.AppendUint32(body, asn)
	}
	w.record(subPeerIndexTable, body)
}

// segment is one AS_PATH segment.
type segment struct {
	kind byte
	asns []uint32
}

func asPath(extended bool, segments ...segment) []byte {
	var value []byte
	for _, s := range segments {
		value = append(value, s.kind, byte(len(s.asns)))
		for _, asn := range s.asns {
			value = binary.BigEndian.AppendUint32(value, asn)
		}
	}
	attributes := []byte{0x40, 1, 1, 0} // ORIGIN IGP comes first, as in real tables
	if extended {
		attributes = append(attributes, 0x50, attrASPath)
		attributes = binary.BigEndian.AppendUint16(attributes, uint16(len(value)))
	} else {
		attributes = append(attributes, 0x40, attrASPath, byte(len(value)))
	}
	return append(attributes, value...)
}

// rib writes one prefix with a route per peer.
func (w *mrtWriter) rib(subtype uint16, prefix netip.Prefix, routes ...[]byte) {
	body := binary.BigEndian.AppendUint32(nil, 7)
	body = append(body, byte(prefix.Bits()))
	body = append(body, prefix.Addr().AsSlice()[:(prefix.Bits()+7)/8]...)
	body = binary.BigEndian.AppendUint16(body, uint16(len(routes)))
	for peer, attributes := range routes {
		body = binary.BigEndian.AppendUint16(body, uint16(peer))
		body = binary.BigEndian.AppendUint32(body, 1790000000)
		if subtype == subRIBIPv4UnicastAddPath || subtype == subRIBIPv6UnicastAddPath {
			body = binary.BigEndian.AppendUint32(body, uint32(peer+1))
		}
		body = binary.BigEndian.AppendUint16(body, uint16(len(attributes)))
		body = append(body, attributes...)
	}
	w.record(subtype, body)
}

func seq(asns ...uint32) segment { return segment{kind: segASSequence, asns: asns} }

func TestReadRIB(t *testing.T) {
	var w mrtWriter
	w.record(99, []byte{1, 2, 3}) // an unknown record is skipped
	w.peers(3356, 1299, 6939)
	w.rib(subRIBIPv4Unicast, netip.MustParsePrefix("1.0.0.0/24"),
		asPath(false, seq(3356, 13335)),
		asPath(false, seq(1299, 1299, 1299, 13335)),                                 // prepending
		asPath(true, seq(6939, 174), segment{kind: segASSet, asns: []uint32{1, 2}}), // a set ends the path
	)
	w.rib(subRIBIPv6UnicastAddPath, netip.MustParsePrefix("2606:4700::/32"),
		asPath(false, seq(3356, 2914, 3356, 13335)), // a loop: not usable
		asPath(false, seq(1299, 64512, 13335)),      // a private number ends the path
	)
	snapshot := append([]byte(nil), w.Bytes()...)
	var paths [][]uint32
	stats, err := ReadRIB(bytes.NewReader(snapshot), func(path []uint32) { paths = append(paths, append([]uint32(nil), path...)) })
	if err != nil {
		t.Fatal(err)
	}
	want := [][]uint32{{3356, 13335}, {1299, 13335}, {6939, 174}, {1299}}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("paths: %v", paths)
	}
	if stats != (RIBStats{Records: 2, Routes: 5, Paths: 4, Peers: 3}) {
		t.Errorf("stats: %+v", stats)
	}

	// A damaged file is an error, not a panic.
	var broken mrtWriter
	broken.record(subRIBIPv4Unicast, []byte{0, 0, 0, 1, 24, 1, 0, 0, 0, 9})
	if _, err := ReadRIB(&broken, func([]uint32) {}); err == nil {
		t.Error("a record that ends too early was accepted")
	}
	if _, err := ReadRIB(bytes.NewReader(snapshot[:len(snapshot)-3]), func([]uint32) {}); err == nil {
		t.Error("a file cut short was accepted")
	}
}
