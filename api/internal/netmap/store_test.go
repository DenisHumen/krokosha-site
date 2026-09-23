package netmap

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := testenv.MySQL(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	pool, err := db.Open(ctx, cfg, 20*time.Second, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if _, err := db.Migrate(ctx, cfg, migrations.Files, log); err != nil {
		t.Fatal(err)
	}
	return pool
}

// saveDay saves a model the way the sync does: a row in netmap_sync, the save, the row finished.
func saveDay(t *testing.T, pool *sql.DB, m *Model, day time.Time, force bool) (SaveStats, int64, error) {
	t.Helper()
	ctx := context.Background()
	id, err := StartSync(ctx, pool, day)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := Save(ctx, pool, m, SaveOptions{Sync: id, Day: day, Force: force})
	if finishErr := FinishSync(ctx, pool, id, day.Add(time.Minute), stats, "overview-test.bin", "report", err); finishErr != nil {
		t.Fatal(finishErr)
	}
	return stats, id, err
}

func overviewOf(t *testing.T, m *Model) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := m.WriteOverview(&buf, DefaultOverview); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var (
	day1 = time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC)
	day2 = day1.AddDate(0, 0, 1)
	day3 = day1.AddDate(0, 0, 2)
)

func TestStoreGivesTheSameModelBack(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	if _, _, err := Load(ctx, pool); !errors.Is(err, ErrNoMap) {
		t.Fatalf("before the first sync: %v", err)
	}
	built := testWorld(t)
	stats, id, err := saveDay(t, pool, built, day1, false)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.First || stats.Networks != 13 || stats.Links != 15 || stats.Prefixes != 4 || stats.Exchanges != 2 {
		t.Errorf("stats: %+v", stats)
	}
	m, loaded, err := Load(ctx, pool)
	if err != nil || loaded != id {
		t.Fatalf("load: sync %d, %v", loaded, err)
	}
	if !m.Built.Equal(day1.Add(time.Minute)) {
		t.Errorf("built %v: when the sync finished", m.Built)
	}
	if !reflect.DeepEqual(m.Nodes, built.Nodes) {
		t.Errorf("nodes:\n%+v\n%+v", m.Nodes, built.Nodes)
	}
	for _, field := range []struct {
		name       string
		got, wants any
	}{
		{"index", m.index, built.index}, {"start", m.start, built.start}, {"adj", m.adj, built.adj},
		{"rel", m.rel, built.rel}, {"src", m.src, built.src}, {"IXs", m.IXs, built.IXs},
		{"facilities", m.Facilities, built.Facilities}, {"ports", m.ports, built.ports},
		{"sites", m.sites, built.sites}, {"lans", m.lans, built.lans}, {"prefixes", m.Prefixes, built.Prefixes},
	} {
		if !reflect.DeepEqual(field.got, field.wants) {
			t.Errorf("%s:\n%+v\n%+v", field.name, field.got, field.wants)
		}
	}
	m.Built = built.Built
	if !bytes.Equal(overviewOf(t, m), overviewOf(t, built)) {
		t.Error("the overview of the stored map differs from the built one")
	}
	// Routes are drawn the same on both.
	for _, pair := range [][2]string{{"192.0.2.10", "198.51.100.20"}, {"192.0.2.10", "1.1.1.1"}, {"203.0.113.5", "192.0.2.1"}} {
		a, errA := built.Route(netip.MustParseAddr(pair[0]), netip.MustParseAddr(pair[1]), nil)
		b, errB := m.Route(netip.MustParseAddr(pair[0]), netip.MustParseAddr(pair[1]), nil)
		if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(errA, errB) {
			t.Errorf("%s → %s:\n%+v %v\n%+v %v", pair[0], pair[1], a, errA, b, errB)
		}
	}
}

