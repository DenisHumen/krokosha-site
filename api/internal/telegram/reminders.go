package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// Things the bot says without being asked (brief B10.4): a second push about a new request
// nobody took, and a morning list of what is open.

const (
	digestStateKey = "bot.digest.last" // app_meta: the day the last digest was sent for
	// digestWindow: a digest is sent at its time or a little later (the service was restarting),
	// never in the afternoon because the server was switched on then.
	digestWindow = 2 * time.Hour
)

// RunReminders looks once a minute whether there is something to remind people of.
func (b *Bot) RunReminders(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			working, cancel := context.WithTimeout(ctx, 50*time.Second)
			b.remind(working)
			b.digest(working)
			cancel()
		}
	}
}

// broadcast sends one message about a request to everybody with access, as a reply to the card
// of the request in each chat.
func (b *Bot) broadcast(ctx context.Context, leadID int64, ref, text string, buttons Keyboard) (reached int) {
	recipients, err := b.opts.Access.Recipients(ctx)
	if err != nil {
		b.opts.Log.Error("telegram: cannot list the members", "error", err)
		return 0
	}
	cards, _ := b.sentMessages(ctx, `SELECT id, lead_id, chat_id, message_id FROM bot_messages WHERE lead_id = ? AND kind = 'card' AND wipe_after IS NULL ORDER BY id`, leadID)
	card := map[int64]int64{}
	for _, message := range cards {
		card[message.ChatID] = message.MessageID
	}
	now := b.opts.Now()
	for _, member := range recipients {
		sent, err := b.opts.API.Send(ctx, Outgoing{
			ChatID: member.TelegramID, Text: text, Buttons: buttons, ReplyTo: card[member.TelegramID],
			Silent: member.MutedUntil.Valid && member.MutedUntil.Time.After(now),
		})
		if err != nil {
			if ctx.Err() == nil {
				b.opts.Log.Warn("telegram: cannot send a reminder", "member", member.Name, "error", err)
			}
			continue
		}
		reached++
		if leadID != 0 {
			b.rememberAs(ctx, leadID, member.TelegramID, sent.MessageID, kindText, ref)
		}
	}
	return reached
}

// remind pushes once more about new requests that have been waiting too long for somebody to
// take them. One reminder per request: a second one would be nagging.
func (b *Bot) remind(ctx context.Context) {
	if b.opts.RemindAfter <= 0 {
		return
	}
	now := b.opts.Now()
	due, err := b.opts.Leads.Unclaimed(ctx, now.Add(-b.opts.RemindAfter))
	if err != nil {
		if ctx.Err() == nil {
			b.opts.Log.Error("telegram: cannot look for requests nobody took", "error", err)
		}
		return
	}
	for _, id := range due {
		lead, err := b.opts.Leads.Get(ctx, id)
		if err != nil {
			continue
		}
		waiting := humanDuration(now.Sub(lead.CreatedAt))
		text := "⏰ <b>#" + lead.Number() + "</b> · " + Escape(lead.Name) + " — ждёт уже " + waiting + ", заявку никто не взял."
		buttons := Keyboard{{{Text: "✅ Взять в работу", Data: leadButtonData("take", id, "")}, {Text: "📇 Карточка", Data: leadButtonData("card", id, "")}}}
		if b.broadcast(ctx, id, "remind", text, buttons) == 0 {
			continue // nobody to tell yet: the request keeps waiting for its reminder
		}
		if err := b.opts.Leads.MarkReminded(ctx, id); err != nil {
			b.opts.Log.Error("telegram: cannot note a reminder — it may be repeated", "lead", lead.Number(), "error", err)
		}
	}
}

// digest sends the morning list of open requests, once a day, at the owner's local time.
func (b *Bot) digest(ctx context.Context) {
	at, err := time.Parse("15:04", b.opts.DigestAt)
	if b.opts.DigestAt == "" || err != nil {
		return
	}
	now := b.opts.Now().In(b.opts.Location)
	due := time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), 0, 0, b.opts.Location)
	if now.Before(due) || now.Sub(due) > digestWindow {
		return
	}
	today := now.Format(time.DateOnly)
	var last string
	if err := b.opts.DB.QueryRowContext(ctx, `SELECT value FROM app_meta WHERE name = ?`, digestStateKey).Scan(&last); err != nil && !errors.Is(err, sql.ErrNoRows) {
		b.opts.Log.Error("telegram: cannot read the state of the digest", "error", err)
		return
	}
	if last == today {
		return
	}
	// Written first: a digest that fails half-way must not be sent again every minute.
	if _, err := b.opts.DB.ExecContext(ctx, `INSERT INTO app_meta (name, value, updated_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value), updated_at = VALUES(updated_at)`,
		digestStateKey, today, b.opts.Now().UTC()); err != nil {
		b.opts.Log.Error("telegram: cannot write the state of the digest", "error", err)
		return
	}

	counts, err := b.opts.Leads.Counts(ctx)
	if err != nil {
		b.opts.Log.Error("telegram: cannot count the requests", "error", err)
		return
	}
	open := counts[leads.StatusNew] + counts[leads.StatusInProgress] + counts[leads.StatusWaitingClient]
	if open == 0 {
		return // a quiet morning needs no message
	}
	list, buttons, err := b.listView(ctx, "Открытые заявки", leads.Filter{Status: "open", Limit: listLimit}, "")
	if err != nil {
		b.opts.Log.Error("telegram: cannot list the requests", "error", err)
		return
	}
	head := fmt.Sprintf("☀️ Доброе утро! 🟡 новых: %d · 🟢 в работе: %d · 🔵 ждём клиента: %d",
		counts[leads.StatusNew], counts[leads.StatusInProgress], counts[leads.StatusWaitingClient])
	b.broadcast(ctx, 0, "", strings.Join([]string{head, "", list}, "\n"), buttons)
}
