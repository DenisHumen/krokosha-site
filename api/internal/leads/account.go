package leads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/loyalty"
)

// Requests as a client sees them in the personal account (docs/architecture.md, 2026-09-24): their
// own requests and inquiries, the public status, the conversation — what they wrote and what was
// answered. Never shown: notes, who of the staff took it, the reasons of a refusal, the spam score,
// the visit it came from.

// ClientSummary is a line of the account's list.
type ClientSummary struct {
	ID        int64         `json:"-"`
	Number    string        `json:"number"`
	Kind      string        `json:"kind"`
	Status    string        `json:"status"`
	Created   time.Time     `json:"created"`
	Updated   time.Time     `json:"updated"`
	Direction string        `json:"direction"`
	Subject   string        `json:"subject,omitempty"`
	Excerpt   string        `json:"excerpt"`
	Discount  loyalty.Offer `json:"-"`
	Amount    *float64      `json:"amount,omitempty"`
	Parent    string        `json:"parent,omitempty"` // an inquiry about this request
	Unread    bool          `json:"unread"`           // an answer or a new status the client has not opened yet
}

// ClientEntry is a line of the conversation as the client sees it.
type ClientEntry struct {
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`                // message | status
	Direction string    `json:"direction,omitempty"` // in — the client's, out — the answer
	Channel   string    `json:"channel,omitempty"`   // form | site | email | telegram | phone
	Body      string    `json:"body,omitempty"`
	Status    string    `json:"status,omitempty"` // for «status»: the new one
	Files     []string  `json:"files,omitempty"`  // names only: files are handed out by the admin area alone
}

// ClientView is one request of the account, with its conversation.
type ClientView struct {
	ClientSummary
	Description string        `json:"description"`
	Budget      string        `json:"budget,omitempty"`
	Timeline    string        `json:"timeline,omitempty"`
	Contact     string        `json:"contact"`
	Method      string        `json:"method"`
	CanWrite    bool          `json:"can_write"`
	Feed        []ClientEntry `json:"feed"`
}

// unread: an answer or a status set by somebody else, later than the client's last look.
const unreadCondition = `(
	EXISTS (SELECT 1 FROM lead_messages m WHERE m.lead_id = l.id AND m.direction = 'out' AND m.created_at > COALESCE(l.client_seen_at, l.created_at))
	OR EXISTS (SELECT 1 FROM lead_events e WHERE e.lead_id = l.id AND e.to_status IS NOT NULL AND e.actor <> 'client'
	           AND e.action <> 'created' AND e.created_at > COALESCE(l.client_seen_at, l.created_at)))`

// ClientLeads lists the requests and inquiries of an account, newest first. Spam is never shown:
// it is not the client's, whatever address it carries.
func (s *Store) ClientLeads(ctx context.Context, clientID int64) ([]ClientSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.kind, l.status, l.created_at, l.updated_at, l.direction, COALESCE(l.subject, ''), LEFT(l.description, 160),
		       l.discount_percent, COALESCE(l.discount_reason, ''), COALESCE(l.discount_detail, ''), l.amount, l.parent_id, `+unreadCondition+`
		FROM leads l WHERE l.client_id = ? AND l.status <> 'spam' AND l.anonymized_at IS NULL
		ORDER BY l.created_at DESC, l.id DESC LIMIT 200`, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClientSummary
	for rows.Next() {
		item, err := scanClientSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanClientSummary(row scanner) (ClientSummary, error) {
	var item ClientSummary
	var amount sql.NullFloat64
	var parent sql.NullInt64
	if err := row.Scan(&item.ID, &item.Kind, &item.Status, &item.Created, &item.Updated, &item.Direction, &item.Subject, &item.Excerpt,
		&item.Discount.Percent, &item.Discount.Reason, &item.Discount.Detail, &amount, &parent, &item.Unread); err != nil {
		return item, err
	}
	item.Number = Number(item.ID)
	if amount.Valid {
		item.Amount = &amount.Float64
	}
	if parent.Valid {
		item.Parent = Number(parent.Int64)
	}
	return item, nil
}

// ClientView reads one request of an account. Somebody else's request is ErrNotFound, exactly like
// one that does not exist.
func (s *Store) ClientView(ctx context.Context, clientID, id int64) (*ClientView, error) {
	view := &ClientView{}
	var budget, timeline sql.NullString
	var amount sql.NullFloat64
	var parent sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT l.id, l.kind, l.status, l.created_at, l.updated_at, l.direction, COALESCE(l.subject, ''), LEFT(l.description, 160),
		       l.discount_percent, COALESCE(l.discount_reason, ''), COALESCE(l.discount_detail, ''), l.amount, l.parent_id, `+unreadCondition+`,
		       l.description, l.budget, l.timeline, l.contact_value, l.contact_method
		FROM leads l WHERE l.id = ? AND l.client_id = ? AND l.status <> 'spam' AND l.anonymized_at IS NULL`, id, clientID).
		Scan(&view.ID, &view.Kind, &view.Status, &view.Created, &view.Updated, &view.Direction, &view.Subject, &view.Excerpt,
			&view.Discount.Percent, &view.Discount.Reason, &view.Discount.Detail, &amount, &parent, &view.Unread,
			&view.Description, &budget, &timeline, &view.Contact, &view.Method)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	view.Number = Number(view.ID)
	view.Budget, view.Timeline = budget.String, timeline.String
	if amount.Valid {
		view.Amount = &amount.Float64
	}
	if parent.Valid {
		view.Parent = Number(parent.Int64)
	}
	view.CanWrite = true

	rows, err := s.db.QueryContext(ctx, `
		SELECT created_at, 'message', direction, channel, body, '', id FROM lead_messages WHERE lead_id = ? AND direction IN ('in', 'out')
		UNION ALL
		SELECT created_at, 'status', '', '', '', to_status, 0 FROM lead_events WHERE lead_id = ? AND to_status IS NOT NULL
		ORDER BY 1, 7`, id, id)
	if err != nil {
		return nil, err
	}
	messages := map[int64]int{} // message id → index in the feed, for the files
	status := ""
	for rows.Next() {
		var entry ClientEntry
		var messageID int64
		if err := rows.Scan(&entry.At, &entry.Kind, &entry.Direction, &entry.Channel, &entry.Body, &entry.Status, &messageID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if entry.Kind == "status" {
			// The same status twice in a row (an answer while waiting) says nothing new.
			if entry.Status == status || entry.Status == StatusSpam {
				continue
			}
			status = entry.Status
		}
		if entry.Direction == "out" && entry.Channel == MethodPhone {
			entry.Body = "" // the owner's record of a call is a note to themselves, not something said to the client
			entry.Kind = "call"
		}
		if messageID > 0 {
			messages[messageID] = len(view.Feed)
		}
		view.Feed = append(view.Feed, entry)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	files, err := s.Attachments(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if index, ok := messages[file.MessageID]; ok {
			view.Feed[index].Files = append(view.Feed[index].Files, file.Filename)
		}
	}
	return view, nil
}

// MarkSeen notes that the client opened a request: its answers are no longer new.
func (s *Store) MarkSeen(ctx context.Context, clientID, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE leads SET client_seen_at = ? WHERE id = ? AND client_id = ?`, s.now().UTC(), id, clientID)
	return err
}

// ClientPost stores what a client wrote in the personal account: the request goes back to work
// if it waited for them, and everybody is told — as with a message in Telegram or a letter.
func (s *Store) ClientPost(ctx context.Context, clientID, id int64, text string) (int64, error) {
	var owner sql.NullInt64
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT client_id, status FROM leads WHERE id = ? AND anonymized_at IS NULL`, id).Scan(&owner, &status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (owner.Int64 != clientID || status == StatusSpam)) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	messageID, err := s.ClientWrote(ctx, id, Incoming{Channel: ChannelSite, Text: text})
	if err == nil {
		err = s.MarkSeen(ctx, clientID, id)
	}
	return messageID, err
}

// LinkByEmail gives an account the requests left with an address it has just proved: that address
// in any letter case, and nothing the table's collation merely takes for it («ánna@» for «anna@»).
func (s *Store) LinkByEmail(ctx context.Context, clientID int64, email string) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE leads SET client_id = ? WHERE client_id IS NULL AND contact_method = 'email' AND LOWER(contact_value) COLLATE utf8mb4_bin = LOWER(?)
		AND contact_value <> '' AND anonymized_at IS NULL AND status <> 'spam'`, clientID, email)
	if err != nil {
		return 0, err
	}
	linked, _ := result.RowsAffected()
	return int(linked), nil
}

