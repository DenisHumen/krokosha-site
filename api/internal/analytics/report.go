package analytics

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Reports read what the service stored — for the admin dashboards (brief B6). Nothing here
// identifies anybody: a visitor is the daily pseudonym, an address is its truncated prefix.
type Reports struct {
	db       *sql.DB
	location *time.Location
	now      func() time.Time
	// keepMonths: how long raw page views are kept (KeepRaw); older periods are read from daily sums.
	keepMonths int
}

// NewReports builds the reader. Days are the owner's days (location), like everywhere in reports.
func NewReports(db *sql.DB, location *time.Location, now func() time.Time) *Reports {
	if location == nil {
		location = time.UTC
	}
	if now == nil {
		now = time.Now
	}
	return &Reports{db: db, location: location, now: now}
}

// MaxPeriodDays bounds a custom range: a year of daily bars is still readable, more is not.
const MaxPeriodDays = 366

// Period is an inclusive range of report days.
type Period struct {
	From, To time.Time // midnight of the first and of the last day, in the owner's time zone
	Kind     string    // day | week | month | custom
}

// Days is the length of the period.
func (p Period) Days() int { return daysBetween(p.From, p.To) + 1 }

// daysBetween counts calendar days from a to b. Through UTC dates: a day with a DST change is
// 23 or 25 hours long, and hours divided by 24 would be off by one around it.
func daysBetween(a, b time.Time) int {
	first := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	second := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(second.Sub(first).Hours() / 24)
}

func (p Period) fromDay() string { return p.From.Format(time.DateOnly) }
func (p Period) toDay() string   { return p.To.Format(time.DateOnly) }

// Shift moves the period back (-1) or forward (+1) by its own length; a month moves by a month.
func (p Period) Shift(direction int) Period {
	switch p.Kind {
	case "month":
		first := time.Date(p.From.Year(), p.From.Month()+time.Month(direction), 1, 0, 0, 0, 0, p.From.Location())
		return Period{From: first, To: first.AddDate(0, 1, -1), Kind: p.Kind}
	default:
		days := p.Days() * direction
		return Period{From: p.From.AddDate(0, 0, days), To: p.To.AddDate(0, 0, days), Kind: p.Kind}
	}
}

// Today is the current report day.
func (r *Reports) Today() time.Time {
	now := r.now().In(r.location)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, r.location)
}

// ParsePeriod understands what the period switcher sends: a kind (day, week, month) with an
// optional anchor day, or a custom from–to range. Anything odd falls back to today —
// a dashboard should open, not argue.
func (r *Reports) ParsePeriod(kind, day, from, to string) Period {
	today := r.Today()
	parse := func(text string) (time.Time, bool) {
		parsed, err := time.ParseInLocation(time.DateOnly, text, r.location)
		if err != nil || parsed.After(today) || parsed.Year() < 2020 {
			return time.Time{}, false
		}
		return parsed, true
	}

	if kind == "custom" {
		start, okFrom := parse(from)
		end, okTo := parse(to)
		if okFrom && okTo && !end.Before(start) {
			period := Period{From: start, To: end, Kind: "custom"}
			if period.Days() <= MaxPeriodDays {
				return period
			}
		}
		return Period{From: today, To: today, Kind: "day"}
	}

	anchor, ok := parse(day)
	if !ok {
		anchor = today
	}
	switch kind {
	case "week": // Monday to Sunday
		monday := anchor.AddDate(0, 0, -((int(anchor.Weekday()) + 6) % 7))
		return Period{From: monday, To: monday.AddDate(0, 0, 6), Kind: "week"}
	case "month":
		first := time.Date(anchor.Year(), anchor.Month(), 1, 0, 0, 0, 0, r.location)
		return Period{From: first, To: first.AddDate(0, 1, -1), Kind: "month"}
	default:
		return Period{From: anchor, To: anchor, Kind: "day"}
	}
}

// Totals are the large numbers of the dashboard.
type Totals struct {
	Visitors  int
	Visits    int
	Pageviews int
	AdVisits  int
	AvgViewMs int // average time a page was actually visible
	Actions   int // clicks on tracked elements (data-track)
}

// Bucket is one bar of the timeline: visits that began in that hour (or on that day).
type Bucket struct {
	Start   time.Time
	Organic int
	Ads     int
}

// Share is one line of a breakdown.
type Share struct {
	Name    string
	Count   int
	Percent float64 // of the breakdown's total
}

