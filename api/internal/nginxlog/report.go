package nginxlog

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
)

// Reports read the aggregates for the «server traffic» screen.
type Reports struct {
	db       *sql.DB
	location *time.Location
}

// NewReports builds the reader of the aggregates; days and hours on screen are the owner's.
func NewReports(db *sql.DB, location *time.Location) *Reports {
	if location == nil {
		location = time.UTC
	}
	return &Reports{db: db, location: location}
}

// Bucket is one bar of the traffic charts: an hour of a day, or a day of a longer period.
type Bucket struct {
	Start      time.Time
	Requests   int
	Bots       int // requests of crawlers and tools, part of Requests
	Bytes      int64
	Errors     int // 5xx
	NotFound   int
	Probes     int // requests for things the site never had
	BotClients int // distinct automated clients (for the overview's timeline)
}

// Totals of a period.
type Totals struct {
	Requests, Bots, NotFound, Probes int
	Bytes                            int64
	Status                           [4]int // 2xx, 3xx, 4xx, 5xx
}

// LatencyBar is one bucket of the response-time histogram.
type LatencyBar struct {
	Label   string
	Count   int
	Percent float64
}

// PathStat is a line of «top URLs».
type PathStat struct {
	Path   string
	Status int
	Hits   int
	Bytes  int64
}

// AgentStat is a line of «who asks».
type AgentStat struct {
	Agent   string
	Kind    string
	Hits    int
	Bytes   int64
	Percent float64 // of all requests of the period
}

// ProbeStat says what scanners were looking for.
type ProbeStat struct {
	Pattern  string
	Hits     int
	Networks int
	LastPath string
	LastSeen time.Time
}

// NetworkStat is a network that probes the site.
type NetworkStat struct {
	Prefix   string
	Hits     int
	Patterns string
}

// Traffic is everything the screen shows about a period.
type Traffic struct {
	Period   analytics.Period
	Hourly   bool
	Totals   Totals
	Timeline []Bucket
	Spikes   []Bucket // the busiest hours of «not found»: a broken link or a scanner at work
	Median   string   // response time, as a bucket: «≤ 25 мс»
	P95      string
	Latency  []LatencyBar
	Pages    []PathStat // answered
	Missing  []PathStat // 404
	Failed   []PathStat // 5xx
	Bots     []AgentStat
	Clients  []AgentStat // browsers, tools, unknown
	Probes   []ProbeStat
	Networks []NetworkStat
	ReadAt   time.Time // when the log was last digested; zero if never
}

var latencyLabels = [...]string{"≤ 5 мс", "≤ 10 мс", "≤ 25 мс", "≤ 50 мс", "≤ 100 мс", "≤ 250 мс", "≤ 500 мс", "≤ 1 с", "≤ 2,5 с", "≤ 5 с", "> 5 с"}

// utcRange turns the owner's days into the UTC bounds of the minute table.
func utcRange(period analytics.Period) (time.Time, time.Time) {
	return period.From.UTC(), period.To.AddDate(0, 0, 1).UTC()
}

