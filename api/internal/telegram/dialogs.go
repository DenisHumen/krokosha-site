package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// What people do with a request from its card (brief B10.4). Every action ends in a method of
// leads.Store — the same one the admin area calls — so the two can never disagree about who took
// a request or what its status is. Things that need a text (an answer, a note, a reason) are
// short dialogs: the bot remembers what it is waiting for, per person.

// Dialog is what the bot is waiting for from a person.
type Dialog struct {
	Kind   string `json:"kind"` // note | reply | reject_reason | reject_letter
	LeadID int64  `json:"lead"`
	Draft  string `json:"draft,omitempty"`  // a text waiting for «send»
	Reason string `json:"reason,omitempty"` // why a request is being rejected
	// Template: the answer was made from it — its files go with the text, and the card does not
	// offer it again.
	Template int64     `json:"template,omitempty"`
	At       time.Time `json:"at"`
}

// dialogLifetime: a text typed hours later is not an answer to a forgotten question.
const dialogLifetime = 2 * time.Hour

const (
	dialogNote         = "note"
	dialogReply        = "reply"
	dialogRejectReason = "reject_reason"
	dialogRejectLetter = "reject_letter"
)

// Dialog returns what the bot is waiting for from a person, or nil.
func (a *Access) Dialog(ctx context.Context, telegramID int64) (*Dialog, error) {
	var raw sql.NullString
	if err := a.db.QueryRowContext(ctx, `SELECT state FROM bot_users WHERE telegram_id = ?`, telegramID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // «no dialog» is a normal answer
		}
		return nil, err
	}
	var dialog Dialog
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &dialog) != nil || a.now().Sub(dialog.At) > dialogLifetime {
		return nil, nil //nolint:nilnil // as above
	}
	return &dialog, nil
}

// SetDialog remembers what the bot is waiting for; nil — nothing any more.
func (a *Access) SetDialog(ctx context.Context, telegramID int64, dialog *Dialog) error {
	var raw sql.NullString
	if dialog != nil {
		dialog.At = a.now().UTC()
		encoded, err := json.Marshal(dialog)
		if err != nil {
			return err
		}
		raw = sql.NullString{String: string(encoded), Valid: true}
	}
	_, err := a.db.ExecContext(ctx, `UPDATE bot_users SET state = ? WHERE telegram_id = ?`, raw, telegramID)
	return err
}

// rejectReasons are the quick reasons of the brief; «спам» is a status of its own and only a new
// request can get it.
var rejectReasons = []string{"не мой профиль", "нет свободного времени", "бюджет не подходит"}

// --- buttons of a card ------------------------------------------------------------------------------

