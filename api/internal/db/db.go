// Package db opens the MySQL database — the source of truth of the service
// (docs/architecture.md §6) — and brings its schema up to date.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

// The pool is small on purpose: the database is tuned for a 2 GB server (max_connections = 40)
// and shares it with everything else.
const (
	maxOpenConns    = 12
	maxIdleConns    = 4
	connMaxLifetime = 30 * time.Minute
)

func driverConfig(cfg config.MySQL) *mysql.Config {
	driver := mysql.NewConfig()
	driver.Net = "tcp"
	driver.Addr = cfg.Addr
	driver.DBName = cfg.Database
	driver.User = cfg.User
	driver.Passwd = cfg.Password
	driver.ParseTime = true
	driver.Loc = time.UTC
	// utf8mb4_unicode_ci exists in MySQL and in MariaDB alike (docs/architecture.md §7).
	driver.Collation = "utf8mb4_unicode_ci"
	driver.Timeout = 5 * time.Second
	driver.ReadTimeout = 30 * time.Second
	driver.WriteTimeout = 30 * time.Second
	driver.Params = map[string]string{"time_zone": "'+00:00'"}
	return driver
}

// Open connects to MySQL, waiting up to wait for it to accept connections: after a reboot the
// service may start before the database container is ready.
func Open(ctx context.Context, cfg config.MySQL, wait time.Duration, log *slog.Logger) (*sql.DB, error) {
	connector, err := mysql.NewConnector(driverConfig(cfg))
	if err != nil {
		return nil, err
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(maxOpenConns)
	pool.SetMaxIdleConns(maxIdleConns)
	pool.SetConnMaxLifetime(connMaxLifetime)

	deadline := time.Now().Add(wait)
	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = pool.PingContext(pingCtx)
		cancel()
		if err == nil {
			return pool, nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			_ = pool.Close()
			return nil, fmt.Errorf("MySQL at %s is not reachable: %w", cfg.Addr, err)
		}
		if attempt == 1 {
			log.Info("waiting for MySQL", "addr", cfg.Addr)
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}
