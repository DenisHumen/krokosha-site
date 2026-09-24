package telegram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// Commands of the staff (brief B10.4): lists of requests, a card by its number, search, the
// week's numbers, silence for a while.

// Commands is the menu next to the input field. Telegram shows one menu to everybody, so it
// lists what every member may use; the owner's commands are in /help. (A client sees the menu
// too — and whatever they pick tells them the state of their own request.)
func Commands() []Command {
	return []Command{
		{Command: "leads", Description: "Открытые заявки"},
		{Command: "stats", Description: "Неделя в цифрах"},
		{Command: "mute", Description: "Тишина: /mute 2h, /mute off"},
		{Command: "help", Description: "Что умеет бот"},
	}
}

const listLimit = 10

// lists are the filters of /leads, in the order of their buttons.
var lists = []struct{ id, name string }{{"open", "Открытые"}, {"new", "Новые"}, {"mine", "Мои"}, {"waiting", "Ждут клиента"}}

func (b *Bot) listFilter(list string, member *Member) (leads.Filter, string) {
	switch list {
	case "new":
		return leads.Filter{Status: leads.StatusNew, Limit: listLimit}, "Новые заявки"
	case "mine":
		return leads.Filter{Status: "open", Assignee: member.Actor(), Limit: listLimit}, "Мои заявки"
	case "waiting":
		return leads.Filter{Status: leads.StatusWaitingClient, Limit: listLimit}, "Ждут ответа клиента"
	default:
		return leads.Filter{Status: "open", Limit: listLimit}, "Открытые заявки"
	}
}

// listView draws a list of requests: a line and a button per request, the filters below.
func (b *Bot) listView(ctx context.Context, title string, filter leads.Filter, withFilters string) (string, Keyboard, error) {
	found, total, err := b.opts.Leads.List(ctx, filter)
	if err != nil {
		return "", nil, err
	}
	lines := []string{"<b>" + Escape(title) + "</b> — " + strconv.Itoa(total), ""}
	var buttons Keyboard
	var row []Button
	for _, item := range found {
		// No names here: a list belongs to no single request, so it cannot be wiped when a client's
		// data is erased. Who it is, the card says — and the card is wiped.
		line := statusIcons[item.Status] + " <b>#" + item.Number() + "</b> · " + Escape(b.directionName(item.Direction)) +
			" · " + item.CreatedAt.In(b.opts.Location).Format("02.01")
		if item.Assignee != "" {
			line += " · " + Escape(item.Assignee)
		}
		if item.LastFromUser {
			line += " · <i>ждёт ответа</i>"
		}
		lines = append(lines, line)
		row = append(row, Button{Text: "#" + item.Number(), Data: leadButtonData("card", item.ID, "")})
		if len(row) == 3 {
			buttons, row = append(buttons, row), nil
		}
	}
	if len(row) > 0 {
		buttons = append(buttons, row)
	}
	switch {
	case total == 0:
		lines = append(lines, "Пусто.")
	case total > len(found):
		lines = append(lines, "", fmt.Sprintf("Показаны последние %d. Остальные — в админке или через /search.", len(found)))
	}
	if withFilters != "" {
		var filters []Button
		for _, list := range lists {
			name := list.name
			if list.id == withFilters {
				name = "· " + name + " ·"
			}
			filters = append(filters, Button{Text: name, Data: "ls:" + list.id})
		}
		buttons = append(buttons, filters[:2], filters[2:])
	}
	return strings.Join(lines, "\n"), buttons, nil
}

func (b *Bot) directionName(id string) string {
	if option, ok := b.opts.Form().Direction(id); ok {
		if label := option.Label.In("ru"); label != "" {
			return label
		}
	}
	return id
}

func (b *Bot) leadsCommand(ctx context.Context, chatID int64, member *Member, list string) {
	filter, title := b.listFilter(list, member)
	if list == "" {
		list = "open"
	}
	text, buttons, err := b.listView(ctx, title, filter, list)
	if err != nil {
		b.opts.Log.Error("telegram: cannot list the requests", "error", err)
		b.say(ctx, Outgoing{ChatID: chatID, Text: "Не получилось прочитать заявки. Попробуйте ещё раз."})
		return
	}
	b.say(ctx, Outgoing{ChatID: chatID, Text: text, Buttons: buttons})
}

