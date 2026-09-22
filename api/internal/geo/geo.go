// Package geo tells the country and the city of a visitor from a local database (brief B5): a
// GeoLite2 file that geoipupdate keeps fresh, or any other database in the MaxMind DB format.
// Nothing is asked over the network, and the address itself is never stored — the answer is.
//
// Without a database everything works, and the «countries» of the dashboard stay empty.
package geo

import (
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Place is where an address is, as far as the database knows.
type Place struct {
	Country string // ISO 3166-1 alpha-2, «UA»
	City    string // in English, as the reports show it
}

// Info is what the status page tells about the database.
type Info struct {
	Path   string
	Loaded bool
	Type   string    // «GeoLite2-City»
	Built  time.Time // when MaxMind built it
}

// Locator answers «where is this address?».
type Locator struct {
	path string
	log  *slog.Logger

	mu       sync.RWMutex
	db       *maxminddb.Reader
	modified time.Time
	checked  time.Time
}

// recheckEvery is how often the file is looked at: geoipupdate replaces it about once a week.
const recheckEvery = 10 * time.Minute

// maxCityBytes is the width of the city column.
const maxCityBytes = 80

// Open reads the database. An empty path, or a file that is not there, gives a locator that
// knows nothing — and that starts to know once the file appears.
func Open(path string, log *slog.Logger) *Locator {
	locator := &Locator{path: path, log: log}
	if path != "" {
		locator.reload(time.Now())
	}
	return locator
}

// Ready reports whether there is a database to ask.
func (l *Locator) Ready() bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.db != nil
}

// Info describes the database in use.
func (l *Locator) Info() Info {
	if l == nil {
		return Info{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	info := Info{Path: l.path, Loaded: l.db != nil}
	if l.db != nil {
		info.Type = l.db.Metadata.DatabaseType
		info.Built = time.Unix(int64(l.db.Metadata.BuildEpoch), 0).UTC() //nolint:gosec // a date of this century
	}
	return info
}

// reload opens the file again when it has changed since it was last opened.
func (l *Locator) reload(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.checked = now
	info, err := os.Stat(l.path)
	if err != nil || info.ModTime().Equal(l.modified) {
		return
	}
	db, err := maxminddb.Open(l.path)
	if err != nil {
		l.log.Warn("geo: the database cannot be read; countries stay unknown", "path", l.path, "error", err)
		return
	}
	if l.db != nil {
		_ = l.db.Close()
	}
	l.db, l.modified = db, info.ModTime()
	l.log.Info("geo: the database is loaded", "path", l.path, "type", db.Metadata.DatabaseType, "built", time.Unix(int64(db.Metadata.BuildEpoch), 0).UTC().Format(time.DateOnly)) //nolint:gosec // a date of this century
}

// Locate looks an address up. Private and unknown addresses give an empty Place.
func (l *Locator) Locate(ip net.IP) Place {
	if l == nil || l.path == "" {
		return Place{}
	}
	l.mu.RLock()
	stale := time.Since(l.checked) > recheckEvery
	l.mu.RUnlock()
	if stale {
		l.reload(time.Now())
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return Place{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.db == nil {
		return Place{}
	}
	var record struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
		City struct {
			Names map[string]string `maxminddb:"names"`
		} `maxminddb:"city"`
	}
	if err := l.db.Lookup(addr.Unmap()).Decode(&record); err != nil {
		return Place{}
	}
	place := Place{Country: record.Country.ISOCode, City: cut(record.City.Names["en"], maxCityBytes)}
	if len(place.Country) != 2 {
		place.Country = ""
	}
	return place
}

// cut shortens text to at most limit bytes without breaking a character.
func cut(text string, limit int) string {
	for len(text) > limit {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	return text
}

// Close releases the file.
func (l *Locator) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db != nil {
		_ = l.db.Close()
		l.db = nil
	}
}
