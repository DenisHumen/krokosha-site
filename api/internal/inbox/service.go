package inbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/imap"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// What happened to a letter.
const (
	OutcomeMatched   = "matched"   // joined the conversation of a request
	OutcomeUnmatched = "unmatched" // waits for a person's decision
	OutcomeBounce    = "bounce"    // a delivery report about a letter of ours, recorded with its request
	OutcomeIgnored   = "ignored"   // our own letter, or one a person threw away
)

// Limits of a letter.
const (
	// MaxLetterBytes is what the service is ready to download: the mail server takes letters
	// up to 25 MB, and base64 makes attachments a third larger than they are.
	MaxLetterBytes = 36 << 20
	// MaxLetterFiles is how many files of one letter are kept with a request.
	MaxLetterFiles = 5
	// Pictures shown inside a letter that are smaller than this are decoration: the logo of a
	// signature, the icons of social networks. A pasted screenshot is larger.
	decorationBytes = 24 << 10
	excerptBytes    = 4000
)

// Options of the service.
type Options struct {
	// Dial connects to the mailbox and signs in.
	Dial   func(ctx context.Context) (*imap.Client, error)
	DB     *sql.DB
	Leads  *leads.Store
	Files  *leads.Files // nil — files of letters are not kept
	Secret []byte       // signs the numbers of requests into addresses (leads.ReplyAddress)
	Inbox  string       // the service mailbox itself, «leads@example.com»
	Log    *slog.Logger
	Now    func() time.Time
	// KeepDays is how long a letter without a request waits for a decision.
	KeepDays int
	// IdleFor is how long the service listens before it asks again by itself.
	IdleFor time.Duration
	// Kick wakes the sender of notifications: a letter that joined a request is announced at once.
	Kick func()

	// For tests: the first pause before connecting again, and the size of a letter worth reading.
	retry    time.Duration
	maxBytes int64
}

// Service reads the mailbox.
type Service struct {
	opts Options

	mu     sync.Mutex
	status Status
}

// Status is what the «system status» screen shows about incoming mail.
type Status struct {
	Connected   bool
	Since       time.Time // of the current connection
	CheckedAt   time.Time // when the mailbox was last looked into
	LastLetter  time.Time
	LastError   string
	LastErrorAt time.Time
	Unmatched   int // letters that wait for a decision
}

// New builds the service.
func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.KeepDays <= 0 {
		opts.KeepDays = 30
	}
	if opts.IdleFor <= 0 {
		opts.IdleFor = 9 * time.Minute
	}
	if opts.Kick == nil {
		opts.Kick = func() {}
	}
	if opts.retry <= 0 {
		opts.retry = 5 * time.Second
	}
	if opts.maxBytes <= 0 {
		opts.maxBytes = MaxLetterBytes
	}
	opts.Inbox = strings.ToLower(opts.Inbox)
	return &Service{opts: opts}
}

// Run reads the mailbox until the context is done: everything that waits, then whatever comes,
// the moment it comes. A connection that breaks is made again, with growing pauses.
func (s *Service) Run(ctx context.Context) {
	pause := s.opts.retry
	for ctx.Err() == nil {
		started := s.opts.Now()
		err := s.session(ctx)
		if ctx.Err() != nil {
			return
		}
		s.note(func(status *Status) {
			status.Connected = false
			status.LastError, status.LastErrorAt = err.Error(), s.opts.Now()
		})
		s.opts.Log.Warn("inbox: the connection to the mailbox ended", "error", err, "retry_in", pause.String())
		if s.opts.Now().Sub(started) > 2*time.Minute {
			pause = s.opts.retry // it worked for a while: not a server that refuses us
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if pause *= 2; pause > 5*time.Minute {
			pause = 5 * time.Minute
		}
	}
}

func (s *Service) session(ctx context.Context) error {
	client, err := s.opts.Dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	box, err := client.Select("INBOX")
	if err != nil {
		return err
	}
	s.note(func(status *Status) { status.Connected, status.Since = true, s.opts.Now() })
	s.opts.Log.Info("inbox: reading the mailbox", "mailbox", s.opts.Inbox, "letters", box.Exists, "idle", client.Has("IDLE"))

	var swept time.Time
	for {
		if err := s.drain(ctx, client, box); err != nil {
			return err
		}
		if now := s.opts.Now(); now.Sub(swept) > 12*time.Hour {
			swept = now
			if err := s.sweep(ctx, client); err != nil {
				s.opts.Log.Warn("inbox: old letters could not be removed", "error", err)
			}
		}
		if _, err := client.Idle(ctx, s.opts.IdleFor); err != nil {
			return err
		}
	}
}

// CheckOnce reads what waits in the mailbox right now and returns.
func (s *Service) CheckOnce(ctx context.Context) error {
	client, err := s.opts.Dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Logout() }()
	box, err := client.Select("INBOX")
	if err != nil {
		return err
	}
	return s.drain(ctx, client, box)
}

