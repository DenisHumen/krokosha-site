package netmap

import (
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Where the sources are. Tests point them elsewhere.
type Places struct {
	CAIDA      string   // directory of CAIDA AS Relationships serial-2
	IPtoASN    string   // the combined table of iptoasn.com
	RouteViews string   // the RouteViews archive
	Collectors []string // RouteViews collectors whose snapshots are read
	PeeringDB  string   // the PeeringDB API
}

// DefaultPlaces are the real ones. route-views2 is the main collector (IPv4), route-views6 the
// IPv6 one: together about 100 MB a day.
var DefaultPlaces = Places{
	CAIDA:      "https://publicdata.caida.org/datasets/as-relationships/serial-2",
	IPtoASN:    "https://iptoasn.com/data/ip2asn-combined.tsv.gz",
	RouteViews: "https://archive.routeviews.org",
	Collectors: []string{"route-views2", "route-views6"},
	PeeringDB:  DefaultPeeringDB,
}

// The files a data directory holds.
const (
	fileCAIDA   = "as-rel2.txt.bz2"
	fileIPtoASN = "ip2asn-combined.tsv.gz"
	dirPDB      = "peeringdb"
)

func ribFile(collector string) string { return "rib." + collector + ".bz2" }

// Fetcher downloads the sources into a data directory.
type Fetcher struct {
	Places    Places
	Client    *http.Client
	UserAgent string
	PDBKey    string           // optional PeeringDB API key
	PDBPause  time.Duration    // between two requests to PeeringDB: PeeringDBPause
	Now       func() time.Time // for tests
	Log       func(format string, args ...any)
}

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now().UTC()
	}
	return time.Now().UTC()
}

func (f *Fetcher) logf(format string, args ...any) {
	if f.Log != nil {
		f.Log(format, args...)
	}
}

// FetchAll downloads everything into dir. CAIDA is refreshed once a month and fetched only when
// the month's file is not there; a failure of one source leaves its previous file in place and is
// reported together with the others.
func (f *Fetcher) FetchAll(ctx context.Context, dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, dirPDB), 0o755); err != nil {
		return err
	}
	var problems []error
	if err := f.FetchCAIDA(ctx, dir); err != nil {
		problems = append(problems, err)
	}
	if err := download(ctx, f.Client, f.Places.IPtoASN, f.UserAgent, "", filepath.Join(dir, fileIPtoASN)); err != nil {
		problems = append(problems, fmt.Errorf("iptoasn: %w", err))
	}
	for _, collector := range f.Places.Collectors {
		if err := f.FetchRIB(ctx, dir, collector); err != nil {
			problems = append(problems, err)
		}
	}
	if err := FetchPeeringDB(ctx, f.Client, f.Places.PeeringDB, f.PDBKey, f.UserAgent, filepath.Join(dir, dirPDB), f.PDBPause); err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

// FetchCAIDA takes the newest monthly file: CAIDA publishes the file of the 1st around the 5th, so
// this month's may not be out yet.
func (f *Fetcher) FetchCAIDA(ctx context.Context, dir string) error {
	stamp := filepath.Join(dir, fileCAIDA+".month")
	have, _ := os.ReadFile(stamp)
	month := time.Date(f.now().Year(), f.now().Month(), 1, 0, 0, 0, 0, time.UTC)
	var last error
	for range 3 {
		name := month.Format("20060102")
		if strings.TrimSpace(string(have)) == name {
			return nil // the newest one there is already here
		}
		url := fmt.Sprintf("%s/%s.as-rel2.txt.bz2", strings.TrimSuffix(f.Places.CAIDA, "/"), name)
		err := download(ctx, f.Client, url, f.UserAgent, "", filepath.Join(dir, fileCAIDA))
		if err == nil {
			f.logf("caida: %s", name)
			return os.WriteFile(stamp, []byte(name+"\n"), 0o644)
		}
		last = err
		if !strings.Contains(err.Error(), "HTTP 404") {
			break
		}
		month = month.AddDate(0, -1, 0)
	}
	if _, err := os.Stat(filepath.Join(dir, fileCAIDA)); err == nil {
		f.logf("caida: keeping %s (%v)", strings.TrimSpace(string(have)), last)
		return nil
	}
	return fmt.Errorf("caida: %w", last)
}

