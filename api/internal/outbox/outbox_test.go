package outbox

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

var quiet = slog.New(slog.DiscardHandler)

type fixture struct {
	t      *testing.T
	db     *sql.DB
	worker *Worker
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := testenv.MySQL(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, cfg, migrations.Files, quiet); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, cfg, 10*time.Second, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	f := &fixture{t: t, db: pool, worker: NewWorker(pool, quiet), now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	f.worker.SetClock(func() time.Time { return f.now })
	return f
}

func (f *fixture) enqueue(task NewTask) {
	f.t.Helper()
	if err := Enqueue(context.Background(), f.db, f.now, task); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) deliver(want int) {
	f.t.Helper()
	got, err := f.worker.Deliver(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	if got != want {
		f.t.Fatalf("the worker took %d tasks, want %d", got, want)
	}
}

func (f *fixture) row(key string) (status string, attempts int, lastError string, next time.Time) {
	f.t.Helper()
	var last sql.NullString
	if err := f.db.QueryRow(`SELECT status, attempts, last_error, next_attempt_at FROM outbox WHERE dedupe_key = ?`, key).
		Scan(&status, &attempts, &last, &next); err != nil {
		f.t.Fatal(err)
	}
	return status, attempts, last.String, next
}

// mailbox is a sender that fails on demand.
type mailbox struct {
	mu   sync.Mutex
	got  []Task
	fail error
}

func (m *mailbox) Send(_ context.Context, task Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.got = append(m.got, task)
	return nil
}

func TestDeliveryAndIdempotentQueueing(t *testing.T) {
	f := newFixture(t)
	mail := &mailbox{}
	f.worker.Register(ChannelEmail, mail)

	task := NewTask{Channel: ChannelEmail, Kind: "lead.notify", LeadID: 42, DedupeKey: "lead:42:notify:email", Payload: map[string]string{"to": "denis@krokosha.xyz"}}
	f.enqueue(task)
	f.enqueue(task) // a retried request handler queues the same thing again: nothing happens
	f.deliver(1)

	if len(mail.got) != 1 || mail.got[0].LeadID != 42 || mail.got[0].Kind != "lead.notify" || !strings.Contains(string(mail.got[0].Payload), "denis@krokosha.xyz") {
		t.Fatalf("delivered: %+v", mail.got)
	}
	if status, attempts, _, _ := f.row(task.DedupeKey); status != "sent" || attempts != 1 {
		t.Errorf("after delivery: %s, %d attempts", status, attempts)
	}
	f.deliver(0) // sent is sent
	f.enqueue(task)
	f.deliver(0) // …and stays sent: the key is remembered
}

func TestRetriesWithGrowingPausesThenGivesUp(t *testing.T) {
	f := newFixture(t)
	mail := &mailbox{fail: errors.New("dial tcp 127.0.0.1:587: connection refused")}
	f.worker.Register(ChannelEmail, mail)
	var gaveUp []string
	f.worker.OnGiveUp(func(_ context.Context, task Task, lastError string) {
		gaveUp = append(gaveUp, task.Kind+": "+lastError)
	})

	f.enqueue(NewTask{Channel: ChannelEmail, Kind: "lead.notify", LeadID: 7, DedupeKey: "lead:7:notify:email", Payload: struct{}{}})
	for attempt := 1; attempt < len(backoff); attempt++ {
		f.deliver(1)
		status, attempts, lastError, next := f.row("lead:7:notify:email")
		if status != "pending" || attempts != attempt || !strings.Contains(lastError, "connection refused") || !next.Equal(f.now.Add(backoff[attempt-1])) {
			t.Fatalf("attempt %d: %s, %d attempts, next at %v (want +%v), error %q", attempt, status, attempts, next.Sub(f.now), backoff[attempt-1], lastError)
		}
		f.deliver(0) // not due yet
		f.now = next
	}
	// The mail server comes back before the last attempt: the message still arrives.
	mail.fail = nil
	f.deliver(1)
	if status, attempts, _, _ := f.row("lead:7:notify:email"); status != "sent" || attempts != len(backoff) {
		t.Errorf("after recovery: %s, %d attempts", status, attempts)
	}
	if len(gaveUp) != 0 {
		t.Errorf("gave up on a task that was delivered: %v", gaveUp)
	}

	// Another one never gets through.
	mail.fail = errors.New("still down")
	f.enqueue(NewTask{Channel: ChannelEmail, Kind: "lead.autoreply", LeadID: 8, DedupeKey: "lead:8:autoreply", Payload: struct{}{}})
	for range len(backoff) {
		f.deliver(1)
		_, _, _, next := f.row("lead:8:autoreply")
		f.now = next
	}
	if status, attempts, _, _ := f.row("lead:8:autoreply"); status != "failed" || attempts != len(backoff) {
		t.Errorf("after all attempts: %s, %d attempts", status, attempts)
	}
	if len(gaveUp) != 1 || gaveUp[0] != "lead.autoreply: still down" {
		t.Errorf("give-up hook: %v", gaveUp)
	}
	f.deliver(0)

	stats, err := ReadStats(context.Background(), f.db, f.now)
	if err != nil || stats.Pending != 0 || stats.Failed != 1 || stats.LastError != "still down" {
		t.Errorf("stats: %+v, %v", stats, err)
	}
}

func TestPermanentErrorsAreNotRetried(t *testing.T) {
	f := newFixture(t)
	f.worker.Register(ChannelEmail, SenderFunc(func(context.Context, Task) error {
		return Permanent(errors.New("550 no such user"))
	}))
	f.enqueue(NewTask{Channel: ChannelEmail, Kind: "lead.autoreply", DedupeKey: "k", Payload: struct{}{}})
	f.deliver(1)
	if status, attempts, lastError, _ := f.row("k"); status != "failed" || attempts != 1 || lastError != "550 no such user" {
		t.Errorf("a permanent failure: %s, %d, %q", status, attempts, lastError)
	}
}

func TestTasksWaitForAChannelThatIsNotSetUp(t *testing.T) {
	f := newFixture(t)
	f.enqueue(NewTask{Channel: ChannelTelegram, Kind: "lead.notify", LeadID: 1, DedupeKey: "lead:1:notify:telegram", Payload: struct{}{}})
	f.deliver(1)
	status, attempts, lastError, next := f.row("lead:1:notify:telegram")
	if status != "pending" || attempts != 0 || !strings.Contains(lastError, "not configured") || !next.Equal(f.now.Add(unconfiguredIn)) {
		t.Fatalf("waiting task: %s, %d attempts, %q, next %v", status, attempts, lastError, next)
	}
	stats, _ := ReadStats(context.Background(), f.db, f.now)
	if stats.Pending != 1 || !stats.OldestPending.Equal(f.now) {
		t.Errorf("stats: %+v", stats)
	}

	// The bot gets its token a week later: nothing was lost.
	telegram := &mailbox{}
	f.worker.Register(ChannelTelegram, telegram)
	f.now = f.now.Add(7 * 24 * time.Hour)
	f.deliver(1)
	if len(telegram.got) != 1 {
		t.Errorf("delivered after the channel appeared: %d", len(telegram.got))
	}
}

// The bot is there, but nobody has joined it yet: the card of a request waits for people, however
// long that takes, and uses up none of its attempts.
func TestTasksWaitForSomebodyToDeliverTo(t *testing.T) {
	f := newFixture(t)
	members := 0
	var delivered []string
	f.worker.Register(ChannelTelegram, SenderFunc(func(_ context.Context, task Task) error {
		if members == 0 {
			return NotReady(errors.New("nobody has access to the bot yet"))
		}
		delivered = append(delivered, task.Kind)
		return nil
	}))
	f.enqueue(NewTask{Channel: ChannelTelegram, Kind: "lead.notify", LeadID: 1, DedupeKey: "k", Payload: struct{}{}})
	for range 20 { // many more looks at the queue than a task has attempts
		f.deliver(1)
		f.now = f.now.Add(unconfiguredIn)
	}
	if status, attempts, lastError, _ := f.row("k"); status != "pending" || attempts != 0 || !strings.Contains(lastError, "nobody has access") {
		t.Fatalf("a task with nobody to go to: %s, %d attempts, %q", status, attempts, lastError)
	}
	// Somebody joins a minute after the last look: the task does not sit out its ten minutes.
	f.now = f.now.Add(-unconfiguredIn + time.Minute)
	members = 1
	f.worker.Hurry(context.Background(), ChannelTelegram)
	f.deliver(1)
	if status, _, _, _ := f.row("k"); status != "sent" || len(delivered) != 1 {
		t.Errorf("after the first person joined: %s, delivered %v", status, delivered)
	}
}

func TestACrashedDeliveryIsPickedUpAgain(t *testing.T) {
	f := newFixture(t)
	mail := &mailbox{}
	f.worker.Register(ChannelEmail, mail)
	f.enqueue(NewTask{Channel: ChannelEmail, Kind: "lead.notify", DedupeKey: "crash", Payload: struct{}{}})
	// The service was killed right after claiming the task.
	if _, err := f.db.Exec(`UPDATE outbox SET status = 'sending', locked_until = ? WHERE dedupe_key = 'crash'`, f.now.Add(lockFor)); err != nil {
		t.Fatal(err)
	}
	f.deliver(0) // the lock still holds: maybe that delivery is merely slow
	f.now = f.now.Add(lockFor + time.Second)
	f.deliver(1)
	if len(mail.got) != 1 {
		t.Errorf("delivered after the lock expired: %d", len(mail.got))
	}
}

func TestQueueingIsPartOfTheCallersTransaction(t *testing.T) {
	f := newFixture(t)
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := Enqueue(context.Background(), tx, f.now, NewTask{Channel: ChannelEmail, Kind: "lead.notify", DedupeKey: "rolled-back", Payload: struct{}{}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	f.deliver(0) // the request was not saved — nobody is told about it either
}

// Alerts about the server go through the same queue — once per occasion, however often a
// watchdog notices the same trouble.
func TestAnAlertIsQueuedOncePerOccasion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alert := Alert{Subject: " Сертификат не продлевается ", Text: "certbot renew: connection refused\n"}
	for range 3 {
		if err := EnqueueAlert(ctx, f.db, f.now, "cert:2026-09-19", alert); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnqueueAlert(ctx, f.db, f.now, "cert:2026-09-20", alert); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE kind = ? AND lead_id IS NULL`, KindAlert).Scan(&queued); err != nil || queued != 4 {
		t.Fatalf("queued = %d (two occasions × two channels), err = %v", queued, err)
	}
	if err := EnqueueAlert(ctx, f.db, f.now, "", alert); err == nil {
		t.Error("an alert without a key was queued")
	}
	if err := EnqueueAlert(ctx, f.db, f.now, "key", Alert{Text: "no subject"}); err == nil {
		t.Error("an alert without a subject was queued")
	}

	inbox := &mailbox{}
	f.worker.Register(ChannelEmail, inbox)
	f.deliver(4) // the two for Telegram wait for a bot
	if len(inbox.got) != 2 {
		t.Fatalf("delivered by mail: %d", len(inbox.got))
	}
	got, err := ReadAlert(inbox.got[0])
	if err != nil || got.Subject != "Сертификат не продлевается" || got.Text != "certbot renew: connection refused" {
		t.Errorf("the alert as the sender sees it: %+v, %v", got, err)
	}
	if _, err := ReadAlert(Task{Payload: []byte(`{"lead_id":1}`)}); !IsPermanent(err) {
		t.Errorf("a task that is no alert: %v", err)
	}
}
