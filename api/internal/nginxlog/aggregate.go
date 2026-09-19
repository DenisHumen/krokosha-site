package nginxlog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// Latency buckets of traffic_minutes, upper bounds. Whatever is slower than the last one goes
// to t_slow.
var latencyBounds = [...]time.Duration{
	5 * time.Millisecond, 10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond,
	250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2500 * time.Millisecond, 5 * time.Second,
}

var latencyColumns = [...]string{"t_5ms", "t_10ms", "t_25ms", "t_50ms", "t_100ms", "t_250ms", "t_500ms", "t_1s", "t_2500ms", "t_5s", "t_slow"}

func latencyBucket(d time.Duration) int {
	for i, bound := range latencyBounds {
		if d <= bound {
			return i
		}
	}
	return len(latencyBounds)
}

type minuteStats struct {
	requests, bots, notFound, probes int
	bytes                            int64
	status                           [4]int // 2xx, 3xx, 4xx, 5xx
	latency                          [len(latencyBounds) + 1]int
}

type pathKey struct {
	day, path string
	status    int
}

type agentKey struct{ day, agent string }

type probeKey struct{ day, prefix, pattern string }

type counter struct {
	hits  int
	bytes int64
}

type probeStats struct {
	hits     int
	lastPath string
	lastSeen time.Time
}

// aggregate collects what one poll of the log adds to the tables.
type aggregate struct {
	location *time.Location
	minutes  map[time.Time]*minuteStats
	paths    map[pathKey]*counter
	agents   map[agentKey]*counter
	kinds    map[string]string // agent → kind
	probes   map[probeKey]*probeStats
	// Distinct clients per hour: hashes of (address, User-Agent), handed to the HyperLogLog counter.
	clients map[time.Time]map[bool]map[string]struct{} // hour → is a bot → hashes
	lines   int
	skipped int
}

func newAggregate(location *time.Location) *aggregate {
	return &aggregate{
		location: location,
		minutes:  map[time.Time]*minuteStats{},
		paths:    map[pathKey]*counter{},
		agents:   map[agentKey]*counter{},
		kinds:    map[string]string{},
		probes:   map[probeKey]*probeStats{},
		clients:  map[time.Time]map[bool]map[string]struct{}{},
	}
}

func (a *aggregate) add(entry Entry) {
	a.lines++
	agent := ClassifyAgent(entry.UserAgent)
	probe := Probe(entry.Path)
	day := entry.Time.In(a.location).Format(time.DateOnly)

	minute := entry.Time.Truncate(time.Minute)
	stats := a.minutes[minute]
	if stats == nil {
		stats = &minuteStats{}
		a.minutes[minute] = stats
	}
	stats.requests++
	stats.bytes += entry.BytesSent
	if class := entry.Status/100 - 2; class >= 0 && class < len(stats.status) {
		stats.status[class]++
	}
	if agent.IsBot() {
		stats.bots++
	}
	if entry.Status == 404 {
		stats.notFound++
	}
	if probe != "" {
		stats.probes++
	}
	stats.latency[latencyBucket(entry.RequestTime)]++

	bump := func(c *counter) {
		c.hits++
		c.bytes += entry.BytesSent
	}
	pk := pathKey{day, entry.Path, entry.Status}
	if a.paths[pk] == nil {
		a.paths[pk] = &counter{}
	}
	bump(a.paths[pk])
	ak := agentKey{day, agent.Name}
	if a.agents[ak] == nil {
		a.agents[ak] = &counter{}
	}
	bump(a.agents[ak])
	a.kinds[agent.Name] = agent.Kind

	if probe != "" {
		key := probeKey{day, clientPrefix(entry.RemoteAddr), probe}
		seen := a.probes[key]
		if seen == nil {
			seen = &probeStats{}
			a.probes[key] = seen
		}
		seen.hits++
		seen.lastPath, seen.lastSeen = entry.Path, entry.Time
	}

	hour := entry.Time.Truncate(time.Hour)
	if a.clients[hour] == nil {
		a.clients[hour] = map[bool]map[string]struct{}{true: {}, false: {}}
	}
	sum := sha256.Sum256([]byte(entry.RemoteAddr + "\n" + entry.UserAgent))
	a.clients[hour][agent.IsBot()][hex.EncodeToString(sum[:8])] = struct{}{}
}