// drain handles every letter nobody looked at yet.
func (s *Service) drain(ctx context.Context, client *imap.Client, box imap.Mailbox) error {
	uids, err := client.Search("UNSEEN")
	if err != nil {
		return err
	}
	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.handle(ctx, client, box, uid); err != nil {
			return fmt.Errorf("letter %d: %w", uid, err)
		}
		s.note(func(status *Status) { status.LastLetter = s.opts.Now() })
	}
	s.note(func(status *Status) { status.CheckedAt, status.LastError = s.opts.Now(), "" })
	return nil
}

// record is a row of the journal.
type record struct {
	Key     [32]byte
	Outcome string
	LeadID  int64
	UID     uint32
	Box     imap.Mailbox
	Letter  *Letter
	Files   int
	Note    string
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// journal writes a letter down; false means it was written down before.
func (s *Service) journal(ctx context.Context, db execer, r record) (bool, error) {
	letter := r.Letter
	if letter == nil {
		letter = &Letter{}
	}
	personal := r.Outcome == OutcomeUnmatched // a letter that found its request leaves nothing of the person here
	keep := func(value string, limit int) any {
		if !personal || value == "" {
			return nil
		}
		return excerpt(value, limit)
	}
	var uid, validity, leadID any
	if r.Outcome == OutcomeUnmatched && r.UID != 0 {
		uid, validity = r.UID, r.Box.UIDValidity
	}
	if r.LeadID != 0 {
		leadID = r.LeadID
	}
	result, err := db.ExecContext(ctx, `
		INSERT IGNORE INTO inbox_letters
		       (received_at, message_key, outcome, lead_id, uid, uid_validity, message_id, from_name, from_address, subject, excerpt, files, automatic, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''))`,
		s.opts.Now().UTC(), r.Key[:], r.Outcome, leadID, uid, validity, keep(letter.MessageID, 250), keep(letter.From.Name, 250),
		keep(letter.From.Address, 250), keep(letter.Subject, 250), keep(letter.Text, excerptBytes), r.Files, letter.Automatic, excerpt(r.Note, 250))
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	return inserted > 0, err
}

// handle decides what one letter is and where it belongs.
func (s *Service) handle(ctx context.Context, client *imap.Client, box imap.Mailbox, uid uint32) error {
	raw, err := client.Fetch(uid, s.opts.maxBytes)
	var tooBig *imap.TooBigError
	switch {
	case errors.Is(err, imap.ErrNotFound):
		return nil
	case errors.As(err, &tooBig):
		note := fmt.Sprintf("письмо занимает %s — больше, чем сервис читает. Оно лежит в ящике %s", leads.FormatSize(tooBig.Size), s.opts.Inbox)
		key := sha256.Sum256(fmt.Appendf(nil, "uid:%d:%d", box.UIDValidity, uid))
		if _, err := s.journal(ctx, s.opts.DB, record{Key: key, Outcome: OutcomeUnmatched, UID: uid, Box: box, Note: note}); err != nil {
			return err
		}
		return client.AddFlags(uid, `\Seen`)
	case err != nil:
		return err
	}

	letter, err := parseSafely(raw)
	if err != nil {
		s.opts.Log.Warn("inbox: a letter could not be read", "uid", uid, "error", err)
		key := sha256.Sum256(raw)
		if _, err := s.journal(ctx, s.opts.DB, record{Key: key, Outcome: OutcomeUnmatched, UID: uid, Box: box, Note: "письмо не удалось разобрать — оно лежит в ящике " + s.opts.Inbox}); err != nil {
			return err
		}
		return client.AddFlags(uid, `\Seen`)
	}
	key := messageKey(letter, raw)

	// Handled before? The same letter delivered twice, or a stop between the database and the mailbox.
	var outcome string
	err = s.opts.DB.QueryRowContext(ctx, `SELECT outcome FROM inbox_letters WHERE message_key = ?`, key[:]).Scan(&outcome)
	switch {
	case err == nil && outcome == OutcomeUnmatched:
		return client.AddFlags(uid, `\Seen`)
	case err == nil:
		return client.Delete(uid)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	r := record{Key: key, UID: uid, Box: box, Letter: letter, Files: len(letter.Files)}
	switch {
	case letter.From.Address == s.opts.Inbox:
		r.Outcome = OutcomeIgnored // our own letter came back by some rule of forwarding: not a conversation
	case letter.Bounce != nil:
		r.Outcome, r.LeadID, r.Note, err = s.bounced(ctx, letter, raw, key)
	default:
		r.Outcome, r.LeadID, r.Note, err = s.place(ctx, letter, key)
	}
	if err != nil {
		return err
	}
	switch r.Outcome {
	case OutcomeMatched, OutcomeBounce:
		// Written down together with the message, in one transaction.
		s.opts.Log.Info("inbox: a letter joined its request", "lead", leads.Number(r.LeadID), "kind", r.Outcome, "files", r.Files)
		s.opts.Kick()
		return client.Delete(uid)
	case OutcomeIgnored:
		if _, err := s.journal(ctx, s.opts.DB, r); err != nil {
			return err
		}
		return client.Delete(uid)
	default:
		if _, err := s.journal(ctx, s.opts.DB, r); err != nil {
			return err
		}
		s.opts.Log.Info("inbox: a letter without a request waits in the admin area", "automatic", letter.Automatic)
		return client.AddFlags(uid, `\Seen`)
	}
}

// parseSafely: a letter is hostile input, and a parser that panics on it must not take the
// whole service down.
func parseSafely(raw []byte) (letter *Letter, err error) {
	defer func() {
		if caught := recover(); caught != nil {
			letter, err = nil, fmt.Errorf("the parser gave up: %v", caught)
		}
	}()
	return Parse(raw)
}

func messageKey(letter *Letter, raw []byte) [32]byte {
	if letter.MessageID != "" {
		return sha256.Sum256([]byte("id:" + letter.MessageID))
	}
	return sha256.Sum256(raw)
}

var errHandledBefore = errors.New("the letter was handled before")

// place finds the request a person's letter belongs to and stores it there.
func (s *Service) place(ctx context.Context, letter *Letter, key [32]byte) (outcome string, leadID int64, note string, err error) {
	how := ""
	for _, address := range letter.Recipients {
		if id, ok := leads.ParseReplyAddress(s.opts.Secret, address); ok {
			leadID = id
			break
		}
	}
	if leadID == 0 && letter.From.Address != "" {
		// No number, but a client we know: their latest request (brief B10.5).
		id, err := s.opts.Leads.ByEmail(ctx, letter.From.Address)
		switch {
		case err == nil:
			leadID, how = id, "письмо без номера заявки — привязано по адресу отправителя"
		case !errors.Is(err, leads.ErrNotFound):
			return "", 0, "", err
		}
	}
	if leadID == 0 {
		return OutcomeUnmatched, 0, "", nil
	}
	err = s.deliver(ctx, leadID, letter, how, "", func(ctx context.Context, tx *sql.Tx, files int) error {
		inserted, err := s.journal(ctx, tx, record{Key: key, Outcome: OutcomeMatched, LeadID: leadID, Letter: letter, Files: files})
		if err == nil && !inserted {
			err = errHandledBefore
		}
		return err
	})
	switch {
	case errors.Is(err, leads.ErrNotFound):
		return OutcomeUnmatched, 0, fmt.Sprintf("заявка %s удалена или обезличена", leads.Number(leadID)), nil
	case errors.Is(err, leads.ErrEmptyText):
		return OutcomeUnmatched, 0, fmt.Sprintf("письмо к заявке %s без текста и без файлов, которые можно принять", leads.Number(leadID)), nil
	case errors.Is(err, errHandledBefore):
		return OutcomeMatched, leadID, "", nil
	case err != nil:
		return "", 0, "", err
	}
	return OutcomeMatched, leadID, "", nil
}

// deliver stores a letter in the conversation of a request: the text, the files that pass the
// inspection, and — through written — the row of the journal: together or not at all.
func (s *Service) deliver(ctx context.Context, leadID int64, letter *Letter, how, by string, written func(ctx context.Context, tx *sql.Tx, files int) error) error {
	uploads, dropped := s.saveFiles(letter.Files)
	notes := []string{}
	if how != "" {
		notes = append(notes, how)
	}
	if by != "" {
		notes = append(notes, "привязано вручную: "+by)
	}
	if lead, err := s.opts.Leads.Get(ctx, leadID); err == nil && lead.ContactMethod == leads.MethodEmail &&
		!strings.EqualFold(lead.ContactValue, letter.From.Address) && letter.From.Address != "" {
		notes = append(notes, "с адреса "+letter.From.Address)
	}
	if len(dropped) > 0 {
		notes = append(notes, "не сохранено: "+strings.Join(dropped, ", "))
	}
	text := letter.Text
	if letter.Automatic {
		text = "[автоответ] " + text
	}
	_, err := s.opts.Leads.ClientWrote(ctx, leadID, leads.Incoming{
		Channel: leads.ChannelEmail, Text: text, Files: uploads, EmailMessageID: letter.MessageID,
		Automatic: letter.Automatic, Note: strings.Join(notes, "; "),
		InTx: func(ctx context.Context, tx *sql.Tx, _ int64) error { return written(ctx, tx, len(uploads)) },
	})
	if err != nil {
		for _, upload := range uploads {
			_ = s.opts.Files.Remove(upload.StoredAs)
		}
	}
	return err
}

// saveFiles keeps the files of a letter that may come with a request — by the rules of the
// contact form: the type is told by the content, the size is limited (brief B10.7).
func (s *Service) saveFiles(files []File) (uploads []leads.Upload, dropped []string) {
	for _, file := range files {
		name := leads.CleanFilename(file.Name)
		switch {
		case file.Inline && !file.TooBig && len(file.Content) < decorationBytes:
			continue // the logo of a signature is not worth a word
		case s.opts.Files == nil:
			dropped = append(dropped, name+" (файлы не принимаются)")
		case file.TooBig:
			dropped = append(dropped, name+" (больше "+leads.FormatSize(leads.MaxAttachmentBytes)+")")
		case len(uploads) >= MaxLetterFiles:
			dropped = append(dropped, name+" (больше пяти файлов в письме)")
		default:
			kind, err := leads.Inspect(name, int64(len(file.Content)), bytes.NewReader(file.Content))
			if err != nil {
				dropped = append(dropped, name+" (тип не принимается)")
				continue
			}
			upload, err := s.opts.Files.Save(name, kind, bytes.NewReader(file.Content))
			if err != nil {
				s.opts.Log.Warn("inbox: a file of a letter could not be saved", "error", err)
				dropped = append(dropped, name+" (не удалось сохранить)")
				continue
			}
			uploads = append(uploads, upload)
		}
	}
	if len(dropped) > 8 {
		dropped = append(dropped[:8], fmt.Sprintf("и ещё %d", len(dropped)-8))
	}
	return uploads, dropped
}

var reStatusInText = regexp.MustCompile(`\b([245]\.\d{1,3}\.\d{1,3})\b`)

// bounced records a delivery report with the request whose letter did not arrive. Which request
// that was is believed only to the signed address of the returned letter: «K-0042» in a header
// can be typed by anybody.
func (s *Service) bounced(ctx context.Context, letter *Letter, raw []byte, key [32]byte) (outcome string, leadID int64, note string, err error) {
	bounce := letter.Bounce
	candidates := []string{bounce.OriginalReplyTo}
	candidates = append(candidates, reAddress.FindAllString(strings.ToLower(string(raw[:min(len(raw), 256<<10)])), 200)...)
	for _, address := range candidates {
		if id, ok := leads.ParseReplyAddress(s.opts.Secret, address); ok {
			leadID = id
			break
		}
	}
	if leadID == 0 {
		return OutcomeUnmatched, 0, "отчёт почтового сервера о доставке — к какой заявке, не сказано", nil
	}
	if bounce.Status == "" {
		if m := reStatusInText.FindStringSubmatch(letter.Full); m != nil {
			bounce.Status = m[1]
		}
	}
	if strings.HasPrefix(bounce.Status, "2") {
		return OutcomeIgnored, 0, "", nil // «delivered»: good to know, nothing to do
	}
	reason := strings.TrimSpace(strings.Join([]string{bounce.Recipient, bounce.Status, bounce.Diagnostic}, " "))
	if reason == "" {
		reason = excerpt(letter.Full, 200)
	}
	if !bounce.Failed() {
		reason = "пока не доставлено, сервер пробует ещё: " + reason
	}
	originalID := ""
	if bounce.Failed() {
		originalID = bounce.OriginalID
	}
	err = s.opts.Leads.Undelivered(ctx, leadID, originalID, reason, func(ctx context.Context, tx *sql.Tx) error {
		inserted, err := s.journal(ctx, tx, record{Key: key, Outcome: OutcomeBounce, LeadID: leadID, Letter: letter})
		if err == nil && !inserted {
			err = errHandledBefore
		}
		return err
	})
	switch {
	case errors.Is(err, leads.ErrNotFound):
		return OutcomeIgnored, 0, "", nil // the request is gone, and so is the reason to tell anybody
	case errors.Is(err, errHandledBefore):
		return OutcomeBounce, leadID, "", nil
	case err != nil:
		return "", 0, "", err
	}
	return OutcomeBounce, leadID, "", nil
}

// sweep removes letters nobody decided about in time — from the mailbox and from the journal —
// and the journal's old rows: they are only there to recognise a letter that comes twice.
func (s *Service) sweep(ctx context.Context, client *imap.Client) error {
	before := s.opts.Now().UTC().AddDate(0, 0, -s.opts.KeepDays)
	uids, err := client.Search("SEEN BEFORE " + before.Format("2-Jan-2006"))
	if err != nil {
		return err
	}
	for _, uid := range uids {
		if err := client.Delete(uid); err != nil {
			return err
		}
	}
	result, err := s.opts.DB.ExecContext(ctx, `DELETE FROM inbox_letters WHERE received_at < ?`, before)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows > 0 || len(uids) > 0 {
		s.opts.Log.Info("inbox: old letters removed", "from_mailbox", len(uids), "from_journal", rows)
	}
	return nil
}

func (s *Service) note(change func(status *Status)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&s.status)
}

// Status tells how reading the mailbox goes.
func (s *Service) Status(ctx context.Context) Status {
	s.mu.Lock()
	status := s.status
	s.mu.Unlock()
	_ = s.opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox_letters WHERE outcome = ?`, OutcomeUnmatched).Scan(&status.Unmatched)
	return status
}