// Section says how far visitors got and where they spent their time.
type Section struct {
	Name        string
	Views       int     // page views in which the section appeared on screen
	Reach       float64 // … as a percentage of all page views
	TimeMs      int64
	TimePercent float64 // share of the time spent in all sections
}

// Conversion is a «score» ring: how many visitors clicked a contact.
type Conversion struct {
	Name     string
	Visitors int
	Percent  float64 // of all visitors of the period
}

// Overview is everything the main dashboard shows about a period.
type Overview struct {
	Period Period
	Hourly bool // buckets are hours (a single day) rather than days
	// Aggregated: the period reaches back past the raw data, so the numbers are the sums of its
	// days (overviewFromDaily) — without the hours of visits, without the day that is not over.
	Aggregated  bool
	Totals      Totals
	Timeline    []Bucket
	Sections    []Section
	Scroll      []Share // page views that reached 25 / 50 / 75 / 100 %
	Conversions []Conversion
	Sources     []Share // direct, search, social, other, ads
	Referrers   []Share
	Campaigns   []Share
	Pages       []Share
	Languages   []Share
	Devices     []Share
	Browsers    []Share
	Systems     []Share
	Countries   []Share
	Clicks      []Share // tracked elements
	Outbound    []Share // hosts of links that lead away; a tracked external link counts in both lists
	Eggs        []Share
}

// Totals counts the visitors, visits and page views of a period, and the average time a page was
// on screen, without the rest of the dashboard: for the header of the admin area and for comparing
// a period with the one before it. AdVisits and Actions stay zero.
func (r *Reports) Totals(ctx context.Context, period Period) (Totals, error) {
	var out Totals
	from, to := period.fromDay(), period.toDay()
	if r.rawGone(period) {
		var viewSum, viewCount int64
		err := r.db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(visitors), 0), COALESCE(SUM(visits), 0), COALESCE(SUM(pageviews), 0),
			       COALESCE(SUM(view_ms_sum), 0), COALESCE(SUM(view_ms_count), 0)
			  FROM analytics_daily WHERE day BETWEEN ? AND ?`, from, to).
			Scan(&out.Visitors, &out.Visits, &out.Pageviews, &viewSum, &viewCount)
		if viewCount > 0 {
			out.AvgViewMs = int((viewSum + viewCount/2) / viewCount)
		}
		return out, err
	}
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT visitor), COUNT(DISTINCT session_id), COUNT(*), COALESCE(ROUND(AVG(NULLIF(duration_ms, 0))), 0)
		FROM analytics_pageviews WHERE day BETWEEN ? AND ?`, from, to).
		Scan(&out.Visitors, &out.Visits, &out.Pageviews, &out.AvgViewMs)
	return out, err
}