func TestStoreWritesOnlyChanges(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	if _, _, err := saveDay(t, pool, testWorld(t), day1, false); err != nil {
		t.Fatal(err)
	}

	// The same map again: nothing changes, nothing is listed.
	stats, _, err := saveDay(t, pool, testWorld(t), day2, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.First || stats.Added+stats.Gone+stats.Changed+stats.NetworksAdded+stats.NetworksGone != 0 {
		t.Errorf("an unchanged map: %+v", stats)
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM netmap_change`); n != 0 {
		t.Errorf("%d changes listed for an unchanged map", n)
	}

	// Day 3: 8000 is gone with its links and addresses, 6000 peers with 7000, and the leaked peering
	// 9100 ━ 9200 becomes what CAIDA says is a customer link.
	sources := worldSources()
	var links []Link
	for _, link := range sources.Links {
		switch {
		case link.A == 8000 || link.B == 8000:
			continue
		case link.A == 9100 && link.B == 9200:
			link.Rel = RelProvider
		}
		links = append(links, link)
	}
	links = append(links, NewLink(6000, 7000, RelPeer, SourceMLP))
	sources.Links = links
	prefixes := &Prefixes{}
	sources.Prefixes.Each(func(r Range) {
		if r.ASN != 8000 {
			prefixes.add(r.First, r.Last, r.ASN)
		}
	})
	prefixes.sort()
	sources.Prefixes = prefixes
	stats, id, err := saveDay(t, pool, Build(sources), day3, true) // one network of thirteen is more than the limit allows
	if err != nil {
		t.Fatal(err)
	}
	if stats.Added != 1 || stats.Gone != 2 || stats.Changed != 1 || stats.NetworksGone != 1 || stats.NetworksAdded != 0 {
		t.Errorf("day 3: %+v", stats)
	}
	got := changesOf(t, pool, id)
	want := []string{"as_gone 8000-0", "link_added 6000-7000 0", "link_gone 1000-8000", "link_gone 4000-8000", "link_kind 9100-9200 -1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changes:\n%q\n%q", got, want)
	}
	// What went away stays, marked; what is new is dated.
	if gone := text(t, pool, `SELECT DATE_FORMAT(gone_at, '%Y-%m-%d') FROM netmap_link WHERE a = 1000 AND b = 8000`); gone != "2026-09-25" {
		t.Errorf("the link that went away: gone_at %q", gone)
	}
	if seen := text(t, pool, `SELECT DATE_FORMAT(first_seen, '%Y-%m-%d') FROM netmap_link WHERE a = 6000 AND b = 7000`); seen != "2026-09-25" {
		t.Errorf("the new link: first_seen %q", seen)
	}
	m, _, err := Load(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Node(8000); ok || m.Links() != 14 {
		t.Errorf("the loaded map still has 8000, or %d links", m.Links())
	}
	if _, ok := m.Prefixes.Lookup(netip.MustParseAddr("203.0.113.5")); ok {
		t.Error("the addresses of 8000 are still announced")
	}

	// Day 4: the world as it was. 8000 comes back — with the day it was first seen.
	stats, id, err = saveDay(t, pool, testWorld(t), day3.AddDate(0, 0, 1), false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Added != 2 || stats.Gone != 1 || stats.NetworksAdded != 1 {
		t.Errorf("day 4: %+v", stats)
	}
	if row := text(t, pool, `SELECT CONCAT(DATE_FORMAT(first_seen, '%Y-%m-%d'), ' ', COALESCE(gone_at, 'here')) FROM netmap_link WHERE a = 1000 AND b = 8000`); row != "2026-09-23 here" {
		t.Errorf("the link that came back: %q", row)
	}
	if got := changesOf(t, pool, id); len(got) != 5 {
		t.Errorf("day 4 changes: %q", got)
	}
}

func TestStoreRefusesAMapMuchSmaller(t *testing.T) {
	pool := testDB(t)
	if _, _, err := saveDay(t, pool, testWorld(t), day1, false); err != nil {
		t.Fatal(err)
	}
	sources := worldSources()
	sources.Links = sources.Links[:4]
	small := Build(sources)
	if _, _, err := saveDay(t, pool, small, day2, false); !errors.Is(err, ErrShrank) {
		t.Fatalf("a map that lost most links was saved: %v", err)
	}
	// The failed sync is noted, and the map stays what it was.
	if n := count(t, pool, `SELECT COUNT(*) FROM netmap_sync WHERE NOT ok AND error LIKE '%much smaller%'`); n != 1 {
		t.Errorf("%d failed syncs noted", n)
	}
	m, _, err := Load(context.Background(), pool)
	if err != nil || m.Links() != 15 {
		t.Fatalf("after a refused save: %v", err)
	}
	if _, _, err := saveDay(t, pool, small, day2, true); err != nil {
		t.Errorf("forced: %v", err)
	}
}

func TestStoreForgetsWhatWentAwayLongAgo(t *testing.T) {
	pool := testDB(t)
	if _, _, err := saveDay(t, pool, testWorld(t), day1, false); err != nil {
		t.Fatal(err)
	}
	sources := worldSources()
	sources.Links = sources.Links[:len(sources.Links)-1] // 8000 ━ 4000, seen in a table only, goes
	if _, _, err := saveDay(t, pool, Build(sources), day2, false); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := Forget(ctx, pool, day2.Add(KeepGone)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM netmap_link WHERE gone_at IS NOT NULL`); n != 1 {
		t.Errorf("forgotten too early: %d gone links left", n)
	}
	if err := Forget(ctx, pool, day2.Add(KeepGone+24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM netmap_link WHERE gone_at IS NOT NULL`) + count(t, pool, `SELECT COUNT(*) FROM netmap_change`); n != 0 {
		t.Errorf("%d rows of the past left", n)
	}
	if n := count(t, pool, `SELECT COUNT(*) FROM netmap_link`); n != 14 {
		t.Errorf("%d links left", n)
	}
}

func count(t *testing.T, pool *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func text(t *testing.T, pool *sql.DB, query string) string {
	t.Helper()
	var s sql.NullString
	if err := pool.QueryRow(query).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s.String
}

// changesOf lists what a sync noted: «kind a-b rel», in order.
func changesOf(t *testing.T, pool *sql.DB, sync int64) []string {
	t.Helper()
	rows, err := pool.Query(`SELECT CONCAT(kind, ' ', a, '-', b, COALESCE(CONCAT(' ', rel), '')) FROM netmap_change WHERE sync_id = ? ORDER BY kind, a, b`, sync)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestWatchLoadsEveryNewMap(t *testing.T) {
	pool := testDB(t)
	service := NewService(ServiceOptions{ClientIP: func(context.Context) net.IP { return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Watch(ctx, pool, service, 20*time.Millisecond, slog.New(slog.DiscardHandler))
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	waitFor := func(what string, ok func(m *Model) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if m := service.Model(); m != nil && ok(m) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("the service never got %s", what)
	}
	time.Sleep(50 * time.Millisecond)
	if service.Model() != nil {
		t.Fatal("a map before any sync")
	}
	if _, _, err := saveDay(t, pool, testWorld(t), day1, false); err != nil {
		t.Fatal(err)
	}
	waitFor("the first map", func(m *Model) bool { return m.Links() == 15 })
	sources := worldSources()
	sources.Links = append(sources.Links, NewLink(6000, 7000, RelPeer, SourceMLP))
	if _, _, err := saveDay(t, pool, Build(sources), day2, false); err != nil {
		t.Fatal(err)
	}
	waitFor("the next map", func(m *Model) bool { return m.Links() == 16 && m.Built.Equal(day2.Add(time.Minute)) })
}
