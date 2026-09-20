package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// Clients in the bot (brief B10.5). A bot cannot write to a person first, so the client comes by
// themselves: the «continue in Telegram» button of the «thank you» page and of the confirmation
// letter carries the random token of their request. From then on the bot is a relay — what the
// client writes lands in the request's conversation and is pushed to the staff; answers of the
// staff go to the client in the bot's name. The client sees the number and the public status of
// their own request, and nothing else: no notes, no names, no other requests.

// clientTexts are in the language of the page the client wrote from.
var clientTexts = map[string]map[string]string{
	"ru": {
		"linked":   "Здравствуйте! Заявка #%s у нас, статус: %s.\n\nЕсли хотите что-то добавить — напишите сюда, сообщение передадут. Ответ придёт в этот же чат.",
		"status":   "Заявка #%s, статус: %s.\n\nНапишите сюда, если хотите что-то добавить.",
		"relayed":  "Передано.",
		"used":     "Эта ссылка уже открыта в другом аккаунте Telegram и второй раз не работает.",
		"slow":     "Слишком много сообщений подряд — давайте чуть медленнее. Последнее сообщение не передано.",
		"failed":   "Не получилось передать сообщение. Попробуйте ещё раз чуть позже.",
		"textonly": "Пока я умею передавать только текст. Напишите словами, а файлы можно прислать письмом.",
		"answer":   "💬 Ответ по заявке #%s:",
		"accepted": "принята", "working": "в работе", "done": "завершена", "closed": "закрыта",
	},
	"uk": {
		"linked":   "Вітаю! Заявка #%s у нас, статус: %s.\n\nЯкщо хочете щось додати — напишіть сюди, повідомлення передадуть. Відповідь прийде в цей самий чат.",
		"status":   "Заявка #%s, статус: %s.\n\nНапишіть сюди, якщо хочете щось додати.",
		"relayed":  "Передано.",
		"used":     "Це посилання вже відкрито в іншому акаунті Telegram і вдруге не працює.",
		"slow":     "Забагато повідомлень поспіль — трохи повільніше, будь ласка. Останнє повідомлення не передано.",
		"failed":   "Не вдалося передати повідомлення. Спробуйте ще раз трохи згодом.",
		"textonly": "Поки що я вмію передавати лише текст. Напишіть словами, а файли можна надіслати листом.",
		"answer":   "💬 Відповідь щодо заявки #%s:",
		"accepted": "прийнята", "working": "в роботі", "done": "завершена", "closed": "закрита",
	},
	"en": {
		"linked":   "Hello! We have your request #%s, status: %s.\n\nIf you would like to add something, write it here and it will be passed on. The answer will come to this chat.",
		"status":   "Request #%s, status: %s.\n\nWrite here if you would like to add something.",
		"relayed":  "Passed on.",
		"used":     "This link has already been opened in another Telegram account and does not work twice.",
		"slow":     "Too many messages in a row — a little slower, please. The last message was not passed on.",
		"failed":   "The message could not be passed on. Please try again a little later.",
		"textonly": "For now I can pass on text only. Please write it in words; files can be sent by email.",
		"answer":   "💬 Reply to your request #%s:",
		"accepted": "received", "working": "in progress", "done": "completed", "closed": "closed",
	},
}

func clientText(lang, key string) string {
	texts, ok := clientTexts[lang]
	if !ok {
		texts = clientTexts["en"]
	}
	return texts[key]
}

// publicStatus is what a client may know about their request: received, in progress, finished.
func publicStatus(lang, status string) string {
	switch status {
	case leads.StatusNew:
		return clientText(lang, "accepted")
	case leads.StatusDone:
		return clientText(lang, "done")
	case leads.StatusRejected:
		return clientText(lang, "closed")
	default:
		return clientText(lang, "working")
	}
}

