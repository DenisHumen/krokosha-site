package analytics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedTwoVisitors plays a small day through the real pipeline. The fixture starts at 21:30 UTC on
// 18 September — half past midnight of the 19th in Kyiv, the owner's time zone:
//
//	00:30  visitor A, paid search → /uk/: two sections, a click on Telegram, scrolled to 75 %
//	02:30  visitor B (phone), direct → /: one section, left for github.com
//	02:35  visitor B again → /privacy/ (same visit)
func seedTwoVisitors(f *fixture) {
	f.post(request{body: batch("a000000000000001", strings.Join([]string{
		`{"t":"pageview","o":0}`, `{"t":"section","x":"hero","o":100}`, `{"t":"section","x":"skills","o":5000}`,
		`{"t":"click","x":"cta-telegram","o":9000}`, `{"t":"outbound","x":"t.me","o":9000}`,
		`{"t":"section_time","x":"hero","v":4000,"o":15000}`, `{"t":"section_time","x":"skills","v":6000,"o":15000}`,
		`{"t":"scroll","v":75,"o":15000}`, `{"t":"time","v":15000,"o":15000}`,
	}, ","))})

	f.advance(2 * time.Hour)
	direct := func(id, path, events string) string {
		return fmt.Sprintf(`{"v":1,"id":"%s","p":"%s","l":"en","e":[%s]}`, id, path, events)
	}
	f.post(request{ip: "198.51.100.20", ua: uaSafariIPhone, body: direct("b000000000000001", "/", strings.Join([]string{
		`{"t":"pageview","o":0}`, `{"t":"section","x":"hero","o":100}`, `{"t":"outbound","x":"github.com","o":2500}`,
		`{"t":"section_time","x":"hero","v":2000,"o":3000}`, `{"t":"scroll","v":25,"o":3000}`, `{"t":"time","v":3000,"o":3000}`,
	}, ","))})
	f.advance(5 * time.Minute)
	f.post(request{ip: "198.51.100.20", ua: uaSafariIPhone, body: direct("b000000000000002", "/privacy/", `{"t":"pageview","o":0},{"t":"egg","x":"konami","o":500}`)})

	f.wait(`SELECT COUNT(*) FROM analytics_pageviews`, 3)
	f.wait(`SELECT COUNT(*) FROM analytics_events`, 10)
}

func (f *fixture) reports() *Reports {
	return NewReports(f.db, f.service.opts.Location, f.service.opts.Now)
}

func TestOverviewOfADay(t *testing.T) {
	f := newFixture(t)
	seedTwoVisitors(f)
	reports := f.reports()

	period := reports.ParsePeriod("day", "", "", "")
	if got := period.From.Format(time.DateOnly); got != "2026-09-19" {
		t.Fatalf("today is %s, want the owner's day 2026-09-19 (it is still the 18th in UTC)", got)
	}
	overview, err := reports.Overview(context.Background(), period)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := overview.Totals, (Totals{Visitors: 2, Visits: 2, Pageviews: 3, AdVisits: 1, AvgViewMs: 9000, Actions: 1}); got != want {
		t.Errorf("totals = %+v, want %+v", got, want)
	}
	if !overview.Hourly || len(overview.Timeline) != 24 {
		t.Fatalf("timeline: hourly=%v, %d buckets", overview.Hourly, len(overview.Timeline))
	}
	for hour, bucket := range overview.Timeline {
		want := Bucket{Start: bucket.Start}
		switch hour {
		case 0:
			want.Ads = 1
		case 2:
			want.Organic = 1
		}
		if bucket != want || bucket.Start.Hour() != hour {
			t.Errorf("hour %d: %+v", hour, bucket)
		}
	}

	if len(overview.Sections) != 2 {
		t.Fatalf("sections: %+v", overview.Sections)
	}
	// Equal time in both sections: the one more visitors reached comes first.
	hero, skills := overview.Sections[0], overview.Sections[1]
	if hero.Name != "hero" || hero.Views != 2 || hero.TimeMs != 6000 || hero.TimePercent != 50 || int(hero.Reach+0.5) != 67 {
		t.Errorf("hero: %+v", hero)
	}
	if skills.Name != "skills" || skills.Views != 1 || skills.TimeMs != 6000 {
		t.Errorf("skills: %+v", skills)
	}

	wantScroll := []int{2, 1, 1, 0} // 25 %, 50 %, 75 %, 100 % — cumulative
	for i, share := range overview.Scroll {
		if share.Count != wantScroll[i] {
			t.Errorf("scrolled to %s: %d page views, want %d", share.Name, share.Count, wantScroll[i])
		}
	}

	conversions := map[string]Conversion{}
	for _, conversion := range overview.Conversions {
		conversions[conversion.Name] = conversion
	}
	if got := conversions["telegram"]; got.Visitors != 1 || got.Percent != 50 {
		t.Errorf("telegram conversion: %+v", got)
	}
	if conversions["email"].Visitors != 0 || conversions["github"].Visitors != 0 {
		t.Errorf("conversions nobody made: %+v", conversions)
	}

	assertShares(t, "sources", overview.Sources, "ads:1", "direct:1")
	assertShares(t, "referrers", overview.Referrers, "google.com:1") // stored without «www.»
	assertShares(t, "campaigns", overview.Campaigns, "google / mikrotik:1")
	assertShares(t, "pages", overview.Pages, "/:1", "/privacy/:1", "/uk/:1")
	assertShares(t, "languages", overview.Languages, "en:2", "uk:1")
	assertShares(t, "devices", overview.Devices, "mobile:2", "desktop:1")
	assertShares(t, "clicks", overview.Clicks, "cta-telegram:1")
	assertShares(t, "outbound", overview.Outbound, "github.com:1", "t.me:1")
	assertShares(t, "eggs", overview.Eggs, "konami:1")
	if got := overview.Languages[0].Percent; int(got+0.5) != 67 {
		t.Errorf("share of English pages = %.1f%%, want 67%%", got)
	}
}

