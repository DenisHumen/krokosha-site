package analytics

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// Rollup keeps the numbers of every finished day for good and removes the raw events once they
// are older than the storage period (brief B5: raw events — 12 months, aggregates — forever).
type Rollup struct {
	DB       *sql.DB
	Location *time.Location // «a day» is the owner's day, as everywhere in the reports
	Now      func() time.Time
	Log      *slog.Logger
	// KeepMonths is how long raw page views and events are kept; 0 — for good.
	KeepMonths int
}

// breakdownRows is how many lines of one breakdown of one day are kept.
const breakdownRows = 50

// A day is summed up an hour after it ended: the last page views of the evening report their
// time on screen a little after midnight.
const settleAfter = time.Hour

func (r Rollup) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r Rollup) location() *time.Location {
	if r.Location != nil {
		return r.Location
	}
	return time.UTC
}

// Run sums up finished days and removes old raw data: at start, then every six hours.
func (r Rollup) Run(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		days, removed, err := r.RunOnce(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			r.Log.Error("analytics: the daily rollup failed", "error", err)
		case days > 0 || removed > 0:
			r.Log.Info("analytics: days summed up, old raw data removed", "days", days, "page_views_removed", removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce sums up every finished day that is not summed up yet, then removes raw data older
// than the storage period — only of days whose sums exist.
func (r Rollup) RunOnce(ctx context.Context) (days int, removed int64, err error) {
	settled := r.now().In(r.location()).Add(-settleAfter)
	today := time.Date(settled.Year(), settled.Month(), settled.Day(), 0, 0, 0, 0, r.location())
	rows, err := r.DB.QueryContext(ctx, `
		SELECT DISTINCT p.day FROM analytics_pageviews p LEFT JOIN analytics_daily d ON d.day = p.day
		 WHERE p.day < ? AND d.day IS NULL ORDER BY p.day`, today.Format(time.DateOnly))
	if err != nil {
		return 0, 0, err
	}
	var pending []time.Time
	for rows.Next() {
		var day time.Time
		if err := rows.Scan(&day); err != nil {
			_ = rows.Close()
			return 0, 0, err
		}
		pending = append(pending, day)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	for _, day := range pending {
		if err := r.sumUp(ctx, day.Format(time.DateOnly)); err != nil {
			return days, 0, err
		}
		days++
	}

	if r.KeepMonths <= 0 {
		return days, 0, nil
	}
	cutoff := today.AddDate(0, -r.KeepMonths, 0).Format(time.DateOnly)
	for ctx.Err() == nil {
		// In portions: a year of page views must not lock the table the site writes to. Events go
		// with their page views (ON DELETE CASCADE).
		result, err := r.DB.ExecContext(ctx, `
			DELETE FROM analytics_pageviews
			 WHERE day < ? AND day IN (SELECT day FROM analytics_daily) ORDER BY id LIMIT 2000`, cutoff)
		if err != nil {
			return days, removed, err
		}
		portion, _ := result.RowsAffected()
		removed += portion
		if portion < 2000 {
			break
		}
	}
	return days, removed, ctx.Err()
}

// breakdownQueries fill analytics_daily_breakdown; each selects «name, n» for one day (the
// argument), largest first.
var breakdownQueries = []struct{ dimension, query string }{
	{"page", `SELECT path, COUNT(*) FROM analytics_pageviews WHERE day = ? GROUP BY path`},
	{"lang", `SELECT COALESCE(lang, ''), COUNT(*) FROM analytics_pageviews WHERE day = ? GROUP BY 1`},
	{"device", `SELECT device, COUNT(*) FROM analytics_pageviews WHERE day = ? GROUP BY 1`},
	{"browser", `SELECT browser, COUNT(*) FROM analytics_pageviews WHERE day = ? GROUP BY 1`},
	{"os", `SELECT os, COUNT(*) FROM analytics_pageviews WHERE day = ? GROUP BY 1`},
	{"country", `SELECT COALESCE(country, ''), COUNT(*) FROM analytics_pageviews WHERE day = ? GROUP BY 1`},
	{"referrer", `SELECT referrer_host, COUNT(*) FROM analytics_pageviews WHERE day = ? AND referrer_host IS NOT NULL GROUP BY 1`},
	// The first page view of a visit says where the visit came from; paid traffic is a source of its own.
	{"source", `SELECT IF(is_ad = 1, 'ads', referrer_kind), COUNT(*) FROM (
			SELECT referrer_kind, is_ad, ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY started_at, id) AS position
			  FROM analytics_pageviews WHERE day = ?) AS first_views WHERE position = 1 GROUP BY 1`},
	{"campaign", `SELECT CONCAT(utm_source, IF(utm_campaign IS NULL, '', CONCAT(' / ', utm_campaign))), COUNT(DISTINCT session_id)
			FROM analytics_pageviews WHERE day = ? AND utm_source IS NOT NULL GROUP BY 1`},
	{"click", `SELECT COALESCE(target, ''), COUNT(*) FROM analytics_events WHERE day = ? AND type = 'click' GROUP BY 1`},
	{"outbound", `SELECT COALESCE(target, ''), COUNT(*) FROM analytics_events WHERE day = ? AND type = 'outbound' GROUP BY 1`},
	{"egg", `SELECT COALESCE(target, ''), COUNT(*) FROM analytics_events WHERE day = ? AND type = 'egg' GROUP BY 1`},
	{"section", `SELECT COALESCE(target, ''), COUNT(DISTINCT pageview) FROM analytics_events WHERE day = ? AND type = 'section' GROUP BY 1`},
	{"section_ms", `SELECT COALESCE(target, ''), COALESCE(SUM(value), 0) FROM analytics_events WHERE day = ? AND type = 'section_time' GROUP BY 1`},
}

// contacts are the ways to reach the owner whose clicks the dashboard shows as conversions.
var contacts = []string{"telegram", "email", "github"}

// sumUp writes the sums of one day, replacing whatever was there: running it twice is harmless.
func (r Rollup) sumUp(ctx context.Context, day string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{"analytics_daily", "analytics_daily_breakdown"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE day = ?`, day); err != nil { //nolint:gosec // the names are the two constants above
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO analytics_daily (day, visitors, visits, ad_visits, pageviews, view_ms_sum, view_ms_count, actions,
		                             scroll25, scroll50, scroll75, scroll100, aggregated_at)
		SELECT ?, COUNT(DISTINCT visitor), COUNT(DISTINCT session_id), COUNT(DISTINCT IF(is_ad = 1, session_id, NULL)), COUNT(*),
		       COALESCE(SUM(duration_ms), 0), COALESCE(SUM(duration_ms > 0), 0),
		       (SELECT COUNT(*) FROM analytics_events WHERE day = ? AND type = 'click'),
		       COALESCE(SUM(max_scroll >= 25), 0), COALESCE(SUM(max_scroll >= 50), 0), COALESCE(SUM(max_scroll >= 75), 0), COALESCE(SUM(max_scroll >= 100), 0), ?
		  FROM analytics_pageviews WHERE day = ?`, day, day, r.now().UTC(), day); err != nil {
		return err
	}
	for _, breakdown := range breakdownQueries {
		// The query is one of the constants of breakdownQueries; the day is a parameter.
		query := `INSERT INTO analytics_daily_breakdown (day, dimension, name, n) SELECT ?, ?, LEFT(name, 200), n ` + //nolint:gosec // see above
			`FROM (SELECT * FROM (` + renamed(breakdown.query) + `) AS counted ORDER BY n DESC, name LIMIT ?) AS largest`
		if _, err := tx.ExecContext(ctx, query, day, breakdown.dimension, day, breakdownRows); err != nil {
			return err
		}
	}
	for _, contact := range contacts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO analytics_daily_breakdown (day, dimension, name, n)
			SELECT ?, 'contact', ?, COUNT(DISTINCT p.visitor) FROM analytics_events e JOIN analytics_pageviews p ON p.id = e.pageview
			 WHERE e.day = ? AND e.type = 'click' AND (e.target = ? OR e.target LIKE ?) HAVING COUNT(DISTINCT p.visitor) > 0`,
			day, contact, day, contact, "%-"+contact); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// renamed gives the two columns of a breakdown query the names the outer query sorts by.
func renamed(query string) string {
	return `SELECT * FROM (` + query + `) AS raw_counts (name, n)`
}