// clientLead returns the request a Telegram account is a client of — the one linked last.
func (b *Bot) clientLead(ctx context.Context, telegramID int64) (int64, error) {
	var leadID int64
	err := b.opts.DB.QueryRowContext(ctx, `SELECT lead_id FROM bot_clients WHERE telegram_id = ? ORDER BY linked_at DESC, lead_id DESC LIMIT 1`, telegramID).Scan(&leadID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return leadID, err
}

// linkClient handles «/start c_<token>». It reports false when the token opens nothing — the
// caller then treats the person like any passer-by, without saying what was wrong.
func (b *Bot) linkClient(ctx context.Context, message *Message, token string) bool {
	lead, err := b.opts.Leads.ByToken(ctx, strings.TrimPrefix(token, ClientPrefix))
	if err != nil || lead.Status == leads.StatusSpam || lead.Anonymized {
		return false // no such request; a robot's one; one whose storage period is over
	}
	chatID, who := message.Chat.ID, message.From.ID
	// One request — one Telegram account: the link works once. The same person opening it again
	// is welcome; somebody else with the same link is not.
	if _, err := b.opts.DB.ExecContext(ctx, `INSERT IGNORE INTO bot_clients (lead_id, telegram_id, linked_at) VALUES (?, ?, ?)`, lead.ID, who, b.opts.Now().UTC()); err != nil {
		b.opts.Log.Error("telegram: cannot link a client", "error", err)
		b.say(ctx, Outgoing{ChatID: chatID, Text: Escape(clientText(lead.Lang, "failed"))})
		return true
	}
	var owner int64
	if err := b.opts.DB.QueryRowContext(ctx, `SELECT telegram_id FROM bot_clients WHERE lead_id = ?`, lead.ID).Scan(&owner); err != nil || owner != who {
		b.say(ctx, Outgoing{ChatID: chatID, Text: Escape(clientText(lead.Lang, "used"))})
		return true
	}
	if err := b.opts.Leads.ClientLinked(ctx, lead.ID); err != nil {
		b.opts.Log.Warn("telegram: cannot write the client's arrival into the history", "error", err)
	}
	b.say(ctx, Outgoing{ChatID: chatID, Text: Escape(fmt.Sprintf(clientText(lead.Lang, "linked"), lead.Number(), publicStatus(lead.Lang, lead.Status)))})
	// Answers written before the client came have been waiting for this.
	b.opts.Hurry(ctx)
	return true
}

// clientWrites handles a message of somebody who is a client. It reports false for everybody else.
func (b *Bot) clientWrites(ctx context.Context, message *Message, command string) bool {
	leadID, err := b.clientLead(ctx, message.From.ID)
	if err != nil {
		b.opts.Log.Error("telegram: cannot look a client up", "error", err)
		return false
	}
	if leadID == 0 {
		return false
	}
	lead, err := b.opts.Leads.Get(ctx, leadID)
	if err != nil || lead.Anonymized {
		return false // deleted or anonymised: for the bot this person is a passer-by again
	}
	chatID := message.Chat.ID
	say := func(key string, args ...any) {
		b.say(ctx, Outgoing{ChatID: chatID, Text: Escape(fmt.Sprintf(clientText(lead.Lang, key), args...))})
	}
	switch {
	case command != "":
		// Commands are for the staff; a client gets the state of their request, whatever they typed.
		say("status", lead.Number(), publicStatus(lead.Lang, lead.Status))
	case strings.TrimSpace(message.Text) == "":
		say("textonly")
	case !b.opts.Cache.Allow(ctx, "tg-client:"+strconv.FormatInt(message.From.ID, 10), 20, time.Hour):
		say("slow")
	default:
		text, _ := cut(message.Text, 4000)
		if _, err := b.opts.Leads.ClientMessage(ctx, leadID, leads.ChannelTelegram, text); err != nil {
			b.opts.Log.Error("telegram: cannot store a client's message", "lead", lead.Number(), "error", err)
			say("failed")
			return true
		}
		b.opts.Kick()
		say("relayed")
	}
	return true
}

// --- deliveries --------------------------------------------------------------------------------------

// answerClient delivers an answer of the staff to a client who continued in Telegram.
func (b *Bot) answerClient(ctx context.Context, leadID, messageID int64) error {
	lead, err := b.opts.Leads.Get(ctx, leadID)
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Permanent(errors.New("the request is gone (deleted on the client's demand?)"))
	}
	if err != nil {
		return err
	}
	var chatID int64
	err = b.opts.DB.QueryRowContext(ctx, `SELECT telegram_id FROM bot_clients WHERE lead_id = ?`, leadID).Scan(&chatID)
	if errors.Is(err, sql.ErrNoRows) {
		// A bot cannot write first. The answer waits until the client opens the link.
		return outbox.NotReady(errors.New("the client has not opened the bot yet"))
	}
	if err != nil {
		return err
	}
	body, _, err := b.opts.Leads.Message(ctx, leadID, messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Permanent(errors.New("the answer is gone"))
	}
	if err != nil {
		return err
	}
	text, _ := cut(body, messageLimit-200)
	_, err = b.opts.API.Send(ctx, Outgoing{ChatID: chatID, Text: Escape(fmt.Sprintf(clientText(lead.Lang, "answer"), lead.Number())) + "\n\n" + Escape(text)})
	var refused *APIError
	switch {
	case err == nil:
		return b.opts.Leads.MarkDelivery(ctx, messageID, "sent", "")
	case errors.As(err, &refused) && refused.Gone():
		_ = b.opts.Leads.MarkDelivery(ctx, messageID, "failed", "")
		return outbox.Permanent(err) // the client blocked the bot: no retry will help
	default:
		return err
	}
}