// Overview computes the dashboard for a period.
func (r *Reports) Overview(ctx context.Context, period Period) (*Overview, error) {
	if r.rawGone(period) {
		return r.overviewFromDaily(ctx, period)
	}
	out := &Overview{Period: period, Hourly: period.Days() == 1}
	from, to := period.fromDay(), period.toDay()

	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT visitor), COUNT(DISTINCT session_id), COUNT(*),
		       COALESCE(ROUND(AVG(NULLIF(duration_ms, 0))), 0)
		FROM analytics_pageviews WHERE day BETWEEN ? AND ?`, from, to).
		Scan(&out.Totals.Visitors, &out.Totals.Visits, &out.Totals.Pageviews, &out.Totals.AvgViewMs)
	if err != nil {
		return nil, err
	}
	err = r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM analytics_events WHERE day BETWEEN ? AND ? AND type = 'click'`, from, to).
		Scan(&out.Totals.Actions)
	if err != nil {
		return nil, err
	}

	if err := r.timeline(ctx, out); err != nil {
		return nil, err
	}
	if err := r.sections(ctx, out); err != nil {
		return nil, err
	}
	if err := r.conversions(ctx, out); err != nil {
		return nil, err
	}

	// Breakdowns of page views by one column each. The column names are constants of this file.
	for _, breakdown := range []struct {
		target *[]Share
		column string
		limit  int
	}{
		{&out.Pages, "path", 8},
		{&out.Languages, "COALESCE(lang, '')", 5},
		{&out.Devices, "device", 5},
		{&out.Browsers, "browser", 6},
		{&out.Systems, "os", 6},
		{&out.Countries, "COALESCE(country, '')", 8},
		{&out.Referrers, "referrer_host", 8},
	} {
		where := ""
		if breakdown.column == "referrer_host" {
			where = " AND referrer_host IS NOT NULL"
		}
		shares, err := r.shares(ctx, breakdown.limit, `SELECT `+breakdown.column+` AS name, COUNT(*) AS n FROM analytics_pageviews
			WHERE day BETWEEN ? AND ?`+where+` GROUP BY name ORDER BY n DESC, name`, from, to)
		if err != nil {
			return nil, err
		}
		*breakdown.target = shares
	}

	// Where visits came from: the first page view of a visit decides. Paid traffic is a source
	// of its own, whatever the referrer says.
	out.Sources, err = r.shares(ctx, 6, `
		SELECT IF(is_ad = 1, 'ads', referrer_kind) AS name, COUNT(*) AS n FROM (
			SELECT referrer_kind, is_ad, ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY started_at, id) AS position
			FROM analytics_pageviews WHERE day BETWEEN ? AND ?
		) AS first_views WHERE position = 1 GROUP BY name ORDER BY n DESC, name`, from, to)
	if err != nil {
		return nil, err
	}
	out.Campaigns, err = r.shares(ctx, 8, `
		SELECT CONCAT(utm_source, IF(utm_campaign IS NULL, '', CONCAT(' / ', utm_campaign))) AS name, COUNT(DISTINCT session_id) AS n
		FROM analytics_pageviews WHERE day BETWEEN ? AND ? AND utm_source IS NOT NULL
		GROUP BY name ORDER BY n DESC, name`, from, to)
	if err != nil {
		return nil, err
	}
	out.Clicks, err = r.shares(ctx, 10, `
		SELECT COALESCE(target, '') AS name, COUNT(*) AS n FROM analytics_events
		WHERE day BETWEEN ? AND ? AND type = 'click' GROUP BY name ORDER BY n DESC, name`, from, to)
	if err != nil {
		return nil, err
	}
	out.Outbound, err = r.shares(ctx, 8, `
		SELECT COALESCE(target, '') AS name, COUNT(*) AS n FROM analytics_events
		WHERE day BETWEEN ? AND ? AND type = 'outbound' GROUP BY name ORDER BY n DESC, name`, from, to)
	if err != nil {
		return nil, err
	}
	out.Eggs, err = r.shares(ctx, 8, `
		SELECT COALESCE(target, '') AS name, COUNT(*) AS n FROM analytics_events
		WHERE day BETWEEN ? AND ? AND type = 'egg' GROUP BY name ORDER BY n DESC, name`, from, to)
	if err != nil {
		return nil, err
	}

	// Scroll depth is cumulative: whoever reached 75 % also reached 50 %.
	var reached [4]int
	err = r.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(max_scroll >= 25), 0), COALESCE(SUM(max_scroll >= 50), 0),
		       COALESCE(SUM(max_scroll >= 75), 0), COALESCE(SUM(max_scroll >= 100), 0)
		FROM analytics_pageviews WHERE day BETWEEN ? AND ?`, from, to).
		Scan(&reached[0], &reached[1], &reached[2], &reached[3])
	if err != nil {
		return nil, err
	}
	for i, depth := range []string{"25%", "50%", "75%", "100%"} {
		out.Scroll = append(out.Scroll, Share{Name: depth, Count: reached[i], Percent: percent(reached[i], out.Totals.Pageviews)})
	}
	return out, nil
}

func percent(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}

// shares runs a «name, count» query sorted by count, keeps the first limit lines and gives each
// its percentage of ALL rows — the tail that is cut off still counts towards 100 %. A site like
// this has a handful of distinct values per breakdown, so reading them all costs nothing.
func (r *Reports) shares(ctx context.Context, limit int, query string, args ...any) ([]Share, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Share
	total := 0
	for rows.Next() {
		var share Share
		if err := rows.Scan(&share.Name, &share.Count); err != nil {
			return nil, err
		}
		total += share.Count
		if len(out) < limit {
			out = append(out, share)
		}
	}
	for i := range out {
		out[i].Percent = percent(out[i].Count, total)
	}
	return out, rows.Err()
}

// timeline counts visits by the moment they began: per hour for a single day, per day otherwise.
func (r *Reports) timeline(ctx context.Context, out *Overview) error {
	period := out.Period
	if out.Hourly {
		day := period.From
		for hour := range 24 { // by the clock, not by elapsed time: a day with a DST change still has hours 0–23
			out.Timeline = append(out.Timeline, Bucket{Start: time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, r.location)})
		}
	} else {
		for day := range period.Days() {
			out.Timeline = append(out.Timeline, Bucket{Start: period.From.AddDate(0, 0, day)})
		}
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT MIN(started_at), MAX(is_ad) FROM analytics_pageviews
		WHERE day BETWEEN ? AND ? GROUP BY session_id`, period.fromDay(), period.toDay())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var started time.Time
		var ad bool
		if err := rows.Scan(&started, &ad); err != nil {
			return err
		}
		local := started.In(r.location)
		index := daysBetween(period.From, local)
		if out.Hourly {
			index = local.Hour()
		}
		if index < 0 || index >= len(out.Timeline) {
			continue // a visit that began the evening before
		}
		if ad {
			out.Timeline[index].Ads++
			out.Totals.AdVisits++
		} else {
			out.Timeline[index].Organic++
		}
	}
	return rows.Err()
}