// leadButton handles «l:<action>:<lead>[:<argument>]».
func (b *Bot) leadButton(ctx context.Context, query *CallbackQuery, member *Member, parts []string, answer func(string, bool)) {
	leadID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || leadID <= 0 || query.Message == nil {
		answer("Эта кнопка больше не работает.", false)
		return
	}
	chatID, argument := query.Message.Chat.ID, ""
	if len(parts) > 3 {
		argument = parts[3]
	}
	if !member.CanAct() && parts[1] != "full" && parts[1] != "history" && parts[1] != "card" {
		answer(notifyOnly, true)
		return
	}
	card, err := b.opts.Leads.Card(ctx, leadID)
	if errors.Is(err, leads.ErrNotFound) {
		answer("Заявки больше нет: данные клиента удалены.", true)
		return
	}
	if err != nil {
		b.opts.Log.Error("telegram: cannot read a request", "error", err)
		answer("Не получилось. Попробуйте ещё раз.", true)
		return
	}
	number := "#" + card.Lead.Number()

	// done ends a dialog step: the buttons of the message that was just used go away, so that
	// nothing can be pressed twice.
	done := func(text string) {
		if err := b.opts.API.Edit(ctx, query.Message.MessageID, Outgoing{ChatID: chatID, Text: text}); err != nil && ctx.Err() == nil {
			b.opts.Log.Warn("telegram: cannot close a dialog message", "error", err)
		}
	}
	failed := func(err error) {
		switch {
		case errors.Is(err, leads.ErrBadTransition):
			answer("Так перейти уже нельзя: заявку успели изменить. Карточка обновлена.", true)
			b.Changed(leadID)
		default:
			b.opts.Log.Error("telegram: an action on a request failed", "lead", number, "action", parts[1], "error", err)
			answer("Не получилось. Попробуйте ещё раз.", true)
		}
	}

	switch parts[1] {
	case "take":
		takenBy, err := b.opts.Leads.Take(ctx, leadID, member.Actor())
		switch {
		case errors.Is(err, leads.ErrAlreadyTaken):
			// The first press wins, wherever it was made; the second learns who was faster.
			answer("Заявку "+number+" уже взял(а) "+takenBy+".", true)
			b.Changed(leadID)
		case err != nil:
			failed(err)
		default:
			answer("Заявка "+number+" ваша.", false)
		}
	case "done", "reopen":
		status := map[string]string{"done": leads.StatusDone, "reopen": leads.StatusInProgress}[parts[1]]
		if err := b.opts.Leads.SetStatus(ctx, leadID, member.Actor(), status, ""); err != nil {
			failed(err)
			return
		}
		answer(number+": "+strings.ToLower(statusNames[status]), false)
	case "card":
		answer("", false)
		b.sendCard(ctx, chatID, member, leadID)
	case "full":
		answer("", false)
		b.sendFull(ctx, chatID, card)
	case "history":
		answer("", false)
		b.sendHistory(ctx, chatID, card)
	case "note":
		answer("", false)
		b.ask(ctx, member, &Dialog{Kind: dialogNote, LeadID: leadID}, chatID,
			"📝 Заметка к "+number+" — клиент её не увидит, коллеги увидят. Напишите текст следующим сообщением.", "Заметка…")
	case "reply":
		answer("", false)
		b.offerReply(ctx, member, chatID, card)
	case "own": // «свой текст» and «изменить»
		answer("", false)
		done("✍️ " + number + ": жду текст.")
		prompt, placeholder := "Напишите ответ клиенту по "+number+" следующим сообщением. Перед отправкой покажу, как он выглядит.", "Ответ клиенту…"
		if card.ReplyVia == leads.MethodPhone {
			prompt, placeholder = "Клиент оставил телефон: сюда записывается итог звонка по "+number+".", "Позвонил — итог: …"
		}
		b.ask(ctx, member, &Dialog{Kind: dialogReply, LeadID: leadID}, chatID, prompt, placeholder)
	case "tpl":
		answer("", false)
		b.useTemplate(ctx, member, query, card, argument)
	case "send":
		b.sendDraft(ctx, member, card, answer, done, failed)
	case "reject":
		answer("", false)
		b.offerReasons(ctx, chatID, card)
	case "why":
		b.reasonChosen(ctx, member, query, card, argument, answer, done, failed)
	case "silent":
		b.rejectNow(ctx, member, card, "", answer, done, failed)
	case "cancel":
		_ = b.opts.Access.SetDialog(ctx, member.TelegramID, nil)
		answer("Отменено.", false)
		done("Отменено: " + number + ".")
	default:
		answer("Эта кнопка больше не работает.", false)
	}
}

// ask opens the reply field for a text the bot then waits for.
func (b *Bot) ask(ctx context.Context, member *Member, dialog *Dialog, chatID int64, prompt, placeholder string) {
	if err := b.opts.Access.SetDialog(ctx, member.TelegramID, dialog); err != nil {
		b.opts.Log.Error("telegram: cannot remember a dialog", "error", err)
		return
	}
	b.sayAbout(ctx, dialog.LeadID, Outgoing{ChatID: chatID, Text: Escape(prompt) + "\n\n/cancel — передумал(а)", ForceReply: true, Placeholder: placeholder})
}

func leadButtonData(action string, leadID int64, argument string) string {
	data := "l:" + action + ":" + strconv.FormatInt(leadID, 10)
	if argument != "" {
		data += ":" + argument
	}
	return data
}

