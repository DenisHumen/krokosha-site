package db

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

var quiet = slog.New(slog.DiscardHandler)

func TestLoadMigrationsOrdersAndValidates(t *testing.T) {
	files := fstest.MapFS{
		"0002_second.sql": {Data: []byte("SELECT 2")},
		"0001_first.sql":  {Data: []byte("SELECT 1")},
		"embed.go":        {Data: []byte("package migrations")},
	}
	got, err := loadMigrations(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].version != 1 || got[0].name != "first" || got[1].version != 2 {
		t.Errorf("got %+v", got)
	}

	for name, files := range map[string]fstest.MapFS{
		"bad name":          {"1_first.sql": {Data: []byte("SELECT 1")}},
		"upper case":        {"0001_First.sql": {Data: []byte("SELECT 1")}},
		"duplicate version": {"0001_a.sql": {Data: []byte("SELECT 1")}, "0001_b.sql": {Data: []byte("SELECT 1")}},
	} {
		if _, err := loadMigrations(files); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestEmbeddedMigrationsAreWellFormed(t *testing.T) {
	got, err := loadMigrations(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no migrations embedded")
	}
	for i, m := range got {
		if m.version != i+1 {
			t.Errorf("migration #%d has version %d: versions must be consecutive from 0001", i+1, m.version)
		}
		if strings.TrimSpace(m.sql) == "" {
			t.Errorf("migration %04d_%s is empty", m.version, m.name)
		}
	}
}

func TestMigrateAppliesEachMigrationOnce(t *testing.T) {
	cfg := testenv.MySQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	files := fstest.MapFS{
		"0001_first.sql": {Data: []byte(`
			CREATE TABLE IF NOT EXISTS one (id INT NOT NULL PRIMARY KEY);
			INSERT INTO one (id) VALUES (1);`)},
	}
	if n, err := Migrate(ctx, cfg, files, quiet); err != nil || n != 1 {
		t.Fatalf("first run: n=%d err=%v", n, err)
	}
	if n, err := Migrate(ctx, cfg, files, quiet); err != nil || n != 0 {
		t.Fatalf("second run must do nothing: n=%d err=%v", n, err)
	}

	files["0002_second.sql"] = &fstest.MapFile{Data: []byte(`ALTER TABLE one ADD COLUMN note VARCHAR(20) NULL;`)}
	if n, err := Migrate(ctx, cfg, files, quiet); err != nil || n != 1 {
		t.Fatalf("new migration: n=%d err=%v", n, err)
	}

	pool, err := Open(ctx, cfg, 10*time.Second, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var rows, versions int
	if err := pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM one`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("rows in `one` = %d (err %v), want 1: the first migration ran more than once", rows, err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil || versions != 2 {
		t.Errorf("recorded versions = %d (err %v), want 2", versions, err)
	}
}

func TestMigrateStopsAtAFailureAndDoesNotRecordIt(t *testing.T) {
	cfg := testenv.MySQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	files := fstest.MapFS{
		"0001_ok.sql":     {Data: []byte(`CREATE TABLE IF NOT EXISTS ok (id INT NOT NULL PRIMARY KEY);`)},
		"0002_broken.sql": {Data: []byte(`CREATE TABLE oops (id INT NOT NULL PRIMARY KEY); THIS IS NOT SQL;`)},
		"0003_never.sql":  {Data: []byte(`CREATE TABLE never (id INT NOT NULL PRIMARY KEY);`)},
	}
	n, err := Migrate(ctx, cfg, files, quiet)
	if err == nil || n != 1 || !strings.Contains(err.Error(), "0002_broken") {
		t.Fatalf("n=%d err=%v, want 1 applied and an error naming 0002_broken", n, err)
	}

	// Fixed and re-run: it continues from the failed one.
	files["0002_broken.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE IF NOT EXISTS oops (id INT NOT NULL PRIMARY KEY);`)}
	if n, err := Migrate(ctx, cfg, files, quiet); err != nil || n != 2 {
		t.Fatalf("after the fix: n=%d err=%v, want 2", n, err)
	}
}

func TestMigrateIsSafeToRunConcurrently(t *testing.T) {
	cfg := testenv.MySQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	files := fstest.MapFS{
		"0001_counter.sql": {Data: []byte(`
			CREATE TABLE IF NOT EXISTS counter (id INT NOT NULL AUTO_INCREMENT PRIMARY KEY);
			INSERT INTO counter () VALUES ();`)},
	}
	var wg sync.WaitGroup
	total := make(chan int, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := Migrate(ctx, cfg, files, quiet)
			if err != nil {
				t.Errorf("Migrate: %v", err)
			}
			total <- n
		}()
	}
	wg.Wait()
	close(total)
	sum := 0
	for n := range total {
		sum += n
	}
	if sum != 1 {
		t.Errorf("the migration was applied %d times by 4 concurrent runs, want exactly 1", sum)
	}
}

func TestOpenGivesUpWhenTheDatabaseIsAbsent(t *testing.T) {
	cfg := testenv.MySQL(t)
	cfg.Addr = "127.0.0.1:1" // nothing listens there
	started := time.Now()
	_, err := Open(context.Background(), cfg, 3*time.Second, quiet)
	if err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(started); elapsed < 2*time.Second || elapsed > 20*time.Second {
		t.Errorf("gave up after %v, want roughly the 3 s it was told to wait", elapsed)
	}
}