func assertShares(t *testing.T, what string, got []Share, want ...string) {
	t.Helper()
	var lines []string
	for _, share := range got {
		lines = append(lines, fmt.Sprintf("%s:%d", share.Name, share.Count))
	}
	if strings.Join(lines, " ") != strings.Join(want, " ") {
		t.Errorf("%s = %v, want %v", what, lines, want)
	}
}

func TestOverviewOfLongerPeriods(t *testing.T) {
	f := newFixture(t)
	seedTwoVisitors(f)
	reports := f.reports()
	ctx := context.Background()

	week := reports.ParsePeriod("week", "", "", "")
	if week.From.Format(time.DateOnly) != "2026-09-14" || week.To.Format(time.DateOnly) != "2026-09-20" || week.Days() != 7 {
		t.Fatalf("week = %s…%s", week.From.Format(time.DateOnly), week.To.Format(time.DateOnly))
	}
	overview, err := reports.Overview(ctx, week)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Hourly || len(overview.Timeline) != 7 {
		t.Fatalf("a week has daily buckets: hourly=%v, %d buckets", overview.Hourly, len(overview.Timeline))
	}
	if saturday := overview.Timeline[5]; saturday.Organic != 1 || saturday.Ads != 1 {
		t.Errorf("Saturday the 19th: %+v", saturday)
	}
	totals, err := reports.Totals(ctx, week)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Totals{Visitors: overview.Totals.Visitors, Visits: overview.Totals.Visits, Pageviews: overview.Totals.Pageviews, AvgViewMs: overview.Totals.AvgViewMs}); totals != want || totals.Visits == 0 {
		t.Errorf("the totals alone = %+v, the dashboard's = %+v", totals, overview.Totals)
	}

	month := reports.ParsePeriod("month", "2026-02-10", "", "")
	if month.From.Format(time.DateOnly) != "2026-02-01" || month.To.Format(time.DateOnly) != "2026-02-28" {
		t.Errorf("February = %s…%s", month.From.Format(time.DateOnly), month.To.Format(time.DateOnly))
	}
	if next := month.Shift(1); next.From.Format(time.DateOnly) != "2026-03-01" || next.To.Format(time.DateOnly) != "2026-03-31" {
		t.Errorf("the month after February = %s…%s", next.From.Format(time.DateOnly), next.To.Format(time.DateOnly))
	}
	empty, err := reports.Overview(ctx, month)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Totals != (Totals{}) || len(empty.Timeline) != 28 || len(empty.Sections) != 0 {
		t.Errorf("an empty month: %+v", empty.Totals)
	}

	// A clock change must not add or lose a day: 25 October 2026 is 25 hours long in Kyiv.
	autumn := reports.ParsePeriod("custom", "", "2026-10-19", "2026-11-01")
	if autumn.Kind != "day" { // the range is in the future — refused, today is shown instead
		t.Errorf("a future range was accepted: %+v", autumn)
	}
	past := reports.ParsePeriod("custom", "", "2025-10-20", "2025-11-02")
	if past.Kind != "custom" || past.Days() != 14 {
		t.Errorf("two weeks across the clock change = %d days", past.Days())
	}

	for name, period := range map[string]Period{
		"nonsense":        reports.ParsePeriod("decade", "yesterday", "", ""),
		"reversed range":  reports.ParsePeriod("custom", "", "2026-09-10", "2026-09-01"),
		"too long":        reports.ParsePeriod("custom", "", "2024-01-01", "2026-09-01"),
		"a day to come":   reports.ParsePeriod("day", "2031-01-01", "", ""),
		"before the site": reports.ParsePeriod("day", "1999-01-01", "", ""),
	} {
		if period.Kind != "day" || period.From.Format(time.DateOnly) != "2026-09-19" {
			t.Errorf("%s: got %s %s, want today", name, period.Kind, period.From.Format(time.DateOnly))
		}
	}
}

