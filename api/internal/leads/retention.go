package leads

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"time"
)

// The end of a request's life (brief B10.7, said so on /privacy): requests and the conversation
// are kept for 24 months after the last thing that happened to them, then anonymised — or
// deleted, if the owner prefers. What robots sent does not deserve two years: spam goes after a
// month. Once a day the attachments directory is swept for files nobody knows about.

// Retention is the rule and the things it works on.
type Retention struct {
	Store *Store
	Files *Files // nil — no attachments here
	Log   *slog.Logger

	KeepMonths int  // 0 — keep forever
	Delete     bool // delete expired requests instead of anonymising them
	SpamDays   int  // 0 — keep spam like everything else
}

// RetentionReport says what one pass did.
type RetentionReport struct {
	Anonymized, Deleted, Spam, Files int
}

const retentionBatch = 200

// expired lists requests nothing has happened to since before the given moment.
func (s *Store) expired(ctx context.Context, condition string, before time.Time) ([]int64, error) {
	//nolint:gosec // the condition is one of the constants in RunOnce
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM leads WHERE `+condition+` AND updated_at < ? ORDER BY id LIMIT ?`, before.UTC(), retentionBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Anonymize removes from a request what identifies the person — name, contact, the text, the
// whole conversation with notes and files, the network, the link to the visit — and keeps what
// the statistics are made of: dates, status, direction, budget, where the request came from.
func (s *Store) Anonymize(ctx context.Context, id int64) error {
	token := make([]byte, 16) // the client's link to the request dies with the data
	if _, err := rand.Read(token); err != nil {
		return err
	}
	now := s.now().UTC()
	s.erasing(ctx, id)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	files, err := storedFiles(ctx, tx, id)
	if err != nil {
		return err
	}
	// A completed order stays with its client as a number: the level of a regular client must not
	// fall because old data went (docs/architecture.md, 2026-09-24). The link itself goes — the
	// request no longer knows whose it was.
	if _, err := tx.ExecContext(ctx, `
		UPDATE clients c JOIN leads l ON l.client_id = c.id
		SET c.orders_carried = c.orders_carried + 1, c.spent_carried = c.spent_carried + COALESCE(l.amount, 0)
		WHERE l.id = ? AND l.status = 'done' AND l.kind = 'request' AND l.anonymized_at IS NULL`, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE leads SET name = '—', contact_value = '', description = '', public_token = ?, session_id = NULL, referrer_host = NULL,
		       ip_prefix = '', sections_seen = NULL, spam_reasons = NULL, client_id = NULL, subject = NULL, discount_detail = NULL,
		       anonymized_at = ?
		WHERE id = ? AND anonymized_at IS NULL`, token, now, id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return ErrNotFound
	}
	for _, query := range []string{
		`DELETE FROM lead_messages WHERE lead_id = ?`,
		`DELETE FROM lead_attachments WHERE lead_id = ?`,
		`DELETE FROM outbox WHERE lead_id = ?`,
		// Reasons of refusals and details of events are the owner's free text: it may name the person.
		`UPDATE lead_events SET details = NULL WHERE lead_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE leads SET reject_reason = NULL WHERE id = ?`, id); err != nil {
		return err
	}
	if err := event(ctx, tx, id, now, "system", "anonymized", "", "", ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.removeFiles(files)
}

// RunOnce applies the rule to everything that is due and sweeps the files.
func (r Retention) RunOnce(ctx context.Context) (RetentionReport, error) {
	var report RetentionReport
	now := r.Store.now()
	// A file that could not be removed is not a reason to stop: the sweep below is for that.
	filesLeft := func(err error) error {
		if errors.Is(err, ErrFilesLeft) {
			r.Log.Warn("retention: a file could not be removed yet", "error", err)
			return nil
		}
		return err
	}

	if r.SpamDays > 0 {
		for {
			ids, err := r.Store.expired(ctx, `status = 'spam'`, now.AddDate(0, 0, -r.SpamDays))
			if err != nil {
				return report, err
			}
			for _, id := range ids {
				if err := filesLeft(r.Store.Delete(ctx, id)); err != nil {
					return report, err
				}
				report.Spam++
			}
			if len(ids) < retentionBatch {
				break
			}
		}
	}

	if r.KeepMonths > 0 {
		before := now.AddDate(0, -r.KeepMonths, 0)
		condition := `anonymized_at IS NULL`
		if r.Delete {
			condition = `1 = 1` // what was anonymised under the other rule goes too
		}
		for {
			ids, err := r.Store.expired(ctx, condition, before)
			if err != nil {
				return report, err
			}
			for _, id := range ids {
				if r.Delete {
					err = r.Store.Delete(ctx, id)
				} else {
					err = r.Store.Anonymize(ctx, id)
				}
				if err := filesLeft(err); err != nil {
					return report, err
				}
				if r.Delete {
					report.Deleted++
				} else {
					report.Anonymized++
				}
			}
			if len(ids) < retentionBatch {
				break
			}
		}
	}

	if r.Files != nil {
		removed, err := r.Files.Sweep(func(name string) (bool, error) { return r.Store.KnowsFile(ctx, name) }, now.Add(-24*time.Hour))
		report.Files = removed
		if err != nil {
			return report, err
		}
	}
	return report, nil
}

// Run applies the rule shortly after the start and then once a day, until the context ends.
func (r Retention) Run(ctx context.Context) {
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		report, err := r.RunOnce(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			r.Log.Error("retention of requests failed", "error", err)
		case report != RetentionReport{}:
			r.Log.Info("retention of requests", "anonymized", report.Anonymized, "deleted", report.Deleted, "spam", report.Spam, "orphan_files", report.Files)
		}
		timer.Reset(24 * time.Hour)
	}
}