func (r *Reports) sections(ctx context.Context, out *Overview) error {
	rows, err := r.db.QueryContext(ctx, `
		SELECT COALESCE(target, ''), COUNT(DISTINCT IF(type = 'section', pageview, NULL)), COALESCE(SUM(IF(type = 'section_time', value, 0)), 0)
		FROM analytics_events WHERE day BETWEEN ? AND ? AND type IN ('section', 'section_time')
		GROUP BY target`, out.Period.fromDay(), out.Period.toDay())
	if err != nil {
		return err
	}
	defer rows.Close()
	var totalTime int64
	for rows.Next() {
		var section Section
		if err := rows.Scan(&section.Name, &section.Views, &section.TimeMs); err != nil {
			return err
		}
		totalTime += section.TimeMs
		out.Sections = append(out.Sections, section)
	}
	for i := range out.Sections {
		section := &out.Sections[i]
		section.Reach = percent(section.Views, out.Totals.Pageviews)
		if totalTime > 0 {
			section.TimePercent = float64(section.TimeMs) * 100 / float64(totalTime)
		}
	}
	sort.SliceStable(out.Sections, func(a, b int) bool {
		if out.Sections[a].TimeMs != out.Sections[b].TimeMs {
			return out.Sections[a].TimeMs > out.Sections[b].TimeMs
		}
		return out.Sections[a].Views > out.Sections[b].Views
	})
	return rows.Err()
}

// conversions: visitors who clicked a way to reach the owner. Track ids end with the contact's
// id (cta-telegram, social-telegram — docs/contract.md), so new buttons are counted by themselves.
func (r *Reports) conversions(ctx context.Context, out *Overview) error {
	for _, contact := range []string{"telegram", "email", "github"} {
		conversion := Conversion{Name: contact}
		err := r.db.QueryRowContext(ctx, `
			SELECT COUNT(DISTINCT p.visitor) FROM analytics_events e JOIN analytics_pageviews p ON p.id = e.pageview
			WHERE e.day BETWEEN ? AND ? AND e.type = 'click' AND (e.target = ? OR e.target LIKE ?)`,
			out.Period.fromDay(), out.Period.toDay(), contact, "%-"+contact).Scan(&conversion.Visitors)
		if err != nil {
			return err
		}
		conversion.Percent = percent(conversion.Visitors, out.Totals.Visitors)
		out.Conversions = append(out.Conversions, conversion)
	}
	return nil
}

// Activity is a line of the feed: stored events, newest first.
type Activity struct {
	At      time.Time
	Visitor string // first 8 hex characters of the daily pseudonym
	Path    string
	Type    string
	Target  string
}

// Recent returns the latest page views and actions — what the live feed shows before anything
// new arrives.
func (r *Reports) Recent(ctx context.Context, limit int) ([]Activity, error) {
	rows, err := r.db.QueryContext(ctx, `
		(SELECT started_at AS at, visitor, path, 'pageview' AS type, '' AS target
		   FROM analytics_pageviews ORDER BY id DESC LIMIT ?)
		UNION ALL
		(SELECT e.occurred_at, p.visitor, p.path, e.type, COALESCE(e.target, '')
		   FROM analytics_events e JOIN analytics_pageviews p ON p.id = e.pageview
		  WHERE e.type IN ('click', 'outbound', 'section', 'egg') ORDER BY e.id DESC LIMIT ?)
		ORDER BY at DESC LIMIT ?`, limit, limit, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var item Activity
		var visitor []byte
		if err := rows.Scan(&item.At, &visitor, &item.Path, &item.Type, &item.Target); err != nil {
			return nil, err
		}
		item.Visitor = shortID(visitor)
		out = append(out, item)
	}
	return out, rows.Err()
}

