// Package geotest writes a MaxMind DB small enough for a test: a few networks, the records the
// service reads. The format (https://maxmind.github.io/MaxMind-DB/): a binary search tree over the
// bits of an address, sixteen zero bytes, the data the leaves point to, a marker, the metadata.
package geotest

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"os"
	"sort"
	"testing"
)

// Place is a record the way GeoLite2-City has it: the country's code and the city's names.
func Place(country, city string) map[string]any {
	record := map[string]any{"country": map[string]any{"iso_code": country, "names": map[string]any{"en": country}}}
	if city != "" {
		record["city"] = map[string]any{"names": map[string]any{"en": city, "uk": "Київ"}}
	}
	return record
}

// Write puts a database of IPv4 networks («203.0.113.0/24» → record) at path — the way
// geoipupdate does it: written beside, then renamed over the old file, which stays readable for
// whoever has it open.
func Write(t testing.TB, path string, networks map[string]map[string]any) {
	t.Helper()
	Replace(t, path, Database(networks))
}

// Replace writes a file beside path and renames it over.
func Replace(t testing.TB, path string, content []byte) {
	t.Helper()
	temporary := path + ".new"
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
}

type node struct {
	children [2]*node
	leaf     bool
	offset   int // of the leaf's data in the data section
	index    int // of an inner node in the tree
}

// Database is the bytes of a database of IPv4 networks.
func Database(networks map[string]map[string]any) []byte {
	root := &node{}
	var data []byte
	prefixes := make([]string, 0, len(networks))
	for prefix := range networks {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	for _, text := range prefixes {
		prefix := netip.MustParsePrefix(text)
		address := binary.BigEndian.Uint32(prefix.Addr().AsSlice())
		current := root
		for bit := range prefix.Bits() {
			side := address >> (31 - bit) & 1
			if bit == prefix.Bits()-1 {
				current.children[side] = &node{leaf: true, offset: len(data)}
				break
			}
			if current.children[side] == nil {
				current.children[side] = &node{}
			}
			current = current.children[side]
		}
		data = append(data, encode(networks[text])...)
	}

	var inner []*node
	for queue := []*node{root}; len(queue) > 0; queue = queue[1:] {
		current := queue[0]
		current.index = len(inner)
		inner = append(inner, current)
		for _, child := range current.children {
			if child != nil && !child.leaf {
				queue = append(queue, child)
			}
		}
	}
	var out bytes.Buffer
	for _, current := range inner {
		for _, child := range current.children {
			value := len(inner) // «nothing here»
			switch {
			case child != nil && child.leaf:
				value = len(inner) + 16 + child.offset
			case child != nil:
				value = child.index
			}
			out.Write([]byte{byte(value >> 16), byte(value >> 8), byte(value)}) //nolint:gosec // 24-bit records of a database of a few nodes
		}
	}
	out.Write(make([]byte, 16))
	out.Write(data)
	out.WriteString("\xab\xcd\xefMaxMind.com")
	out.Write(encode(map[string]any{
		"binary_format_major_version": uint16(2), "binary_format_minor_version": uint16(0),
		"build_epoch": uint64(1789000000), "database_type": "Test-City", "description": map[string]any{"en": "written by a test"},
		"ip_version": uint16(4), "languages": []any{"en"}, "node_count": uint32(len(inner)), "record_size": uint16(24), //nolint:gosec // a handful of nodes
	}))
	return out.Bytes()
}

func control(kind, size int) []byte {
	switch {
	case kind > 7: // extended type: the number goes into a byte of its own
		return append(control(0, size)[:1], append([]byte{byte(kind - 7)}, control(0, size)[1:]...)...) //nolint:gosec // kinds are small constants
	case size < 29:
		return []byte{byte(kind<<5 | size)} //nolint:gosec // kind < 8, size < 29
	default:
		return []byte{byte(kind<<5 | 29), byte(size - 29)} //nolint:gosec // sizes of a test record
	}
}

func number(value uint64) []byte {
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, value)
	return bytes.TrimLeft(raw, "\x00")
}

func encode(value any) []byte {
	var out []byte
	switch v := value.(type) {
	case string:
		out = append(control(2, len(v)), v...)
	case uint16:
		out = append(control(5, len(number(uint64(v)))), number(uint64(v))...)
	case uint32:
		out = append(control(6, len(number(uint64(v)))), number(uint64(v))...)
	case uint64:
		out = append(control(9, len(number(v))), number(v)...)
	case []any:
		out = control(11, len(v))
		for _, item := range v {
			out = append(out, encode(item)...)
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out = control(7, len(v))
		for _, key := range keys {
			out = append(append(out, encode(key)...), encode(v[key])...)
		}
	default:
		panic("the test writer does not know this type")
	}
	return out
}
