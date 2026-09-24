package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// The «Заявки» screen is a messenger (design «компактный чат», 2026-09-24): the conversations on
// the left, the chosen one on the right, the answer at the bottom. The page takes exactly the
// window; only the list and the conversation scroll. Whatever channel a client writes through —
// the form, the account, a letter, the bot — the message is here, and the list counts what the
// staff have not read; an answer written here goes everywhere the client can be reached.

// listState is how the list is narrowed and sorted. It travels with every link of the screen and
// every form of the conversation, so that an answer or a status does not reset the list.
type listState struct {
	Tab, Search, Sort string
}

// sortRecent: the latest conversation first; the default is by priority.
const sortRecent = "recent"

func listStateOf(values url.Values) listState {
	state := listState{Tab: values.Get("tab"), Search: strings.TrimSpace(values.Get("q")), Sort: values.Get("sort")}
	if !slices.Contains(leads.Tabs, state.Tab) {
		state.Tab = leads.TabAll
	}
	if state.Sort != sortRecent {
		state.Sort = ""
	}
	for len(state.Search) > 200 {
		_, size := utf8.DecodeLastRuneInString(state.Search)
		state.Search = state.Search[:len(state.Search)-size]
	}
	return state
}

// listStateFrom reads the state of a page: from the address, or from the form that was sent.
func listStateFrom(r *http.Request) listState {
	if r.Method == http.MethodPost {
		values, _ := url.ParseQuery(r.PostFormValue("list"))
		return listStateOf(values)
	}
	return listStateOf(r.URL.Query())
}

// Query is the state as a query string; "" — the defaults.
func (s listState) Query() string {
	values := url.Values{}
	if s.Tab != "" {
		values.Set("tab", s.Tab)
	}
	if s.Search != "" {
		values.Set("q", s.Search)
	}
	if s.Sort != "" {
		values.Set("sort", s.Sort)
	}
	return values.Encode()
}

var tabNames = map[string]string{
	leads.TabAll: "Все", leads.TabUnanswered: "Ждут ответа", leads.TabWork: "В работе",
	leads.TabWaiting: "Ждём клиента", leads.TabClosed: "Закрытые", leads.TabSpam: "Спам",
}

// Short names of the statuses, for the narrow column of the list.
var statusShort = map[string]string{
	leads.StatusNew: "новая", leads.StatusInProgress: "в работе", leads.StatusWaitingClient: "ждём",
	leads.StatusDone: "готово", leads.StatusRejected: "отказ", leads.StatusSpam: "спам",
}

var priorityNames = [...]string{"новый", "низкий", "средний", "высокий"}

type convTab struct {
	ID, Name string
	Count    int
	Current  bool
	Query    string
}

// convRow is a line of the list.
type convRow struct {
	leads.Conversation
	Priority int
	Topic    string // the subject of an inquiry, or the direction of a request
	Budget   string // «≤ $500», «$1–3k», «—»
	Last     string // «20:14», «вчера», «21 сен»
	Tip      string
	Current  bool
}

type listData struct {
	State   listState
	Query   string // State.Query()
	Tabs    []convTab
	Rows    []convRow
	All     int // every conversation but spam
	Waiting int // open, the client wrote last
	Cut     bool
	SortQ   string // the state with the other sort
	Sorted  string // «Приоритет» | «Новые»
	Current int64
	// Pulse: the newest message of a client when the page was drawn (see leadsPulse).
	Pulse leads.Pulse
}

