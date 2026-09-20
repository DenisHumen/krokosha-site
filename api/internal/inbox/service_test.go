package inbox

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/imap"
	"github.com/DenisHumen/krokosha-site/api/internal/imap/imaptest"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

var (
	quiet  = slog.New(slog.NewTextHandler(io.Discard, nil))
	secret = []byte("a secret of the test, long enough")
	noon   = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
)

const mailbox = "leads@krokosha.xyz"

type fixture struct {
	t        *testing.T
	db       *sql.DB
	now      time.Time
	store    *leads.Store
	filesDir string
	server   *imaptest.Server
	service  *Service
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

	f := &fixture{t: t, db: pool, now: noon, filesDir: filepath.Join(t.TempDir(), "attachments")}
	clock := func() time.Time { return f.now }
	files := leads.NewFiles(f.filesDir)
	f.store = leads.NewStore(pool, clock)
	f.store.UseFiles(files)
	f.server = imaptest.New(t, mailbox, "the password of the mailbox")
	f.service = New(Options{
		Dial: func(ctx context.Context) (*imap.Client, error) {
			return imap.Dial(ctx, imap.Options{Addr: f.server.Addr, User: f.server.User, Password: f.server.Password, Timeout: 5 * time.Second})
		},
		DB: pool, Leads: f.store, Files: files, Secret: secret, Inbox: mailbox, Log: quiet, Now: clock,
		IdleFor: 300 * time.Millisecond, retry: 50 * time.Millisecond,
	})
	return f
}

func (f *fixture) lead(email string) *leads.Lead {
	f.t.Helper()
	lead, err := f.store.Create(context.Background(), leads.Submission{
		Name: "Иван Петров", ContactMethod: leads.MethodEmail, ContactValue: email, Direction: "networks",
		Description: "Нужно настроить MikroTik и два VLAN в офисе на двадцать человек.", Lang: "ru",
	}, leads.Verdict{}, analytics.SessionSummary{}, "203.0.113.0/24")
	if err != nil {
		f.t.Fatal(err)
	}
	return lead
}

// waitingForClient takes the request and answers it: the ball is on the client's side.
func (f *fixture) waitingForClient(id int64) (answerID int64) {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.store.Take(ctx, id, "denis"); err != nil {
		f.t.Fatal(err)
	}
	answerID, err := f.store.Reply(ctx, id, "denis", "Спасибо, изучу и отвечу до конца дня.")
	if err != nil {
		f.t.Fatal(err)
	}
	if got := f.text(`SELECT status FROM leads WHERE id = ?`, id); got != leads.StatusWaitingClient {
		f.t.Fatalf("status after an answer = %s", got)
	}
	return answerID
}