// templates returns the ready-made texts of a kind in the client's language: eight of them. Answers
// come the way the card of the admin area orders them (leads.Rank) — what fits the conversation
// first; top marks the three that fit best.
func (b *Bot) templates(ctx context.Context, kind string, card *leads.Card) (out []leads.Template, top map[int64]bool) {
	all, err := b.opts.Leads.Templates(ctx, kind)
	if err != nil {
		b.opts.Log.Error("telegram: cannot read the templates", "error", err)
		return nil, nil
	}
	top = map[int64]bool{}
	if kind == "reply" {
		sent, err := b.opts.Leads.UsedTemplates(ctx, card.Lead.ID)
		if err != nil {
			b.opts.Log.Warn("telegram: cannot tell the templates sent already", "error", err)
		}
		for _, suggestion := range leads.Rank(all, leads.SituationOf(card, sent, b.opts.Now())) {
			if len(out) == 8 {
				break
			}
			out = append(out, suggestion.Template)
			top[suggestion.Template.ID] = suggestion.Top
		}
		return out, top
	}
	for _, item := range all {
		if item.Lang == card.Lead.Lang && len(out) < 8 {
			out = append(out, item)
		}
	}
	return out, top
}

// templateLabel is the button of a template: «★ Стоимость · 📎2».
func templateLabel(item leads.Template, top bool) string {
	label := item.Title
	if top {
		label = "★ " + label
	}
	if len(item.Media) > 0 {
		label += " · 📎" + strconv.Itoa(len(item.Media))
	}
	return label
}

// --- reading --------------------------------------------------------------------------------------

func (b *Bot) sendFull(ctx context.Context, chatID int64, card *leads.Card) {
	text := "📄 <b>#" + card.Lead.Number() + "</b> · " + Escape(card.Lead.Name) + "\n\n"
	for _, chunk := range chunks(card.Lead.Description, messageLimit-len(text)) {
		b.sayAbout(ctx, card.Lead.ID, Outgoing{ChatID: chatID, Text: text + Escape(chunk)})
		text = ""
	}
}

// chunks cuts a long text into pieces Telegram accepts, at line ends where it can.
func chunks(text string, limit int) []string {
	var out []string
	runes := []rune(text)
	for len(runes) > limit {
		at := limit
		for i := limit; i > limit/2; i-- {
			if runes[i] == '\n' {
				at = i
				break
			}
		}
		out = append(out, strings.TrimSpace(string(runes[:at])))
		runes = runes[at:]
	}
	return append(out, strings.TrimSpace(string(runes)))
}

func (b *Bot) sendHistory(ctx context.Context, chatID int64, card *leads.Card) {
	lines := []string{"🕘 <b>История #" + card.Lead.Number() + "</b>", ""}
	for _, entry := range card.Feed {
		at := entry.At.In(b.opts.Location).Format("02.01 15:04")
		var line string
		switch {
		case entry.Kind == "event" && entry.Action == "created":
			line = "заявка получена"
		case entry.Kind == "event" && entry.Action == "assigned":
			line = Escape(entry.Author) + " взял(а) в работу"
		case entry.Kind == "event" && entry.Action == "status":
			line = Escape(entry.Author) + ": " + statusNames[entry.From] + " → " + statusNames[entry.To]
			if entry.Body != "" {
				line += " — " + Escape(entry.Body)
			}
		case entry.Kind == "event":
			continue // «replied» and the like: the message itself is in the feed
		case entry.Direction == "in":
			body, _ := cut(entry.Body, 300)
			line = "👤 " + Escape(body)
		case entry.Direction == "note":
			body, _ := cut(entry.Body, 300)
			line = "📝 " + Escape(entry.Author) + ": " + Escape(body)
		default:
			body, _ := cut(entry.Body, 300)
			delivery := map[string]string{"queued": " (ждёт отправки)", "failed": " (НЕ доставлено)"}[entry.Delivery]
			line = "💬 " + Escape(entry.Author) + delivery + ": " + Escape(body)
		}
		lines = append(lines, "<i>"+at+"</i> "+line)
	}
	text := strings.Join(lines, "\n")
	if shortened, was := cut(text, messageLimit); was {
		text = shortened + "\n… остальное — в админке"
	}
	b.sayAbout(ctx, card.Lead.ID, Outgoing{ChatID: chatID, Text: text})
}

// --- answering ------------------------------------------------------------------------------------

// channelName says where the next answer goes (leads.Card.Reach): everywhere the client can be
// reached — the addresses of the form and of the client's letters, the bot, the personal account.
func (b *Bot) channelName(_ context.Context, card *leads.Card) string {
	var addresses []string
	telegram := ""
	for _, target := range card.Reach.Targets {
		switch {
		case target.Channel == leads.ChannelEmail:
			addresses = append(addresses, target.To)
		case target.To != "":
			telegram = "в Telegram через бота"
		case telegram == "":
			telegram = "в Telegram, когда клиент откроет бота по ссылке «Продолжить в Telegram»"
		}
	}
	var ways []string
	if len(addresses) > 0 {
		ways = append(ways, "письмом на "+strings.Join(addresses, ", "))
	}
	if telegram != "" {
		ways = append(ways, telegram)
	}
	if card.Reach.Account {
		ways = append(ways, "в личный кабинет")
	}
	return strings.Join(ways, "; ")
}