func TestVisitsAndTheirPaths(t *testing.T) {
	f := newFixture(t)
	seedTwoVisitors(f)
	reports := f.reports()
	ctx := context.Background()

	visits, total, err := reports.Visits(ctx, reports.ParsePeriod("day", "", "", ""), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(visits) != 2 {
		t.Fatalf("visits: %d of %d", len(visits), total)
	}
	phone, paid := visits[0], visits[1] // newest first
	if phone.Entry != "/" || phone.Pages != 2 || phone.Source != "direct" || phone.Device != "mobile" || phone.Converted || phone.Actions != 0 || phone.ViewMs != 3000 {
		t.Errorf("the phone visit: %+v", phone)
	}
	if paid.Entry != "/uk/" || paid.Pages != 1 || paid.Source != "ads" || paid.Referrer != "google.com" || paid.Campaign != "google" ||
		!paid.Converted || paid.Actions != 1 || paid.MaxScroll != 75 || paid.IPPrefix != "203.0.113.0/24" || paid.Lang != "uk" {
		t.Errorf("the paid visit: %+v", paid)
	}
	if len(paid.Visitor) != 8 || paid.Visitor == phone.Visitor {
		t.Errorf("visitors: %q and %q", paid.Visitor, phone.Visitor)
	}
	if page2, _, err := reports.Visits(ctx, reports.ParsePeriod("day", "", "", ""), 1, 1); err != nil || len(page2) != 1 || page2[0].ID != paid.ID {
		t.Errorf("second page: %+v, %v", page2, err)
	}

	detail, err := reports.Visit(ctx, phone.ID)
	if err != nil {
		t.Fatal(err)
	}
	var path []string
	for _, step := range detail.Steps {
		path = append(path, step.Type+" "+step.Path+" "+step.Target)
	}
	want := []string{"pageview / ", "section / hero", "outbound / github.com", "pageview /privacy/ ", "egg /privacy/ konami"}
	if strings.Join(path, "|") != strings.Join(want, "|") {
		t.Errorf("path of the visit:\n got %v\nwant %v", path, want)
	}
	if detail.Visit.Pages != 2 || detail.Visit.Entry != "/" || detail.Visit.ViewMs != 3000 {
		t.Errorf("summary of the visit: %+v", detail.Visit)
	}

	for _, id := range []string{"", "zz", "0000000000000000", phone.ID + "00"} {
		if _, err := reports.Visit(ctx, id); !errors.Is(err, ErrNoVisit) {
			t.Errorf("visit %q: %v, want ErrNoVisit", id, err)
		}
	}
}

func TestRecentActivityAndExport(t *testing.T) {
	f := newFixture(t)
	seedTwoVisitors(f)
	reports := f.reports()
	ctx := context.Background()

	recent, err := reports.Recent(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, item := range recent {
		lines = append(lines, item.Type+" "+item.Path+" "+item.Target)
	}
	// Newest first; «time on a section» is bookkeeping, not activity.
	want := []string{"egg /privacy/ konami", "pageview /privacy/ ", "outbound / github.com", "section / hero"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("recent activity:\n got %v\nwant %v", lines, want)
	}

	var pageviews [][]string
	period := reports.ParsePeriod("day", "", "", "")
	if err := reports.ExportPageviews(ctx, period, func(row []string) error {
		pageviews = append(pageviews, append([]string(nil), row...))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(pageviews) != 4 || pageviews[0][0] != "id" || len(pageviews[1]) != len(pageviews[0]) {
		t.Fatalf("page views export: %d rows", len(pageviews))
	}
	first := strings.Join(pageviews[1], ",")
	for _, fragment := range []string{"/uk/", ",2026-09-19,", ",google.com,", "mikrotik", "203.0.113.0/24", "desktop", "15000", "75"} {
		if !strings.Contains(first, fragment) {
			t.Errorf("exported page view lacks %q: %s", fragment, first)
		}
	}
	if strings.Contains(first, visitorIP) {
		t.Errorf("the export contains a full address: %s", first)
	}

	events := 0
	if err := reports.ExportEvents(ctx, period, func([]string) error { events++; return nil }); err != nil {
		t.Fatal(err)
	}
	if events != 1+10 {
		t.Errorf("events export: %d rows, want the header and 10 events", events)
	}
	stop := errors.New("disk full")
	if err := reports.ExportEvents(ctx, period, func([]string) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("an error of the consumer must end the export: %v", err)
	}
}
