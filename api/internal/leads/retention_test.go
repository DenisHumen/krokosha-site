package leads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// age moves the last activity of a request into the past.
func (f *fixture) age(id int64, months int) {
	f.t.Helper()
	when := f.now.AddDate(0, -months, -1)
	if _, err := f.db.Exec(`UPDATE leads SET created_at = ?, updated_at = ? WHERE id = ?`, when, when, id); err != nil {
		f.t.Fatal(err)
	}
}

// TestOldRequestsAreAnonymised: brief B10.7 and the promise of the privacy page — 24 months.
func TestOldRequestsAreAnonymised(t *testing.T) {
	f := newFixture(t)
	f.acceptFiles = true
	store := NewStore(f.db, func() time.Time { return f.now })
	store.UseFiles(f.files)
	ctx := context.Background()

	values := validValues()
	if got := f.upload(values, []testFile{{"spec.pdf", pdfFile()}}, map[string]string{"Accept": "application/json", "X-Real-IP": "198.51.100.1"}); got.status != 201 {
		t.Fatalf("seeding: %d %s", got.status, got.body)
	}
	old, recent := int64(1), f.seed(nil)
	robot := f.seed(func(v map[string]string) { v["website"] = "x" })
	oldRobot := f.seed(func(v map[string]string) { v["website"] = "x" })
	if _, err := store.Take(ctx, old, "denis"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reply(ctx, old, "denis", "Спасибо, отвечу сегодня."); err != nil {
		t.Fatal(err)
	}
	_ = store.AddNote(ctx, old, "denis", "Иван, знакомый Петра")
	if err := store.SetStatus(ctx, old, "denis", StatusRejected, "Иван передумал"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE leads SET source = 'ads', utm_campaign = 'mikrotik-kyiv', country = 'UA', referrer_host = 'google.com' WHERE id = ?`, old); err != nil {
		t.Fatal(err)
	}
	before, err := store.Get(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	f.age(old, 24)
	f.age(recent, 23)
	f.age(oldRobot, 2)

	rule := Retention{Store: store, Files: f.files, Log: quiet, KeepMonths: 24, SpamDays: 30}
	report, err := rule.RunOnce(ctx)
	if err != nil || report != (RetentionReport{Anonymized: 1, Spam: 1}) {
		t.Fatalf("the first pass: %+v %v", report, err)
	}

	// The person is gone…
	after, err := store.Get(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "—" || after.ContactValue != "" || after.Description != "" || after.IPPrefix != "" || after.Session.ReferrerHost != "" {
		t.Errorf("what is left of the person: %+v", after)
	}
	if f.onDisk() != 0 {
		t.Errorf("files left on disk: %d", f.onDisk())
	}
	for table, want := range map[string]int{"lead_messages": 0, "lead_attachments": 0, "outbox": 0} {
		if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE lead_id = %d`, table, old)); n != want {
			t.Errorf("%s of the anonymised request: %d rows", table, n)
		}
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM lead_events WHERE lead_id = %d AND details IS NOT NULL`, old)); n != 0 {
		t.Errorf("free text left in the history: %d rows", n)
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM leads WHERE id = %d AND reject_reason IS NULL AND session_id IS NULL`, old)); n != 1 {
		t.Error("the reason of the refusal or the link to the visit is still there")
	}
	// …the link the client had opens nothing…
	if _, err := store.ByToken(ctx, before.PublicToken); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("the client's link after anonymisation: %v", err)
	}
	// …and what the statistics are made of stays.
	if after.Status != StatusRejected || after.Direction != "networks" || after.Budget != "$1–3k" || after.Session.Source != "ads" ||
		after.Session.UTMCampaign != "mikrotik-kyiv" || after.Session.Country != "UA" || !after.CreatedAt.Equal(f.now.AddDate(0, -24, -1)) {
		t.Errorf("the statistics of the anonymised request: %+v", after)
	}
	card, err := store.Card(ctx, old)
	if err != nil || !card.AnonymizedAt.Valid || len(card.Feed) == 0 || card.Feed[len(card.Feed)-1].Action != "anonymized" {
		t.Errorf("the card of the anonymised request: %+v %v", card, err)
	}

	// Younger requests are untouched; of the robots only the old one is gone.
	if kept, err := store.Get(ctx, recent); err != nil || kept.Name != "Иван Петров" {
		t.Errorf("a request of 23 months: %+v %v", kept, err)
	}
	if _, err := store.Get(ctx, robot); err != nil {
		t.Errorf("fresh spam must stay for a month: %v", err)
	}
	if _, err := store.Get(ctx, oldRobot); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("old spam: %v", err)
	}

	// The second pass finds nothing to do.
	if report, err := rule.RunOnce(ctx); err != nil || report != (RetentionReport{}) {
		t.Errorf("the second pass: %+v %v", report, err)
	}

	// The other rule — delete — also takes what was anonymised before.
	rule.Delete = true
	if report, err := rule.RunOnce(ctx); err != nil || report != (RetentionReport{Deleted: 1}) {
		t.Errorf("the deleting pass: %+v %v", report, err)
	}
	if _, err := store.Get(ctx, old); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("the expired request after the deleting pass: %v", err)
	}
	// Nothing at all: «0» switches the rule off.
	f.age(recent, 60)
	if report, err := (Retention{Store: store, Log: quiet}).RunOnce(ctx); err != nil || report != (RetentionReport{}) {
		t.Errorf("a pass without a rule: %+v %v", report, err)
	}
}

func TestRetentionSweepsOrphanFiles(t *testing.T) {
	f := newFixture(t)
	f.acceptFiles = true
	store := NewStore(f.db, time.Now)
	store.UseFiles(f.files)
	if got := f.upload(validValues(), []testFile{{"spec.pdf", pdfFile()}}, asJSON); got.status != 201 {
		t.Fatalf("seeding: %d %s", got.status, got.body)
	}
	// A file whose request never made it into the database, written the day before yesterday.
	orphan, err := f.files.Save("lost.txt", KindTXT, strings.NewReader("text"))
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(f.filesDir)
	for _, entry := range entries {
		when := time.Now().Add(-48 * time.Hour)
		_ = os.Chtimes(filepath.Join(f.filesDir, entry.Name()), when, when)
	}

	report, err := Retention{Store: store, Files: f.files, Log: quiet, KeepMonths: 24}.RunOnce(context.Background())
	if err != nil || report != (RetentionReport{Files: 1}) {
		t.Fatalf("the pass: %+v %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(f.filesDir, orphan.StoredAs)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the orphan is still there: %v", err)
	}
	if f.onDisk() != 1 {
		t.Errorf("files on disk: %d, want the one that belongs to a request", f.onDisk())
	}
}
