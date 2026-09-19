// Package outbox delivers notifications reliably (brief B10.2). Whoever has something to tell —
// «a new request came in» — writes a task in the same database transaction as the thing itself;
// a worker delivers tasks with retries and growing pauses. The mail server or Telegram being
// down delays a message, it never loses one; a task is never delivered twice on purpose.
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Channels.
const (
	ChannelEmail    = "email"
	ChannelTelegram = "telegram"
)

// NewTask is what Enqueue stores.
type NewTask struct {
	Channel string
	Kind    string // lead.notify, lead.autoreply, lead.reply…
	LeadID  int64  // 0 = not about a request
	// DedupeKey makes queueing idempotent: a second Enqueue with the same key does nothing.
	DedupeKey string
	Payload   any // marshalled to JSON
}

// Task is what a Sender gets.
type Task struct {
	ID       int64
	Channel  string
	Kind     string
	LeadID   int64
	Payload  json.RawMessage
	Attempts int // deliveries tried before this one
}

// Sender delivers tasks of one channel. An error means «try again later», unless it is wrapped
// with Permanent.
type Sender interface {
	Send(ctx context.Context, task Task) error
}

// SenderFunc adapts a function to Sender.
type SenderFunc func(ctx context.Context, task Task) error

// Send calls f.
func (f SenderFunc) Send(ctx context.Context, task Task) error { return f(ctx, task) }

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks an error no retry can fix (an address the server refuses for good, a payload
// that cannot be read): the task fails at once instead of being retried for a day.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var permanent permanentError
	return errors.As(err, &permanent)
}

type notReadyError struct{ err error }

func (e notReadyError) Error() string { return e.err.Error() }
func (e notReadyError) Unwrap() error { return e.err }

// NotReady marks «there is nobody to deliver to yet»: the bot has no members so far, the client
// has not opened the bot. Like a channel that is not configured, the task waits and no attempt
// is used up — what it waits for depends on people, not on a server coming back.
func NotReady(err error) error {
	if err == nil {
		return nil
	}
	return notReadyError{err}
}

// IsNotReady reports whether err was marked with NotReady.
func IsNotReady(err error) bool {
	var notReady notReadyError
	return errors.As(err, &notReady)
}

