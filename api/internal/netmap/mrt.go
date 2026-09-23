package netmap

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MRT (RFC 6396) is the format RouteViews and RIPE RIS keep routing tables in. A snapshot is a
// TABLE_DUMP_V2 file: a table of the collector's peers, then one record per prefix with the route
// every peer has for it. Only the AS paths matter here: two ASes next to each other in a path are
// linked.
const (
	mrtTableDumpV2 = 13

	subPeerIndexTable = 1
	subRIBIPv4Unicast = 2
	subRIBIPv6Unicast = 4
	// RFC 8050: the same with a path identifier in front of the attributes of every entry.
	subRIBIPv4UnicastAddPath = 8
	subRIBIPv6UnicastAddPath = 10

	attrASPath    = 2
	segASSet      = 1
	segASSequence = 2

	// maxRecord guards against a damaged file announcing a record of gigabytes.
	maxRecord = 16 << 20
)

// RIBStats counts what a snapshot contained.
type RIBStats struct {
	Records int // prefixes
	Routes  int // routes of all peers for all prefixes
	Paths   int // routes with a usable AS path
	Peers   int // peers of the collector
}

// ReadRIB reads an uncompressed MRT snapshot and calls fn with the AS path of every route in it:
// AS_SEQUENCE segments only, prepending collapsed. A path with a loop — the trick of «poisoning» a
// route — is skipped, and an AS_SET cuts a path in two. fn must not keep the slice.
func ReadRIB(r io.Reader, fn func(path []uint32)) (RIBStats, error) {
	var stats RIBStats
	in := bufio.NewReaderSize(r, 1<<20)
	var header [12]byte
	body := make([]byte, 0, 64<<10)
	path := make([]uint32, 0, 64)
	for {
		if _, err := io.ReadFull(in, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return stats, nil
			}
			return stats, fmt.Errorf("mrt header: %w", err)
		}
		kind := binary.BigEndian.Uint16(header[4:6])
		subtype := binary.BigEndian.Uint16(header[6:8])
		length := binary.BigEndian.Uint32(header[8:12])
		if length > maxRecord {
			return stats, fmt.Errorf("mrt record of %d bytes: the file is damaged", length)
		}
		if cap(body) < int(length) {
			body = make([]byte, length)
		}
		body = body[:length]
		if _, err := io.ReadFull(in, body); err != nil {
			return stats, fmt.Errorf("mrt record: %w", err)
		}
		if kind != mrtTableDumpV2 {
			continue
		}
		switch subtype {
		case subPeerIndexTable:
			peers, err := peerCount(body)
			if err != nil {
				return stats, err
			}
			stats.Peers = peers
		case subRIBIPv4Unicast, subRIBIPv6Unicast, subRIBIPv4UnicastAddPath, subRIBIPv6UnicastAddPath:
			addPath := subtype == subRIBIPv4UnicastAddPath || subtype == subRIBIPv6UnicastAddPath
			if err := ribRecord(body, addPath, &stats, path, fn); err != nil {
				return stats, err
			}
		}
	}
}

var errShort = errors.New("mrt: a record ends too early")

// peerCount reads the peer index table: collector id (4), view name (2 + n), peer count (2).
func peerCount(body []byte) (int, error) {
	if len(body) < 6 {
		return 0, errShort
	}
	nameLength := int(binary.BigEndian.Uint16(body[4:6]))
	if len(body) < 6+nameLength+2 {
		return 0, errShort
	}
	return int(binary.BigEndian.Uint16(body[6+nameLength:])), nil
}

// ribRecord reads one prefix: sequence (4), prefix length (1), prefix, entry count (2), entries.
func ribRecord(body []byte, addPath bool, stats *RIBStats, path []uint32, fn func([]uint32)) error {
	if len(body) < 5 {
		return errShort
	}
	bits := int(body[4])
	at := 5 + (bits+7)/8
	if len(body) < at+2 {
		return errShort
	}
	entries := int(binary.BigEndian.Uint16(body[at:]))
	at += 2
	stats.Records++
	for range entries {
		// Peer index (2), originated time (4), [path identifier (4)], attribute length (2).
		skip := 6
		if addPath {
			skip += 4
		}
		if len(body) < at+skip+2 {
			return errShort
		}
		at += skip
		attributesLength := int(binary.BigEndian.Uint16(body[at:]))
		at += 2
		if len(body) < at+attributesLength {
			return errShort
		}
		stats.Routes++
		if asPath, ok := findASPath(body[at : at+attributesLength]); ok {
			if collected, usable := collectPath(asPath, path[:0]); usable {
				stats.Paths++
				fn(collected)
			}
		}
		at += attributesLength
	}
	return nil
}

// findASPath finds the AS_PATH among BGP path attributes: flags (1), type (1), length (1, or 2 when
// the «extended length» flag is set), value.
func findASPath(attributes []byte) ([]byte, bool) {
	for at := 0; at+3 <= len(attributes); {
		flags, kind := attributes[at], attributes[at+1]
		var length, header int
		if flags&0x10 != 0 {
			if at+4 > len(attributes) {
				return nil, false
			}
			length, header = int(binary.BigEndian.Uint16(attributes[at+2:])), 4
		} else {
			length, header = int(attributes[at+2]), 3
		}
		if at+header+length > len(attributes) {
			return nil, false
		}
		if kind == attrASPath {
			return attributes[at+header : at+header+length], true
		}
		at += header + length
	}
	return nil, false
}

// collectPath turns an AS_PATH (4-byte AS numbers, as TABLE_DUMP_V2 always has them) into a list
// of ASes, prepending collapsed. An AS_SET, a confederation segment or a private AS number ends the
// usable part: joining the ASes on both sides of it would invent a link. A path that visits an AS
// twice is not usable.
func collectPath(value []byte, path []uint32) ([]uint32, bool) {
	for at := 0; at+2 <= len(value); {
		kind, count := value[at], int(value[at+1])
		at += 2
		if at+4*count > len(value) {
			return path, false
		}
		if kind != segASSequence {
			break
		}
		for i := range count {
			asn := binary.BigEndian.Uint32(value[at+4*i:])
			if !PublicASN(asn) {
				return path, len(path) > 0
			}
			if len(path) > 0 && path[len(path)-1] == asn {
				continue
			}
			for _, seen := range path {
				if seen == asn {
					return path, false
				}
			}
			path = append(path, asn)
		}
		at += 4 * count
	}
	return path, len(path) > 0
}