func (b *Bot) offerReply(ctx context.Context, member *Member, chatID int64, card *leads.Card) {
	lead := card.Lead
	if card.ReplyVia == leads.MethodPhone {
		// There is nothing to send to a phone number: the «answer» is the record of the call.
		b.ask(ctx, member, &Dialog{Kind: dialogReply, LeadID: lead.ID}, chatID,
			"📞 Клиент оставил телефон: "+lead.ContactValue+". Позвоните и запишите итог по #"+lead.Number()+" следующим сообщением.", "Позвонил — итог: …")
		return
	}
	var buttons Keyboard
	templates, top := b.templates(ctx, "reply", card)
	for _, item := range templates {
		buttons = append(buttons, []Button{{Text: templateLabel(item, top[item.ID]), Data: leadButtonData("tpl", lead.ID, strconv.FormatInt(item.ID, 10))}})
	}
	buttons = append(buttons, []Button{{Text: "✍️ Свой текст", Data: leadButtonData("own", lead.ID, "")}, {Text: "Отмена", Data: leadButtonData("cancel", lead.ID, "")}})
	b.sayAbout(ctx, lead.ID, Outgoing{ChatID: chatID, Buttons: buttons,
		Text: "💬 Ответ клиенту по <b>#" + lead.Number() + "</b> (" + Escape(lead.Name) + ") — уйдёт " + Escape(b.channelName(ctx, card)) + ".\nВыберите шаблон или напишите свой текст."})
}

func (b *Bot) useTemplate(ctx context.Context, member *Member, query *CallbackQuery, card *leads.Card, rawID string) {
	for _, kind := range []string{"reply", "reject"} {
		templates, _ := b.templates(ctx, kind, card)
		for _, item := range templates {
			if strconv.FormatInt(item.ID, 10) != rawID {
				continue
			}
			dialog := &Dialog{Kind: dialogReply, LeadID: card.Lead.ID, Draft: leads.FillTemplate(item.Body, card.Lead, leads.LinksFor(b.opts.SiteURL, card.Lead))}
			if kind == "reply" {
				dialog.Template = item.ID
			}
			if kind == "reject" {
				previous, _ := b.opts.Access.Dialog(ctx, member.TelegramID)
				if previous == nil || previous.Kind != dialogRejectLetter || previous.LeadID != card.Lead.ID {
					break // the reason was chosen too long ago: start over from the card
				}
				dialog.Kind, dialog.Reason = dialogRejectLetter, previous.Reason
			}
			_ = b.opts.API.Edit(ctx, query.Message.MessageID, Outgoing{ChatID: query.Message.Chat.ID, Text: "Шаблон: " + Escape(item.Title)})
			b.preview(ctx, member, query.Message.Chat.ID, card, dialog)
			return
		}
	}
	_ = b.opts.API.Edit(ctx, query.Message.MessageID, Outgoing{ChatID: query.Message.Chat.ID, Text: "Этого шаблона больше нет. Откройте «Ответить» на карточке ещё раз."})
}