func (f *fixture) check() {
	f.t.Helper()
	if err := f.service.CheckOnce(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) text(query string, args ...any) string {
	f.t.Helper()
	var text sql.NullString
	if err := f.db.QueryRow(query, args...).Scan(&text); err != nil && !errors.Is(err, sql.ErrNoRows) {
		f.t.Fatal(err)
	}
	return text.String
}

func (f *fixture) storedFiles() int {
	entries, err := os.ReadDir(f.filesDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal(err)
	}
	return len(entries)
}

type part struct {
	name    string
	content []byte
	inline  bool
}

// compose writes a letter the way a mail program would.
func compose(from, to, messageID, text string, extra string, parts ...part) []byte {
	var out strings.Builder
	fmt.Fprintf(&out, "From: %s\nTo: %s\nSubject: Re: request\nMessage-ID: <%s>\nDate: Tue, 1 Sep 2026 13:20:00 +0300\n%s", from, to, messageID, extra)
	if len(parts) == 0 {
		out.WriteString("Content-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: quoted-printable\n\n" + qp(text) + "\n")
		return crlf(out.String())
	}
	out.WriteString("Content-Type: multipart/mixed; boundary=frontier\n\n--frontier\nContent-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: quoted-printable\n\n" + qp(text) + "\n")
	for _, p := range parts {
		disposition := "attachment"
		if p.inline {
			disposition = "inline"
		}
		fmt.Fprintf(&out, "--frontier\nContent-Type: application/octet-stream\nContent-Disposition: %s; filename=\"%s\"\nContent-Transfer-Encoding: base64\n\n%s", disposition, p.name, b64(p.content))
	}
	out.WriteString("--frontier--\n")
	return crlf(out.String())
}

const quoteOfOurLetter = "\n\nпн, 1 сент. 2026 г. в 10:04, Denis Humen <denis@krokosha.xyz>:\n\n> Спасибо, изучу и отвечу до конца дня.\n> --\n> Denis Humen\n"

// TestAnAnswerJoinsItsRequest: brief B10.5 from end to end — the number in the address, the
// quote cut off, the file kept, the request back at work, everybody told.
func TestAnAnswerJoinsItsRequest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	lead := f.lead("ivan@company.test")
	f.waitingForClient(lead.ID)
	queued := f.count(`SELECT COUNT(*) FROM outbox`)

	address := leads.ReplyAddress(secret, mailbox, lead.ID)
	// Mail programs change the case of addresses as they please, and people answer from other mailboxes.
	f.server.Deliver(compose("Ivan <Ivan.Petrov@Gmail.test>", "Denis <"+strings.ToUpper(address)+">", "answer-1@gmail.test",
		"Да, VLAN два: офис и гости.\nСхему прикладываю."+quoteOfOurLetter, "", part{name: "схема сети.pdf", content: pdf}))
	f.check()

	var body, channel, emailID string
	var messageID int64
	if err := f.db.QueryRow(`SELECT id, body, channel, COALESCE(email_message_id, '') FROM lead_messages WHERE lead_id = ? AND direction = 'in' AND channel <> 'form'`, lead.ID).
		Scan(&messageID, &body, &channel, &emailID); err != nil {
		t.Fatal(err)
	}
	if body != "Да, VLAN два: офис и гости.\nСхему прикладываю." || channel != leads.ChannelEmail || emailID != "answer-1@gmail.test" {
		t.Errorf("the message: %q via %s, id %q", body, channel, emailID)
	}
	if got := f.text(`SELECT status FROM leads WHERE id = ?`, lead.ID); got != leads.StatusInProgress {
		t.Errorf("status = %s: the client answered, the request is back at work", got)
	}
	files, err := f.store.MessageFiles(ctx, lead.ID, messageID)
	if err != nil || len(files) != 1 || files[0].Filename != "схема сети.pdf" || files[0].Kind != leads.KindPDF {
		t.Fatalf("files = %+v, %v", files, err)
	}
	_, content, err := f.store.OpenAttachment(ctx, lead.ID, files[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := io.ReadAll(content)
	_ = content.Close()
	if !bytes.Equal(stored, pdf) {
		t.Error("the file on disk is not the file of the letter")
	}
	if note := f.text(`SELECT details FROM lead_events WHERE lead_id = ? AND action = 'client_replied'`, lead.ID); note != "с адреса ivan.petrov@gmail.test" {
		t.Errorf("the history: %q", note)
	}
	if got := f.count(`SELECT COUNT(*) FROM outbox WHERE kind = ?`, leads.TaskClientMessage); got != 2 || f.count(`SELECT COUNT(*) FROM outbox`) != queued+2 {
		t.Errorf("notifications about the letter: %d", got)
	}
	// The next answer by mail refers to the client's letter: one thread in their mail program.
	if thread, _ := f.store.ThreadIDs(ctx, lead.ID); len(thread) == 0 || thread[len(thread)-1] != "answer-1@gmail.test" {
		t.Errorf("thread = %q", thread)
	}
	// The letter is with the request now — and nowhere else.
	if left := f.server.Messages(); len(left) != 0 {
		t.Errorf("the letter is still in the mailbox: %d", len(left))
	}
	var outcome string
	var personal int
	if err := f.db.QueryRow(`SELECT outcome, (from_address IS NOT NULL) + (subject IS NOT NULL) + (excerpt IS NOT NULL) + (message_id IS NOT NULL) FROM inbox_letters WHERE lead_id = ?`, lead.ID).
		Scan(&outcome, &personal); err != nil || outcome != OutcomeMatched || personal != 0 {
		t.Errorf("the journal: %s, personal fields %d, %v", outcome, personal, err)
	}

	// Deleting the client's data takes the journal's row along.
	if err := f.store.Delete(ctx, lead.ID); err != nil {
		t.Fatal(err)
	}
	if f.count(`SELECT COUNT(*) FROM inbox_letters`) != 0 || f.storedFiles() != 0 {
		t.Error("something of a deleted request is left")
	}
}

func TestLettersWithoutANumber(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.lead("ivan@company.test")
	latest := f.lead("Ivan@company.test")

	// A client we know, writing to the bare address: their latest request.
	f.server.Deliver(compose("ivan@Company.test", mailbox, "plain-1@company.test", "Забыл сказать: роутер hEX S.", ""))
	// A stranger.
	strangers := f.server.Deliver(compose("=?UTF-8?B?0J7Qu9C10L3QsA==?= <olena@else.test>", mailbox, "stranger-1@else.test",
		"Добрый день! Пишу по рекомендации, <b>нужна</b> помощь с сетью.", "", part{name: "план.pdf", content: pdf}))
	// A number that was made up: the signature of another request.
	forged := strings.Replace(leads.ReplyAddress(secret, mailbox, 1), "k-0001", "k-0002", 1)
	f.server.Deliver(compose("mallory@evil.test", forged, "forged-1@evil.test", "Пожалуйста, смените реквизиты оплаты.", ""))
	f.check()

	if got := f.text(`SELECT body FROM lead_messages WHERE lead_id = ? AND channel = 'email'`, latest.ID); got != "Забыл сказать: роутер hEX S." {
		t.Errorf("the known client's letter: %q", got)
	}
	if note := f.text(`SELECT details FROM lead_events WHERE lead_id = ? AND action = 'client_replied'`, latest.ID); !strings.Contains(note, "по адресу отправителя") {
		t.Errorf("the history does not say how the letter was placed: %q", note)
	}
	if f.count(`SELECT COUNT(*) FROM lead_messages WHERE channel = 'email'`) != 1 {
		t.Fatal("a stranger's or a forged letter got into a conversation")
	}

	waiting, err := f.service.Waiting(ctx)
	if err != nil || len(waiting) != 2 {
		t.Fatalf("waiting = %+v, %v", waiting, err)
	}
	mallory, olena := waiting[0], waiting[1]
	if olena.FromName != "Олена" || olena.FromAddress != "olena@else.test" || olena.Files != 1 || !strings.Contains(olena.Excerpt, "<b>нужна</b>") {
		t.Errorf("the stranger's letter: %+v", olena)
	}
	if mallory.FromAddress != "mallory@evil.test" {
		t.Errorf("the forged letter: %+v", mallory)
	}
	// They wait in the mailbox, marked as read — and nothing of them is unpacked.
	left := f.server.Messages()
	if len(left) != 2 || !left[0].Flags[`\Seen`] || !left[1].Flags[`\Seen`] || f.storedFiles() != 0 {
		t.Fatalf("the mailbox: %d letters, files on disk: %d", len(left), f.storedFiles())
	}
	if status := f.service.Status(ctx); status.Unmatched != 2 || status.CheckedAt.IsZero() {
		t.Errorf("status = %+v", status)
	}
	// Looking again changes nothing.
	f.check()
	if got, _ := f.service.Waiting(ctx); len(got) != 2 {
		t.Fatalf("a second look: %d letters", len(got))
	}

	// A person decides: the stranger's letter belongs to the first request…
	if err := f.service.Attach(ctx, olena.ID, 1, "denis"); err != nil {
		t.Fatal(err)
	}
	if got := f.text(`SELECT body FROM lead_messages WHERE lead_id = 1 AND channel = 'email'`); !strings.Contains(got, "по рекомендации") {
		t.Errorf("the attached letter: %q", got)
	}
	if note := f.text(`SELECT details FROM lead_events WHERE lead_id = 1 AND action = 'client_replied'`); !strings.Contains(note, "привязано вручную: denis") || !strings.Contains(note, "olena@else.test") {
		t.Errorf("the history: %q", note)
	}
	if f.storedFiles() != 1 {
		t.Errorf("the file of the attached letter: %d on disk", f.storedFiles())
	}
	if err := f.service.Attach(ctx, olena.ID, 1, "denis"); !errors.Is(err, ErrGone) {
		t.Errorf("attaching twice: %v", err)
	}
	// …and the other one is thrown away.
	if err := f.service.Discard(ctx, mallory.ID); err != nil {
		t.Fatal(err)
	}
	if left := f.server.Messages(); len(left) != 0 {
		t.Errorf("the mailbox still has %d letters (the stranger's was %d)", len(left), strangers)
	}
	if got, _ := f.service.Waiting(ctx); len(got) != 0 {
		t.Errorf("still waiting: %+v", got)
	}
	if f.count(`SELECT COUNT(*) FROM inbox_letters WHERE from_address IS NOT NULL OR excerpt IS NOT NULL OR subject IS NOT NULL`) != 0 {
		t.Error("the journal keeps what it should have forgotten")
	}
	if f.count(`SELECT COUNT(*) FROM inbox_letters`) != 3 {
		t.Error("the journal forgot that the letters were handled")
	}
}

func TestTheSameLetterTwice(t *testing.T) {
	f := newFixture(t)
	lead := f.lead("ivan@company.test")
	address := leads.ReplyAddress(secret, mailbox, lead.ID)
	letter := compose("ivan@company.test", address, "twice@company.test", "Одно и то же письмо.", "Cc: "+mailbox+"\n")
	f.server.Deliver(letter)
	f.server.Deliver(letter) // the copy for the address in Cc
	f.check()
	if got := f.count(`SELECT COUNT(*) FROM lead_messages WHERE lead_id = ? AND channel = 'email'`, lead.ID); got != 1 {
		t.Fatalf("messages = %d", got)
	}
	if left := f.server.Messages(); len(left) != 0 {
		t.Errorf("the copy stays in the mailbox: %d", len(left))
	}
}

func TestADeliveryReport(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	lead := f.lead("ivan@compny.test")
	answerID := f.waitingForClient(lead.ID)
	sentAs := fmt.Sprintf("lead-%d.reply-%d@krokosha.xyz", lead.ID, answerID)
	if err := f.store.MarkDelivery(ctx, answerID, "sent", sentAs); err != nil {
		t.Fatal(err)
	}
	report := func(messageID, replyTo, status string) []byte {
		return crlf(`Return-Path: <>
From: Mail Delivery System <MAILER-DAEMON@mail.krokosha.xyz>
To: ` + mailbox + `
Subject: Undelivered Mail Returned to Sender
Message-ID: <` + messageID + `>
Auto-Submitted: auto-replied
Content-Type: multipart/report; report-type=delivery-status; boundary="b1"

--b1
Content-Type: text/plain

I'm sorry to have to inform you that your message could not be delivered.

--b1
Content-Type: message/delivery-status

Reporting-MTA: dns; mail.krokosha.xyz

Final-Recipient: rfc822; ivan@compny.test
Action: failed
Status: ` + status + `
Diagnostic-Code: X-Postfix; Host or domain name not found

--b1
Content-Type: text/rfc822-headers

From: Denis Humen <denis@krokosha.xyz>
To: ivan@compny.test
Reply-To: Denis Humen <` + replyTo + `>
Message-ID: <` + sentAs + `>
X-Krokosha-Lead: K-0001

--b1--
`)
	}
	queued := f.count(`SELECT COUNT(*) FROM outbox`)
	f.server.Deliver(report("dsn-1@mail.krokosha.xyz", leads.ReplyAddress(secret, mailbox, lead.ID), "5.4.4"))
	// Anybody can type «K-0001» into a letter; without the signed address a report moves nothing.
	f.server.Deliver(report("dsn-2@evil.test", "leads+k-0001.aaaaaaaaaaaaaaaa@krokosha.xyz", "5.1.1"))
	f.check()

	if got := f.text(`SELECT delivery FROM lead_messages WHERE id = ?`, answerID); got != "failed" {
		t.Errorf("the answer that did not arrive is marked %q", got)
	}
	if got := f.count(`SELECT COUNT(*) FROM lead_events WHERE lead_id = ? AND action = 'undelivered'`, lead.ID); got != 1 {
		t.Fatalf("history lines about the report: %d", got)
	}
	if reason := f.text(`SELECT details FROM lead_events WHERE lead_id = ? AND action = 'undelivered'`, lead.ID); !strings.Contains(reason, "ivan@compny.test 5.4.4 Host or domain name not found") {
		t.Errorf("reason = %q", reason)
	}
	if got := f.count(`SELECT COUNT(*) FROM outbox WHERE kind = ?`, leads.TaskUndelivered); got != 2 || f.count(`SELECT COUNT(*) FROM outbox`) != queued+2 {
		t.Errorf("the staff is told %d times", got)
	}
	if got := f.text(`SELECT status FROM leads WHERE id = ?`, lead.ID); got != leads.StatusWaitingClient {
		t.Errorf("a report moved the request to %s", got)
	}
	waiting, _ := f.service.Waiting(ctx)
	if len(waiting) != 1 || !waiting[0].Automatic || !strings.Contains(waiting[0].Note, "отчёт почтового сервера") {
		t.Errorf("the forged report: %+v", waiting)
	}
}

func TestAnOutOfOfficeNote(t *testing.T) {
	f := newFixture(t)
	lead := f.lead("ivan@company.test")
	f.waitingForClient(lead.ID)
	queued := f.count(`SELECT COUNT(*) FROM outbox`)
	f.server.Deliver(compose("ivan@company.test", leads.ReplyAddress(secret, mailbox, lead.ID), "ooo@company.test", "Я в отпуске до 15 сентября.", "Auto-Submitted: auto-replied\n"))
	f.check()
	if got := f.text(`SELECT body FROM lead_messages WHERE lead_id = ? AND channel = 'email' AND direction = 'in'`, lead.ID); got != "[автоответ] Я в отпуске до 15 сентября." {
		t.Errorf("the note in the conversation: %q", got)
	}
	if got := f.text(`SELECT status FROM leads WHERE id = ?`, lead.ID); got != leads.StatusWaitingClient {
		t.Errorf("a robot's note moved the request to %s", got)
	}
	if f.count(`SELECT COUNT(*) FROM outbox`) != queued {
		t.Error("somebody is woken up for a robot's note")
	}
}

func TestFilesOfLetters(t *testing.T) {
	f := newFixture(t)
	lead := f.lead("ivan@company.test")
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{7}, 40<<10)...)
	parts := []part{
		{name: "logo.png", content: png[:2000], inline: true},                  // decoration: dropped without a word
		{name: "screenshot.png", content: png, inline: true},                   // a pasted picture: kept
		{name: "invoice.pdf", content: []byte("MZ\x90\x00 this is a program")}, // a program in disguise
		{name: "../../etc/cron.d/x.txt", content: []byte("plain text\n")},
	}
	for i := 0; i < 5; i++ {
		parts = append(parts, part{name: fmt.Sprintf("doc-%d.pdf", i), content: pdf})
	}
	f.server.Deliver(compose("ivan@company.test", leads.ReplyAddress(secret, mailbox, lead.ID), "files@company.test", "", "", parts...))
	f.check()

	if got := f.text(`SELECT body FROM lead_messages WHERE lead_id = ? AND channel = 'email'`, lead.ID); got != "(без текста — только файлы)" {
		t.Errorf("a letter of files only: %q", got)
	}
	var names []string
	rows, err := f.db.Query(`SELECT filename FROM lead_attachments WHERE lead_id = ? ORDER BY id`, lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		names = append(names, name)
	}
	if got := strings.Join(names, " "); got != "screenshot.png x.txt doc-0.pdf doc-1.pdf doc-2.pdf" {
		t.Errorf("kept: %s", got)
	}
	if f.storedFiles() != MaxLetterFiles {
		t.Errorf("files on disk: %d", f.storedFiles())
	}
	note := f.text(`SELECT details FROM lead_events WHERE lead_id = ? AND action = 'client_replied'`, lead.ID)
	for _, want := range []string{"invoice.pdf (тип не принимается)", "doc-3.pdf (больше пяти файлов в письме)", "doc-4.pdf"} {
		if !strings.Contains(note, want) {
			t.Errorf("the history does not mention %q: %q", want, note)
		}
	}
	if strings.Contains(note, "logo.png") {
		t.Errorf("the logo of a signature is mentioned: %q", note)
	}
}

