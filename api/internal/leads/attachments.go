package leads

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"
)

// Attachment is a file that came with a request: what the database knows about it.
type Attachment struct {
	ID        int64
	LeadID    int64
	MessageID int64 // the message it came with; 0 — none
	CreatedAt time.Time
	Filename  string // the sender's name for it, cleaned; still untrusted text
	Kind      string
	Size      int64
	SHA256    []byte // of the content: Telegram is not sent the same file twice
	StoredAs  string
}

// ErrFilesLeft: the rows are gone, some files are still on disk. The daily sweep removes files
// without rows, so this is something to log, not something to show as a failure.
var ErrFilesLeft = errors.New("some files of the deleted request are still on disk; the daily sweep will remove them")

const attachmentColumns = `id, lead_id, COALESCE(message_id, 0), created_at, filename, kind, size, sha256, stored_as`

func scanAttachment(row interface{ Scan(...any) error }) (Attachment, error) {
	var file Attachment
	err := row.Scan(&file.ID, &file.LeadID, &file.MessageID, &file.CreatedAt, &file.Filename, &file.Kind, &file.Size, &file.SHA256, &file.StoredAs)
	return file, err
}

// Attachments lists the files of a request, oldest first.
func (s *Store) Attachments(ctx context.Context, leadID int64) ([]Attachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+attachmentColumns+` FROM lead_attachments WHERE lead_id = ? ORDER BY id`, leadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		file, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, file)
	}
	return out, rows.Err()
}

// MessageFiles lists the files that came with one message of a request.
func (s *Store) MessageFiles(ctx context.Context, leadID, messageID int64) ([]Attachment, error) {
	files, err := s.Attachments(ctx, leadID)
	if err != nil {
		return nil, err
	}
	var out []Attachment
	for _, file := range files {
		if file.MessageID == messageID {
			out = append(out, file)
		}
	}
	return out, nil
}

// OpenAttachment finds a file of this very request and opens it. Asking for a file through
// another request's address finds nothing.
func (s *Store) OpenAttachment(ctx context.Context, leadID, id int64) (*Attachment, *os.File, error) {
	if s.files == nil {
		return nil, nil, ErrNotFound
	}
	file, err := scanAttachment(s.db.QueryRowContext(ctx, `SELECT `+attachmentColumns+` FROM lead_attachments WHERE id = ? AND lead_id = ?`, id, leadID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	content, err := s.files.Open(file.StoredAs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return &file, content, nil
}

// storedFiles returns the names on disk of a request's files, to remove them once the rows are gone.
func storedFiles(ctx context.Context, tx *sql.Tx, leadID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT stored_as FROM lead_attachments WHERE lead_id = ?`, leadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// removeFiles deletes files whose rows were just deleted.
func (s *Store) removeFiles(names []string) error {
	if s.files == nil || len(names) == 0 {
		return nil
	}
	var failed error
	for _, name := range names {
		if err := s.files.Remove(name); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	if failed != nil {
		return errors.Join(ErrFilesLeft, failed)
	}
	return nil
}

// KnowsFile reports whether a file on disk belongs to some request — the question of the sweep.
func (s *Store) KnowsFile(ctx context.Context, storedAs string) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lead_attachments WHERE stored_as = ?`, storedAs).Scan(&found)
	return found > 0, err
}