// preview shows a text the way the client will get it, with «send», «change» and «cancel».
func (b *Bot) preview(ctx context.Context, member *Member, chatID int64, card *leads.Card, dialog *Dialog) {
	lead := card.Lead
	if err := b.opts.Access.SetDialog(ctx, member.TelegramID, dialog); err != nil {
		b.opts.Log.Error("telegram: cannot remember a draft", "error", err)
		return
	}
	title, send := "Предпросмотр ответа по <b>#"+lead.Number()+"</b> — уйдёт "+Escape(b.channelName(ctx, card))+":", "✅ Отправить"
	buttons := Keyboard{{{Text: send, Data: leadButtonData("send", lead.ID, "")}, {Text: "✏️ Изменить", Data: leadButtonData("own", lead.ID, "")}}}
	switch {
	case card.ReplyVia == leads.MethodPhone:
		title = "Итог звонка по <b>#" + lead.Number() + "</b> — клиенту ничего не отправляется:"
		buttons[0][0].Text = "✅ Записать"
	case dialog.Kind == dialogRejectLetter:
		title = "Письмо с отказом по <b>#" + lead.Number() + "</b> (причина: " + Escape(dialog.Reason) + ") — уйдёт " + Escape(b.channelName(ctx, card)) + ":"
		buttons[0][0].Text = "✅ Отправить и отклонить"
		buttons = append(buttons, []Button{{Text: "Закрыть молча", Data: leadButtonData("silent", lead.ID, "")}})
	}
	buttons = append(buttons, []Button{{Text: "Отмена", Data: leadButtonData("cancel", lead.ID, "")}})
	draft, _ := cut(dialog.Draft, messageLimit-600)
	text := title + "\n\n" + Escape(draft)
	if files := b.templateFiles(ctx, dialog); len(files) > 0 && card.ReplyVia != leads.MethodPhone {
		names := make([]string, 0, len(files))
		for _, file := range files {
			names = append(names, Escape(file.Filename))
		}
		text += "\n\n📎 С ответом уйдут файлы шаблона: " + strings.Join(names, ", ")
	}
	b.sayAbout(ctx, lead.ID, Outgoing{ChatID: chatID, Text: text, Buttons: buttons})
}

// templateFiles are the files of the template an answer was made from; none if it has gone.
func (b *Bot) templateFiles(ctx context.Context, dialog *Dialog) []leads.Media {
	if dialog.Template == 0 || dialog.Kind != dialogReply {
		return nil
	}
	item, err := b.opts.Leads.Template(ctx, dialog.Template)
	if err != nil {
		return nil
	}
	return item.Media
}

func (b *Bot) sendDraft(ctx context.Context, member *Member, card *leads.Card, answer func(string, bool), done func(string), failed func(error)) {
	number := "#" + card.Lead.Number()
	dialog, err := b.opts.Access.Dialog(ctx, member.TelegramID)
	if err != nil || dialog == nil || dialog.LeadID != card.Lead.ID || strings.TrimSpace(dialog.Draft) == "" {
		answer("Черновик не найден — начните с кнопки на карточке.", true)
		return
	}
	if dialog.Kind == dialogRejectLetter {
		b.rejectNow(ctx, member, card, dialog.Draft, answer, done, failed)
		return
	}
	answerOf := leads.Answer{Text: dialog.Draft}
	if dialog.Template != 0 {
		answerOf.Templates = []int64{dialog.Template}
		if card.ReplyVia != leads.MethodPhone {
			for _, file := range b.templateFiles(ctx, dialog) {
				answerOf.Media = append(answerOf.Media, file.ID)
			}
		}
	}
	if _, err := b.opts.Leads.ReplyWith(ctx, card.Lead.ID, member.Actor(), answerOf); err != nil {
		failed(err)
		return
	}
	_ = b.opts.Access.SetDialog(ctx, member.TelegramID, nil)
	b.opts.Kick()
	b.opts.Audit(ctx, "bot:"+member.Actor(), "lead.reply", card.Lead.Number(), "")
	if card.ReplyVia == leads.MethodPhone {
		answer("Записано.", false)
		done("📞 Итог звонка по " + number + " записан.")
		return
	}
	answer("Ответ поставлен в очередь на отправку.", false)
	done("💬 Ответ по " + number + " уходит клиенту. Доставку видно в «Истории».")
}

// --- rejecting ------------------------------------------------------------------------------------

func (b *Bot) offerReasons(ctx context.Context, chatID int64, card *leads.Card) {
	lead := card.Lead
	var buttons Keyboard
	for i, reason := range rejectReasons {
		buttons = append(buttons, []Button{{Text: reason, Data: leadButtonData("why", lead.ID, strconv.Itoa(i))}})
	}
	if lead.Status == leads.StatusNew {
		buttons = append(buttons, []Button{{Text: "🚫 это спам", Data: leadButtonData("why", lead.ID, "spam")}})
	}
	buttons = append(buttons, []Button{{Text: "✍️ Своя причина", Data: leadButtonData("why", lead.ID, "own")}, {Text: "Отмена", Data: leadButtonData("cancel", lead.ID, "")}})
	b.sayAbout(ctx, lead.ID, Outgoing{ChatID: chatID, Text: "❌ Отклонить <b>#" + lead.Number() + "</b> (" + Escape(lead.Name) + "). Причина?", Buttons: buttons})
}