func TestARequestThatIsGone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	lead := f.lead("ivan@company.test")
	address := leads.ReplyAddress(secret, mailbox, lead.ID)
	if err := f.store.Delete(ctx, lead.ID); err != nil {
		t.Fatal(err)
	}
	f.server.Deliver(compose("ivan@company.test", address, "late@company.test", "Есть новости?", "", part{name: "a.pdf", content: pdf}))
	f.check()
	waiting, _ := f.service.Waiting(ctx)
	if len(waiting) != 1 || !strings.Contains(waiting[0].Note, "K-0001 удалена") || f.storedFiles() != 0 {
		t.Fatalf("waiting = %+v, files on disk: %d", waiting, f.storedFiles())
	}
}

func TestLettersThatCannotBeRead(t *testing.T) {
	f := newFixture(t)
	f.service.opts.maxBytes = 2000
	f.server.Deliver(compose("ivan@company.test", mailbox, "huge@company.test", strings.Repeat("очень много текста ", 500), ""))
	f.server.Deliver([]byte("this is not a letter at all"))
	f.check()
	f.check()
	waiting, _ := f.service.Waiting(context.Background())
	if len(waiting) != 2 {
		t.Fatalf("waiting = %+v", waiting)
	}
	if !strings.Contains(waiting[0].Note, "не удалось разобрать") || !strings.Contains(waiting[1].Note, "больше, чем сервис читает") {
		t.Errorf("notes: %q, %q", waiting[0].Note, waiting[1].Note)
	}
	for _, message := range f.server.Messages() {
		if !message.Flags[`\Seen`] {
			t.Error("a letter that cannot be read will be tried again and again")
		}
	}
}

