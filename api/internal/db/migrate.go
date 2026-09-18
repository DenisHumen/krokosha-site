package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

// Migrations are plain SQL files named NNNN_description.sql, applied in order, each exactly once.
//
// MySQL commits implicitly on every DDL statement, so a migration cannot be rolled back as a
// whole. Two rules keep that safe: a migration is recorded only after it fully succeeded, and
// migrations are written to be re-runnable (CREATE TABLE IF NOT EXISTS and the like).

var reMigrationName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(files fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	var migrations []migration
	seen := map[int]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := reMigrationName.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("migration %q: expected NNNN_description.sql", entry.Name())
		}
		version, _ := strconv.Atoi(match[1])
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %q and %q share version %d", other, entry.Name(), version)
		}
		seen[version] = entry.Name()
		body, err := fs.ReadFile(files, entry.Name())
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration{version: version, name: match[2], sql: string(body)})
	}
	sort.Slice(migrations, func(a, b int) bool { return migrations[a].version < migrations[b].version })
	return migrations, nil
}

// Migrate applies the migrations that have not been applied yet and returns how many it ran.
// It uses its own short-lived connection that allows several statements per file; the pool the
// service works with never does.
func Migrate(ctx context.Context, cfg config.MySQL, files fs.FS, log *slog.Logger) (int, error) {
	migrations, err := loadMigrations(files)
	if err != nil {
		return 0, err
	}

	driver := driverConfig(cfg)
	driver.MultiStatements = true
	connector, err := mysql.NewConnector(driver)
	if err != nil {
		return 0, err
	}
	pool := sql.OpenDB(connector)
	defer pool.Close()
	// One connection: the advisory lock below belongs to a session.
	pool.SetMaxOpenConns(1)
	conn, err := pool.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	// Two instances starting at once (an update racing a restart) must not migrate in parallel.
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('krokosha_migrate', 60)`).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked.Valid || locked.Int64 != 1 {
		return 0, errors.New("another process is migrating the database")
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT RELEASE_LOCK('krokosha_migrate')`)
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INT          NOT NULL PRIMARY KEY,
		name       VARCHAR(190) NOT NULL,
		applied_at DATETIME(3)  NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`); err != nil {
		return 0, fmt.Errorf("schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := conn.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			_ = rows.Close()
			return 0, err
		}
		applied[version] = true
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	ran := 0
	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		log.Info("applying migration", "version", m.version, "name", m.name)
		if _, err := conn.ExecContext(ctx, m.sql); err != nil {
			return ran, fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err)
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, UTC_TIMESTAMP(3))`,
			m.version, m.name); err != nil {
			return ran, fmt.Errorf("migration %04d_%s: recording: %w", m.version, m.name, err)
		}
		ran++
	}
	return ran, nil
}
