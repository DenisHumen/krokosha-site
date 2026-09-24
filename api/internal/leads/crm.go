package leads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// What people do with requests (brief B10.4, B10.6). The admin area uses these methods today, the
// Telegram bot will use the very same ones: whoever presses «take» first gets the request, in
// whichever of the two it happens.

// Statuses lists the statuses in the order of the board.
var Statuses = []string{StatusNew, StatusInProgress, StatusWaitingClient, StatusDone, StatusRejected, StatusSpam}

// transitions says where a request may go from each status (brief B10.4). Anything may be
// «reopened» — put back to new or in progress — so that a mistake is never final.
var transitions = map[string][]string{
	StatusNew:           {StatusInProgress, StatusRejected, StatusSpam},
	StatusInProgress:    {StatusWaitingClient, StatusDone, StatusRejected, StatusNew},
	StatusWaitingClient: {StatusInProgress, StatusDone, StatusRejected},
	StatusDone:          {StatusInProgress},
	StatusRejected:      {StatusInProgress, StatusNew},
	StatusSpam:          {StatusNew},
}

// Errors of the operations.
var (
	ErrNotFound      = errors.New("no such request")
	ErrAlreadyTaken  = errors.New("the request was taken by someone else")
	ErrBadTransition = errors.New("the request cannot go to this status from where it is")
	ErrEmptyText     = errors.New("the text is empty")
)

// NextStatuses returns where a request in the given status may go.
func NextStatuses(status string) []string { return transitions[status] }

// Summary is a line of the list and a card of the board.
type Summary struct {
	ID            int64
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Name          string
	ContactMethod string
	ContactValue  string
	Direction     string
	Budget        string
	Excerpt       string
	Assignee      string
	Source        string
	Campaign      string
	SpamScore     int
	Messages      int
	LastFromUser  bool   // the last word is the client's: somebody should answer
	Kind          string // request | inquiry
	Discount      int    // the percent the request got
}

// Number of the request.
func (s Summary) Number() string { return Number(s.ID) }

// Filter narrows the list.
type Filter struct {
	Status string // "" = everything except spam, "all" = everything, "open" = what still needs somebody
	Query  string // in the name, the contact, the description
	// Assignee narrows to the requests one person took (the bot's «мои»).
	Assignee string
	Limit    int
	Offset   int
}

// List returns requests, newest first, and how many match in total.
func (s *Store) List(ctx context.Context, filter Filter) ([]Summary, int, error) {
	where, args := []string{"1 = 1"}, []any{}
	switch filter.Status {
	case "":
		where = append(where, "l.status <> 'spam'")
	case "all":
	case "open":
		where = append(where, "l.status IN ('new', 'in_progress', 'waiting_client')")
	default:
		where = append(where, "l.status = ?")
		args = append(args, filter.Status)
	}
	if filter.Assignee != "" {
		where = append(where, "l.assignee = ?")
		args = append(args, filter.Assignee)
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		if id, ok := parseNumber(query); ok {
			where = append(where, "l.id = ?")
			args = append(args, id)
		} else {
			like := "%" + escapeLike(query) + "%"
			where = append(where, "(l.name LIKE ? OR l.contact_value LIKE ? OR l.description LIKE ?)")
			args = append(args, like, like, like)
		}
	}
	condition := strings.Join(where, " AND ")

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM leads l WHERE `+condition, args...).Scan(&total); err != nil { // the condition is built from the constants above
		return nil, 0, err
	}
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	//nolint:gosec // as above
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.status, l.created_at, l.updated_at, l.name, l.contact_method, l.contact_value, l.direction,
		       COALESCE(l.budget, ''), LEFT(l.description, 240), COALESCE(l.assignee, ''), COALESCE(l.source, ''),
		       COALESCE(l.utm_campaign, l.utm_source, ''), l.spam_score, l.kind, l.discount_percent,
		       (SELECT COUNT(*) FROM lead_messages m WHERE m.lead_id = l.id AND m.direction <> 'note'),
		       COALESCE((SELECT m.direction FROM lead_messages m WHERE m.lead_id = l.id AND m.direction <> 'note' ORDER BY m.id DESC LIMIT 1), '')
		FROM leads l WHERE `+condition+` ORDER BY l.created_at DESC, l.id DESC LIMIT ? OFFSET ?`,
		append(args, filter.Limit, filter.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var item Summary
		var last string
		if err := rows.Scan(&item.ID, &item.Status, &item.CreatedAt, &item.UpdatedAt, &item.Name, &item.ContactMethod, &item.ContactValue,
			&item.Direction, &item.Budget, &item.Excerpt, &item.Assignee, &item.Source, &item.Campaign, &item.SpamScore, &item.Kind, &item.Discount,
			&item.Messages, &last); err != nil {
			return nil, 0, err
		}
		// Only where an answer is still owed: a new request is waiting to be taken, a closed one
		// needs nothing. (A phone call counts once it is written down as an answer.)
		open := item.Status == StatusInProgress || item.Status == StatusWaitingClient
		item.LastFromUser = last == "in" && open
		out = append(out, item)
	}
	return out, total, rows.Err()
}

