package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// Cards of requests (brief B10.4). A new request comes to everybody with access as a card with
// buttons. When anything happens to the request — here or in the admin area — the card changes
// in every chat at once. When a client's data is erased, so is everything the bot wrote about
// it: what is gone from the server must not live on in Telegram chats.

// Kinds of remembered messages.
const (
	kindCard = "card" // one per chat, rewritten when the request changes
	kindText = "text" // everything else about a request: the full text, the history, previews
)

var (
	statusIcons = map[string]string{
		leads.StatusNew: "🟡", leads.StatusInProgress: "🟢", leads.StatusWaitingClient: "🔵",
		leads.StatusDone: "✅", leads.StatusRejected: "❌", leads.StatusSpam: "🚫",
	}
	statusNames = map[string]string{
		leads.StatusNew: "Новая", leads.StatusInProgress: "В работе", leads.StatusWaitingClient: "Ждём клиента",
		leads.StatusDone: "Завершена", leads.StatusRejected: "Отклонена", leads.StatusSpam: "Спам",
	}
	contactIcons = map[string]string{leads.MethodEmail: "✉️", leads.MethodTelegram: "✈️", leads.MethodPhone: "📞"}
)

const (
	cardRule        = "━━━━━━━━━━━━━━━━━━"
	cardDescription = 700  // characters of the task shown on the card; the rest is behind «Полностью»
	messageLimit    = 4000 // Telegram takes 4096 characters; the margin is for what escaping adds
)

// cut shortens a text to so many characters, on a character boundary.
func cut(text string, limit int) (string, bool) {
	runes := []rune(text)
	if len(runes) <= limit {
		return text, false
	}
	return strings.TrimSpace(string(runes[:limit])), true
}

// contactLine is the client's contact the way Telegram makes it clickable: addresses and phone
// numbers it links by itself, a user name needs a link.
func contactLine(lead *leads.Lead) string {
	value := Escape(lead.ContactValue)
	if lead.ContactMethod == leads.MethodTelegram {
		value = `<a href="https://t.me/` + Escape(strings.TrimPrefix(lead.ContactValue, "@")) + `">` + value + `</a>`
	}
	return contactIcons[lead.ContactMethod] + " " + value
}

// renderCard draws the card of a request: the text and the buttons that fit its status.
func (b *Bot) renderCard(card *leads.Card) (string, Keyboard) {
	lead := card.Lead
	direction := lead.Direction
	if option, ok := b.opts.Form().Direction(lead.Direction); ok {
		if label := option.Label.In("ru"); label != "" {
			direction = label
		}
	}
	icon := statusIcons[lead.Status]
	if lead.Status == leads.StatusNew {
		icon = "🆕"
	}
	lines := []string{icon + " <b>Заявка #" + lead.Number() + "</b> · " + Escape(direction), cardRule}

	if card.AnonymizedAt.Valid {
		lines = append(lines, "Срок хранения истёк: данные клиента и переписка удалены.")
	} else {
		lines = append(lines, "👤 "+Escape(lead.Name), contactLine(lead))
		var terms []string
		if lead.Budget != "" {
			terms = append(terms, "💰 "+Escape(lead.Budget))
		}
		if lead.Timeline != "" {
			terms = append(terms, "⏱ "+Escape(lead.Timeline))
		}
		if len(terms) > 0 {
			lines = append(lines, strings.Join(terms, "   "))
		}
		description, shortened := cut(lead.Description, cardDescription)
		description = Escape(description)
		if shortened {
			description += "… <i>(полностью — по кнопке)</i>"
		}
		lines = append(lines, cardRule, "📝 "+description)
		var files int
		for _, entry := range card.Feed {
			files += len(entry.Files)
		}
		if files > 0 {
			lines = append(lines, fmt.Sprintf("📎 файлов: %d — в админке", files))
		}
		if source, sections, spent := leads.VisitSummary(lead.Session); source != "" || sections != "" {
			lines = append(lines, cardRule)
			if source != "" {
				lines = append(lines, "🌍 "+Escape(source))
			}
			if sections != "" {
				seen := "👀 смотрел: " + Escape(sections)
				if spent != "" {
					seen += " (" + spent + ")"
				}
				lines = append(lines, seen)
			}
		}
	}

	status := "Статус: " + statusIcons[lead.Status] + " " + statusNames[lead.Status]
	if card.Assignee != "" && lead.Status != leads.StatusNew {
		status += " · взял(а) " + Escape(card.Assignee)
		if card.AssignedAt.Valid {
			status += " · " + card.AssignedAt.Time.In(b.opts.Location).Format("02.01 15:04")
		}
	} else {
		status += " · " + lead.CreatedAt.In(b.opts.Location).Format("02.01 15:04")
	}
	if lead.Status == leads.StatusRejected && card.RejectReason != "" {
		status += "\nПричина: " + Escape(card.RejectReason)
	}
	if lead.Verdict.Score > 0 && lead.Status == leads.StatusNew {
		status += fmt.Sprintf("\n⚠️ подозрение на спам: %d из 100", lead.Verdict.Score)
	}
	lines = append(lines, cardRule, status)
	return strings.Join(lines, "\n"), b.cardButtons(card)
}