// listButton switches the filter of a list that is already on the screen.
func (b *Bot) listButton(ctx context.Context, query *CallbackQuery, member *Member, list string, answer func(string, bool)) {
	answer("", false)
	if query.Message == nil {
		return
	}
	filter, title := b.listFilter(list, member)
	text, buttons, err := b.listView(ctx, title, filter, list)
	if err != nil {
		b.opts.Log.Error("telegram: cannot list the requests", "error", err)
		return
	}
	if err := b.opts.API.Edit(ctx, query.Message.MessageID, Outgoing{ChatID: query.Message.Chat.ID, Text: text, Buttons: buttons}); err != nil && ctx.Err() == nil {
		b.opts.Log.Warn("telegram: cannot redraw a list", "error", err)
	}
}

var reLeadNumber = regexp.MustCompile(`(?i)^#?(?:k-?)?0*([1-9][0-9]{0,17})$`)

// leadCommand sends the card of a request by its number: «/lead K-0042», «/lead 42».
func (b *Bot) leadCommand(ctx context.Context, chatID int64, member *Member, argument string) {
	match := reLeadNumber.FindStringSubmatch(strings.TrimSpace(argument))
	if match == nil {
		b.say(ctx, Outgoing{ChatID: chatID, Text: "Нужен номер заявки: <code>/lead K-0042</code>"})
		return
	}
	id, _ := strconv.ParseInt(match[1], 10, 64)
	if !b.sendCard(ctx, chatID, member, id) {
		b.say(ctx, Outgoing{ChatID: chatID, Text: "Заявки #" + leads.Number(id) + " нет: такого номера не было, либо данные клиента удалены."})
	}
}

// sendCard sends a fresh card of a request into a chat. It is remembered like the first one:
// both follow the request from then on.
func (b *Bot) sendCard(ctx context.Context, chatID int64, member *Member, leadID int64) bool {
	card, err := b.opts.Leads.Card(ctx, leadID)
	if err != nil {
		if !errors.Is(err, leads.ErrNotFound) {
			b.opts.Log.Error("telegram: cannot read a request", "error", err)
		}
		return false
	}
	text, buttons := b.renderCard(card)
	if !member.CanAct() {
		buttons = b.readingButtons(card)
	}
	sent, err := b.opts.API.Send(ctx, Outgoing{ChatID: chatID, Text: text, Buttons: buttons})
	if err != nil {
		if ctx.Err() == nil {
			b.opts.Log.Warn("telegram: cannot send a card", "error", err)
		}
		return true
	}
	b.remember(ctx, leadID, chatID, sent.MessageID, kindCard)
	return true
}

func (b *Bot) searchCommand(ctx context.Context, chatID int64, query string) {
	if len([]rune(query)) < 2 {
		b.say(ctx, Outgoing{ChatID: chatID, Text: "Что искать? Например: <code>/search mikrotik</code> — по имени, контакту, тексту или номеру."})
		return
	}
	shown, _ := cut(query, 60)
	text, buttons, err := b.listView(ctx, "Поиск: "+shown, leads.Filter{Query: query, Limit: listLimit}, "")
	if err != nil {
		b.opts.Log.Error("telegram: cannot search the requests", "error", err)
		b.say(ctx, Outgoing{ChatID: chatID, Text: "Не получилось выполнить поиск. Попробуйте ещё раз."})
		return
	}
	b.say(ctx, Outgoing{ChatID: chatID, Text: text, Buttons: buttons})
}