// parseNumber understands «K-0042», «#K-42» and «42».
func parseNumber(query string) (int64, bool) {
	text := strings.TrimPrefix(strings.ToUpper(strings.TrimPrefix(query, "#")), "K-")
	var id int64
	if _, err := fmt.Sscanf(text, "%d", &id); err != nil || id <= 0 || fmt.Sprint(id) != strings.TrimLeft(text, "0") {
		return 0, false
	}
	return id, true
}

func escapeLike(text string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)
}

// ChannelMessages counts the messages of clients and the answers to them in one channel since a
// moment: the «Бот» screen says how busy Telegram is.
func (s *Store) ChannelMessages(ctx context.Context, channel string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lead_messages WHERE channel = ? AND direction IN ('in', 'out') AND created_at >= ?`,
		channel, since.UTC()).Scan(&count)
	return count, err
}

// Counts returns how many requests there are in each status.
func (s *Store) Counts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM leads GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
	}
	return out, rows.Err()
}

// Entry is one line of a request's feed: a message, a note, or something that happened to it.
type Entry struct {
	At        time.Time
	Kind      string // message | note | event
	Direction string // in | out, for messages
	Channel   string
	Author    string
	Body      string
	Delivery  string // queued | sent | failed, for answers
	Action    string // for events: created | status | assigned…
	From, To  string // statuses, for events
	MessageID int64
	Files     []Attachment // what came with this message
}

// Card is everything about one request.
type Card struct {
	Lead            *Lead
	Assignee        string
	AssignedAt      sql.NullTime
	FirstResponseAt sql.NullTime
	RejectReason    string
	AnonymizedAt    sql.NullTime // set when the storage period ran out: the person and the conversation are gone
	// ReplyVia is how the next answer will reach the client: email | telegram | phone.
	ReplyVia string
	// BotLinked: the client opened the bot by the link of this request (ClientLinked).
	BotLinked bool
	Feed      []Entry
}

// Card reads a request with its conversation and history, oldest first.
func (s *Store) Card(ctx context.Context, id int64) (*Card, error) {
	lead, err := s.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	card := &Card{Lead: lead}
	var assignee, reason sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT assignee, assigned_at, first_response_at, reject_reason, anonymized_at FROM leads WHERE id = ?`, id).
		Scan(&assignee, &card.AssignedAt, &card.FirstResponseAt, &reason, &card.AnonymizedAt); err != nil {
		return nil, err
	}
	card.Assignee, card.RejectReason = assignee.String, reason.String
	if card.ReplyVia, err = replyChannel(ctx, s.db, id, lead.ContactMethod, lead.ClientID); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lead_events WHERE lead_id = ? AND action = 'client_linked')`, id).
		Scan(&card.BotLinked); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT created_at, IF(direction = 'note', 'note', 'message'), direction, channel, COALESCE(author, ''), body, COALESCE(delivery, ''), '', '', '', id
		  FROM lead_messages WHERE lead_id = ?
		UNION ALL
		SELECT created_at, 'event', '', '', actor, COALESCE(details, ''), '', action, COALESCE(from_status, ''), COALESCE(to_status, ''), 0
		  FROM lead_events WHERE lead_id = ?
		ORDER BY 1`, id, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var entry Entry
		if err := rows.Scan(&entry.At, &entry.Kind, &entry.Direction, &entry.Channel, &entry.Author, &entry.Body, &entry.Delivery,
			&entry.Action, &entry.From, &entry.To, &entry.MessageID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		card.Feed = append(card.Feed, entry)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	files, err := s.Attachments(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		for i := range card.Feed {
			if card.Feed[i].MessageID == file.MessageID && file.MessageID != 0 {
				card.Feed[i].Files = append(card.Feed[i].Files, file)
			}
		}
	}
	return card, nil
}