// FetchRIB takes the snapshot of midnight UTC of a collector; before RouteViews has published it,
// yesterday's.
func (f *Fetcher) FetchRIB(ctx context.Context, dir, collector string) error {
	day := f.now().Truncate(24 * time.Hour)
	var last error
	for range 2 {
		url := fmt.Sprintf("%s/%s/bgpdata/%s/RIBS/rib.%s.0000.bz2", strings.TrimSuffix(f.Places.RouteViews, "/"),
			collector, day.Format("2006.01"), day.Format("20060102"))
		err := download(ctx, f.Client, url, f.UserAgent, "", filepath.Join(dir, ribFile(collector)))
		if err == nil {
			f.logf("routeviews %s: %s", collector, day.Format("2006-01-02"))
			return nil
		}
		last = err
		day = day.AddDate(0, 0, -1)
	}
	return fmt.Errorf("routeviews %s: %w", collector, last)
}

// LoadedSources is what LoadSources read, with a line about each part.
type LoadedSources struct {
	Sources
	Report []string
}

// LoadSources reads a data directory filled by FetchAll. Only CAIDA is required; without the
// others the map has no addresses, no daily links, no exchange points.
func LoadSources(dir string, collectors []string) (*LoadedSources, error) {
	out := &LoadedSources{}
	caida, err := openCompressed(filepath.Join(dir, fileCAIDA))
	if err != nil {
		return nil, fmt.Errorf("caida: %w", err)
	}
	out.Links, err = ParseRelationships(caida)
	_ = caida.Close()
	if err != nil {
		return nil, fmt.Errorf("caida: %w", err)
	}
	out.Report = append(out.Report, fmt.Sprintf("caida: %d links", len(out.Links)))

	if table, err := openCompressed(filepath.Join(dir, fileIPtoASN)); err == nil {
		out.Prefixes, out.Names, err = ParseIPtoASN(table)
		_ = table.Close()
		if err != nil {
			return nil, fmt.Errorf("iptoasn: %w", err)
		}
		v4, v6 := out.Prefixes.Len()
		out.Report = append(out.Report, fmt.Sprintf("iptoasn: %d IPv4 and %d IPv6 ranges, %d names", v4, v6, len(out.Names)))
	}

	// Tens of millions of routes repeat a few hundred thousand pairs of neighbours: only the pairs
	// are kept, once each.
	adjacent := make(map[[2]uint32]struct{}, 1<<18)
	for _, collector := range collectors {
		rib, err := openCompressed(filepath.Join(dir, ribFile(collector)))
		if err != nil {
			continue
		}
		before := len(adjacent)
		stats, err := ReadRIB(rib, func(path []uint32) {
			for i := 1; i < len(path); i++ {
				link := NewLink(path[i-1], path[i], RelUnknown, SourceRIB)
				adjacent[[2]uint32{link.A, link.B}] = struct{}{}
			}
		})
		_ = rib.Close()
		if err != nil {
			return nil, fmt.Errorf("routeviews %s: %w", collector, err)
		}
		out.Report = append(out.Report, fmt.Sprintf("routeviews %s: %d peers, %d routes, %d new pairs of neighbours",
			collector, stats.Peers, stats.Routes, len(adjacent)-before))
	}
	for pair := range adjacent {
		out.Links = append(out.Links, Link{A: pair[0], B: pair[1], Rel: RelUnknown, Sources: SourceRIB})
	}

	if db, err := LoadPeeringDB(filepath.Join(dir, dirPDB)); err == nil {
		out.PeeringDB = db
		out.Report = append(out.Report, fmt.Sprintf("peeringdb: %d networks, %d exchanges, %d data centres, %d ports",
			len(db.Networks), len(db.IXs), len(db.Facilities), len(db.Ports)))
	}
	return out, nil
}

// compressed is a decompressing reader that closes the file under it.
type compressed struct {
	io.Reader
	file *os.File
}

func (c *compressed) Close() error { return c.file.Close() }

// openCompressed opens a file, decompressing .bz2 and .gz by the name.
func openCompressed(path string) (io.ReadCloser, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasSuffix(path, ".bz2"):
		return &compressed{Reader: bzip2.NewReader(file), file: file}, nil
	case strings.HasSuffix(path, ".gz"):
		reader, err := gzip.NewReader(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		return &compressed{Reader: reader, file: file}, nil
	}
	return file, nil
}