// LinkByTelegram gives an account the requests whose «continue in Telegram» this Telegram account
// opened: the bot knows it was them.
func (s *Store) LinkByTelegram(ctx context.Context, clientID, telegramID int64) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE leads l JOIN bot_clients b ON b.lead_id = l.id SET l.client_id = ?
		WHERE b.telegram_id = ? AND l.client_id IS NULL AND l.anonymized_at IS NULL AND l.status <> 'spam'`, clientID, telegramID)
	if err != nil {
		return 0, err
	}
	linked, _ := result.RowsAffected()
	return int(linked), nil
}

// ErrNoClient: there is no such account.
var ErrNoClient = errors.New("no such client")

// ErrOtherClient: the request is in another client's account; it is detached from there first.
var ErrOtherClient = errors.New("the request belongs to another client")

// Attach gives a request to an account, or takes it away with clientID 0 — the owner's decision in
// the admin area (a request by phone, a request left with another address).
func (s *Store) Attach(ctx context.Context, id, clientID int64, actor string) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if clientID > 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients WHERE id = ?`, clientID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNoClient
		}
		// A request in another client's account leaves it only by being detached, on purpose: a
		// mistyped number must not hand somebody's request, and its conversation, to a stranger.
		var current sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT client_id FROM leads WHERE id = ? AND anonymized_at IS NULL FOR UPDATE`, id).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if current.Valid && current.Int64 != clientID {
			return ErrOtherClient
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE leads SET client_id = ?, updated_at = ? WHERE id = ? AND anonymized_at IS NULL`, nullID(clientID), now, id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return ErrNotFound
	}
	details := "отвязана от личного кабинета"
	if clientID > 0 {
		details = fmt.Sprintf("привязана к клиенту #%d", clientID)
	}
	if err := event(ctx, tx, id, now, actor, "client", "", "", details); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed(id)
	return nil
}