// Traffic computes the screen for a period.
func (r *Reports) Traffic(ctx context.Context, period analytics.Period) (*Traffic, error) {
	out := &Traffic{Period: period, Hourly: period.Days() == 1}
	timeline, err := r.Timeline(ctx, period)
	if err != nil {
		return nil, err
	}
	out.Timeline = timeline

	from, to := utcRange(period)
	latency := make([]int, len(latencyColumns))
	targets := []any{&out.Totals.Requests, &out.Totals.Bytes, &out.Totals.Status[0], &out.Totals.Status[1], &out.Totals.Status[2],
		&out.Totals.Status[3], &out.Totals.Bots, &out.Totals.NotFound, &out.Totals.Probes}
	sums := make([]string, 0, len(latencyColumns))
	for i, column := range latencyColumns {
		sums = append(sums, "COALESCE(SUM("+column+"), 0)")
		targets = append(targets, &latency[i])
	}
	err = r.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(requests), 0), COALESCE(SUM(bytes_sent), 0), COALESCE(SUM(status_2xx), 0), COALESCE(SUM(status_3xx), 0),
		       COALESCE(SUM(status_4xx), 0), COALESCE(SUM(status_5xx), 0), COALESCE(SUM(bot_requests), 0),
		       COALESCE(SUM(not_found), 0), COALESCE(SUM(probes), 0), `+strings.Join(sums, ", ")+`
		FROM traffic_minutes WHERE minute >= ? AND minute < ?`, from, to).Scan(targets...)
	if err != nil {
		return nil, err
	}
	out.Median, out.P95 = percentile(latency, 50), percentile(latency, 95)
	for i, count := range latency {
		out.Latency = append(out.Latency, LatencyBar{Label: latencyLabels[i], Count: count, Percent: share(count, out.Totals.Requests)})
	}

	// «Spikes»: the hours with the most 404s, if there were enough to call it a spike.
	rows, err := r.db.QueryContext(ctx, `
		SELECT DATE_FORMAT(minute, '%Y-%m-%d %H:00:00') AS hour, SUM(not_found) AS missing, SUM(requests), SUM(probes)
		FROM traffic_minutes WHERE minute >= ? AND minute < ? GROUP BY hour HAVING missing >= 20 ORDER BY missing DESC LIMIT 5`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hour string
		var spike Bucket
		if err := rows.Scan(&hour, &spike.NotFound, &spike.Requests, &spike.Probes); err != nil {
			return nil, err
		}
		if spike.Start, err = time.ParseInLocation(time.DateTime, hour, time.UTC); err != nil {
			return nil, err
		}
		spike.Start = spike.Start.In(r.location)
		out.Spikes = append(out.Spikes, spike)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	day1, day2 := period.From.Format(time.DateOnly), period.To.Format(time.DateOnly)
	if out.Pages, err = r.paths(ctx, day1, day2, "status < 400", 12); err != nil {
		return nil, err
	}
	if out.Missing, err = r.paths(ctx, day1, day2, "status = 404", 12); err != nil {
		return nil, err
	}
	if out.Failed, err = r.paths(ctx, day1, day2, "status >= 500", 8); err != nil {
		return nil, err
	}
	if out.Bots, err = r.agents(ctx, day1, day2, "kind = 'bot'", 12, out.Totals.Requests); err != nil {
		return nil, err
	}
	if out.Clients, err = r.agents(ctx, day1, day2, "kind <> 'bot'", 10, out.Totals.Requests); err != nil {
		return nil, err
	}
	if err := r.probes(ctx, day1, day2, out); err != nil {
		return nil, err
	}

	var readAt sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT MAX(updated_at) FROM traffic_state`).Scan(&readAt); err != nil {
		return nil, err
	}
	out.ReadAt = readAt.Time
	return out, nil
}