// Pauses between attempts. After the last one the task is given up on: eight tries in two days.
var backoff = []time.Duration{
	30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute,
	2 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

const (
	pollEvery      = 5 * time.Second
	batchSize      = 10
	sendTimeout    = 45 * time.Second
	lockFor        = 2 * time.Minute  // a delivery taking longer than this is considered dead
	unconfiguredIn = 10 * time.Minute // look again whether a channel got its sender
)

// Execer is what Enqueue needs: a transaction, usually.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Enqueue stores a task. Call it inside the transaction that stores what the task is about.
func Enqueue(ctx context.Context, tx Execer, now time.Time, task NewTask) error {
	payload, err := json.Marshal(task.Payload)
	if err != nil {
		return fmt.Errorf("outbox payload: %w", err)
	}
	var lead sql.NullInt64
	if task.LeadID > 0 {
		lead = sql.NullInt64{Int64: task.LeadID, Valid: true}
	}
	// INSERT IGNORE: the unique dedupe key turns a repeated Enqueue into a no-op.
	_, err = tx.ExecContext(ctx, `
		INSERT IGNORE INTO outbox (created_at, channel, kind, lead_id, dedupe_key, payload, next_attempt_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, now.UTC(), task.Channel, task.Kind, lead, task.DedupeKey, string(payload), now.UTC())
	return err
}

// Worker delivers due tasks.
type Worker struct {
	db  *sql.DB
	log *slog.Logger
	now func() time.Time

	mu      sync.RWMutex
	senders map[string]Sender
	// OnGiveUp is called when a task has used up its attempts, so that the failure can be
	// reported through another channel (brief B10.2).
	onGiveUp func(ctx context.Context, task Task, lastError string)

	wake chan struct{}
}

// NewWorker builds a worker. Register senders, then Run.
func NewWorker(db *sql.DB, log *slog.Logger) *Worker {
	return &Worker{db: db, log: log, now: time.Now, senders: map[string]Sender{}, wake: make(chan struct{}, 1)}
}

// SetClock replaces the clock (tests).
func (w *Worker) SetClock(now func() time.Time) { w.now = now }

// Register sets the sender of a channel. Tasks of a channel without a sender wait.
func (w *Worker) Register(channel string, sender Sender) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.senders[channel] = sender
}

// OnGiveUp sets the hook called for tasks that failed for good.
func (w *Worker) OnGiveUp(hook func(ctx context.Context, task Task, lastError string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onGiveUp = hook
}

// Kick makes the worker look at the queue now rather than at its next tick.
func (w *Worker) Kick() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Hurry makes the waiting tasks of a channel due right now. Tasks that found nobody to deliver
// to look again only every ten minutes; when somebody appears — the first person joins the bot —
// there is no reason to make them wait.
func (w *Worker) Hurry(ctx context.Context, channel string) {
	if _, err := w.db.ExecContext(ctx, `UPDATE outbox SET next_attempt_at = ? WHERE status = 'pending' AND channel = ? AND next_attempt_at > ?`,
		w.now().UTC(), channel, w.now().UTC()); err != nil {
		w.log.Error("outbox: cannot hurry the queue", "channel", channel, "error", err)
	}
	w.Kick()
}

// Run delivers tasks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		for {
			delivered, err := w.Deliver(ctx)
			if err != nil && ctx.Err() == nil {
				w.log.Error("outbox: cannot work through the queue", "error", err)
			}
			if err != nil || delivered < batchSize {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

// Deliver takes one batch of due tasks and tries each once. It returns how many it took.
func (w *Worker) Deliver(ctx context.Context) (int, error) {
	now := w.now().UTC()
	// A worker that died in the middle of a delivery left its tasks «sending»: give them back.
	if _, err := w.db.ExecContext(ctx, `UPDATE outbox SET status = 'pending' WHERE status = 'sending' AND locked_until < ?`, now); err != nil {
		return 0, err
	}

	rows, err := w.db.QueryContext(ctx, `
		SELECT id, channel, kind, COALESCE(lead_id, 0), payload, attempts FROM outbox
		WHERE status = 'pending' AND next_attempt_at <= ? ORDER BY next_attempt_at, id LIMIT ?`, now, batchSize)
	if err != nil {
		return 0, err
	}
	var tasks []Task
	for rows.Next() {
		var task Task
		var payload []byte
		if err := rows.Scan(&task.ID, &task.Channel, &task.Kind, &task.LeadID, &payload, &task.Attempts); err != nil {
			_ = rows.Close()
			return 0, err
		}
		task.Payload = payload
		tasks = append(tasks, task)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	for _, task := range tasks {
		if ctx.Err() != nil {
			return len(tasks), ctx.Err()
		}
		w.deliver(ctx, task)
	}
	return len(tasks), nil
}

func (w *Worker) deliver(ctx context.Context, task Task) {
	now := w.now().UTC()
	w.mu.RLock()
	sender, hook := w.senders[task.Channel], w.onGiveUp
	w.mu.RUnlock()

	if sender == nil {
		// Not an attempt: the channel simply is not set up (yet). The task waits.
		_, _ = w.db.ExecContext(ctx, `UPDATE outbox SET next_attempt_at = ?, last_error = ? WHERE id = ? AND status = 'pending'`,
			now.Add(unconfiguredIn), "the channel «"+task.Channel+"» is not configured", task.ID)
		return
	}

	// Claim it. Should another worker ever run beside this one, only one of them gets the row.
	result, err := w.db.ExecContext(ctx, `UPDATE outbox SET status = 'sending', locked_until = ? WHERE id = ? AND status = 'pending'`,
		now.Add(lockFor), task.ID)
	if err != nil {
		w.log.Error("outbox: cannot claim a task", "task", task.ID, "error", err)
		return
	}
	if claimed, _ := result.RowsAffected(); claimed == 0 {
		return
	}

	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	err = sender.Send(sendCtx, task)
	cancel()

	// The bookkeeping must happen even if the service is shutting down right now.
	saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelSave()
	now = w.now().UTC()
	if err == nil {
		if _, err := w.db.ExecContext(saveCtx, `UPDATE outbox SET status = 'sent', sent_at = ?, attempts = attempts + 1, last_error = NULL, locked_until = NULL WHERE id = ?`, now, task.ID); err != nil {
			w.log.Error("outbox: delivered, but could not record it — the task may be sent again", "task", task.ID, "error", err)
		}
		return
	}

	if IsNotReady(err) {
		_, _ = w.db.ExecContext(saveCtx, `UPDATE outbox SET status = 'pending', last_error = ?, next_attempt_at = ?, locked_until = NULL WHERE id = ?`,
			truncate(err.Error(), 500), now.Add(unconfiguredIn), task.ID)
		return
	}
	attempts := task.Attempts + 1
	message := truncate(err.Error(), 500)
	if IsPermanent(err) || attempts >= len(backoff) {
		w.log.Error("outbox: giving up on a task", "task", task.ID, "channel", task.Channel, "kind", task.Kind, "attempts", attempts, "error", message)
		_, _ = w.db.ExecContext(saveCtx, `UPDATE outbox SET status = 'failed', attempts = ?, last_error = ?, locked_until = NULL WHERE id = ?`, attempts, message, task.ID)
		if hook != nil {
			hook(saveCtx, task, message)
		}
		return
	}
	pause := backoff[attempts-1]
	w.log.Warn("outbox: delivery failed, will retry", "task", task.ID, "channel", task.Channel, "kind", task.Kind, "attempt", attempts, "retry_in", pause.String(), "error", message)
	_, _ = w.db.ExecContext(saveCtx, `UPDATE outbox SET status = 'pending', attempts = ?, last_error = ?, next_attempt_at = ?, locked_until = NULL WHERE id = ?`,
		attempts, message, now.Add(pause), task.ID)
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit]
}

// Stats is the state of the queue for the «system status» screen.
type Stats struct {
	Pending       int
	Failed        int
	OldestPending time.Time // zero when nothing waits
	LastError     string
}

// ReadStats counts what waits and what was given up on in the last 30 days.
func ReadStats(ctx context.Context, db *sql.DB, now time.Time) (Stats, error) {
	var stats Stats
	var oldest sql.NullTime
	var lastError sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(status IN ('pending', 'sending')), 0),
		       COALESCE(SUM(status = 'failed' AND created_at > ?), 0),
		       MIN(IF(status IN ('pending', 'sending'), created_at, NULL)),
		       (SELECT last_error FROM outbox WHERE last_error IS NOT NULL AND status <> 'sent' ORDER BY id DESC LIMIT 1)
		FROM outbox`, now.UTC().AddDate(0, 0, -30)).Scan(&stats.Pending, &stats.Failed, &oldest, &lastError)
	stats.OldestPending, stats.LastError = oldest.Time, lastError.String
	return stats, err
}