// ErrBadAmount: a sum must be a number of money, not below zero.
var ErrBadAmount = errors.New("the amount is not a sum of money")

// SetAmount records what the order came to (nil — not known). Completed orders and their sums make
// the levels of regular clients.
func (s *Store) SetAmount(ctx context.Context, id int64, actor string, amount *float64) error {
	value := sql.NullFloat64{}
	details := "сумма заказа стёрта"
	if amount != nil {
		if *amount < 0 || *amount > 1e9 || math.IsNaN(*amount) || math.IsInf(*amount, 0) {
			return ErrBadAmount
		}
		value = sql.NullFloat64{Float64: math.Round(*amount*100) / 100, Valid: true}
		details = "сумма заказа: " + strconv.FormatFloat(value.Float64, 'f', -1, 64)
	}
	return s.update(ctx, id, actor, "amount", details, `UPDATE leads SET amount = ?, updated_at = ? WHERE id = ?`, value)
}

// SetDiscount sets the discount of a request by hand: any percent, with a note the client sees.
// A discount of the eggs replaced this way returns to the client — it was not given after all.
func (s *Store) SetDiscount(ctx context.Context, id int64, actor string, percent int, note string) error {
	if percent < 0 || percent > 100 {
		return ErrBadAmount
	}
	note = cut(clean(note, false), 100)
	details := fmt.Sprintf("скидка %d%% вручную", percent)
	if note != "" {
		details += " — " + note
	}
	reason := sql.NullString{String: loyalty.ReasonManual, Valid: true}
	return s.update(ctx, id, actor, "discount", details, `
		UPDATE leads SET discount_percent = ?, discount_reason = ?, discount_detail = ?, eggs_receipt = NULL, eggs_span_s = NULL, updated_at = ?
		WHERE id = ?`, percent, reason, null(note))
}

// update runs one change of a request with its line in the history. The query takes its values,
// then the time, then the id.
func (s *Store) update(ctx context.Context, id int64, actor, action, details, query string, values ...any) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, query, append(values, now, id)...)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM leads WHERE id = ?`, id).Scan(&exists); err != nil || exists == 0 {
			return ErrNotFound
		}
	}
	if err := event(ctx, tx, id, now, actor, action, "", "", details); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed(id)
	return nil
}