func (b *Bot) reasonChosen(ctx context.Context, member *Member, query *CallbackQuery, card *leads.Card, choice string, answer func(string, bool), done func(string), failed func(error)) {
	lead := card.Lead
	number := "#" + lead.Number()
	switch choice {
	case "spam":
		if err := b.opts.Leads.SetStatus(ctx, lead.ID, member.Actor(), leads.StatusSpam, ""); err != nil {
			failed(err)
			return
		}
		b.opts.Audit(ctx, "bot:"+member.Actor(), "lead.status", lead.Number(), leads.StatusSpam)
		answer("В спам.", false)
		done("🚫 " + number + " — в спаме. Вернуть можно в админке.")
	case "own":
		answer("", false)
		done("✍️ " + number + ": жду причину.")
		b.ask(ctx, member, &Dialog{Kind: dialogRejectReason, LeadID: lead.ID}, query.Message.Chat.ID, "Причина отказа по "+number+" — следующим сообщением.", "Причина…")
	default:
		index, err := strconv.Atoi(choice)
		if err != nil || index < 0 || index >= len(rejectReasons) {
			answer("Эта кнопка больше не работает.", false)
			return
		}
		answer("", false)
		done("❌ " + number + ": " + rejectReasons[index])
		b.offerLetter(ctx, member, query.Message.Chat.ID, card, rejectReasons[index])
	}
}

// offerLetter asks whether the client should get a polite refusal (brief B10.4) or none at all.
func (b *Bot) offerLetter(ctx context.Context, member *Member, chatID int64, card *leads.Card, reason string) {
	lead := card.Lead
	if err := b.opts.Access.SetDialog(ctx, member.TelegramID, &Dialog{Kind: dialogRejectLetter, LeadID: lead.ID, Reason: reason}); err != nil {
		b.opts.Log.Error("telegram: cannot remember a dialog", "error", err)
		return
	}
	buttons := Keyboard{}
	if card.ReplyVia != leads.MethodPhone {
		templates, _ := b.templates(ctx, "reject", card)
		for _, item := range templates {
			buttons = append(buttons, []Button{{Text: "✉️ " + item.Title, Data: leadButtonData("tpl", lead.ID, strconv.FormatInt(item.ID, 10))}})
		}
	}
	buttons = append(buttons, []Button{{Text: "Закрыть молча", Data: leadButtonData("silent", lead.ID, "")}, {Text: "Отмена", Data: leadButtonData("cancel", lead.ID, "")}})
	text := "Отправить клиенту вежливый отказ по <b>#" + lead.Number() + "</b>? Письмо можно будет посмотреть перед отправкой."
	if card.ReplyVia == leads.MethodPhone {
		text = "Клиент оставил только телефон — письма не будет. Отклонить <b>#" + lead.Number() + "</b>?"
	}
	b.sayAbout(ctx, lead.ID, Outgoing{ChatID: chatID, Text: text, Buttons: buttons})
}

// rejectNow rejects a request: with a letter to the client first, if there is one.
func (b *Bot) rejectNow(ctx context.Context, member *Member, card *leads.Card, letter string, answer func(string, bool), done func(string), failed func(error)) {
	number := "#" + card.Lead.Number()
	dialog, err := b.opts.Access.Dialog(ctx, member.TelegramID)
	if err != nil || dialog == nil || dialog.LeadID != card.Lead.ID || dialog.Kind != dialogRejectLetter {
		answer("Причина не выбрана — начните с «Отклонить» на карточке.", true)
		return
	}
	if strings.TrimSpace(letter) != "" {
		if _, err := b.opts.Leads.Reply(ctx, card.Lead.ID, member.Actor(), letter); err != nil {
			failed(err)
			return
		}
		b.opts.Kick()
	}
	if err := b.opts.Leads.SetStatus(ctx, card.Lead.ID, member.Actor(), leads.StatusRejected, dialog.Reason); err != nil {
		failed(err)
		return
	}
	_ = b.opts.Access.SetDialog(ctx, member.TelegramID, nil)
	b.opts.Audit(ctx, "bot:"+member.Actor(), "lead.status", card.Lead.Number(), leads.StatusRejected)
	answer("Отклонена.", false)
	if strings.TrimSpace(letter) != "" {
		done("❌ " + number + " отклонена, письмо клиенту уходит.")
	} else {
		done("❌ " + number + " отклонена молча.")
	}
}