// statsCommand: the week in numbers — requests, how they ended, how fast the first answer came.
func (b *Bot) statsCommand(ctx context.Context, chatID int64) {
	now := b.opts.Now()
	funnel, err := b.opts.Leads.Funnel(ctx, now.AddDate(0, 0, -7), now.Add(time.Minute))
	if err != nil {
		b.opts.Log.Error("telegram: cannot count the week", "error", err)
		b.say(ctx, Outgoing{ChatID: chatID, Text: "Не получилось посчитать. Попробуйте ещё раз."})
		return
	}
	real := funnel.Total - funnel.Spam
	lines := []string{
		"<b>За 7 дней</b>", "",
		fmt.Sprintf("Заявок: %d (и ещё спама: %d)", real, funnel.Spam),
		fmt.Sprintf("🟡 новых: %d · 🟢 в работе: %d · 🔵 ждём клиента: %d", funnel.New, funnel.InProgress, funnel.Waiting),
		fmt.Sprintf("✅ завершено: %d · ❌ отклонено: %d", funnel.Done, funnel.Rejected),
	}
	if real > 0 {
		lines = append(lines, fmt.Sprintf("Дошло до работы: %d%%", (funnel.InProgress+funnel.Waiting+funnel.Done)*100/real))
	}
	if funnel.FirstResponse > 0 {
		lines = append(lines, "Первая реакция (медиана): "+humanDuration(funnel.FirstResponse))
	}
	for i, source := range funnel.Sources {
		if i == 0 {
			lines = append(lines, "", "Откуда:")
		}
		name := source.Source
		if name == "" {
			name = "визит неизвестен"
		}
		if source.Campaign != "" {
			name += " · " + source.Campaign
		}
		lines = append(lines, fmt.Sprintf("• %s — %d", Escape(name), source.Requests))
	}
	b.say(ctx, Outgoing{ChatID: chatID, Text: strings.Join(lines, "\n")})
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "меньше минуты"
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d ч %02d мин", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d дн", int(d.Hours())/24)
	}
}

var reMute = regexp.MustCompile(`^([0-9]{1,3})\s*([mhdмчд])`)

// muteCommand: «/mute 2h» — cards and pushes keep coming, without sound; «/mute off» ends it.
func (b *Bot) muteCommand(ctx context.Context, chatID int64, member *Member, argument string) {
	argument = strings.ToLower(strings.TrimSpace(argument))
	if argument == "off" || argument == "0" || argument == "выкл" {
		if err := b.opts.Access.Mute(ctx, member.TelegramID, time.Time{}); err != nil {
			b.opts.Log.Error("telegram: cannot unmute", "error", err)
			return
		}
		b.say(ctx, Outgoing{ChatID: chatID, Text: "Звук включён."})
		return
	}
	match := reMute.FindStringSubmatch(argument)
	if match == nil {
		state := "Сейчас звук включён."
		if member.MutedUntil.Valid && member.MutedUntil.Time.After(b.opts.Now()) {
			state = "Сейчас тишина до " + member.MutedUntil.Time.In(b.opts.Location).Format("02.01 15:04") + "."
		}
		b.say(ctx, Outgoing{ChatID: chatID, Text: state + "\n<code>/mute 30m</code>, <code>/mute 2h</code>, <code>/mute 1d</code> — заявки приходят без звука; <code>/mute off</code> — вернуть звук."})
		return
	}
	amount, _ := strconv.Atoi(match[1])
	unit := map[string]time.Duration{"m": time.Minute, "м": time.Minute, "h": time.Hour, "ч": time.Hour, "d": 24 * time.Hour, "д": 24 * time.Hour}[match[2]]
	length := time.Duration(amount) * unit
	if length <= 0 || length > 7*24*time.Hour {
		b.say(ctx, Outgoing{ChatID: chatID, Text: "От минуты до семи дней."})
		return
	}
	until := b.opts.Now().Add(length)
	if err := b.opts.Access.Mute(ctx, member.TelegramID, until); err != nil {
		b.opts.Log.Error("telegram: cannot mute", "error", err)
		return
	}
	b.say(ctx, Outgoing{ChatID: chatID, Text: "Тишина до " + until.In(b.opts.Location).Format("02.01 15:04") + ": заявки приходят, но без звука. <code>/mute off</code> — вернуть звук."})
}
