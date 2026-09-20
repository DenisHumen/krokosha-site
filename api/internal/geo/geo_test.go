package geo

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// --- a MaxMind DB small enough to be written by a test ---
// The format: a binary search tree over the bits of an address, sixteen zero bytes, the data the
// leaves point to, a marker, a map of metadata. https://maxmind.github.io/MaxMind-DB/

func control(kind, size int) []byte {
	switch {
	case kind > 7: // extended type: the number goes into a byte of its own
		return append(control(0, size)[:1], append([]byte{byte(kind - 7)}, control(0, size)[1:]...)...)
	case size < 29:
		return []byte{byte(kind<<5 | size)}
	default:
		return []byte{byte(kind<<5 | 29), byte(size - 29)}
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

type trie struct {
	children [2]*trie
	leaf     bool
	offset   int // of the leaf's data in the data section
	index    int // of an inner node in the tree
}

func writeDatabase(t *testing.T, path string, networks map[string]map[string]any) {
	t.Helper()
	root := &trie{}
	var data []byte
	prefixes := make([]string, 0, len(networks))
	for prefix := range networks {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	for _, text := range prefixes {
		prefix := netip.MustParsePrefix(text)
		address := binary.BigEndian.Uint32(prefix.Addr().AsSlice())
		node := root
		for bit := range prefix.Bits() {
			side := address >> (31 - bit) & 1
			if bit == prefix.Bits()-1 {
				node.children[side] = &trie{leaf: true, offset: len(data)}
				break
			}
			if node.children[side] == nil {
				node.children[side] = &trie{}
			}
			node = node.children[side]
		}
		data = append(data, encode(networks[text])...)
	}

	var inner []*trie
	for queue := []*trie{root}; len(queue) > 0; queue = queue[1:] {
		node := queue[0]
		node.index = len(inner)
		inner = append(inner, node)
		for _, child := range node.children {
			if child != nil && !child.leaf {
				queue = append(queue, child)
			}
		}
	}
	var out bytes.Buffer
	for _, node := range inner {
		for _, child := range node.children {
			value := len(inner) // «nothing here»
			switch {
			case child != nil && child.leaf:
				value = len(inner) + 16 + child.offset
			case child != nil:
				value = child.index
			}
			out.Write([]byte{byte(value >> 16), byte(value >> 8), byte(value)})
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
	temporary := path + ".new"
	if err := os.WriteFile(temporary, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil { // the way geoipupdate replaces the file
		t.Fatal(err)
	}
}

func place(country, city string) map[string]any {
	record := map[string]any{"country": map[string]any{"iso_code": country, "names": map[string]any{"en": country}}}
	if city != "" {
		record["city"] = map[string]any{"names": map[string]any{"en": city, "uk": "Київ"}}
	}
	return record
}

func TestLocate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	writeDatabase(t, path, map[string]map[string]any{
		"203.0.113.0/24":  place("UA", "Kyiv"),
		"198.51.100.0/25": place("DE", ""),
	})
	locator := Open(path, quiet)
	defer locator.Close()
	if !locator.Ready() {
		t.Fatal("the database was not opened")
	}
	for address, want := range map[string]Place{
		"203.0.113.7":         {Country: "UA", City: "Kyiv"},
		"203.0.113.255":       {Country: "UA", City: "Kyiv"},
		"::ffff:203.0.113.7":  {Country: "UA", City: "Kyiv"}, // how a dual-stack listener sees an IPv4 visitor
		"198.51.100.1":        {Country: "DE"},
		"198.51.100.200":      {},                            // the other half of that network is nobody's
		"8.8.8.8":             {},
		"2001:db8::1":         {}, // this database knows IPv4 only
		"10.0.0.1":            {},
	} {
		if got := locator.Locate(net.ParseIP(address)); got != want {
			t.Errorf("%s: %+v, want %+v", address, got, want)
		}
	}
	if got := locator.Locate(nil); got != (Place{}) {
		t.Errorf("no address: %+v", got)
	}
}

func TestANewDatabaseIsNoticed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	locator := Open(path, quiet) // the file is not there yet: geoipupdate has not run
	defer locator.Close()
	if locator.Ready() || locator.Locate(net.ParseIP("203.0.113.7")) != (Place{}) {
		t.Fatal("a database that does not exist answered")
	}

	writeDatabase(t, path, map[string]map[string]any{"203.0.113.0/24": place("UA", "Kyiv")})
	locator.checked = time.Time{} // ten minutes later
	if got := locator.Locate(net.ParseIP("203.0.113.7")); got.Country != "UA" {
		t.Fatalf("after the file appeared: %+v", got)
	}

	// A week later geoipupdate brings a new file.
	writeDatabase(t, path, map[string]map[string]any{"203.0.113.0/24": place("PL", "Warsaw")})
	if err := os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	locator.checked = time.Time{}
	if got := locator.Locate(net.ParseIP("203.0.113.7")); got != (Place{Country: "PL", City: "Warsaw"}) {
		t.Fatalf("after the file was replaced: %+v", got)
	}

	// A file that is no database does not take the old one away.
	if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, time.Now().Add(2*time.Hour), time.Now().Add(2*time.Hour))
	locator.checked = time.Time{}
	if got := locator.Locate(net.ParseIP("203.0.113.7")); got.Country != "PL" {
		t.Fatalf("after a broken file: %+v", got)
	}

	var none *Locator
	if none.Locate(net.ParseIP("203.0.113.7")) != (Place{}) || Open("", quiet).Ready() {
		t.Error("a locator without a database answered")
	}
}