// Take assigns a new request to whoever asks first. A second «take» — from the admin area or
// from the bot, a moment later — gets ErrAlreadyTaken and learns who was faster.
func (s *Store) Take(ctx context.Context, id int64, actor string) (takenBy string, err error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	// The WHERE clause is the lock: of two racing updates only one finds the row still «new».
	result, err := tx.ExecContext(ctx, `UPDATE leads SET status = 'in_progress', assignee = ?, assigned_at = ?, updated_at = ? WHERE id = ? AND status = 'new'`,
		actor, now, now, id)
	if err != nil {
		return "", err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		var status string
		var assignee sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT status, assignee FROM leads WHERE id = ?`, id).Scan(&status, &assignee); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", ErrNotFound
			}
			return "", err
		}
		return assignee.String, ErrAlreadyTaken
	}
	if err := event(ctx, tx, id, now, actor, "assigned", StatusNew, StatusInProgress, ""); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.changed(id)
	return actor, nil
}

// SetStatus moves a request along the allowed transitions and writes it into the history.
func (s *Store) SetStatus(ctx context.Context, id int64, actor, status, reason string) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var current string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM leads WHERE id = ? FOR UPDATE`, id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if current == status {
		return nil
	}
	allowed := false
	for _, next := range transitions[current] {
		allowed = allowed || next == status
	}
	if !allowed {
		return fmt.Errorf("%w: %s → %s", ErrBadTransition, current, status)
	}

	closed := sql.NullTime{Time: now, Valid: status == StatusDone || status == StatusRejected}
	reason = cut(strings.TrimSpace(reason), 255)
	if _, err := tx.ExecContext(ctx, `
		UPDATE leads SET status = ?, updated_at = ?, closed_at = ?, reject_reason = IF(? = 'rejected', NULLIF(?, ''), reject_reason),
		       assignee = IF(? = 'in_progress' AND assignee IS NULL, ?, assignee), assigned_at = IF(? = 'in_progress' AND assigned_at IS NULL, ?, assigned_at)
		WHERE id = ?`, status, now, closed, status, reason, status, actor, status, now, id); err != nil {
		return err
	}
	if err := event(ctx, tx, id, now, actor, "status", current, status, reason); err != nil {
		return err
	}
	// A request rescued from spam was never announced to anybody: do it now.
	if current == StatusSpam && status == StatusNew {
		for _, channel := range []string{outbox.ChannelEmail, outbox.ChannelTelegram} {
			if err := outbox.Enqueue(ctx, tx, now, outbox.NewTask{Channel: channel, Kind: TaskNotify, LeadID: id,
				DedupeKey: fmt.Sprintf("lead:%d:notify:%s", id, channel), Payload: TaskPayload{LeadID: id}}); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed(id)
	return nil
}

// AddNote stores an internal comment: the client never sees it, colleagues do.
func (s *Store) AddNote(ctx context.Context, id int64, actor, text string) error {
	text = clean(text, true)
	if text == "" {
		return ErrEmptyText
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO lead_messages (lead_id, created_at, direction, channel, author, body)
		SELECT id, ?, 'note', 'admin', ?, ? FROM leads WHERE id = ?`, now, actor, cut(text, 8000), id)
	if err != nil {
		return err
	}
	if inserted, _ := result.RowsAffected(); inserted == 0 {
		return ErrNotFound
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE leads SET updated_at = ? WHERE id = ?`, now, id); err != nil {
		return err
	}
	s.changed(id)
	return nil
}

// replyChannel says how an answer reaches the client: the way the client wrote last. Somebody who
// left an email address and then continued in Telegram is answered in Telegram; before they write
// anything, the contact of the form decides. Opening the bot by the link of the «thank you» page
// counts as coming to Telegram: the bot told the client the answer would come to that chat. A client
// with a personal account who left only a phone is answered in the account: the answer is there at
// once, and the client is told about it.
func replyChannel(ctx context.Context, db querier, id int64, method string, clientID int64) (string, error) {
	var last string
	var lastAt time.Time
	err := db.QueryRowContext(ctx, `SELECT channel, created_at FROM lead_messages WHERE lead_id = ? AND direction = 'in' AND channel IN ('telegram', 'email') ORDER BY id DESC LIMIT 1`, id).Scan(&last, &lastAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		last = method
	case err != nil:
		return "", err
	case last == ChannelEmail && method != MethodEmail:
		last = method // a letter from somebody whose address the form does not have: see the mail step
	}
	if last != ChannelTelegram {
		var linkedAt time.Time
		err := db.QueryRowContext(ctx, `SELECT created_at FROM lead_events WHERE lead_id = ? AND action = 'client_linked' ORDER BY id DESC LIMIT 1`, id).Scan(&linkedAt)
		switch {
		case err == nil && linkedAt.After(lastAt): // lastAt is zero when the client wrote nothing yet
			last = ChannelTelegram
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return "", err
		}
	}
	if last == MethodPhone && clientID > 0 {
		return ChannelSite, nil
	}
	return last, nil
}

// Reply stores an answer to the client and queues its delivery through the channel the client
// chose (brief B10.4). The request then waits for the client; the first answer stops the clock
// of «time to first reaction». A phone call cannot be delivered by a machine: for those the
// text is the record of the call.
func (s *Store) Reply(ctx context.Context, id int64, actor, text string) (messageID int64, err error) {
	text = clean(text, true)
	if text == "" {
		return 0, ErrEmptyText
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var status, method string
	var clientID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT status, contact_method, client_id FROM leads WHERE id = ? FOR UPDATE`, id).Scan(&status, &method, &clientID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	channel, err := replyChannel(ctx, tx, id, method, clientID.Int64)
	if err != nil {
		return 0, err
	}
	delivery := sql.NullString{String: "queued", Valid: true}
	switch channel {
	case MethodPhone:
		delivery = sql.NullString{} // nothing to deliver: the call has happened
	case ChannelSite:
		delivery.String = "sent" // it is in the account the moment it is stored; the notice about it is a task of its own
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO lead_messages (lead_id, created_at, direction, channel, author, body, delivery) VALUES (?, ?, 'out', ?, ?, ?, ?)`,
		id, now, channel, actor, cut(text, 8000), delivery)
	if err != nil {
		return 0, err
	}
	if messageID, err = result.LastInsertId(); err != nil {
		return 0, err
	}

	next := StatusWaitingClient
	if status == StatusDone || status == StatusRejected || status == StatusSpam {
		next = status // an answer to a closed request does not reopen it
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE leads SET status = ?, updated_at = ?, first_response_at = COALESCE(first_response_at, ?),
		       assignee = COALESCE(assignee, ?), assigned_at = COALESCE(assigned_at, ?) WHERE id = ?`, next, now, now, actor, now, id); err != nil {
		return 0, err
	}
	if err := event(ctx, tx, id, now, actor, "replied", status, next, ""); err != nil {
		return 0, err
	}
	switch channel {
	case MethodPhone:
	case ChannelSite:
		// The client is told where the answer is: by the address or in the Telegram of the account.
		notice, err := accountChannel(ctx, tx, clientID.Int64)
		if err != nil {
			return 0, err
		}
		if notice != "" {
			if err := outbox.Enqueue(ctx, tx, now, outbox.NewTask{Channel: notice, Kind: TaskSiteReply, LeadID: id,
				DedupeKey: fmt.Sprintf("lead:%d:site:%d", id, messageID), Payload: TaskPayload{LeadID: id, MessageID: messageID}}); err != nil {
				return 0, err
			}
		}
	default:
		outboxChannel := outbox.ChannelEmail
		if channel == MethodTelegram {
			outboxChannel = outbox.ChannelTelegram
		}
		if err := outbox.Enqueue(ctx, tx, now, outbox.NewTask{Channel: outboxChannel, Kind: TaskReply, LeadID: id,
			DedupeKey: fmt.Sprintf("lead:%d:reply:%d", id, messageID), Payload: TaskPayload{LeadID: id, MessageID: messageID}}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.changed(id)
	return messageID, nil
}

// Channels a client can write through after the form.
const (
	ChannelTelegram = "telegram"
	ChannelEmail    = "email"
)

// Incoming is something a client sent after the form: a message in Telegram, a letter.
type Incoming struct {
	Channel string // ChannelTelegram | ChannelEmail
	Text    string
	// Files are on disk already (Files.Save). When the message cannot be stored, removing them
	// is the caller's job — it put them there.
	Files []Upload
	// EmailMessageID of a letter: the next answer by mail refers to it, and the client's mail
	// program keeps the conversation in one thread.
	EmailMessageID string
	// Automatic: an out-of-office note or the like. It is kept in the conversation, but the
	// request does not come back to work for it and nobody is woken up.
	Automatic bool
	// Note goes into the history: «привязано по адресу отправителя», «не сохранено: video.mp4».
	Note string
	// InTx, when set, runs inside the transaction that stores the message: whoever keeps a
	// record of having handled a letter writes it here — both are stored, or neither.
	InTx func(ctx context.Context, tx *sql.Tx, messageID int64) error
}

// ClientMessage stores what a client wrote in Telegram through the bot.
func (s *Store) ClientMessage(ctx context.Context, id int64, channel, text string) (messageID int64, err error) {
	return s.ClientWrote(ctx, id, Incoming{Channel: channel, Text: text})
}

// ClientWrote stores what a client sent after the form — in Telegram through the bot, or by
// answering a letter (brief B10.5). A request that was waiting for the client goes back to
// work, and everybody is told, through the same outbox as everything else.
func (s *Store) ClientWrote(ctx context.Context, id int64, in Incoming) (messageID int64, err error) {
	text := clean(in.Text, true)
	if text == "" && len(in.Files) > 0 {
		text = "(без текста — только файлы)"
	}
	if text == "" {
		return 0, ErrEmptyText
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM leads WHERE id = ? AND anonymized_at IS NULL FOR UPDATE`, id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO lead_messages (lead_id, created_at, direction, channel, body, email_message_id) VALUES (?, ?, 'in', ?, ?, NULLIF(?, ''))`,
		id, now, in.Channel, cut(text, 8000), cut(in.EmailMessageID, 255))
	if err != nil {
		return 0, err
	}
	if messageID, err = result.LastInsertId(); err != nil {
		return 0, err
	}
	for _, file := range in.Files {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO lead_attachments (lead_id, message_id, created_at, filename, kind, size, sha256, stored_as) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, messageID, now, cut(file.Filename, 255), file.Kind, file.Size, file.SHA256, file.StoredAs); err != nil {
			return 0, err
		}
	}
	next := status
	if status == StatusWaitingClient && !in.Automatic {
		next = StatusInProgress // the client answered: the ball is ours again (brief B10.4)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE leads SET status = ?, updated_at = ? WHERE id = ?`, next, now, id); err != nil {
		return 0, err
	}
	action := "client_replied"
	if in.Automatic {
		action = "auto_reply"
	}
	if err := event(ctx, tx, id, now, "client", action, status, next, cut(in.Note, 255)); err != nil {
		return 0, err
	}
	// What robots sent is not announced, whatever they write afterwards.
	if status != StatusSpam && !in.Automatic {
		for _, outboxChannel := range []string{outbox.ChannelTelegram, outbox.ChannelEmail} {
			if err := outbox.Enqueue(ctx, tx, now, outbox.NewTask{Channel: outboxChannel, Kind: TaskClientMessage, LeadID: id,
				DedupeKey: fmt.Sprintf("lead:%d:client:%d:%s", id, messageID, outboxChannel), Payload: TaskPayload{LeadID: id, MessageID: messageID}}); err != nil {
				return 0, err
			}
		}
	}
	if in.InTx != nil {
		if err := in.InTx(ctx, tx, messageID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.changed(id)
	return messageID, nil
}

// ByEmail finds the latest request of a client by the address they left in the form — for a
// letter that names no request (brief B10.5). Spam and anonymised requests have no client.
func (s *Store) ByEmail(ctx context.Context, address string) (id int64, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT id FROM leads
		 WHERE contact_method = 'email' AND LOWER(contact_value) = LOWER(?) AND anonymized_at IS NULL AND status <> 'spam'
		 ORDER BY id DESC LIMIT 1`, address).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// Undelivered records that a mail server returned a letter of ours (brief B10.5): the answer it
// carried is marked as failed, the history says why, and the staff hears about it — a client
// who never got the answer keeps waiting for it. emailMessageID may be empty: then the letter
// was the automatic confirmation, or the report did not say.
func (s *Store) Undelivered(ctx context.Context, id int64, emailMessageID, reason string, inTx func(ctx context.Context, tx *sql.Tx) error) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM leads WHERE id = ? AND anonymized_at IS NULL FOR UPDATE`, id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if emailMessageID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE lead_messages SET delivery = 'failed' WHERE lead_id = ? AND direction = 'out' AND email_message_id = ?`, id, emailMessageID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE leads SET updated_at = ? WHERE id = ?`, now, id); err != nil {
		return err
	}
	if err := event(ctx, tx, id, now, "system", "undelivered", "", "", cut(reason, 255)); err != nil {
		return err
	}
	if status != StatusSpam {
		for _, outboxChannel := range []string{outbox.ChannelTelegram, outbox.ChannelEmail} {
			if err := outbox.Enqueue(ctx, tx, now, outbox.NewTask{Channel: outboxChannel, Kind: TaskUndelivered, LeadID: id,
				DedupeKey: fmt.Sprintf("lead:%d:undelivered:%d:%s", id, now.UnixMilli(), outboxChannel), Payload: TaskPayload{LeadID: id, Note: cut(reason, 255)}}); err != nil {
				return err
			}
		}
	}
	if inTx != nil {
		if err := inTx(ctx, tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed(id)
	return nil
}

// ClientLinked writes into the history that the client opened the bot by the link of the «thank
// you» page: from now on answers can reach them in Telegram.
func (s *Store) ClientLinked(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := event(ctx, tx, id, s.now().UTC(), "client", "client_linked", "", "", "Telegram"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed(id)
	return nil
}

// Unclaimed lists new requests nobody has taken since before the given moment and nobody was
// reminded of yet (brief B10.4, «напоминания»).
func (s *Store) Unclaimed(ctx context.Context, before time.Time) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id FROM leads l
		WHERE l.status = 'new' AND l.created_at < ?
		  AND NOT EXISTS (SELECT 1 FROM lead_events e WHERE e.lead_id = l.id AND e.action = 'reminded')
		ORDER BY l.id LIMIT 20`, before.UTC())
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

// MarkReminded writes down that people were reminded of a request: one reminder is enough.
func (s *Store) MarkReminded(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO lead_events (lead_id, created_at, actor, action) VALUES (?, ?, 'system', 'reminded')`, id, s.now().UTC())
	return err
}

// IncomingMessage returns the text of a stored message of the client, for whoever announces it.
func (s *Store) IncomingMessage(ctx context.Context, leadID, messageID int64) (body, channel string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT body, channel FROM lead_messages WHERE id = ? AND lead_id = ? AND direction = 'in'`, messageID, leadID).Scan(&body, &channel)
	return body, channel, err
}

// Message returns the text of a stored answer, for the sender that delivers it.
func (s *Store) Message(ctx context.Context, leadID, messageID int64) (body, author string, err error) {
	var who sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT body, author FROM lead_messages WHERE id = ? AND lead_id = ? AND direction = 'out'`, messageID, leadID).Scan(&body, &who)
	return body, who.String, err
}

// MarkDelivery records what became of an answer: sent or failed.
func (s *Store) MarkDelivery(ctx context.Context, messageID int64, delivery, emailMessageID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lead_messages SET delivery = ?, email_message_id = COALESCE(NULLIF(?, ''), email_message_id) WHERE id = ?`,
		delivery, emailMessageID, messageID)
	return err
}

// ThreadIDs returns the Message-IDs of the letters already sent about a request, oldest first,
// so that an answer lands in the same thread of the client's mail program.
func (s *Store) ThreadIDs(ctx context.Context, leadID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT email_message_id FROM lead_messages WHERE lead_id = ? AND email_message_id IS NOT NULL ORDER BY id`, leadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Delete removes everything about a request — the request, the conversation, the files, the
// history, the notifications still queued — when the client asks for it (brief B10.6). What
// remains is one line in the audit log of the admin area, written by the caller: that K-0042
// was deleted, by whom and when, nothing about the person.
//
// ErrFilesLeft means the database part is done and a file could not be removed right now.
func (s *Store) Delete(ctx context.Context, id int64) error {
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE lead_id = ?`, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM leads WHERE id = ?`, id) // messages, files' rows and events go with it (ON DELETE CASCADE)
	if err != nil {
		return err
	}
	if deleted, _ := result.RowsAffected(); deleted == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Only now: a file must not disappear while its request may still stay.
	return s.removeFiles(files)
}

func event(ctx context.Context, tx *sql.Tx, id int64, now time.Time, actor, action, from, to, details string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO lead_events (lead_id, created_at, actor, action, from_status, to_status, details) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, now, actor, action, null(from), null(to), null(cut(details, 255)))
	return err
}

// --- the funnel ----------------------------------------------------------------------------------

// Funnel is the summary above the list (brief B10.6).
type Funnel struct {
	Total, New, InProgress, Waiting, Done, Rejected, Spam int
	// Amount is what the completed ones came to, as the owner entered it (Lead.Amount).
	Amount float64
	// FirstResponse is the median time from a request to the first answer; zero when unknown.
	FirstResponse time.Duration
	Sources       []SourceStat
}

// SourceStat says how requests of one origin ended.
type SourceStat struct {
	Source   string // ads | search | social | direct | other; "" when the visit is unknown
	Campaign string // utm_campaign, so that advertising campaigns can be told apart
	Requests int
	Done     int
}

// Funnel counts the requests created in [from, to).
func (s *Store) Funnel(ctx context.Context, from, to time.Time) (*Funnel, error) {
	out := &Funnel{}
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(status = 'new'), 0), COALESCE(SUM(status = 'in_progress'), 0), COALESCE(SUM(status = 'waiting_client'), 0),
		       COALESCE(SUM(status = 'done'), 0), COALESCE(SUM(status = 'rejected'), 0), COALESCE(SUM(status = 'spam'), 0),
		       COALESCE(SUM(IF(status = 'done', COALESCE(amount, 0), 0)), 0)
		FROM leads WHERE created_at >= ? AND created_at < ?`, from.UTC(), to.UTC()).
		Scan(&out.Total, &out.New, &out.InProgress, &out.Waiting, &out.Done, &out.Rejected, &out.Spam, &out.Amount)
	if err != nil {
		return nil, err
	}

	// The median, not the mean: one request answered after a holiday must not spoil the number.
	rows, err := s.db.QueryContext(ctx, `
		SELECT TIMESTAMPDIFF(SECOND, created_at, first_response_at) AS seconds FROM leads
		WHERE created_at >= ? AND created_at < ? AND first_response_at IS NOT NULL AND status <> 'spam' ORDER BY seconds`, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	var seconds []int64
	for rows.Next() {
		var value int64
		if err := rows.Scan(&value); err != nil {
			_ = rows.Close()
			return nil, err
		}
		seconds = append(seconds, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(seconds) > 0 {
		out.FirstResponse = time.Duration(seconds[len(seconds)/2]) * time.Second
	}

	sources, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(source, '') AS origin, COALESCE(utm_campaign, '') AS campaign, COUNT(*) AS requests, COALESCE(SUM(status = 'done'), 0)
		FROM leads WHERE created_at >= ? AND created_at < ? AND status <> 'spam'
		GROUP BY origin, campaign ORDER BY requests DESC, origin, campaign LIMIT 8`, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer sources.Close()
	for sources.Next() {
		var stat SourceStat
		if err := sources.Scan(&stat.Source, &stat.Campaign, &stat.Requests, &stat.Done); err != nil {
			return nil, err
		}
		out.Sources = append(out.Sources, stat)
	}
	return out, sources.Err()
}

// Export streams all requests matching the filter, oldest first, as rows of text.
func (s *Store) Export(ctx context.Context, fn func(row []string) error) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, created_at, status, COALESCE(assignee, ''), name, contact_method, contact_value, direction, COALESCE(budget, ''),
		       COALESCE(timeline, ''), lang, description, COALESCE(source, ''), COALESCE(referrer_host, ''), COALESCE(utm_source, ''),
		       COALESCE(utm_campaign, ''), COALESCE(country, ''), COALESCE(device, ''), spam_score,
		       COALESCE(first_response_at, ''), COALESCE(closed_at, '')
		FROM leads ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if err := fn([]string{"number", "created_at_utc", "status", "assignee", "name", "contact_method", "contact", "direction", "budget", "timeline",
		"lang", "description", "source", "referrer", "utm_source", "utm_campaign", "country", "device", "spam_score", "first_response_at_utc", "closed_at_utc"}); err != nil {
		return err
	}
	raw := make([]sql.RawBytes, 21)
	targets := make([]any, len(raw))
	for i := range raw {
		targets[i] = &raw[i]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		line := make([]string, len(raw))
		for i, cell := range raw {
			line[i] = string(cell)
		}
		var id int64
		_, _ = fmt.Sscan(line[0], &id)
		line[0] = Number(id)
		if err := fn(line); err != nil {
			return err
		}
	}
	return rows.Err()
}

// --- ready-made answers --------------------------------------------------------------------------

// Template is a ready-made answer or refusal, editable in the admin area.
type Template struct {
	ID    int64
	Kind  string // reply | reject
	Lang  string
	Title string
	Body  string
}

// Templates lists the templates of a kind ("" = all) in the order of the editor.
func (s *Store) Templates(ctx context.Context, kind string) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, lang, title, body FROM reply_templates WHERE (? = '' OR kind = ?) ORDER BY kind, lang, position, id`, kind, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Template
	for rows.Next() {
		var item Template
		if err := rows.Scan(&item.ID, &item.Kind, &item.Lang, &item.Title, &item.Body); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SaveTemplate adds a template (ID 0) or changes one.
func (s *Store) SaveTemplate(ctx context.Context, item Template) error {
	item.Title, item.Body = clean(item.Title, false), clean(item.Body, true)
	if item.Title == "" || item.Body == "" {
		return ErrEmptyText
	}
	if item.Kind != "reply" && item.Kind != "reject" {
		return errors.New("a template is a reply or a reject")
	}
	if !languages[item.Lang] {
		return errors.New("unknown language of a template")
	}
	now := s.now().UTC()
	if item.ID == 0 {
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO reply_templates (kind, lang, position, title, body, updated_at)
			SELECT ?, ?, COALESCE(MAX(position), 0) + 1, ?, ?, ? FROM reply_templates WHERE kind = ? AND lang = ?`,
			item.Kind, item.Lang, cut(item.Title, 100), cut(item.Body, 8000), now, item.Kind, item.Lang)
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE reply_templates SET kind = ?, lang = ?, title = ?, body = ?, updated_at = ? WHERE id = ?`,
		item.Kind, item.Lang, cut(item.Title, 100), cut(item.Body, 8000), now, item.ID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTemplate removes a template.
func (s *Store) DeleteTemplate(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM reply_templates WHERE id = ?`, id)
	return err
}

// FillTemplate puts the client's name and the number of the request into a template.
func FillTemplate(body string, lead *Lead) string {
	return strings.NewReplacer("{name}", lead.Name, "{id}", "#"+lead.Number()).Replace(body)
}