// otherPaths is where page addresses go once a day has more distinct ones than maxPathsPerDay.
const otherPaths = "(прочие адреса)"

// store writes the aggregate in the given transaction. pathsToday answers how many distinct
// paths a day already has in the table; beyond the cap new ones are folded into one row.
func (a *aggregate) store(ctx context.Context, tx *sql.Tx, maxPathsPerDay int) error {
	for minute, s := range a.minutes {
		args := []any{minute, s.requests, s.bytes, s.status[0], s.status[1], s.status[2], s.status[3], s.bots, s.notFound, s.probes}
		for _, count := range s.latency {
			args = append(args, count)
		}
		updates := make([]string, 0, len(latencyColumns))
		for _, column := range latencyColumns {
			updates = append(updates, column+" = "+column+" + VALUES("+column+")")
		}
		//nolint:gosec // the concatenated parts are the column names listed at the top of this file
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO traffic_minutes (minute, requests, bytes_sent, status_2xx, status_3xx, status_4xx, status_5xx,
				bot_requests, not_found, probes, `+strings.Join(latencyColumns[:], ", ")+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?`+strings.Repeat(", ?", len(latencyColumns))+`)
			ON DUPLICATE KEY UPDATE requests = requests + VALUES(requests), bytes_sent = bytes_sent + VALUES(bytes_sent),
				status_2xx = status_2xx + VALUES(status_2xx), status_3xx = status_3xx + VALUES(status_3xx),
				status_4xx = status_4xx + VALUES(status_4xx), status_5xx = status_5xx + VALUES(status_5xx),
				bot_requests = bot_requests + VALUES(bot_requests), not_found = not_found + VALUES(not_found),
				probes = probes + VALUES(probes), `+strings.Join(updates, ", "), args...); err != nil {
			return err
		}
	}

	if err := a.storePaths(ctx, tx, maxPathsPerDay); err != nil {
		return err
	}
	for key, c := range a.agents {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO traffic_agents (day, agent, kind, hits, bytes_sent) VALUES (?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE hits = hits + VALUES(hits), bytes_sent = bytes_sent + VALUES(bytes_sent), kind = VALUES(kind)`,
			key.day, key.agent, a.kinds[key.agent], c.hits, c.bytes); err != nil {
			return err
		}
	}
	for key, p := range a.probes {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO traffic_probes (day, ip_prefix, pattern, hits, last_path, last_seen) VALUES (?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE hits = hits + VALUES(hits), last_path = VALUES(last_path), last_seen = VALUES(last_seen)`,
			key.day, key.prefix, key.pattern, p.hits, p.lastPath, p.lastSeen); err != nil {
			return err
		}
	}
	return nil
}

// storePaths keeps the table bounded: a scanner that tries ten thousand addresses in a day adds
// to «other addresses», not ten thousand rows. Paths already in the table keep counting.
func (a *aggregate) storePaths(ctx context.Context, tx *sql.Tx, maxPerDay int) error {
	byDay := map[string][]pathKey{}
	for key := range a.paths {
		byDay[key.day] = append(byDay[key.day], key)
	}
	for day, keys := range byDay {
		var known int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM traffic_paths WHERE day = ?`, day).Scan(&known); err != nil {
			return err
		}
		// Successful pages first: if something has to be folded, let it be the noise.
		sort.Slice(keys, func(i, j int) bool {
			if (keys[i].status < 400) != (keys[j].status < 400) {
				return keys[i].status < 400
			}
			return a.paths[keys[i]].hits > a.paths[keys[j]].hits
		})
		for _, key := range keys {
			c := a.paths[key]
			path := key.path
			if known >= maxPerDay {
				var exists bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM traffic_paths WHERE day = ? AND path = ? AND status = ?)`,
					day, path, key.status).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					path = otherPaths
				}
			}
			result, err := tx.ExecContext(ctx, `
				INSERT INTO traffic_paths (day, path, status, hits, bytes_sent) VALUES (?, ?, ?, ?, ?)
				ON DUPLICATE KEY UPDATE hits = hits + VALUES(hits), bytes_sent = bytes_sent + VALUES(bytes_sent)`,
				day, path, key.status, c.hits, c.bytes)
			if err != nil {
				return err
			}
			if affected, _ := result.RowsAffected(); affected == 1 { // 1 = inserted, 2 = updated
				known++
			}
		}
	}
	return nil
}