func shortID(raw []byte) string {
	text := hex.EncodeToString(raw)
	if len(text) > 8 {
		return text[:8]
	}
	return text
}

// Visit is one line of the «sessions» screen.
type Visit struct {
	ID         string // hex of the session id
	Visitor    string
	Started    time.Time
	Pages      int
	Actions    int
	ViewMs     int64
	Entry      string
	Source     string // direct | search | social | other | ads
	Referrer   string
	Campaign   string
	Device     string
	Browser    string
	OS         string
	Country    string
	City       string
	IPPrefix   string
	Lang       string
	MaxScroll  int
	Converted  bool // clicked a contact
	LastSeenAt time.Time
}

// Visits lists the visits of a period, newest first.
func (r *Reports) Visits(ctx context.Context, period Period, limit, offset int) ([]Visit, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT session_id) FROM analytics_pageviews WHERE day BETWEEN ? AND ?`,
		period.fromDay(), period.toDay()).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT v.session_id, v.visitor, v.started_at, v.pages, v.view_ms, v.path, IF(v.any_ad = 1, 'ads', v.referrer_kind),
		       COALESCE(v.referrer_host, ''), COALESCE(v.utm_source, ''), v.device, v.browser, v.os, COALESCE(v.country, ''),
		       COALESCE(v.city, ''), v.ip_prefix, COALESCE(v.lang, ''), v.deepest, v.last_view,
		       (SELECT COUNT(*) FROM analytics_events e JOIN analytics_pageviews p ON p.id = e.pageview
		         WHERE p.session_id = v.session_id AND e.type = 'click'),
		       (SELECT COUNT(*) FROM analytics_events e JOIN analytics_pageviews p ON p.id = e.pageview
		         WHERE p.session_id = v.session_id AND e.type = 'click'
		           AND (e.target LIKE '%-telegram' OR e.target LIKE '%-email' OR e.target LIKE '%-github'))
		FROM (
			SELECT session_id, visitor, started_at, path, referrer_kind, referrer_host, utm_source, device, browser, os,
			       country, city, ip_prefix, lang,
			       COUNT(*) OVER w AS pages, SUM(duration_ms) OVER w AS view_ms, MAX(is_ad) OVER w AS any_ad,
			       MAX(max_scroll) OVER w AS deepest, MAX(started_at) OVER w AS last_view,
			       ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY started_at, id) AS position
			FROM analytics_pageviews WHERE day BETWEEN ? AND ?
			WINDOW w AS (PARTITION BY session_id)
		) AS v
		WHERE v.position = 1 ORDER BY v.started_at DESC LIMIT ? OFFSET ?`,
		period.fromDay(), period.toDay(), limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Visit
	for rows.Next() {
		var visit Visit
		var session, visitor []byte
		var contacts int
		if err := rows.Scan(&session, &visitor, &visit.Started, &visit.Pages, &visit.ViewMs, &visit.Entry, &visit.Source,
			&visit.Referrer, &visit.Campaign, &visit.Device, &visit.Browser, &visit.OS, &visit.Country, &visit.City,
			&visit.IPPrefix, &visit.Lang, &visit.MaxScroll, &visit.LastSeenAt, &visit.Actions, &contacts); err != nil {
			return nil, 0, err
		}
		visit.ID, visit.Visitor, visit.Converted = hex.EncodeToString(session), shortID(visitor), contacts > 0
		out = append(out, visit)
	}
	return out, total, rows.Err()
}

// Step is a moment of a visit: a page opened, a section reached, a click.
type Step struct {
	At     time.Time
	Path   string
	Type   string // pageview | section | section_time | click | outbound | egg
	Target string
	Value  int64
}

// VisitDetail is the path of one visitor through the site.
type VisitDetail struct {
	Visit Visit
	Steps []Step
}

// ErrNoVisit is returned for an id that matches nothing.
var ErrNoVisit = errors.New("no such visit")

