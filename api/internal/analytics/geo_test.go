package analytics

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/DenisHumen/krokosha-site/api/internal/geo"
	"github.com/DenisHumen/krokosha-site/api/internal/geo/geotest"
)

// Brief B5: the country and the city come from a local database; the address itself does not
// stay. Without the database the columns stay empty, and so does the «geography» card.
func TestTheCountryAndTheCityAreStoredWhenThereIsADatabase(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "GeoLite2-City.mmdb")
	geotest.Write(t, path, map[string]map[string]any{"203.0.113.0/24": geotest.Place("UA", "Kyiv")})
	f.service.opts.Geo = geo.Open(path, quiet)
	defer f.service.opts.Geo.Close()

	if code := f.post(request{body: batch("00112233aabbccdd", `{"t":"pageview","o":0}`)}); code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", code)
	}
	if code := f.post(request{body: batch("00112233aabbccee", `{"t":"pageview","o":0}`), ip: "198.51.100.9"}); code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", code)
	}
	if code := f.post(request{body: batch("00112233aabbccff", `{"t":"pageview","o":0}`), ip: "203.0.113.200"}); code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", code)
	}
	f.wait(`SELECT COUNT(*) FROM analytics_pageviews`, 3)

	var country, city sql.NullString
	if err := f.db.QueryRow(`SELECT country, city FROM analytics_pageviews WHERE pageview_id = UNHEX('00112233aabbccdd')`).Scan(&country, &city); err != nil {
		t.Fatal(err)
	}
	if country.String != "UA" || city.String != "Kyiv" {
		t.Errorf("a visitor from Kyiv: country %q, city %q", country.String, city.String)
	}
	if err := f.db.QueryRow(`SELECT country, city FROM analytics_pageviews WHERE pageview_id = UNHEX('00112233aabbccee')`).Scan(&country, &city); err != nil {
		t.Fatal(err)
	}
	if country.Valid || city.Valid {
		t.Errorf("a visitor the database does not know: country %v, city %v", country, city)
	}

	overview, err := f.reports().Overview(context.Background(), f.reports().ParsePeriod("day", "", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Countries) != 2 || overview.Countries[0].Name != "UA" || overview.Countries[0].Count != 2 || overview.Countries[1].Name != "" {
		t.Errorf("countries of the day: %+v", overview.Countries)
	}
}
