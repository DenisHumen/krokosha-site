package geo

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/geo/geotest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestLocate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	geotest.Write(t, path, map[string]map[string]any{
		"203.0.113.0/24":  geotest.Place("UA", "Kyiv"),
		"198.51.100.0/25": geotest.Place("DE", ""),
		"192.0.2.0/24":    geotest.Place("PL", strings.Repeat("Ł", 50)), // wider than the column: cut, not torn
	})
	locator := Open(path, quiet)
	defer locator.Close()
	if !locator.Ready() {
		t.Fatal("the database was not opened")
	}
	if info := locator.Info(); !info.Loaded || info.Type != "Test-City" || info.Built.Year() != 2026 || info.Path != path {
		t.Errorf("info: %+v", info)
	}
	for address, want := range map[string]Place{
		"203.0.113.7":        {Country: "UA", City: "Kyiv"},
		"203.0.113.255":      {Country: "UA", City: "Kyiv"},
		"::ffff:203.0.113.7": {Country: "UA", City: "Kyiv"}, // how a dual-stack listener sees an IPv4 visitor
		"198.51.100.1":       {Country: "DE"},
		"198.51.100.200":     {}, // the other half of that network is nobody's
		"192.0.2.1":          {Country: "PL", City: strings.Repeat("Ł", 40)},
		"8.8.8.8":            {},
		"2001:db8::1":        {}, // this database knows IPv4 only
		"10.0.0.1":           {},
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
	if info := locator.Info(); info.Loaded || info.Path != path {
		t.Errorf("info without a file: %+v", info)
	}

	geotest.Write(t, path, map[string]map[string]any{"203.0.113.0/24": geotest.Place("UA", "Kyiv")})
	locator.checked = time.Time{} // ten minutes later
	if got := locator.Locate(net.ParseIP("203.0.113.7")); got.Country != "UA" {
		t.Fatalf("after the file appeared: %+v", got)
	}

	// A week later geoipupdate brings a new file.
	geotest.Write(t, path, map[string]map[string]any{"203.0.113.0/24": geotest.Place("PL", "Warsaw")})
	if err := os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	locator.checked = time.Time{}
	if got := locator.Locate(net.ParseIP("203.0.113.7")); got != (Place{Country: "PL", City: "Warsaw"}) {
		t.Fatalf("after the file was replaced: %+v", got)
	}

	// A file that is no database does not take the old one away.
	geotest.Replace(t, path, []byte("not a database"))
	_ = os.Chtimes(path, time.Now().Add(2*time.Hour), time.Now().Add(2*time.Hour))
	locator.checked = time.Time{}
	if got := locator.Locate(net.ParseIP("203.0.113.7")); got.Country != "PL" {
		t.Fatalf("after a broken file: %+v", got)
	}

	var none *Locator
	if none.Locate(net.ParseIP("203.0.113.7")) != (Place{}) || none.Ready() || none.Info().Loaded || Open("", quiet).Ready() {
		t.Error("a locator without a database answered")
	}
	none.Close()
}
