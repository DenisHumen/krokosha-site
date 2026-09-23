package netmap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// The map in MySQL (migrations/0012_netmap.sql). Save writes what a sync built — only what differs
// from what the tables hold — and Load gives the API the model of the newest successful sync.

// ErrNoMap is Load's answer before the first sync has finished.
var ErrNoMap = errors.New("netmap: the map has not been built yet")

// ErrShrank is Save refusing a map much smaller than the stored one: a source that came broken
// would otherwise make half of the internet «go away» for a night.
var ErrShrank = errors.New("netmap: the new map is much smaller than the stored one")

// KeepGone is how long a network or a link that went away, and a change, are remembered.
const KeepGone = 90 * 24 * time.Hour

// shrinkLimit is the part of the stored map a new one must keep, unless forced.
const shrinkLimit = 0.85

// batchRows is how many rows go into one statement.
const batchRows = 500

// SaveOptions are what a save needs besides the model.
type SaveOptions struct {
	Sync  int64     // the id of the netmap_sync row, for the list of changes
	Day   time.Time // the day of the map
	Force bool      // save a map even when it is much smaller than the stored one
}

// SaveStats is what a save found.
type SaveStats struct {
	Networks, Links, Prefixes, Exchanges int // in the new map
	Added, Gone, Changed                 int // links
	NetworksAdded, NetworksGone          int
	First                                bool // the tables were empty: nothing was listed as a change
}