func (h *Handler) conversationList(ctx context.Context, state listState, current int64) (listData, error) {
	data := listData{State: state, Query: state.Query(), Current: current, Sorted: "Приоритет"}
	counts, err := h.opts.Leads.ConversationCounts(ctx)
	if err != nil {
		return data, err
	}
	data.All, data.Waiting = counts[leads.TabAll], counts[leads.TabUnanswered]
	for _, tab := range leads.Tabs {
		if tab == leads.TabSpam && counts[tab] == 0 && state.Tab != tab {
			continue // no spam, no tab
		}
		query := state
		query.Tab = tab
		data.Tabs = append(data.Tabs, convTab{ID: tab, Name: tabNames[tab], Count: counts[tab], Current: tab == state.Tab, Query: query.Query()})
	}
	other := state
	if state.Sort == sortRecent {
		other.Sort, data.Sorted = "", "Новые"
	} else {
		other.Sort = sortRecent
	}
	data.SortQ = other.Query()

	items, err := h.opts.Leads.Conversations(ctx, leads.ConversationFilter{Tab: state.Tab, Query: state.Search})
	if err != nil {
		return data, err
	}
	data.Cut = len(items) >= leads.ConversationsLimit
	for _, item := range items {
		data.Rows = append(data.Rows, h.convRow(item, current))
	}
	if state.Sort != sortRecent {
		// By priority, then by what is unread, then the latest (the order of the query).
		sort.SliceStable(data.Rows, func(i, j int) bool {
			a, b := data.Rows[i], data.Rows[j]
			if a.Priority != b.Priority {
				return a.Priority > b.Priority
			}
			return a.Unread > b.Unread
		})
	}
	data.Pulse, err = h.opts.Leads.PulseOf(ctx, current)
	return data, err
}

func (h *Handler) convRow(item leads.Conversation, current int64) convRow {
	row := convRow{Conversation: item, Priority: h.priority(item), Budget: shortBudget(item.Budget), Current: item.ID == current,
		Topic: item.Subject, Last: h.shortWhen(item.LastAt)}
	if row.Topic == "" {
		row.Topic = h.directionName(item.Direction)
	}
	orders := "заказов не было"
	if item.Orders > 0 {
		orders = plural(item.Orders, "заказ", "заказа", "заказов")
		if item.Spent > 0 {
			orders += " · " + h.money(item.Spent)
		}
	}
	budget := item.Budget
	if budget == "" {
		budget = "не указан"
	}
	row.Tip = fmt.Sprintf("Приоритет: %s · %s · бюджет %s", priorityNames[row.Priority], orders, budget)
	return row
}

// priority of a conversation (leads.Priority): the client's orders and the budget of the request.
func (h *Handler) priority(item leads.Conversation) int {
	var options []string
	if h.opts.Form != nil {
		for _, option := range h.opts.Form().Budgets {
			options = append(options, option.In("en"))
		}
	}
	return leads.Priority(leads.Activity(item.Orders, item.Spent), leads.BudgetLevel(options, item.Budget))
}

// shortBudget: «up to $500» → «≤ $500», «over $10k» → «$10k+», «not sure yet» → «—».
func shortBudget(label string) string {
	switch {
	case strings.IndexFunc(label, unicode.IsDigit) < 0:
		return "—"
	case strings.HasPrefix(label, "up to "):
		return "≤ " + strings.TrimPrefix(label, "up to ")
	case strings.HasPrefix(label, "over "):
		return strings.TrimPrefix(label, "over ") + "+"
	}
	return label
}

