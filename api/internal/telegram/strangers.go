package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// Messages of people the bot does not know: not staff, not a client who came by the link of their
// request. The bot tells them how to reach us (stranger); what they wrote reaches the staff too,
// so that it is not lost — once in six hours per person, twenty people an hour at most, so that
// the bot is nobody's loudspeaker.

// TaskStranger is the outbox task of such a message.
const TaskStranger = "telegram.stranger"

type strangerNote struct {
	TelegramID int64  `json:"telegram_id"`
	Name       string `json:"name"`
	Username   string `json:"username,omitempty"`
	Text       string `json:"text"`
}

// passOn queues what a stranger wrote for the staff.
func (b *Bot) passOn(ctx context.Context, message *Message) {
	text := strings.TrimSpace(message.Text)
	if b.opts.DB == nil || text == "" || message.From == nil {
		return
	}
	who := strconv.FormatInt(message.From.ID, 10)
	if !b.opts.Cache.Allow(ctx, "tg-stranger-note:"+who, 1, 6*time.Hour) || !b.opts.Cache.Allow(ctx, "tg-stranger-notes", 20, time.Hour) {
		return
	}
	shown, shortened := cut(text, 1000)
	if shortened {
		shown += "…"
	}
	note := strangerNote{
		TelegramID: message.From.ID, Name: strings.TrimSpace(message.From.FirstName + " " + message.From.LastName),
		Username: message.From.Username, Text: shown,
	}
	err := outbox.Enqueue(ctx, b.opts.DB, b.opts.Now(), outbox.NewTask{Channel: outbox.ChannelTelegram, Kind: TaskStranger,
		DedupeKey: fmt.Sprintf("stranger:%d:%d", message.From.ID, message.MessageID), Payload: note})
	if err != nil {
		b.opts.Log.Warn("telegram: cannot pass a stranger's message on", "error", err)
		return
	}
	b.opts.Kick()
}

// strangerWrote tells everybody with access what a stranger wrote. There is no request to answer
// in: a reply is written to the person directly, by the link of their profile.
func (b *Bot) strangerWrote(ctx context.Context, task outbox.Task) error {
	var note strangerNote
	if err := json.Unmarshal(task.Payload, &note); err != nil || note.TelegramID == 0 {
		return outbox.Permanent(fmt.Errorf("unreadable task payload: %s", task.Payload))
	}
	recipients, err := b.opts.Access.Recipients(ctx)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return outbox.NotReady(errors.New("nobody has access to the bot yet"))
	}
	who, link := Escape(note.Name), "tg://user?id="+strconv.FormatInt(note.TelegramID, 10)
	if who == "" {
		who = "Без имени"
	}
	if note.Username != "" {
		who, link = who+" · @"+Escape(note.Username), "https://t.me/"+note.Username
	}
	text := "✉️ <b>Сообщение в бот без заявки</b>\n\n<a href=\"" + Escape(link) + "\">" + who + "</a>:\n" + Escape(note.Text) +
		"\n\nЧеловек не открывал ссылку своей заявки, поэтому ответить ему из бота нельзя. Бот объяснил, как оставить заявку; написать человеку можно самому — по ссылке на его имени."
	now := b.opts.Now()
	var failed error
	for _, member := range recipients {
		_, err := b.opts.API.Send(ctx, Outgoing{ChatID: member.TelegramID, Text: text, Silent: member.MutedUntil.Valid && member.MutedUntil.Time.After(now)})
		var refused *APIError
		if err != nil && (!errors.As(err, &refused) || !refused.Gone()) { // a member who blocked the bot is nobody to tell
			failed = errors.Join(failed, err)
		}
	}
	return failed
}