// Save writes the model into the tables of the map.
func Save(ctx context.Context, db *sql.DB, m *Model, opt SaveOptions) (SaveStats, error) {
	day := opt.Day.UTC().Format(time.DateOnly)
	v4, v6 := m.Prefixes.Len()
	stats := SaveStats{Networks: len(m.Nodes), Links: m.Links(), Prefixes: v4 + v6, Exchanges: len(m.IXs)}

	var aliveLinks, aliveNetworks, prefixes int
	err := db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM netmap_link WHERE gone_at IS NULL),
		(SELECT COUNT(*) FROM netmap_as WHERE gone_at IS NULL),
		(SELECT COUNT(*) FROM netmap_prefix)`).Scan(&aliveLinks, &aliveNetworks, &prefixes)
	if err != nil {
		return stats, err
	}
	stats.First = aliveLinks == 0
	if !opt.Force && (shrunk(stats.Links, aliveLinks) || shrunk(stats.Networks, aliveNetworks) || shrunk(stats.Prefixes, prefixes)) {
		return stats, fmt.Errorf("%w: %d links instead of %d, %d networks instead of %d, %d prefixes instead of %d",
			ErrShrank, stats.Links, aliveLinks, stats.Networks, aliveNetworks, stats.Prefixes, prefixes)
	}

	changes := &journal{sync: opt.Sync, day: day, off: stats.First}
	networks, err := asTable.save(ctx, db, asRows(m), day)
	if err != nil {
		return stats, err
	}
	for _, row := range networks.added {
		changes.add("as_added", row.asn, 0, nil)
	}
	for _, key := range networks.gone {
		changes.add("as_gone", key, 0, nil)
	}
	stats.NetworksAdded, stats.NetworksGone = len(networks.added), len(networks.gone)

	links, err := linkTable.save(ctx, db, linkRows(m), day)
	if err != nil {
		return stats, err
	}
	for _, row := range links.added {
		changes.add("link_added", row.a, row.b, &row.rel)
	}
	for _, key := range links.gone {
		changes.add("link_gone", key[0], key[1], nil)
	}
	for _, row := range links.changed {
		if row.rel != links.before[[2]uint32{row.a, row.b}].row.rel { // not only where it was seen
			changes.add("link_kind", row.a, row.b, &row.rel)
			stats.Changed++
		}
	}
	stats.Added, stats.Gone = len(links.added), len(links.gone)

	if _, err := prefixTable.save(ctx, db, prefixRows(m), day); err != nil {
		return stats, err
	}
	if _, err := ixTable.save(ctx, db, ixRows(m), day); err != nil {
		return stats, err
	}
	if _, err := facilityTable.save(ctx, db, facilityRows(m), day); err != nil {
		return stats, err
	}
	if _, err := portTable.save(ctx, db, portRows(m), day); err != nil {
		return stats, err
	}
	if _, err := siteTable.save(ctx, db, siteRows(m), day); err != nil {
		return stats, err
	}
	if _, err := lanTable.save(ctx, db, lanRows(m), day); err != nil {
		return stats, err
	}
	return stats, changes.write(ctx, db)
}

func shrunk(now, before int) bool { return float64(now) < shrinkLimit*float64(before) }

// Forget deletes networks and links gone for longer than KeepGone, and changes as old.
func Forget(ctx context.Context, db *sql.DB, now time.Time) error {
	before := now.UTC().Add(-KeepGone).Format(time.DateOnly)
	for _, query := range []string{
		`DELETE FROM netmap_link WHERE gone_at < ? LIMIT 20000`,
		`DELETE FROM netmap_as WHERE gone_at < ? LIMIT 20000`,
		`DELETE FROM netmap_change WHERE day < ? LIMIT 20000`,
	} {
		for {
			result, err := db.ExecContext(ctx, query, before)
			if err != nil {
				return err
			}
			if n, _ := result.RowsAffected(); n < 20000 {
				break
			}
		}
	}
	return nil
}

// StartSync notes the start of a sync; its id marks the changes it finds.
func StartSync(ctx context.Context, db *sql.DB, now time.Time) (int64, error) {
	result, err := db.ExecContext(ctx, `INSERT INTO netmap_sync (started_at) VALUES (?)`, now.UTC())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// FinishSync notes how a sync ended. Only a sync without a failure is loaded by the API.
func FinishSync(ctx context.Context, db *sql.DB, id int64, now time.Time, stats SaveStats, overview, report string, failure error) error {
	var message any
	if failure != nil {
		message = cutTo(failure.Error(), 1000)
	}
	_, err := db.ExecContext(ctx, `UPDATE netmap_sync SET finished_at = ?, ok = ?, networks = ?, links = ?, prefixes = ?,
		exchanges = ?, added = ?, gone = ?, changed = ?, overview = ?, report = ?, error = ? WHERE id = ?`,
		now.UTC(), failure == nil, stats.Networks, stats.Links, stats.Prefixes, stats.Exchanges,
		stats.Added, stats.Gone, stats.Changed, cutTo(overview, 64), cutTo(report, 60000), message, id)
	return err
}

// SyncRun is one run of the sync, as netmap_sync keeps it.
type SyncRun struct {
	ID                                   int64
	Started, Finished                    time.Time // Finished is zero while it runs
	OK                                   bool
	Networks, Links, Prefixes, Exchanges int
	Added, Gone, Changed                 int
	Error                                string
}

// Runs are the newest run of the sync and the newest successful one; an ID of 0 — there is none.
func Runs(ctx context.Context, db *sql.DB) (last, lastOK SyncRun, err error) {
	read := func(where string) (SyncRun, error) {
		var run SyncRun
		var finished sql.NullTime
		var failure sql.NullString
		err := db.QueryRowContext(ctx, `SELECT id, started_at, finished_at, ok, networks, links, prefixes, exchanges,
			added, gone, changed, error FROM netmap_sync `+where+` ORDER BY id DESC LIMIT 1`).Scan(
			&run.ID, &run.Started, &finished, &run.OK, &run.Networks, &run.Links, &run.Prefixes, &run.Exchanges,
			&run.Added, &run.Gone, &run.Changed, &failure)
		if errors.Is(err, sql.ErrNoRows) {
			return SyncRun{}, nil
		}
		run.Finished, run.Error = finished.Time, failure.String
		return run, err
	}
	if last, err = read(""); err != nil {
		return last, lastOK, err
	}
	lastOK, err = read("WHERE ok")
	return last, lastOK, err
}

// LatestSync is the newest successful sync: its id and when it finished; 0 without one.
func LatestSync(ctx context.Context, db *sql.DB) (int64, time.Time, error) {
	var id int64
	var finished time.Time
	err := db.QueryRowContext(ctx, `SELECT id, finished_at FROM netmap_sync WHERE ok ORDER BY id DESC LIMIT 1`).Scan(&id, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, nil
	}
	return id, finished, err
}

// Load restores the model of the newest successful sync.
func Load(ctx context.Context, db *sql.DB) (*Model, int64, error) {
	id, built, err := LatestSync(ctx, db)
	if err != nil || id == 0 {
		return nil, 0, errors.Join(ErrNoMap, err)
	}
	var s stored
	if s.networks, err = asTable.alive(ctx, db); err != nil {
		return nil, 0, err
	}
	if s.links, err = linkTable.alive(ctx, db); err != nil {
		return nil, 0, err
	}
	if s.prefixes, err = loadPrefixes(ctx, db); err != nil {
		return nil, 0, err
	}
	if s.ixs, err = ixTable.alive(ctx, db); err != nil {
		return nil, 0, err
	}
	if s.facilities, err = facilityTable.alive(ctx, db); err != nil {
		return nil, 0, err
	}
	if s.ports, err = portTable.alive(ctx, db); err != nil {
		return nil, 0, err
	}
	if s.sites, err = siteTable.alive(ctx, db); err != nil {
		return nil, 0, err
	}
	if s.lans, err = lanTable.alive(ctx, db); err != nil {
		return nil, 0, err
	}
	return s.restore(built), id, nil
}

// --- the rows of the tables ---------------------------------------------------------------------

type asRow struct {
	asn           uint32
	name, country string
	kind          string
	lat, lon      float32
	placed        Placement
}

type linkRow struct {
	a, b    uint32
	rel     Rel
	sources uint8
}

type prefixRow struct {
	first, last string // the bytes of the addresses: 4 of IPv4, 16 of IPv6
	asn         uint32
}

// placeRow is an exchange point or a data centre.
type placeRow struct {
	id                  int
	name, city, country string
	lat, lon            float32
	placed              bool
}

type portRow struct {
	asn   uint32
	ix    int
	speed int64
	ports uint16
}

type siteRow struct {
	asn      uint32
	facility int
}

type lanRow struct {
	prefix string
	ix     int
}

func asRows(m *Model) []asRow {
	rows := make([]asRow, len(m.Nodes))
	for i := range m.Nodes {
		n := &m.Nodes[i]
		rows[i] = asRow{asn: n.ASN, name: cut(n.Name), country: country(n.Country), kind: cutTo(n.Type, 32), lat: n.Lat, lon: n.Lon, placed: n.Placed}
	}
	return rows
}

func linkRows(m *Model) []linkRow {
	rows := make([]linkRow, 0, m.Links())
	for u := int32(0); int(u) < len(m.Nodes); u++ {
		m.Neighbours(u, func(v int32, rel Rel, sources uint8) {
			if a, b := m.Nodes[u].ASN, m.Nodes[v].ASN; a < b {
				rows = append(rows, linkRow{a: a, b: b, rel: rel, sources: sources})
			}
		})
	}
	return rows
}

func prefixRows(m *Model) []prefixRow {
	v4, v6 := m.Prefixes.Len()
	rows := make([]prefixRow, 0, v4+v6)
	m.Prefixes.Each(func(r Range) {
		rows = append(rows, prefixRow{first: string(r.First.AsSlice()), last: string(r.Last.AsSlice()), asn: r.ASN})
	})
	return rows
}

func ixRows(m *Model) []placeRow {
	rows := make([]placeRow, len(m.IXs))
	for i, x := range m.IXs {
		rows[i] = placeRow{id: x.ID, name: cut(x.Name), city: cut(x.City), country: country(x.Country), lat: x.Lat, lon: x.Lon, placed: x.Placed}
	}
	return rows
}

func facilityRows(m *Model) []placeRow {
	rows := make([]placeRow, len(m.Facilities))
	for i, f := range m.Facilities {
		rows[i] = placeRow{id: f.ID, name: cut(f.Name), city: cut(f.City), country: country(f.Country), lat: f.Lat, lon: f.Lon, placed: f.Placed}
	}
	return rows
}

func portRows(m *Model) []portRow {
	var rows []portRow
	for i := range m.Nodes {
		for _, p := range m.ports[i] {
			rows = append(rows, portRow{asn: m.Nodes[i].ASN, ix: m.IXs[p.IX].ID, speed: p.Speed, ports: p.Count})
		}
	}
	return rows
}

func siteRows(m *Model) []siteRow {
	var rows []siteRow
	for i := range m.Nodes {
		for _, f := range m.sites[i] {
			rows = append(rows, siteRow{asn: m.Nodes[i].ASN, facility: m.Facilities[f].ID})
		}
	}
	return rows
}

func lanRows(m *Model) []lanRow {
	rows := make([]lanRow, len(m.lans))
	for i, l := range m.lans {
		rows[i] = lanRow{prefix: l.prefix.String(), ix: m.IXs[l.ix].ID}
	}
	return rows
}

// --- the tables ---------------------------------------------------------------------------------

// scanner reads one row of a query.
type scanner interface{ Scan(dest ...any) error }

// table is one table of the map: its columns (the key first), how its rows travel, and whether
// what goes away is only marked (with first_seen and gone_at) or deleted.
type table[K comparable, R comparable] struct {
	name    string
	columns []string
	keys    int
	history bool
	key     func(R) K
	keyArgs func(K) []any // the values of a key, column by column
	values  func(R) []any
	scan    func(scanner) (R, error)
}

var asTable = table[uint32, asRow]{
	name: "netmap_as", columns: []string{"asn", "name", "country", "type", "lat", "lon", "placed"}, keys: 1, history: true,
	key:     func(r asRow) uint32 { return r.asn },
	keyArgs: func(k uint32) []any { return []any{k} },
	values: func(r asRow) []any {
		return []any{r.asn, r.name, r.country, r.kind, float64(r.lat), float64(r.lon), uint8(r.placed)}
	},
	scan: func(s scanner) (asRow, error) {
		var r asRow
		var lat, lon float64
		err := s.Scan(&r.asn, &r.name, &r.country, &r.kind, &lat, &lon, &r.placed)
		r.lat, r.lon = float32(lat), float32(lon)
		return r, err
	},
}

var linkTable = table[[2]uint32, linkRow]{
	name: "netmap_link", columns: []string{"a", "b", "rel", "sources"}, keys: 2, history: true,
	key:     func(r linkRow) [2]uint32 { return [2]uint32{r.a, r.b} },
	keyArgs: func(k [2]uint32) []any { return []any{k[0], k[1]} },
	values:  func(r linkRow) []any { return []any{r.a, r.b, int8(r.rel), r.sources} },
	scan: func(s scanner) (linkRow, error) {
		var r linkRow
		return r, s.Scan(&r.a, &r.b, &r.rel, &r.sources)
	},
}

var prefixTable = table[string, prefixRow]{
	name: "netmap_prefix", columns: []string{"first", "last", "asn"}, keys: 1,
	key:     func(r prefixRow) string { return r.first },
	keyArgs: func(k string) []any { return []any{[]byte(k)} },
	values:  func(r prefixRow) []any { return []any{[]byte(r.first), []byte(r.last), r.asn} },
	scan: func(s scanner) (prefixRow, error) {
		var r prefixRow
		var first, last []byte
		err := s.Scan(&first, &last, &r.asn)
		r.first, r.last = string(first), string(last)
		return r, err
	},
}

func placeTable(name string) table[int, placeRow] {
	return table[int, placeRow]{
		name: name, columns: []string{"id", "name", "city", "country", "lat", "lon", "placed"}, keys: 1,
		key:     func(r placeRow) int { return r.id },
		keyArgs: func(k int) []any { return []any{k} },
		values: func(r placeRow) []any {
			return []any{r.id, r.name, r.city, r.country, float64(r.lat), float64(r.lon), r.placed}
		},
		scan: func(s scanner) (placeRow, error) {
			var r placeRow
			var lat, lon float64
			err := s.Scan(&r.id, &r.name, &r.city, &r.country, &lat, &lon, &r.placed)
			r.lat, r.lon = float32(lat), float32(lon)
			return r, err
		},
	}
}

var (
	ixTable       = placeTable("netmap_ix")
	facilityTable = placeTable("netmap_facility")
)

var portTable = table[[2]int64, portRow]{
	name: "netmap_port", columns: []string{"asn", "ix", "speed", "ports"}, keys: 2,
	key:     func(r portRow) [2]int64 { return [2]int64{int64(r.asn), int64(r.ix)} },
	keyArgs: func(k [2]int64) []any { return []any{k[0], k[1]} },
	values:  func(r portRow) []any { return []any{r.asn, r.ix, r.speed, r.ports} },
	scan: func(s scanner) (portRow, error) {
		var r portRow
		return r, s.Scan(&r.asn, &r.ix, &r.speed, &r.ports)
	},
}

var siteTable = table[siteRow, siteRow]{
	name: "netmap_site", columns: []string{"asn", "facility"}, keys: 2,
	key:     func(r siteRow) siteRow { return r },
	keyArgs: func(k siteRow) []any { return []any{k.asn, k.facility} },
	values:  func(r siteRow) []any { return []any{r.asn, r.facility} },
	scan: func(s scanner) (siteRow, error) {
		var r siteRow
		return r, s.Scan(&r.asn, &r.facility)
	},
}

var lanTable = table[string, lanRow]{
	name: "netmap_lan", columns: []string{"prefix", "ix"}, keys: 1,
	key:     func(r lanRow) string { return r.prefix },
	keyArgs: func(k string) []any { return []any{k} },
	values:  func(r lanRow) []any { return []any{r.prefix, r.ix} },
	scan: func(s scanner) (lanRow, error) {
		var r lanRow
		return r, s.Scan(&r.prefix, &r.ix)
	},
}

// saved is what a save of one table did: the rows that appeared (or came back), the keys that
// went away, the rows that changed, and the rows as they were before.
type saved[K comparable, R comparable] struct {
	added, changed []R
	gone           []K
	before         map[K]storedRow[R]
}

// storedRow is a row the table held before a save.
type storedRow[R comparable] struct {
	row   R
	gone  bool // marked gone (a table with history)
	still bool // the new map has it too
}

// save makes the table hold wanted: new and changed rows are written, rows that went away are
// deleted, or only marked gone in a table with history.
func (t table[K, R]) save(ctx context.Context, db *sql.DB, wanted []R, day string) (saved[K, R], error) {
	out := saved[K, R]{before: map[K]storedRow[R]{}}
	query := "SELECT " + strings.Join(t.columns, ", ")
	if t.history {
		query += ", gone_at IS NOT NULL"
	}
	rows, err := db.QueryContext(ctx, query+" FROM "+t.name)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var entry storedRow[R]
		if t.history {
			entry.row, err = t.scan(withGone{rows, &entry.gone})
		} else {
			entry.row, err = t.scan(rows)
		}
		if err != nil {
			_ = rows.Close()
			return out, fmt.Errorf("%s: %w", t.name, err)
		}
		out.before[t.key(entry.row)] = entry
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, fmt.Errorf("%s: %w", t.name, err)
	}

	var write []R
	fresh := map[K]bool{} // new keys already taken
	for _, row := range wanted {
		key := t.key(row)
		entry, known := out.before[key]
		if (known && entry.still) || fresh[key] {
			continue // the same key twice: the first one counts
		}
		switch {
		case !known:
			fresh[key] = true
			fallthrough
		case entry.gone:
			out.added = append(out.added, row)
			write = append(write, row)
		case entry.row != row:
			out.changed = append(out.changed, row)
			write = append(write, row)
		}
		if known {
			entry.still = true
			out.before[key] = entry
		}
	}
	for key, entry := range out.before {
		if !entry.still && !entry.gone {
			out.gone = append(out.gone, key)
		}
	}
	if err := t.write(ctx, db, write, day); err != nil {
		return out, err
	}
	return out, t.remove(ctx, db, out.gone, day)
}

// withGone reads the extra «gone_at IS NOT NULL» column of a table with history.
type withGone struct {
	rows *sql.Rows
	gone *bool
}

func (w withGone) Scan(dest ...any) error { return w.rows.Scan(append(dest, w.gone)...) }

// write inserts rows, or updates the ones that are there; a row of a table with history that
// comes back is no longer gone, and keeps the day it was first seen.
func (t table[K, R]) write(ctx context.Context, db *sql.DB, rows []R, day string) error {
	columns := t.columns
	one := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(columns)), ", ")
	if t.history {
		columns = append(append([]string(nil), columns...), "first_seen", "gone_at")
		one += ", ?, NULL"
	}
	one += ")"
	updates := make([]string, 0, len(t.columns))
	for _, column := range t.columns[t.keys:] {
		updates = append(updates, column+" = VALUES("+column+")")
	}
	if t.history {
		updates = append(updates, "gone_at = NULL")
	}
	if len(updates) == 0 { // every column is the key: an existing row is already right
		updates = append(updates, t.columns[0]+" = "+t.columns[0])
	}
	suffix := " ON DUPLICATE KEY UPDATE " + strings.Join(updates, ", ")
	for start := 0; start < len(rows); start += batchRows {
		batch := rows[start:min(start+batchRows, len(rows))]
		args := make([]any, 0, len(batch)*(len(t.columns)+1))
		for _, row := range batch {
			args = append(args, t.values(row)...)
			if t.history {
				args = append(args, day)
			}
		}
		statement := "INSERT INTO " + t.name + " (" + strings.Join(columns, ", ") + ") VALUES " + //nolint:gosec // names of the tables above, not input
			strings.TrimSuffix(strings.Repeat(one+", ", len(batch)), ", ") + suffix
		if _, err := db.ExecContext(ctx, statement, args...); err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
	}
	return nil
}

// remove deletes rows by key, or marks them gone in a table with history.
func (t table[K, R]) remove(ctx context.Context, db *sql.DB, keys []K, day string) error {
	keyColumns := t.columns[:t.keys]
	one := "(" + strings.TrimSuffix(strings.Repeat("?, ", t.keys), ", ") + ")"
	for start := 0; start < len(keys); start += batchRows {
		batch := keys[start:min(start+batchRows, len(keys))]
		args := make([]any, 0, len(batch)*t.keys+1)
		if t.history {
			args = append(args, day)
		}
		for _, key := range batch {
			args = append(args, t.keyArgs(key)...)
		}
		where := "(" + strings.Join(keyColumns, ", ") + ") IN (" + strings.TrimSuffix(strings.Repeat(one+", ", len(batch)), ", ") + ")"
		statement := "DELETE FROM " + t.name + " WHERE " + where //nolint:gosec // names of the tables above, not input
		if t.history {
			statement = "UPDATE " + t.name + " SET gone_at = ? WHERE gone_at IS NULL AND " + where
		}
		if _, err := db.ExecContext(ctx, statement, args...); err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
	}
	return nil
}

// alive reads the rows of the map as it is now.
func (t table[K, R]) alive(ctx context.Context, db *sql.DB) ([]R, error) {
	query := "SELECT " + strings.Join(t.columns, ", ") + " FROM " + t.name //nolint:gosec // names of the tables above, not input
	if t.history {
		query += " WHERE gone_at IS NULL"
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []R
	for rows.Next() {
		row, err := t.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.name, err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// loadPrefixes reads the addresses straight into their compact form: half a million rows would
// otherwise pass through the memory as strings first.
func loadPrefixes(ctx context.Context, db *sql.DB) (*Prefixes, error) {
	rows, err := db.QueryContext(ctx, `SELECT first, last, asn FROM netmap_prefix`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prefixes := &Prefixes{}
	for rows.Next() {
		var first, last sql.RawBytes
		var asn uint32
		if err := rows.Scan(&first, &last, &asn); err != nil {
			return nil, fmt.Errorf("netmap_prefix: %w", err)
		}
		a, okA := netip.AddrFromSlice(first)
		b, okB := netip.AddrFromSlice(last)
		if okA && okB && a.Is4() == b.Is4() {
			prefixes.add(a, b, asn)
		}
	}
	prefixes.sort()
	return prefixes, rows.Err()
}

// journal gathers the changes of a save for netmap_change.
type journal struct {
	sync int64
	day  string
	off  bool // the first save: everything is new, nothing is a change
	args []any
	rows int
}

func (j *journal) add(kind string, a, b uint32, rel *Rel) {
	if j.off {
		return
	}
	var r any
	if rel != nil {
		r = int8(*rel)
	}
	j.args = append(j.args, j.sync, j.day, kind, a, b, r)
	j.rows++
}

func (j *journal) write(ctx context.Context, db *sql.DB) error {
	const columns = 6
	for start := 0; start < j.rows; start += batchRows {
		n := min(batchRows, j.rows-start)
		statement := "INSERT INTO netmap_change (sync_id, day, kind, a, b, rel) VALUES " + //nolint:gosec // placeholders only
			strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?), ", n), ", ")
		if _, err := db.ExecContext(ctx, statement, j.args[start*columns:(start+n)*columns]...); err != nil {
			return fmt.Errorf("netmap_change: %w", err)
		}
	}
	return nil
}

// --- the model again ----------------------------------------------------------------------------

// stored is the map as the tables hold it.
type stored struct {
	networks   []asRow
	links      []linkRow
	prefixes   *Prefixes
	ixs        []placeRow
	facilities []placeRow
	ports      []portRow
	sites      []siteRow
	lans       []lanRow
}

// restore makes the model again: the same one Build made, as far as the tables keep it.
func (s *stored) restore(built time.Time) *Model {
	m := &Model{Built: built.UTC(), Prefixes: s.prefixes}
	if m.Prefixes == nil {
		m.Prefixes = &Prefixes{}
	}

	// Networks, and a bare one for the end of a link whose own row is missing.
	known := make(map[uint32]bool, len(s.networks))
	for _, row := range s.networks {
		known[row.asn] = true
	}
	for _, link := range s.links {
		for _, asn := range []uint32{link.a, link.b} {
			if !known[asn] {
				known[asn] = true
				s.networks = append(s.networks, asRow{asn: asn})
			}
		}
	}
	sort.Slice(s.networks, func(i, j int) bool { return s.networks[i].asn < s.networks[j].asn })
	m.Nodes = make([]Node, len(s.networks))
	m.index = make(map[uint32]int32, len(s.networks))
	for i, row := range s.networks {
		m.Nodes[i] = Node{ASN: row.asn, Name: row.name, Country: row.country, Type: row.kind, Lat: row.lat, Lon: row.lon, Placed: row.placed}
		m.index[row.asn] = int32(i)
	}
	links := make([]Link, len(s.links))
	for i, row := range s.links {
		links[i] = Link{A: row.a, B: row.b, Rel: row.rel, Sources: row.sources}
	}
	sort.Slice(links, func(i, j int) bool { return links[i].Key() < links[j].Key() })
	m.link(links)

	sort.Slice(s.facilities, func(i, j int) bool { return s.facilities[i].id < s.facilities[j].id })
	facIndex := make(map[int]int32, len(s.facilities))
	for i, row := range s.facilities {
		facIndex[row.id] = int32(i)
		m.Facilities = append(m.Facilities, Facility{ID: row.id, Name: row.name, City: row.city, Country: row.country, Lat: row.lat, Lon: row.lon, Placed: row.placed})
	}
	sort.Slice(s.ixs, func(i, j int) bool { return s.ixs[i].id < s.ixs[j].id })
	ixIndex := make(map[int]int32, len(s.ixs))
	for i, row := range s.ixs {
		ixIndex[row.id] = int32(i)
		m.IXs = append(m.IXs, IX{ID: row.id, Name: row.name, City: row.city, Country: row.country, Lat: row.lat, Lon: row.lon, Placed: row.placed})
	}

	m.ports = make([][]Port, len(m.Nodes))
	for _, row := range s.ports {
		node, okN := m.index[row.asn]
		x, okX := ixIndex[row.ix]
		if !okN || !okX {
			continue
		}
		m.ports[node] = append(m.ports[node], Port{IX: x, Speed: row.speed, Count: row.ports})
		m.IXs[x].Capacity += row.speed
		m.IXs[x].Members++
	}
	for i := range m.ports {
		sort.Slice(m.ports[i], func(a, b int) bool { return m.ports[i][a].IX < m.ports[i][b].IX })
	}
	m.sites = make([][]int32, len(m.Nodes))
	for _, row := range s.sites {
		node, okN := m.index[row.asn]
		f, okF := facIndex[row.facility]
		if okN && okF {
			m.sites[node] = append(m.sites[node], f)
		}
	}
	for i := range m.sites {
		sort.Slice(m.sites[i], func(a, b int) bool { return m.sites[i][a] < m.sites[i][b] })
	}
	for _, row := range s.lans {
		prefix, err := netip.ParsePrefix(row.prefix)
		if x, ok := ixIndex[row.ix]; ok && err == nil {
			m.lans = append(m.lans, lan{prefix: prefix, ix: x})
		}
	}
	sort.Slice(m.lans, func(i, j int) bool { return m.lans[i].prefix.String() < m.lans[j].prefix.String() })
	return m
}