// Timeline returns the bars of a period: hours of a single day, days otherwise.
func (r *Reports) Timeline(ctx context.Context, period analytics.Period) ([]Bucket, error) {
	hourly := period.Days() == 1
	var buckets []Bucket
	if hourly {
		day := period.From
		for hour := range 24 {
			buckets = append(buckets, Bucket{Start: time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, r.location)})
		}
	} else {
		for day := range period.Days() {
			buckets = append(buckets, Bucket{Start: period.From.AddDate(0, 0, day)})
		}
	}
	index := func(hourUTC time.Time) int {
		local := hourUTC.In(r.location)
		if hourly {
			if local.Format(time.DateOnly) != period.From.Format(time.DateOnly) {
				return -1
			}
			return local.Hour()
		}
		first := time.Date(period.From.Year(), period.From.Month(), period.From.Day(), 0, 0, 0, 0, time.UTC)
		this := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
		return int(this.Sub(first).Hours() / 24)
	}

	from, to := utcRange(period)
	rows, err := r.db.QueryContext(ctx, `
		SELECT DATE_FORMAT(minute, '%Y-%m-%d %H:00:00') AS hour, SUM(requests), SUM(bot_requests), SUM(bytes_sent), SUM(status_5xx), SUM(not_found)
		FROM traffic_minutes WHERE minute >= ? AND minute < ? GROUP BY hour`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hour string
		var b Bucket
		if err := rows.Scan(&hour, &b.Requests, &b.Bots, &b.Bytes, &b.Errors, &b.NotFound); err != nil {
			return nil, err
		}
		at, err := time.ParseInLocation(time.DateTime, hour, time.UTC)
		if err != nil {
			return nil, err
		}
		if i := index(at); i >= 0 && i < len(buckets) {
			buckets[i].Requests += b.Requests
			buckets[i].Bots += b.Bots
			buckets[i].Bytes += b.Bytes
			buckets[i].Errors += b.Errors
			buckets[i].NotFound += b.NotFound
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	clients, err := r.db.QueryContext(ctx, `SELECT hour, bot_clients FROM traffic_hours WHERE hour >= ? AND hour < ?`, from, to)
	if err != nil {
		return nil, err
	}
	defer clients.Close()
	for clients.Next() {
		var at time.Time
		var bots int
		if err := clients.Scan(&at, &bots); err != nil {
			return nil, err
		}
		if i := index(at); i >= 0 && i < len(buckets) {
			buckets[i].BotClients += bots
		}
	}
	return buckets, clients.Err()
}

// paths and agents take their condition from the constants in Traffic above, never from a request.
func (r *Reports) paths(ctx context.Context, from, to, where string, limit int) ([]PathStat, error) {
	//nolint:gosec // see above
	rows, err := r.db.QueryContext(ctx, `
		SELECT path, MIN(status), SUM(hits) AS total, SUM(bytes_sent) FROM traffic_paths
		WHERE day BETWEEN ? AND ? AND `+where+` GROUP BY path ORDER BY total DESC, path LIMIT ?`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PathStat
	for rows.Next() {
		var stat PathStat
		if err := rows.Scan(&stat.Path, &stat.Status, &stat.Hits, &stat.Bytes); err != nil {
			return nil, err
		}
		out = append(out, stat)
	}
	return out, rows.Err()
}

func (r *Reports) agents(ctx context.Context, from, to, where string, limit, requests int) ([]AgentStat, error) {
	//nolint:gosec // see paths
	rows, err := r.db.QueryContext(ctx, `
		SELECT agent, MAX(kind), SUM(hits) AS total, SUM(bytes_sent) FROM traffic_agents
		WHERE day BETWEEN ? AND ? AND `+where+` GROUP BY agent ORDER BY total DESC, agent LIMIT ?`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentStat
	for rows.Next() {
		var stat AgentStat
		if err := rows.Scan(&stat.Agent, &stat.Kind, &stat.Hits, &stat.Bytes); err != nil {
			return nil, err
		}
		stat.Percent = share(stat.Hits, requests)
		out = append(out, stat)
	}
	return out, rows.Err()
}

func (r *Reports) probes(ctx context.Context, from, to string, out *Traffic) error {
	rows, err := r.db.QueryContext(ctx, `
		SELECT pattern, SUM(hits) AS total, COUNT(DISTINCT ip_prefix), MAX(last_seen),
		       SUBSTRING_INDEX(GROUP_CONCAT(last_path ORDER BY last_seen DESC SEPARATOR '\n'), '\n', 1)
		FROM traffic_probes WHERE day BETWEEN ? AND ? GROUP BY pattern ORDER BY total DESC, pattern`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var stat ProbeStat
		if err := rows.Scan(&stat.Pattern, &stat.Hits, &stat.Networks, &stat.LastSeen, &stat.LastPath); err != nil {
			return err
		}
		out.Probes = append(out.Probes, stat)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	networks, err := r.db.QueryContext(ctx, `
		SELECT ip_prefix, SUM(hits) AS total, GROUP_CONCAT(DISTINCT pattern ORDER BY pattern SEPARATOR ', ')
		FROM traffic_probes WHERE day BETWEEN ? AND ? GROUP BY ip_prefix ORDER BY total DESC, ip_prefix LIMIT 10`, from, to)
	if err != nil {
		return err
	}
	defer networks.Close()
	for networks.Next() {
		var stat NetworkStat
		if err := networks.Scan(&stat.Prefix, &stat.Hits, &stat.Patterns); err != nil {
			return err
		}
		out.Networks = append(out.Networks, stat)
	}
	return networks.Err()
}

func share(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}

// percentile names the histogram bucket in which the given percentile of requests falls.
func percentile(histogram []int, p int) string {
	total := 0
	for _, count := range histogram {
		total += count
	}
	if total == 0 {
		return "—"
	}
	threshold := (total*p + 99) / 100 // rounded up: the 95th of 10 requests is the 10th, not the 9th
	seen := 0
	for i, count := range histogram {
		seen += count
		if seen >= threshold {
			return latencyLabels[i]
		}
	}
	return latencyLabels[len(latencyLabels)-1]
}