// --- a reply to a message ---------------------------------------------------------------------------

// replyTo takes a reply — Telegram's own, a swipe — to a message of the bot about a request (its
// card, a message of its client, a preview) as the text of an answer to that request: the draft
// goes to the preview with «send», as after «💬 Ответить». It returns false when the message replies
// to nothing the bot sent about a request, or when the bot is waiting for a text about that very
// request (a note, a reason, an answer being written): then the dialog takes the text — the prompts
// of the dialogs open Telegram's reply field themselves.
func (b *Bot) replyTo(ctx context.Context, message *Message, member *Member) bool {
	if message.ReplyToMessage == nil || strings.TrimSpace(message.Text) == "" || b.opts.DB == nil || b.opts.Leads == nil {
		return false
	}
	var leadID int64
	err := b.opts.DB.QueryRowContext(ctx, `SELECT lead_id FROM bot_messages WHERE chat_id = ? AND message_id = ? AND wipe_after IS NULL`,
		message.Chat.ID, message.ReplyToMessage.MessageID).Scan(&leadID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			b.opts.Log.Error("telegram: cannot tell what a reply answers", "error", err)
		}
		return false
	}
	if dialog, err := b.opts.Access.Dialog(ctx, member.TelegramID); err == nil && dialog != nil && dialog.LeadID == leadID {
		return false
	}
	card, err := b.opts.Leads.Card(ctx, leadID)
	switch {
	case errors.Is(err, leads.ErrNotFound):
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: "Заявки больше нет: данные клиента удалены."})
		return true
	case err != nil:
		b.opts.Log.Error("telegram: cannot read a request", "error", err)
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: "Не получилось. Попробуйте ещё раз."})
		return true
	case card.Lead.Status == leads.StatusSpam || card.AnonymizedAt.Valid:
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: "По заявке #" + card.Lead.Number() + " ответить нельзя: это спам или данные клиента уже удалены."})
		return true
	}
	b.preview(ctx, member, message.Chat.ID, card, &Dialog{Kind: dialogReply, LeadID: leadID, Draft: strings.TrimSpace(message.Text)})
	return true
}

// --- texts the bot was waiting for ----------------------------------------------------------------

// dialogText handles a plain message from a member. It returns false when the bot was not waiting
// for anything.
func (b *Bot) dialogText(ctx context.Context, message *Message, member *Member) bool {
	dialog, err := b.opts.Access.Dialog(ctx, member.TelegramID)
	if err != nil {
		b.opts.Log.Error("telegram: cannot read a dialog", "error", err)
		return false
	}
	if dialog == nil {
		return false
	}
	chatID, text := message.Chat.ID, strings.TrimSpace(message.Text)
	if text == "" {
		b.say(ctx, Outgoing{ChatID: chatID, Text: "Нужен текст. /cancel — передумал(а)."})
		return true
	}
	card, err := b.opts.Leads.Card(ctx, dialog.LeadID)
	if err != nil {
		_ = b.opts.Access.SetDialog(ctx, member.TelegramID, nil)
		b.say(ctx, Outgoing{ChatID: chatID, Text: "Заявки больше нет: данные клиента удалены."})
		return true
	}
	number := "#" + card.Lead.Number()

	switch dialog.Kind {
	case dialogNote:
		if err := b.opts.Leads.AddNote(ctx, dialog.LeadID, member.Actor(), text); err != nil {
			b.opts.Log.Error("telegram: cannot store a note", "error", err)
			b.say(ctx, Outgoing{ChatID: chatID, Text: "Не получилось сохранить заметку. Попробуйте ещё раз."})
			return true
		}
		_ = b.opts.Access.SetDialog(ctx, member.TelegramID, nil)
		b.sayAbout(ctx, dialog.LeadID, Outgoing{ChatID: chatID, Text: "📝 Заметка к " + number + " сохранена."})
	case dialogReply, dialogRejectLetter:
		dialog.Draft = text
		b.preview(ctx, member, chatID, card, dialog)
	case dialogRejectReason:
		reason, _ := cut(text, 200)
		b.offerLetter(ctx, member, chatID, card, reason)
	default:
		_ = b.opts.Access.SetDialog(ctx, member.TelegramID, nil)
		return false
	}
	return true
}