// clientWrote pushes a client's new message to everybody with access, as a reply to the card of
// the request in each chat. Run again after a failure, it reaches only those it missed.
func (b *Bot) clientWrote(ctx context.Context, leadID, messageID int64) error {
	lead, err := b.opts.Leads.Get(ctx, leadID)
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Permanent(errors.New("the request is gone"))
	}
	if err != nil {
		return err
	}
	body, channel, err := b.opts.Leads.IncomingMessage(ctx, leadID, messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Permanent(errors.New("the client's message is gone"))
	}
	if err != nil {
		return err
	}
	files, err := b.opts.Leads.MessageFiles(ctx, leadID, messageID)
	if err != nil {
		return err
	}

	via := map[string]string{leads.ChannelTelegram: "в Telegram", leads.ChannelEmail: "письмом"}[channel]
	shown, shortened := cut(body, 3000)
	if shortened {
		shown += "…"
	}
	text := "💬 <b>#" + lead.Number() + "</b> · " + Escape(lead.Name) + " пишет " + via + ":\n\n" + Escape(shown)
	if len(files) > 0 {
		// The files stay on the server (brief B10.7): they are downloaded from the admin area.
		text += fmt.Sprintf("\n\n📎 файлов: %d — в админке", len(files))
	}
	buttons := Keyboard{{{Text: "💬 Ответить", Data: leadButtonData("reply", leadID, "")}, {Text: "📇 Карточка", Data: leadButtonData("card", leadID, "")}}}
	return b.tellStaff(ctx, leadID, "in:"+strconv.FormatInt(messageID, 10), text, buttons)
}

// undelivered tells everybody that a letter to the client came back: the client is still
// waiting for an answer that never arrived.
func (b *Bot) undelivered(ctx context.Context, leadID int64, taskID int64, reason string) error {
	lead, err := b.opts.Leads.Get(ctx, leadID)
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Permanent(errors.New("the request is gone"))
	}
	if err != nil {
		return err
	}
	shown, _ := cut(reason, 300)
	text := "⚠️ <b>#" + lead.Number() + "</b> · письмо клиенту не доставлено\n\n" + Escape(shown) +
		"\n\n" + Escape(lead.Name) + " ответа не получил(а). Проверьте адрес в карточке или свяжитесь другим способом."
	buttons := Keyboard{{{Text: "📇 Карточка", Data: leadButtonData("card", leadID, "")}}}
	return b.tellStaff(ctx, leadID, "undelivered:"+strconv.FormatInt(taskID, 10), text, buttons)
}

// tellStaff sends a text about a request to everybody with access, as a reply to the card of the
// request in each chat. ref names the occasion: run again after a failure, the text reaches only
// those it missed. What is sent is remembered — and wiped with the request.
func (b *Bot) tellStaff(ctx context.Context, leadID int64, ref, text string, buttons Keyboard) error {
	recipients, err := b.opts.Access.Recipients(ctx)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return outbox.NotReady(errors.New("nobody has access to the bot yet"))
	}
	reached, err := b.sentMessages(ctx, `SELECT id, lead_id, chat_id, message_id FROM bot_messages WHERE lead_id = ? AND ref = ?`, leadID, ref)
	if err != nil {
		return err
	}
	cards, err := b.sentMessages(ctx, `SELECT id, lead_id, chat_id, message_id FROM bot_messages WHERE lead_id = ? AND kind = 'card' AND wipe_after IS NULL ORDER BY id`, leadID)
	if err != nil {
		return err
	}
	done, card := map[int64]bool{}, map[int64]int64{}
	for _, message := range reached {
		done[message.ChatID] = true
	}
	for _, message := range cards {
		card[message.ChatID] = message.MessageID // the latest card of the chat
	}

	now := b.opts.Now()
	var failed error
	for _, member := range recipients {
		if done[member.TelegramID] {
			continue
		}
		sent, err := b.opts.API.Send(ctx, Outgoing{
			ChatID: member.TelegramID, Text: text, Buttons: buttons, ReplyTo: card[member.TelegramID],
			Silent: member.MutedUntil.Valid && member.MutedUntil.Time.After(now),
		})
		var refused *APIError
		switch {
		case err == nil:
			b.rememberAs(ctx, leadID, member.TelegramID, sent.MessageID, kindText, ref)
		case errors.As(err, &refused) && refused.Gone():
			b.opts.Log.Warn("telegram: a member cannot be reached any more", "member", member.Name, "error", err)
		default:
			failed = errors.Join(failed, err)
		}
	}
	return failed
}