func (b *Bot) cardButtons(card *leads.Card) Keyboard {
	id := fmt.Sprint(card.Lead.ID)
	button := func(text, action string) Button { return Button{Text: text, Data: "l:" + action + ":" + id} }
	last := []Button{button("📄 Полностью", "full"), button("🕘 История", "history")}
	if b.opts.AdminURL != "" {
		last = append(last, Button{Text: "🔗 В админке", URL: strings.TrimRight(b.opts.AdminURL, "/") + "/leads/" + id})
	}
	if card.AnonymizedAt.Valid {
		return Keyboard{last[1:]}
	}
	answer := "💬 Ответить"
	if card.ReplyVia == leads.MethodPhone {
		answer = "📞 Итог звонка"
	}
	switch card.Lead.Status {
	case leads.StatusNew:
		return Keyboard{{button("✅ Взять в работу", "take"), button("❌ Отклонить", "reject")}, {button(answer, "reply"), button("📝 Заметка", "note")}, last}
	case leads.StatusInProgress, leads.StatusWaitingClient:
		return Keyboard{{button(answer, "reply"), button("📝 Заметка", "note")}, {button("✔️ Завершить", "done"), button("❌ Отклонить", "reject")}, last}
	default: // done, rejected, spam: a mistake must never be final
		return Keyboard{{button("↩️ Вернуть в работу", "reopen"), button("📝 Заметка", "note")}, last}
	}
}

// --- what was sent where ---------------------------------------------------------------------------

type sentMessage struct {
	ID, LeadID, ChatID, MessageID int64
}

// remember writes down a message about a request, so that it can be rewritten and, one day, wiped.
func (b *Bot) remember(ctx context.Context, leadID, chatID, messageID int64, kind string) {
	b.rememberAs(ctx, leadID, chatID, messageID, kind, "")
}

// rememberAs also notes what the message was about («in:17» — the push about the client's message
// 17), so that a push retried after a failure skips the chats it has reached.
func (b *Bot) rememberAs(ctx context.Context, leadID, chatID, messageID int64, kind, ref string) {
	if _, err := b.opts.DB.ExecContext(ctx, `INSERT IGNORE INTO bot_messages (lead_id, chat_id, message_id, kind, ref, created_at) VALUES (?, ?, ?, ?, NULLIF(?, ''), ?)`,
		leadID, chatID, messageID, kind, ref, b.opts.Now().UTC()); err != nil {
		b.opts.Log.Error("telegram: cannot remember a sent message — it will not be wiped with the request", "lead", leads.Number(leadID), "error", err)
	}
}

