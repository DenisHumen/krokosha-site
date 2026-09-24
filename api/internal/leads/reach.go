package leads

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Where an answer to a request goes (docs/architecture.md, 2026-09-24): everywhere the client can
// be reached, so that nobody misses an answer however they wrote to us. A request in a personal
// account is answered in the account, to the account's address and to its Telegram; every request
// also goes back the ways the client came — the address of the form and the addresses they wrote
// from, the Telegram that opened the request's link into the bot. The staff may leave a channel
// out. Each target is a delivery of its own (lead_deliveries), with a status of its own: a bounced
// letter does not hide that the same answer reached Telegram.

// Target is one place an answer goes.
type Target struct {
	Channel string // ChannelEmail | ChannelTelegram
	// To is the address, or the Telegram chat as a number. "" for Telegram: the chat of the
	// request's link into the bot, once the client opens it — a bot cannot write first.
	To string
	// Why the client is reached there, for the staff: «кабинет», «форма», «письмо клиента»,
	// «ссылка в бот».
	Why string
}

// Reach is everywhere an answer to a request can go.
type Reach struct {
	// Account: the request is in a personal account that is not blocked; an answer shows there.
	Account bool
	Targets []Target
	// Phone of the form: what the staff call — nothing a machine can deliver.
	Phone string
}

// Channels are the channels of the targets, each once, in their order.
func (r Reach) Channels() []string {
	var out []string
	for _, target := range r.Targets {
		if !contains(out, target.Channel) {
			out = append(out, target.Channel)
		}
	}
	return out
}

// Has tells whether a channel reaches the client.
func (r Reach) Has(channel string) bool {
	for _, target := range r.Targets {
		if target.Channel == channel {
			return true
		}
	}
	return false
}

// Only keeps the targets of the chosen channels; none chosen — all of them.
func (r Reach) Only(channels []string) []Target {
	if len(channels) == 0 {
		return r.Targets
	}
	var out []Target
	for _, target := range r.Targets {
		if contains(channels, target.Channel) {
			out = append(out, target)
		}
	}
	return out
}

