package inbox

import (
	"context"
	"database/sql"
	"errors"
	"net/mail"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/imap"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// Letters without a request (brief B10.5): nobody could tell whose they are, so a person does.
// The letter itself waits in the mailbox; nothing of it is unpacked until somebody says which
// request it belongs to.

// Waiting is such a letter, as the admin area shows it. Every text in it is untrusted.
type Waiting struct {
	ID          int64
	ReceivedAt  time.Time
	FromName    string
	FromAddress string
	Subject     string
	Excerpt     string
	Files       int
	Automatic   bool
	Note        string
}

// ErrGone: somebody else decided about the letter a moment ago.
var ErrGone = errors.New("the letter does not wait for a decision any more")

// Waiting lists the letters that wait, newest first.
func (s *Service) Waiting(ctx context.Context) ([]Waiting, error) {
	rows, err := s.opts.DB.QueryContext(ctx, `
		SELECT id, received_at, COALESCE(from_name, ''), COALESCE(from_address, ''), COALESCE(subject, ''), COALESCE(excerpt, ''), files, automatic, COALESCE(note, '')
		  FROM inbox_letters WHERE outcome = ? ORDER BY id DESC LIMIT 200`, OutcomeUnmatched)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Waiting
	for rows.Next() {
		var letter Waiting
		if err := rows.Scan(&letter.ID, &letter.ReceivedAt, &letter.FromName, &letter.FromAddress, &letter.Subject, &letter.Excerpt,
			&letter.Files, &letter.Automatic, &letter.Note); err != nil {
			return nil, err
		}
		out = append(out, letter)
	}
	return out, rows.Err()
}

type waitingRow struct {
	Waiting
	UID, UIDValidity sql.NullInt64
	MessageID        string
}

func (s *Service) waiting(ctx context.Context, id int64) (*waitingRow, error) {
	var row waitingRow
	err := s.opts.DB.QueryRowContext(ctx, `
		SELECT id, COALESCE(from_name, ''), COALESCE(from_address, ''), COALESCE(subject, ''), COALESCE(excerpt, ''), uid, uid_validity, COALESCE(message_id, '')
		  FROM inbox_letters WHERE id = ? AND outcome = ?`, id, OutcomeUnmatched).
		Scan(&row.ID, &row.FromName, &row.FromAddress, &row.Subject, &row.Excerpt, &row.UID, &row.UIDValidity, &row.MessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGone
	}
	return &row, err
}

// open finds the letter in the mailbox again. Zero means it is not there any more.
func (s *Service) open(ctx context.Context, row *waitingRow) (*imap.Client, uint32, error) {
	client, err := s.opts.Dial(ctx)
	if err != nil {
		return nil, 0, err
	}
	box, err := client.Select("INBOX")
	if err != nil {
		_ = client.Close()
		return nil, 0, err
	}
	if row.UID.Valid && row.UIDValidity.Valid && uint32(row.UIDValidity.Int64) == box.UIDValidity { //nolint:gosec // what the mailbox said, stored and read back
		return client, uint32(row.UID.Int64), nil //nolint:gosec // same
	}
	// The mailbox was rebuilt since (a move to another server): the numbers mean nothing now.
	if quoted, err := imap.Quote("<" + row.MessageID + ">"); err == nil && row.MessageID != "" {
		if uids, err := client.Search("HEADER Message-ID " + quoted); err == nil && len(uids) > 0 {
			return client, uids[len(uids)-1], nil
		}
	}
	return client, 0, nil
}

// Attach puts a waiting letter into the conversation of a request, files included, and removes
// it from the mailbox. actor is who decided.
func (s *Service) Attach(ctx context.Context, id, leadID int64, actor string) error {
	row, err := s.waiting(ctx, id)
	if err != nil {
		return err
	}
	client, uid, err := s.open(ctx, row)
	if err != nil {
		return err
	}
	defer func() { _ = client.Logout() }()

	// What the list showed is all there is when the letter itself is gone from the mailbox.
	letter := &Letter{From: mail.Address{Name: row.FromName, Address: row.FromAddress}, Subject: row.Subject, Text: row.Excerpt, MessageID: row.MessageID}
	if uid != 0 {
		raw, err := client.Fetch(uid, s.opts.maxBytes)
		switch {
		case err == nil:
			if parsed, err := parseSafely(raw); err == nil {
				letter = parsed
			}
		case errors.Is(err, imap.ErrNotFound):
			uid = 0
		default:
			var tooBig *imap.TooBigError
			if !errors.As(err, &tooBig) {
				return err
			}
		}
	}
	letter.Automatic = false // a person says this belongs to the conversation: treat it as a message

	err = s.deliver(ctx, leadID, letter, "", actor, func(ctx context.Context, tx *sql.Tx, files int) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE inbox_letters
			   SET outcome = ?, lead_id = ?, files = ?, uid = NULL, uid_validity = NULL, message_id = NULL,
			       from_name = NULL, from_address = NULL, subject = NULL, excerpt = NULL, note = NULL
			 WHERE id = ? AND outcome = ?`, OutcomeMatched, leadID, files, id, OutcomeUnmatched)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed == 0 {
			return ErrGone
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.opts.Log.Info("inbox: a letter was attached to a request by hand", "lead", leads.Number(leadID), "by", actor)
	if uid != 0 {
		if err := client.Delete(uid); err != nil {
			// It is marked as read, so it is not handled twice; the sweep removes it later.
			s.opts.Log.Warn("inbox: an attached letter could not be removed from the mailbox", "error", err)
		}
	}
	return nil
}

// Discard throws a waiting letter away: from the mailbox and, but for the mark that it was
// seen, from the journal.
func (s *Service) Discard(ctx context.Context, id int64) error {
	row, err := s.waiting(ctx, id)
	if err != nil {
		return err
	}
	client, uid, err := s.open(ctx, row)
	if err != nil {
		return err
	}
	defer func() { _ = client.Logout() }()
	if uid != 0 {
		if err := client.Delete(uid); err != nil && !errors.Is(err, imap.ErrNotFound) {
			return err
		}
	}
	_, err = s.opts.DB.ExecContext(ctx, `
		UPDATE inbox_letters
		   SET outcome = ?, uid = NULL, uid_validity = NULL, message_id = NULL, from_name = NULL, from_address = NULL, subject = NULL, excerpt = NULL, note = NULL
		 WHERE id = ? AND outcome = ?`, OutcomeIgnored, id, OutcomeUnmatched)
	return err
}
