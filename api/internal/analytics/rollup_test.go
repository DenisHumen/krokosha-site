package analytics

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// TestADaySummedUpTellsWhatTheRawDataTold: brief B5 — raw events live for a year, the sums of
// days for good. The dashboard of an old period, built from the sums, must show the numbers the
// raw data showed while it was there.
func TestADaySummedUpTellsWhatTheRawDataTold(t *testing.T) {
	f := newFixture(t)
	seedTwoVisitors(f) // the owner's 19 September
	ctx := context.Background()
	reports := f.reports()
	day := reports.ParsePeriod("day", "2026-09-19", "", "")
	fromRaw, err := reports.Overview(ctx, day)
	if err != nil {
		t.Fatal(err)
	}

	rollup := Rollup{DB: f.db, Location: f.service.opts.Location, Now: f.service.opts.Now, Log: quiet, KeepMonths: 12}
	// The day is not over: nothing to sum up.
	if days, removed, err := rollup.RunOnce(ctx); err != nil || days != 0 || removed != 0 {
		t.Fatalf("during the day: %d days, %d removed, %v", days, removed, err)
	}
	// Half an hour after midnight the day's last page views may still report their time on screen.
	f.advance(21*time.Hour + 30*time.Minute)
	if days, _, _ := rollup.RunOnce(ctx); days != 0 {
		t.Fatal("a day was summed up before it settled")
	}
	f.advance(time.Hour)
	if days, removed, err := rollup.RunOnce(ctx); err != nil || days != 1 || removed != 0 {
		t.Fatalf("after the day: %d days, %d removed, %v", days, removed, err)
	}
	if days, _, _ := rollup.RunOnce(ctx); days != 0 {
		t.Fatal("a day was summed up twice")
	}

	// Thirteen months on, the raw data of that day goes, the sums stay.
	f.advance(13 * 31 * 24 * time.Hour)
	reports.KeepRaw(12)
	if days, removed, err := rollup.RunOnce(ctx); err != nil || days != 0 || removed != 3 {
		t.Fatalf("a year later: %d days, %d page views removed, %v", days, removed, err)
	}
	if f.count(`SELECT COUNT(*) FROM analytics_pageviews`)+f.count(`SELECT COUNT(*) FROM analytics_events`) != 0 {
		t.Fatal("raw data older than the storage period is still there")
	}
	fromSums, err := reports.Overview(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if !fromSums.Aggregated || fromSums.Hourly {
		t.Fatalf("the overview of an old day: aggregated=%v hourly=%v", fromSums.Aggregated, fromSums.Hourly)
	}
	if fromSums.Totals != fromRaw.Totals {
		t.Errorf("totals: %+v from the sums, %+v from the raw data", fromSums.Totals, fromRaw.Totals)
	}
	for name, pair := range map[string][2]any{
		"sections": {fromSums.Sections, fromRaw.Sections}, "scroll": {fromSums.Scroll, fromRaw.Scroll},
		"conversions": {fromSums.Conversions, fromRaw.Conversions}, "sources": {fromSums.Sources, fromRaw.Sources},
		"referrers": {fromSums.Referrers, fromRaw.Referrers}, "campaigns": {fromSums.Campaigns, fromRaw.Campaigns},
		"pages": {fromSums.Pages, fromRaw.Pages}, "languages": {fromSums.Languages, fromRaw.Languages},
		"devices": {fromSums.Devices, fromRaw.Devices}, "browsers": {fromSums.Browsers, fromRaw.Browsers},
		"systems": {fromSums.Systems, fromRaw.Systems}, "countries": {fromSums.Countries, fromRaw.Countries},
		"clicks": {fromSums.Clicks, fromRaw.Clicks}, "outbound": {fromSums.Outbound, fromRaw.Outbound}, "eggs": {fromSums.Eggs, fromRaw.Eggs},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s:\n from the sums %+v\n from the raw  %+v", name, pair[0], pair[1])
		}
	}
	if len(fromSums.Timeline) != 1 || fromSums.Timeline[0].Organic != 1 || fromSums.Timeline[0].Ads != 1 {
		t.Errorf("the timeline of the day: %+v", fromSums.Timeline)
	}

	// A period inside the storage time is still read from raw data, hour by hour.
	recent := reports.ParsePeriod("day", "", "", "")
	if overview, err := reports.Overview(ctx, recent); err != nil || overview.Aggregated || !overview.Hourly {
		t.Errorf("today's overview: %+v, %v", overview, err)
	}
	// «For good» means for good: with no limit nothing is removed, and nothing is read from sums.
	reports.KeepRaw(0)
	if overview, _ := reports.Overview(ctx, day); overview.Aggregated {
		t.Error("without a storage limit the overview was read from the sums")
	}
}

// Raw data goes only where the sums exist: a day that could not be summed up keeps its page views.
func TestRawDataWithoutSumsStays(t *testing.T) {
	f := newFixture(t)
	seedTwoVisitors(f)
	ctx := context.Background()
	f.advance(14 * 31 * 24 * time.Hour)
	rollup := Rollup{DB: f.db, Location: f.service.opts.Location, Now: f.service.opts.Now, Log: quiet, KeepMonths: 12}
	days, removed, err := rollup.RunOnce(ctx)
	if err != nil || days != 1 || removed != 3 {
		t.Fatalf("summed up and removed in one run: %d days, %d removed, %v", days, removed, err)
	}
	if got := f.count(`SELECT pageviews FROM analytics_daily WHERE day = '2026-09-19'`); got != 3 {
		t.Fatalf("the sums of the day: %d page views", got)
	}
	// KeepMonths 0: nothing is ever removed.
	seedTwoVisitors(f)
	f.advance(20 * 31 * 24 * time.Hour)
	forever := rollup
	forever.KeepMonths = 0
	if _, removed, err := forever.RunOnce(ctx); err != nil || removed != 0 || f.count(`SELECT COUNT(*) FROM analytics_pageviews`) != 3 {
		t.Fatalf("with no limit: %d removed, %v", removed, err)
	}
}

// count reads one number.
func (f *fixture) count(query string) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(query).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}