func TestOldLettersGo(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	old := f.server.Deliver(compose("someone@else.test", mailbox, "old@else.test", "Старое письмо без заявки.", ""))
	f.check()
	fresh := f.server.Deliver(compose("other@else.test", mailbox, "fresh@else.test", "Свежее письмо без заявки.", ""))
	f.server.Backdate(old, time.Now().AddDate(0, 0, -45))

	f.now = noon.AddDate(0, 0, 40)
	f.check()
	client, err := f.service.opts.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Select("INBOX"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.sweep(ctx, client); err != nil {
		t.Fatal(err)
	}
	left := f.server.Messages()
	if len(left) != 1 || left[0].UID != fresh {
		t.Fatalf("the mailbox after the sweep: %+v", left)
	}
	waiting, _ := f.service.Waiting(ctx)
	if len(waiting) != 1 || waiting[0].FromAddress != "other@else.test" {
		t.Fatalf("waiting after the sweep: %+v", waiting)
	}
}

// TestTheServiceListens: no polling — a letter is in the conversation a moment after it
// arrived; and a mail server that restarts is found again.
func TestTheServiceListens(t *testing.T) {
	f := newFixture(t)
	lead := f.lead("ivan@company.test")
	address := leads.ReplyAddress(secret, mailbox, lead.ID)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.service.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the service does not stop")
		}
	}()
	arrived := func(text string) bool {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if f.count(`SELECT COUNT(*) FROM lead_messages WHERE lead_id = ? AND body = ?`, lead.ID, text) == 1 {
				return true
			}
			time.Sleep(50 * time.Millisecond)
		}
		return false
	}
	waitFor := func(what string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !condition() {
			if time.Now().After(deadline) {
				t.Fatalf("%s: not within ten seconds", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	waitFor("connected", func() bool { return f.service.Status(ctx).Connected })
	f.server.Deliver(compose("ivan@company.test", address, "live-1@company.test", "Первое письмо.", ""))
	if !arrived("Первое письмо.") {
		t.Fatal("the first letter did not arrive")
	}

	f.server.Hangup()
	waitFor("the break noticed", func() bool { return !f.service.Status(ctx).Connected || f.service.Status(ctx).LastError != "" })
	f.server.Deliver(compose("ivan@company.test", address, "live-2@company.test", "Письмо после перезапуска сервера.", ""))
	if !arrived("Письмо после перезапуска сервера.") {
		t.Fatalf("the letter after a restart did not arrive; status: %+v", f.service.Status(ctx))
	}
	waitFor("connected again", func() bool { return f.service.Status(ctx).Connected })
}
