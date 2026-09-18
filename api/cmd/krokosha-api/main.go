// Command krokosha-api is the backend of the site: one binary under systemd, listening on
// loopback behind nginx (brief B2). Configuration comes from the environment, see
// deploy/env/.env.example.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "krokosha-api:", err)
		os.Exit(1)
	}
}

func run() error {
	env, err := config.LoadEnvFromOS()
	if err != nil {
		return err
	}
	log := newLogger(env.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// After a reboot the database container may need a moment; systemd restarts us if it takes longer.
	pool, err := db.Open(ctx, env.MySQL, 90*time.Second, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	applied, err := db.Migrate(ctx, env.MySQL, migrations.Files, log)
	if err != nil {
		return err
	}
	if applied > 0 {
		log.Info("database schema updated", "migrations", applied)
	}

	store, err := cache.New(ctx, env.RedisURL, log)
	if err != nil {
		return err
	}
	defer store.Close()

	srv := server.New(server.Deps{
		Env:     env,
		DB:      pool,
		Cache:   store,
		Log:     log,
		Version: version(),
		Started: time.Now(),
	})
	return srv.Run(ctx)
}

func newLogger(level string) *slog.Logger {
	levels := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}
	// journald adds the timestamp itself.
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: levels[level],
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		},
	}))
}

// version is the commit the binary was built from: the server builds inside the git checkout,
// so the toolchain stamps it without any build flags.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	revision, dirty := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision == "" {
		return "unknown"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if dirty {
		revision += "-dirty"
	}
	return revision
}