func (b *Bot) sentMessages(ctx context.Context, query string, args ...any) ([]sentMessage, error) {
	rows, err := b.opts.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sentMessage
	for rows.Next() {
		var message sentMessage
		if err := rows.Scan(&message.ID, &message.LeadID, &message.ChatID, &message.MessageID); err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, rows.Err()
}

// sayAbout sends a text that belongs to a request and remembers it.
func (b *Bot) sayAbout(ctx context.Context, leadID int64, message Outgoing) {
	sent, err := b.opts.API.Send(ctx, message)
	if err != nil {
		if ctx.Err() == nil {
			b.opts.Log.Warn("telegram: cannot send a message", "error", err)
		}
		return
	}
	b.remember(ctx, leadID, message.ChatID, sent.MessageID, kindText)
}

// --- delivery: the outbox hands new requests over ------------------------------------------------

// Send delivers a task of the «telegram» channel (outbox.Sender).
func (b *Bot) Send(ctx context.Context, task outbox.Task) error {
	if task.Kind == outbox.KindAlert {
		return b.alert(ctx, task)
	}
	var payload leads.TaskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.LeadID <= 0 {
		return outbox.Permanent(fmt.Errorf("unreadable task payload: %s", task.Payload))
	}
	switch task.Kind {
	case leads.TaskNotify:
		return b.announce(ctx, payload.LeadID)
	case leads.TaskReply:
		return b.answerClient(ctx, payload.LeadID, payload.MessageID)
	case leads.TaskClientMessage:
		return b.clientWrote(ctx, payload.LeadID, payload.MessageID)
	case leads.TaskUndelivered:
		return b.undelivered(ctx, payload.LeadID, task.ID, payload.Note)
	default:
		return outbox.Permanent(fmt.Errorf("the bot does not know the task %q", task.Kind))
	}
}

// announce sends the card of a request to everybody with access who does not have it yet. Run
// again after a failure, it reaches only those it missed.
func (b *Bot) announce(ctx context.Context, leadID int64) error {
	card, err := b.opts.Leads.Card(ctx, leadID)
	if errors.Is(err, leads.ErrNotFound) {
		return outbox.Permanent(errors.New("the request is gone (deleted on the client's demand?)"))
	}
	if err != nil {
		return err
	}
	recipients, err := b.opts.Access.Recipients(ctx)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return outbox.NotReady(errors.New("nobody has access to the bot yet: sudo krokosha-cli bot invite --owner"))
	}
	have, err := b.sentMessages(ctx, `SELECT id, lead_id, chat_id, message_id FROM bot_messages WHERE lead_id = ? AND kind = 'card'`, leadID)
	if err != nil {
		return err
	}
	delivered := map[int64]bool{}
	for _, message := range have {
		delivered[message.ChatID] = true
	}

	text, buttons := b.renderCard(card)
	now := b.opts.Now()
	var failed error
	for _, member := range recipients {
		if delivered[member.TelegramID] {
			continue
		}
		sent, err := b.opts.API.Send(ctx, Outgoing{
			ChatID: member.TelegramID, Text: text, Buttons: buttons,
			Silent: member.MutedUntil.Valid && member.MutedUntil.Time.After(now), // /mute: the card comes, the sound does not
		})
		var refused *APIError
		switch {
		case err == nil:
			b.remember(ctx, leadID, member.TelegramID, sent.MessageID, kindCard)
		case errors.As(err, &refused) && refused.Gone():
			// They blocked the bot: that is between them and Telegram, the others still get the card.
			b.opts.Log.Warn("telegram: a member cannot be reached any more", "member", member.Name, "error", err)
		default:
			failed = errors.Join(failed, err)
		}
	}
	return failed
}

// --- keeping cards in step ------------------------------------------------------------------------

// Changed tells the bot that something happened to a request (leads.Store.OnChange). It only
// takes a note: the cards are redrawn by RunCards.
func (b *Bot) Changed(leadID int64) {
	select {
	case b.redraw <- leadID:
	default: // so many changes at once that the queue is full: the next change redraws the card anyway
	}
}

