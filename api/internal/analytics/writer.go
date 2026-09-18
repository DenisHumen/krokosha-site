package analytics

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// record is one accepted batch, ready to be stored.
type record struct {
	pageviewID [8]byte
	visitor    [16]byte
	session    [8]byte
	startedAt  time.Time // UTC, estimated start of the page view
	day        string    // date in the site's time zone
	path       string
	lang       string
	refHost    string
	refKind    string
	utm        UTM
	isAd       bool
	ipPrefix   string
	client     Client
	durationMs int64
	maxScroll  int64
	events     []storedEvent
}

type storedEvent struct {
	occurredAt time.Time
	kind       string
	target     string
	value      int64
}

// writer stores records in batches from a single goroutine: MySQL sees one short transaction per
// second instead of one per page view, and a burst of visitors cannot exhaust the connection pool.
type writer struct {
	db      *sql.DB
	log     *slog.Logger
	queue   chan record
	dropped atomic.Int64
	stored  atomic.Int64

	flushEvery time.Duration
	maxBatch   int
}

func newWriter(db *sql.DB, log *slog.Logger) *writer {
	return &writer{
		db:         db,
		log:        log,
		queue:      make(chan record, 2048),
		flushEvery: time.Second,
		maxBatch:   200,
	}
}

// enqueue never blocks a request: when the database cannot keep up the oldest data wins and new
// batches are dropped (and counted). Statistics are not worth a slow page.
func (w *writer) enqueue(r record) bool {
	select {
	case w.queue <- r:
		return true
	default:
		if w.dropped.Add(1)%100 == 1 {
			w.log.Warn("analytics queue is full, dropping events", "dropped", w.dropped.Load())
		}
		return false
	}
}

// run stores records until ctx is cancelled, then writes whatever is still queued.
func (w *writer) run(ctx context.Context) {
	ticker := time.NewTicker(w.flushEvery)
	defer ticker.Stop()
	batch := make([]record, 0, w.maxBatch)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		// A shutdown must not lose the last second: store with a context of its own.
		storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := w.store(storeCtx, batch); err != nil {
			w.dropped.Add(int64(len(batch)))
			w.log.Error("cannot store analytics events", "records", len(batch), "error", err)
		} else {
			w.stored.Add(int64(len(batch)))
		}
		batch = batch[:0]
	}

	for {
		select {
		case r := <-w.queue:
			batch = append(batch, r)
			if len(batch) >= w.maxBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case r := <-w.queue:
					batch = append(batch, r)
				default:
					flush()
					return
				}
			}
		}
	}
}

const upsertPageview = `
INSERT INTO analytics_pageviews
    (pageview_id, visitor, session_id, started_at, day, path, lang, referrer_host, referrer_kind,
     utm_source, utm_medium, utm_campaign, utm_term, utm_content, is_ad, ip_prefix,
     device, browser, os, duration_ms, max_scroll)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
    id          = LAST_INSERT_ID(id),
    duration_ms = GREATEST(duration_ms, VALUES(duration_ms)),
    max_scroll  = GREATEST(max_scroll, VALUES(max_scroll))`

func (w *writer) store(ctx context.Context, batch []record) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, r := range batch {
		result, err := tx.ExecContext(ctx, upsertPageview,
			r.pageviewID[:], r.visitor[:], r.session[:], r.startedAt, r.day, r.path, nullable(r.lang),
			nullable(r.refHost), r.refKind,
			nullable(r.utm.Source), nullable(r.utm.Medium), nullable(r.utm.Campaign), nullable(r.utm.Term), nullable(r.utm.Content),
			r.isAd, r.ipPrefix, r.client.Device, r.client.Browser, r.client.OS, r.durationMs, r.maxScroll)
		if err != nil {
			return err
		}
		if len(r.events) == 0 {
			continue
		}
		pageview, err := result.LastInsertId()
		if err != nil {
			return err
		}

		query := strings.Builder{}
		query.WriteString(`INSERT INTO analytics_events (pageview, occurred_at, day, type, target, value) VALUES `)
		args := make([]any, 0, len(r.events)*6)
		for i, event := range r.events {
			if i > 0 {
				query.WriteString(", ")
			}
			query.WriteString("(?, ?, ?, ?, ?, ?)")
			args = append(args, pageview, event.occurredAt, r.day, event.kind, nullable(event.target), event.value)
		}
		if _, err := tx.ExecContext(ctx, query.String(), args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