// Nothing: no machine can deliver an answer and there is no account to show it in — a request by
// phone. The text of such an «answer» is the record of the call.
func (r Reach) Nothing() bool { return !r.Account && len(r.Targets) == 0 }

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// rowsQuerier is a transaction or the pool, for reads of more than one row.
type rowsQuerier interface {
	querier
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Reach tells where an answer to a request would go now.
func (s *Store) Reach(ctx context.Context, id int64) (Reach, error) {
	return reachOf(ctx, s.db, id)
}

func reachOf(ctx context.Context, q rowsQuerier, id int64) (Reach, error) {
	var reach Reach
	var method, value string
	var clientID sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT contact_method, contact_value, client_id FROM leads WHERE id = ? AND anonymized_at IS NULL`, id).
		Scan(&method, &value, &clientID)
	if errors.Is(err, sql.ErrNoRows) {
		return reach, ErrNotFound
	}
	if err != nil {
		return reach, err
	}
	seen := map[string]bool{}
	add := func(channel, to, why string) {
		key := channel + ":" + strings.ToLower(to)
		if seen[key] {
			return
		}
		seen[key] = true
		reach.Targets = append(reach.Targets, Target{Channel: channel, To: to, Why: why})
	}

	// The account first: a client with one hears of an answer every way they sign in with.
	if clientID.Valid {
		var email sql.NullString
		var telegram sql.NullInt64
		var blocked bool
		err := q.QueryRowContext(ctx, `SELECT email, telegram_id, disabled_at IS NOT NULL FROM clients WHERE id = ?`, clientID.Int64).
			Scan(&email, &telegram, &blocked)
		switch {
		case errors.Is(err, sql.ErrNoRows): // deleted a moment ago: the request is nobody's
		case err != nil:
			return reach, err
		case !blocked:
			reach.Account = true
			if email.String != "" {
				add(ChannelEmail, email.String, "кабинет")
			}
			if telegram.Int64 != 0 {
				add(ChannelTelegram, strconv.FormatInt(telegram.Int64, 10), "кабинет")
			}
		}
	}

	switch method {
	case MethodEmail:
		if value != "" {
			add(ChannelEmail, value, "форма")
		}
	case MethodPhone:
		reach.Phone = value
	}

	// The addresses the client's letters came from: an answer goes back where they wrote from.
	rows, err := q.QueryContext(ctx, `
		SELECT from_address FROM lead_messages
		WHERE lead_id = ? AND direction = 'in' AND channel = 'email' AND from_address IS NOT NULL AND from_address <> ''
		GROUP BY from_address ORDER BY MIN(id)`, id)
	if err != nil {
		return reach, err
	}
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			_ = rows.Close()
			return reach, err
		}
		add(ChannelEmail, address, "письмо клиента")
	}
	if err := rows.Close(); err != nil {
		return reach, err
	}

	// The Telegram that opened the request's link into the bot.
	var chat int64
	err = q.QueryRowContext(ctx, `SELECT telegram_id FROM bot_clients WHERE lead_id = ?`, id).Scan(&chat)
	switch {
	case err == nil:
		add(ChannelTelegram, strconv.FormatInt(chat, 10), "ссылка в бот")
	case !errors.Is(err, sql.ErrNoRows):
		return reach, err
	case method == MethodTelegram && !reach.Has(ChannelTelegram):
		// A Telegram name in the form and the link not opened yet: the answer waits for it there.
		add(ChannelTelegram, "", "ссылка в бот")
	}
	return reach, nil
}

// --- deliveries --------------------------------------------------------------------------------------

// Delivery is one target of one answer.
type Delivery struct {
	ID        int64
	MessageID int64
	LeadID    int64
	Channel   string
	To        string
	Status    string // queued | sent | failed
	SentParts int
}

const deliveryColumns = `id, message_id, lead_id, channel, target, status, sent_parts`

func scanDelivery(row interface{ Scan(...any) error }) (Delivery, error) {
	var d Delivery
	err := row.Scan(&d.ID, &d.MessageID, &d.LeadID, &d.Channel, &d.To, &d.Status, &d.SentParts)
	return d, err
}

// Delivery reads one delivery; ErrNotFound when it is gone (the request was deleted).
func (s *Store) Delivery(ctx context.Context, id int64) (Delivery, error) {
	d, err := scanDelivery(s.db.QueryRowContext(ctx, `SELECT `+deliveryColumns+` FROM lead_deliveries WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// Deliveries are the deliveries of the answers of a request, by the message.
func (s *Store) Deliveries(ctx context.Context, leadID int64) (map[int64][]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deliveryColumns+` FROM lead_deliveries WHERE lead_id = ? ORDER BY id`, leadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out[d.MessageID] = append(out[d.MessageID], d)
	}
	return out, rows.Err()
}

// MarkTarget records what became of one delivery, and sums the answer up: sent once any delivery
// reached the client (the client has it), queued while none did and some still wait, failed when
// none can. What became of each is in the deliveries themselves.
func (s *Store) MarkTarget(ctx context.Context, id int64, status, emailMessageID string) error {
	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `
		UPDATE lead_deliveries SET status = ?, email_message_id = COALESCE(NULLIF(?, ''), email_message_id), updated_at = ? WHERE id = ?`,
		status, emailMessageID, now, id); err != nil {
		return err
	}
	var messageID int64
	if err := s.db.QueryRowContext(ctx, `SELECT message_id FROM lead_deliveries WHERE id = ?`, id).Scan(&messageID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // the request was deleted meanwhile
		}
		return err
	}
	if emailMessageID != "" {
		// The Message-ID of the letter keeps the conversation in one thread in the client's mail.
		if _, err := s.db.ExecContext(ctx, `UPDATE lead_messages SET email_message_id = COALESCE(email_message_id, ?) WHERE id = ?`,
			emailMessageID, messageID); err != nil {
			return err
		}
	}
	return sumUp(ctx, s.db, messageID)
}

// execer runs a statement: the pool or a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// sumUp sums an answer's deliveries up into lead_messages.delivery — all of them, whatever channel.
func sumUp(ctx context.Context, q execer, messageID int64) error {
	_, err := q.ExecContext(ctx, `
		UPDATE lead_messages m JOIN (
			SELECT message_id,
			       CASE WHEN SUM(status = 'sent') > 0 THEN 'sent' WHEN SUM(status = 'queued') > 0 THEN 'queued' ELSE 'failed' END AS summary
			FROM lead_deliveries WHERE message_id = ? GROUP BY message_id
		) d ON d.message_id = m.id
		SET m.delivery = d.summary`, messageID)
	return err
}

// MarkTargetParts records that the first parts of an answer reached one Telegram chat.
func (s *Store) MarkTargetParts(ctx context.Context, id int64, parts int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lead_deliveries SET sent_parts = ?, updated_at = ? WHERE id = ? AND sent_parts < ?`,
		parts, s.now().UTC(), id, parts)
	return err
}

// queueDeliveries writes the deliveries of an answer: one per target, each queued.
func queueDeliveries(ctx context.Context, tx *sql.Tx, now time.Time, leadID, messageID int64, targets []Target) ([]int64, error) {
	ids := make([]int64, 0, len(targets))
	for _, target := range targets {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO lead_deliveries (message_id, lead_id, channel, target, status, created_at, updated_at) VALUES (?, ?, ?, ?, 'queued', ?, ?)`,
			messageID, leadID, target.Channel, cut(target.To, 200), now, now)
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}