// Erasing marks everything the bot wrote about a request for wiping (leads.Store.OnErase). It
// runs just before the request disappears from the database.
func (b *Bot) Erasing(ctx context.Context, leadID int64) {
	if _, err := b.opts.DB.ExecContext(ctx, `UPDATE bot_messages SET wipe_after = ? WHERE lead_id = ? AND wipe_after IS NULL`, b.opts.Now().UTC(), leadID); err != nil {
		b.opts.Log.Error("telegram: cannot mark messages for wiping", "lead", leads.Number(leadID), "error", err)
	}
	select {
	case b.wipe <- struct{}{}:
	default:
	}
}

// RunCards redraws cards of changed requests and wipes messages of erased ones, until ctx ends.
func (b *Bot) RunCards(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case leadID := <-b.redraw:
			working, cancel := context.WithTimeout(ctx, 30*time.Second)
			b.redrawCards(working, leadID)
			cancel()
		case <-b.wipe:
			b.wipeMessages(ctx)
		case <-ticker.C:
			b.wipeMessages(ctx) // what Telegram refused a minute ago
		}
	}
}

func (b *Bot) redrawCards(ctx context.Context, leadID int64) {
	card, err := b.opts.Leads.Card(ctx, leadID)
	if err != nil {
		if !errors.Is(err, leads.ErrNotFound) {
			b.opts.Log.Error("telegram: cannot read a request to redraw its cards", "lead", leads.Number(leadID), "error", err)
		}
		return
	}
	cards, err := b.sentMessages(ctx, `SELECT id, lead_id, chat_id, message_id FROM bot_messages WHERE lead_id = ? AND kind = 'card' AND wipe_after IS NULL`, leadID)
	if err != nil {
		b.opts.Log.Error("telegram: cannot find the cards of a request", "lead", leads.Number(leadID), "error", err)
		return
	}
	text, buttons := b.renderCard(card)
	for _, message := range cards {
		if err := b.opts.API.Edit(ctx, message.MessageID, Outgoing{ChatID: message.ChatID, Text: text, Buttons: buttons}); err != nil && ctx.Err() == nil {
			b.opts.Log.Warn("telegram: cannot redraw a card", "lead", leads.Number(leadID), "error", err)
		}
	}
}

// wipeMessages replaces what the bot wrote about erased requests with a line saying so. A
// message Telegram no longer lets us touch (deleted by the person, a chat that is gone) counts
// as wiped; anything else is tried again later, for a month.
func (b *Bot) wipeMessages(ctx context.Context) {
	now := b.opts.Now().UTC()
	due, err := b.sentMessages(ctx, `SELECT id, lead_id, chat_id, message_id FROM bot_messages WHERE wipe_after <= ? ORDER BY id LIMIT 100`, now)
	if err != nil {
		if ctx.Err() == nil {
			b.opts.Log.Error("telegram: cannot look for messages to wipe", "error", err)
		}
		return
	}
	for _, message := range due {
		if ctx.Err() != nil {
			return
		}
		text := "🗑 Заявка #" + leads.Number(message.LeadID) + ": данные клиента удалены."
		err := b.opts.API.Edit(ctx, message.MessageID, Outgoing{ChatID: message.ChatID, Text: text})
		var refused *APIError
		switch {
		case err == nil, errors.As(err, &refused) && (refused.Gone() || refused.Code == 400):
			_, _ = b.opts.DB.ExecContext(ctx, `DELETE FROM bot_messages WHERE id = ?`, message.ID)
		default:
			b.opts.Log.Warn("telegram: cannot wipe a message yet", "lead", leads.Number(message.LeadID), "error", err)
			_, _ = b.opts.DB.ExecContext(ctx, `UPDATE bot_messages SET wipe_after = ? WHERE id = ?`, now.Add(10*time.Minute), message.ID)
		}
		// One message a second per chat is what Telegram allows; a wipe is never in a hurry.
		if !pause(ctx, 50*time.Millisecond) {
			return
		}
	}
	// A month of refusals: the row is dropped, the log has said why often enough.
	_, _ = b.opts.DB.ExecContext(ctx, `DELETE FROM bot_messages WHERE wipe_after IS NOT NULL AND created_at < ? AND wipe_after < ?`, now.AddDate(0, -1, 0), now)
}
