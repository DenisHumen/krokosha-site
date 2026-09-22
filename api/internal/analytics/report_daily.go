package analytics

import (
	"context"
	"sort"
	"time"
)

// KeepRaw tells the reports how long raw page views are kept (Rollup.KeepMonths): a period that
// begins earlier is answered from the sums of days, which stay for good. 0 — raw data is never removed.
func (r *Reports) KeepRaw(months int) { r.keepMonths = months }

// rawGone: the period reaches back past the point where raw page views are removed.
func (r *Reports) rawGone(period Period) bool {
	if r.keepMonths <= 0 {
		return false
	}
	return period.From.Before(r.Today().AddDate(0, -r.keepMonths, 0))
}

// overviewFromDaily builds the dashboard of a period from the sums of its days. The numbers are
// the ones the raw data gave while it was there; what is lost is the hour of a visit and
// everything about single visits. The day that is not over yet is not in the sums.
func (r *Reports) overviewFromDaily(ctx context.Context, period Period) (*Overview, error) {
	out := &Overview{Period: period, Aggregated: true}
	from, to := period.fromDay(), period.toDay()

	var viewSum, viewCount int64
	var reached [4]int
	err := r.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(visitors), 0), COALESCE(SUM(visits), 0), COALESCE(SUM(ad_visits), 0), COALESCE(SUM(pageviews), 0),
		       COALESCE(SUM(view_ms_sum), 0), COALESCE(SUM(view_ms_count), 0), COALESCE(SUM(actions), 0),
		       COALESCE(SUM(scroll25), 0), COALESCE(SUM(scroll50), 0), COALESCE(SUM(scroll75), 0), COALESCE(SUM(scroll100), 0)
		  FROM analytics_daily WHERE day BETWEEN ? AND ?`, from, to).
		Scan(&out.Totals.Visitors, &out.Totals.Visits, &out.Totals.AdVisits, &out.Totals.Pageviews, &viewSum, &viewCount, &out.Totals.Actions,
			&reached[0], &reached[1], &reached[2], &reached[3])
	if err != nil {
		return nil, err
	}
	if viewCount > 0 {
		out.Totals.AvgViewMs = int((viewSum + viewCount/2) / viewCount)
	}
	for i, depth := range []string{"25%", "50%", "75%", "100%"} {
		out.Scroll = append(out.Scroll, Share{Name: depth, Count: reached[i], Percent: percent(reached[i], out.Totals.Pageviews)})
	}

	for day := range period.Days() {
		out.Timeline = append(out.Timeline, Bucket{Start: period.From.AddDate(0, 0, day)})
	}
	rows, err := r.db.QueryContext(ctx, `SELECT day, visits, ad_visits FROM analytics_daily WHERE day BETWEEN ? AND ?`, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var day time.Time
		var visits, ads int
		if err := rows.Scan(&day, &visits, &ads); err != nil {
			_ = rows.Close()
			return nil, err
		}
		local := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, r.location)
		if index := daysBetween(period.From, local); index >= 0 && index < len(out.Timeline) {
			out.Timeline[index].Organic, out.Timeline[index].Ads = max(visits-ads, 0), ads
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	dimension := func(name string, limit int) ([]Share, error) {
		return r.shares(ctx, limit, `SELECT name, SUM(n) AS total FROM analytics_daily_breakdown
			WHERE day BETWEEN ? AND ? AND dimension = ? GROUP BY name ORDER BY total DESC, name`, from, to, name)
	}
	for _, breakdown := range []struct {
		target *[]Share
		name   string
		limit  int
	}{
		{&out.Pages, "page", 8}, {&out.Languages, "lang", 5}, {&out.Devices, "device", 5}, {&out.Browsers, "browser", 6},
		{&out.Systems, "os", 6}, {&out.Countries, "country", 8}, {&out.Referrers, "referrer", 8}, {&out.Sources, "source", 6},
		{&out.Campaigns, "campaign", 8}, {&out.Clicks, "click", 10}, {&out.Outbound, "outbound", 8}, {&out.Eggs, "egg", 8},
	} {
		if *breakdown.target, err = dimension(breakdown.name, breakdown.limit); err != nil {
			return nil, err
		}
	}

	// Sections: how many page views showed them, and the time spent in them.
	views, err := dimension("section", breakdownRows)
	if err != nil {
		return nil, err
	}
	spent, err := dimension("section_ms", breakdownRows)
	if err != nil {
		return nil, err
	}
	sections := map[string]*Section{}
	for _, share := range views {
		sections[share.Name] = &Section{Name: share.Name, Views: share.Count}
	}
	var totalTime int64
	for _, share := range spent {
		if sections[share.Name] == nil {
			sections[share.Name] = &Section{Name: share.Name}
		}
		sections[share.Name].TimeMs = int64(share.Count)
		totalTime += int64(share.Count)
	}
	for _, section := range sections {
		section.Reach = percent(section.Views, out.Totals.Pageviews)
		if totalTime > 0 {
			section.TimePercent = float64(section.TimeMs) * 100 / float64(totalTime)
		}
		out.Sections = append(out.Sections, *section)
	}
	sort.SliceStable(out.Sections, func(a, b int) bool {
		if out.Sections[a].TimeMs != out.Sections[b].TimeMs {
			return out.Sections[a].TimeMs > out.Sections[b].TimeMs
		}
		if out.Sections[a].Views != out.Sections[b].Views {
			return out.Sections[a].Views > out.Sections[b].Views
		}
		return out.Sections[a].Name < out.Sections[b].Name
	})

	clicked, err := dimension("contact", len(contacts))
	if err != nil {
		return nil, err
	}
	for _, contact := range contacts {
		conversion := Conversion{Name: contact}
		for _, share := range clicked {
			if share.Name == contact {
				conversion.Visitors = share.Count
			}
		}
		conversion.Percent = percent(conversion.Visitors, out.Totals.Visitors)
		out.Conversions = append(out.Conversions, conversion)
	}
	return out, nil
}
