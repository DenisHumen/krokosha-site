package netmap

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the overview the web tests read")

// goldenOverview is where the web tests (web/tests/unit/netmap.test.ts) read the overview of the
// test world with the page's own decoder: the two ends of the format are checked on one file.
var goldenOverview = filepath.Join("..", "..", "..", "web", "tests", "fixtures", "netmap-overview.bin")

func TestOverviewGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := testWorld(t).WriteOverview(&buf, OverviewOptions{Core: 1000, Named: 20, Bundles: 100, Cell: 2}); err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(goldenOverview, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenOverview)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("the overview changed: if on purpose, go test ./internal/netmap -run TestOverviewGolden -update, and run the web tests")
	}
}

// overviewFile is the overview read back the way the page reads it.
type overviewFile struct {
	header                    [16]uint32
	asn                       []uint32
	lat, lon                  []int16
	size, degree, kind, place []uint8
	xLat, xLon                []int16
	members                   []uint16
	capacity                  []uint32
	bundleLinks               []uint32
	coreA, coreB              []uint16
	coreRel                   []int8
	text                      struct {
		Countries []string    `json:"countries"`
		Names     []string    `json:"names"`
		Exchanges [][3]string `json:"exchanges"`
		Types     []string    `json:"types"`
	}
}

func readOverview(t *testing.T, raw []byte) *overviewFile {
	t.Helper()
	f := &overviewFile{}
	r := bytes.NewReader(raw)
	read := func(into any) {
		if err := binary.Read(r, binary.LittleEndian, into); err != nil {
			t.Fatalf("reading the overview: %v", err)
		}
	}
	align := func() {
		for (len(raw)-r.Len())%4 != 0 {
			if _, err := r.ReadByte(); err != nil {
				t.Fatal(err)
			}
		}
	}
	read(&f.header)
	n, x, b, c := f.header[3], f.header[4], f.header[5], f.header[6]
	f.asn = make([]uint32, n)
	f.lat, f.lon = make([]int16, n), make([]int16, n)
	f.size, f.degree, f.kind, f.place = make([]uint8, n), make([]uint8, n), make([]uint8, n), make([]uint8, n)
	for _, into := range []any{f.asn, f.lat, f.lon, f.size, f.degree, f.kind, f.place} {
		read(into)
	}
	align()
	f.xLat, f.xLon, f.members, f.capacity = make([]int16, x), make([]int16, x), make([]uint16, x), make([]uint32, x)
	read(f.xLat)
	read(f.xLon)
	read(f.members)
	align()
	read(f.capacity)
	bundleCoords := make([]int16, 4*b)
	read(bundleCoords)
	align()
	f.bundleLinks = make([]uint32, b)
	read(f.bundleLinks)
	f.coreA, f.coreB, f.coreRel = make([]uint16, c), make([]uint16, c), make([]int8, c)
	read(f.coreA)
	read(f.coreB)
	read(f.coreRel)
	align()
	text := make([]byte, f.header[13])
	read(text)
	if r.Len() != 0 {
		t.Errorf("%d bytes after the text", r.Len())
	}
	if err := json.Unmarshal(text, &f.text); err != nil {
		t.Fatalf("text: %v", err)
	}
	return f
}

func TestOverview(t *testing.T) {
	m := testWorld(t)
	var buf bytes.Buffer
	if err := m.WriteOverview(&buf, OverviewOptions{Core: 1000, Named: 4, Bundles: 100, Cell: 2}); err != nil {
		t.Fatal(err)
	}
	f := readOverview(t, buf.Bytes())
	h := f.header
	if string(buf.Bytes()[:4]) != "KNM1" || h[1] != OverviewVersion || h[2] != uint32(m.Built.Unix()) {
		t.Errorf("header: %v", h)
	}
	// The five networks of the leaked chain have no place: they are not drawn.
	if h[3] != 8 || h[9] != 15 || h[10] != 13 || h[8] != 4 {
		t.Errorf("counts: %d nodes, %d links, %d ASes, %d named", h[3], h[9], h[10], h[8])
	}
	// Largest first: 1000 and 2000 have the same cone and degree, the smaller number wins.
	if f.asn[0] != 1000 || f.asn[1] != 2000 {
		t.Errorf("order: %v", f.asn)
	}
	for i, asn := range f.asn {
		if asn == 6000 && (f.lat[i] != 5045 || f.lon[i] != 3052 || f.size[i] != 1 || f.text.Countries[f.place[i]] != "UA") {
			t.Errorf("6000: %d, %d, size %d, country %q", f.lat[i], f.lon[i], f.size[i], f.text.Countries[f.place[i]])
		}
		if asn == 7000 && f.text.Types[f.kind[i]] != "Content" {
			t.Errorf("type of 7000: %q", f.text.Types[f.kind[i]])
		}
	}
	if len(f.text.Names) != 4 || f.text.Names[0] != "" {
		t.Errorf("names: %q", f.text.Names)
	}
	// One exchange point with a place and members; its capacity in Gbit/s.
	if h[4] != 1 || f.members[0] != 2 || f.capacity[0] != 300 || f.xLat[0] != 5011 || f.text.Exchanges[0] != [3]string{"TEST-IX", "Frankfurt", "DE"} {
		t.Errorf("exchanges: %d, %v, %v, %v", h[4], f.members, f.capacity, f.text.Exchanges)
	}
	// Every link between two placed networks is in the core; bundles join different cells only.
	if h[6] != 10 || h[7] != 8 {
		t.Errorf("core: %d links among %d", h[6], h[7])
	}
	for i := range f.coreA {
		if f.coreA[i] >= f.coreB[i] || f.coreB[i] >= uint16(h[7]) {
			t.Errorf("core link %d: %d–%d", i, f.coreA[i], f.coreB[i])
		}
	}
	if h[5] == 0 || f.bundleLinks[0] < f.bundleLinks[len(f.bundleLinks)-1] {
		t.Errorf("bundles: %d, %v", h[5], f.bundleLinks)
	}
}