// shortWhen: the time today, «вчера», the day this year, the date before.
func (h *Handler) shortWhen(t time.Time) string {
	local, now := t.In(h.opts.Location), time.Now().In(h.opts.Location)
	switch {
	case sameDay(local, now):
		return local.Format("15:04")
	case sameDay(local, now.AddDate(0, 0, -1)):
		return "вчера"
	case local.Year() == now.Year():
		return fmt.Sprintf("%d %s", local.Day(), shortMonths[local.Month()])
	}
	return local.Format("02.01.06")
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

var monthsOf = [...]string{"", "января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}

// dayTitle names a day of the conversation: «Сегодня», «Вчера», «23 сентября», «23 сентября 2025».
func (h *Handler) dayTitle(t time.Time) string {
	local, now := t.In(h.opts.Location), time.Now().In(h.opts.Location)
	switch {
	case sameDay(local, now):
		return "Сегодня"
	case sameDay(local, now.AddDate(0, 0, -1)):
		return "Вчера"
	case local.Year() == now.Year():
		return fmt.Sprintf("%d %s", local.Day(), monthsOf[local.Month()])
	}
	return fmt.Sprintf("%d %s %d", local.Day(), monthsOf[local.Month()], local.Year())
}

// --- the conversation ------------------------------------------------------------------------------

// feedItem is a line of the conversation: a day, a message, a note or an event.
type feedItem struct {
	Date  string // a new day begins: its title
	Entry leads.Entry
	// Head is the caption over the first message of a run: «Иван · почта», «Вы · почта, Telegram»,
	// «Заметка · клиент не видит»; "" — the same author and channel as above (2px apart).
	Head string
	Mine bool // ours: on the right
	// Unread: the first message the staff had not seen before they opened the conversation.
	Unread bool
	// State of an answer (Deliveries): queued | sent | failed | partly; Marks — each target.
	State string
	Marks []deliveryMark
	Time  string
}

type deliveryMark struct {
	Channel, To, Status string
}

// channelChoice is a channel the answer may go through (the picker of the composer).
type channelChoice struct {
	Channel, Name string
	Targets       []leads.Target
}

// statusAction is a button that moves the request on.
type statusAction struct {
	Status, Label string
}

var channelNames = map[string]string{
	leads.ChannelEmail: "почта", leads.ChannelTelegram: "Telegram", leads.ChannelSite: "кабинет",
	"form": "форма", leads.MethodPhone: "звонок",
}

// feed lays the conversation out: days, runs of messages, events; what came after the staff last
// looked is marked.
func (h *Handler) feed(ctx context.Context, card *leads.Card, me string, unread int) ([]feedItem, error) {
	deliveries, err := h.opts.Leads.Deliveries(ctx, card.Lead.ID)
	if err != nil {
		return nil, err
	}
	first := strings.Fields(card.Lead.Name)
	client := "Клиент"
	if len(first) > 0 {
		client = first[0]
	}
	// The first unread message is the unread-th client message from the end.
	firstUnread := -1
	for i, seen := len(card.Feed)-1, 0; i >= 0 && seen < unread; i-- {
		if entry := card.Feed[i]; entry.Kind == "message" && entry.Direction == "in" {
			seen++
			firstUnread = i
		}
	}

	var out []feedItem
	var day, run string
	for i, entry := range card.Feed {
		item := feedItem{Entry: entry, Time: entry.At.In(h.opts.Location).Format("15:04"), Unread: i == firstUnread}
		if title := h.dayTitle(entry.At); title != day {
			day, item.Date, run = title, title, ""
		}
		switch {
		case entry.Kind == "event" && (entry.Action == "replied" || entry.Action == "client_replied") && entry.From == entry.To && entry.Body == "":
			continue // the message itself says it; a change of status or a remark («с адреса …») is news
		case entry.Kind == "event":
			run = ""
		case entry.Kind == "note":
			item.Mine = true
			if key := "note:" + entry.Author; key != run {
				item.Head, run = "Заметка · клиент не видит", key
				if entry.Author != me {
					item.Head = entry.Author + " · заметка · клиент не видит"
				}
			}
		case entry.Direction == "in":
			if key := "in:" + entry.Channel; key != run || item.Unread {
				item.Head, run = client+" · "+nameOr(channelNames, entry.Channel, entry.Channel), key
			}
		default:
			item.Mine = true
			var channels []string
			for _, d := range deliveries[entry.MessageID] {
				item.Marks = append(item.Marks, deliveryMark{Channel: d.Channel, To: d.To, Status: d.Status})
				if name := channelNames[d.Channel]; !slices.Contains(channels, name) {
					channels = append(channels, name)
				}
			}
			switch {
			case entry.Channel == leads.MethodPhone:
				channels = []string{"итог звонка"}
			case entry.Channel == leads.ChannelSite || card.Reach.Account && len(channels) > 0:
				channels = append(channels, "кабинет")
			case len(channels) == 0:
				channels = []string{nameOr(channelNames, entry.Channel, entry.Channel)}
			}
			item.State = answerState(entry.Delivery, item.Marks)
			who := "Вы"
			if entry.Author != me {
				who = entry.Author
			}
			if key := "out:" + entry.Author + ":" + strings.Join(channels, ","); key != run {
				item.Head, run = who+" · "+strings.Join(channels, ", "), key
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// answerState sums an answer up: every target reached — sent; some — partly; none yet — queued.
func answerState(summary string, marks []deliveryMark) string {
	if len(marks) == 0 {
		return summary // an answer from before deliveries, or a record of a call ("")
	}
	var sent, failed, queued int
	for _, mark := range marks {
		switch mark.Status {
		case "sent":
			sent++
		case "failed":
			failed++
		default:
			queued++
		}
	}
	switch {
	case queued > 0:
		return "queued"
	case failed > 0 && sent > 0:
		return "partly"
	case failed > 0:
		return "failed"
	}
	return "sent"
}

func nameOr(names map[string]string, key, fallback string) string {
	if name, ok := names[key]; ok {
		return name
	}
	return fallback
}

// channelChoices groups the reach by channel, for the picker of the composer.
func channelChoices(reach leads.Reach) []channelChoice {
	var out []channelChoice
	for _, channel := range reach.Channels() {
		choice := channelChoice{Channel: channel, Name: channelNames[channel]}
		for _, target := range reach.Targets {
			if target.Channel == channel {
				choice.Targets = append(choice.Targets, target)
			}
		}
		out = append(out, choice)
	}
	return out
}

// statusActions: the first button of the header, and the rest of the moves (in «Детали»).
func statusActions(status string) (primary statusAction, others []statusAction) {
	switch status {
	case leads.StatusNew:
		primary = statusAction{leads.StatusInProgress, "Взять в работу"}
	case leads.StatusInProgress, leads.StatusWaitingClient:
		primary = statusAction{leads.StatusDone, "Завершить"}
	case leads.StatusDone, leads.StatusRejected:
		primary = statusAction{leads.StatusInProgress, "Вернуть в работу"}
	case leads.StatusSpam:
		primary = statusAction{leads.StatusNew, "Не спам"}
	}
	labels := map[string]string{
		leads.StatusInProgress: "В работу", leads.StatusWaitingClient: "Ждём клиента", leads.StatusNew: "Вернуть в новые",
		leads.StatusSpam: "Это спам", leads.StatusDone: "Завершить",
	}
	for _, next := range leads.NextStatuses(status) {
		if next != primary.Status && next != leads.StatusRejected {
			others = append(others, statusAction{next, labels[next]})
		}
	}
	return primary, others
}

// leadsPulse answers the open messenger's «anything new?» (static/admin.js): the newest message
// of a client, anywhere and in the open conversation. It does not keep the session alive.
func (h *Handler) leadsPulse(w http.ResponseWriter, r *http.Request) {
	lead, _ := strconv.ParseInt(r.URL.Query().Get("lead"), 10, 64)
	pulse, err := h.opts.Leads.PulseOf(r.Context(), max(lead, 0))
	if err != nil {
		h.opts.Log.Error("cannot read the pulse of the requests", "error", err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(pulse)
}

// What became of an answer, in words and in a mark next to its time.
var answerStates = map[string]string{
	"queued": "ждёт отправки", "sent": "доставлено", "failed": "не доставлено", "partly": "доставлено не везде",
}

var deliveryMarks = map[string]string{"queued": "◷", "sent": "✓", "failed": "!", "partly": "✓!"}