// Visit returns the path of one visit.
func (r *Reports) Visit(ctx context.Context, id string) (*VisitDetail, error) {
	session, err := hex.DecodeString(id)
	if err != nil || len(session) != 8 {
		return nil, ErrNoVisit
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, visitor, started_at, path, IF(is_ad = 1, 'ads', referrer_kind), COALESCE(referrer_host, ''),
		       COALESCE(utm_source, ''), device, browser, os, COALESCE(country, ''), COALESCE(city, ''), ip_prefix,
		       COALESCE(lang, ''), duration_ms, max_scroll
		FROM analytics_pageviews WHERE session_id = ? ORDER BY started_at, id`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	detail := &VisitDetail{Visit: Visit{ID: id}}
	var ids []any
	for rows.Next() {
		var pageview int64
		var visitor []byte
		var view Visit
		var viewMs int64
		if err := rows.Scan(&pageview, &visitor, &view.Started, &view.Entry, &view.Source, &view.Referrer, &view.Campaign,
			&view.Device, &view.Browser, &view.OS, &view.Country, &view.City, &view.IPPrefix, &view.Lang, &viewMs, &view.MaxScroll); err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			view.ID, view.Visitor = id, shortID(visitor)
			detail.Visit = view
		}
		ids = append(ids, pageview)
		detail.Visit.Pages++
		detail.Visit.ViewMs += viewMs
		detail.Visit.MaxScroll = max(detail.Visit.MaxScroll, view.MaxScroll)
		detail.Visit.LastSeenAt = view.Started
		detail.Steps = append(detail.Steps, Step{At: view.Started, Path: view.Entry, Type: TypePageview, Value: viewMs})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNoVisit
	}

	events, err := r.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT e.occurred_at, p.path, e.type, COALESCE(e.target, ''), COALESCE(e.value, 0)
		FROM analytics_events e JOIN analytics_pageviews p ON p.id = e.pageview
		WHERE e.pageview IN (%s) AND e.type <> 'section_time' ORDER BY e.occurred_at, e.id`,
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")), ids...)
	if err != nil {
		return nil, err
	}
	defer events.Close()
	for events.Next() {
		var step Step
		if err := events.Scan(&step.At, &step.Path, &step.Type, &step.Target, &step.Value); err != nil {
			return nil, err
		}
		if step.Type == TypeClick {
			detail.Visit.Actions++
		}
		detail.Steps = append(detail.Steps, step)
	}
	sort.SliceStable(detail.Steps, func(a, b int) bool { return detail.Steps[a].At.Before(detail.Steps[b].At) })
	return detail, events.Err()
}

// ExportPageviews streams the page views of a period, oldest first, to fn.
func (r *Reports) ExportPageviews(ctx context.Context, period Period, fn func(row []string) error) error {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, started_at, DATE_FORMAT(day, '%Y-%m-%d'), HEX(visitor), HEX(session_id), path, COALESCE(lang, ''), referrer_kind,
		       COALESCE(referrer_host, ''), COALESCE(utm_source, ''), COALESCE(utm_medium, ''), COALESCE(utm_campaign, ''),
		       COALESCE(utm_term, ''), COALESCE(utm_content, ''), is_ad, ip_prefix, COALESCE(country, ''), COALESCE(city, ''),
		       device, browser, os, duration_ms, max_scroll
		FROM analytics_pageviews WHERE day BETWEEN ? AND ? ORDER BY id`, period.fromDay(), period.toDay())
	if err != nil {
		return err
	}
	defer rows.Close()
	if err := fn([]string{"id", "started_at_utc", "day", "visitor", "visit", "path", "lang", "referrer_kind", "referrer_host",
		"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "is_ad", "ip_prefix", "country", "city",
		"device", "browser", "os", "visible_ms", "max_scroll"}); err != nil {
		return err
	}
	return streamRows(rows, 23, fn)
}

// ExportEvents streams the stored events of a period, oldest first, to fn.
func (r *Reports) ExportEvents(ctx context.Context, period Period, fn func(row []string) error) error {
	rows, err := r.db.QueryContext(ctx, `
		SELECT e.id, e.occurred_at, DATE_FORMAT(e.day, '%Y-%m-%d'), e.pageview, HEX(p.session_id), p.path, e.type, COALESCE(e.target, ''), COALESCE(e.value, '')
		FROM analytics_events e JOIN analytics_pageviews p ON p.id = e.pageview
		WHERE e.day BETWEEN ? AND ? ORDER BY e.id`, period.fromDay(), period.toDay())
	if err != nil {
		return err
	}
	defer rows.Close()
	if err := fn([]string{"id", "occurred_at_utc", "day", "pageview", "visit", "path", "type", "target", "value"}); err != nil {
		return err
	}
	return streamRows(rows, 9, fn)
}

func streamRows(rows *sql.Rows, columns int, fn func(row []string) error) error {
	raw := make([]sql.RawBytes, columns)
	targets := make([]any, columns)
	for i := range raw {
		targets[i] = &raw[i]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		line := make([]string, columns)
		for i, cell := range raw {
			line[i] = string(cell)
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return rows.Err()
}
