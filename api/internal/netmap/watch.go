package netmap

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"runtime"
	"runtime/debug"
	"time"
)

// WatchEvery is how often the API looks for a newer map.
const WatchEvery = 30 * time.Second

// retryFailedAfter is when a map that could not be loaded is tried again: reading it is not free.
const retryFailedAfter = 30 * time.Minute

// Watch keeps the service on the newest map in the database: it loads one at once, and again
// whenever a newer sync has finished (`krokosha-cli netmap sync`, every night). It returns when
// ctx is done. A map that fails to load leaves the one in service as it is.
func Watch(ctx context.Context, db *sql.DB, service *Service, every time.Duration, log *slog.Logger) {
	var loaded, failed int64
	var failedAt time.Time
	check := func() {
		latest, _, err := LatestSync(ctx, db)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("netmap: cannot look for a new map", "error", err)
			}
			return
		}
		if latest == 0 || latest == loaded || (latest == failed && time.Since(failedAt) < retryFailedAfter) {
			return
		}
		started := time.Now()
		m, id, err := Load(ctx, db)
		switch {
		case errors.Is(err, ErrNoMap):
			return
		case err != nil:
			if ctx.Err() == nil {
				log.Error("netmap: the map cannot be loaded", "sync", latest, "error", err)
				failed, failedAt = latest, time.Now()
			}
			return
		}
		service.Use(m)
		loaded = id
		// Loading takes several times the memory of the map for a moment; on a small server that
		// moment should end now, not whenever the collector gets to it.
		runtime.GC()
		debug.FreeOSMemory()
		v4, v6 := m.Prefixes.Len()
		log.Info("netmap: the map is loaded", "sync", id, "networks", len(m.Nodes), "links", m.Links(),
			"prefixes", v4+v6, "exchanges", len(m.IXs), "took", time.Since(started).Round(time.Millisecond))
	}
	check()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}
