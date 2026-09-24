package admin

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/clients"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram"
)

// Requests in the admin area (brief B10.6): the list, the board, the card with the conversation,
// ready-made answers. Everything here ends in a method of leads.Store — the same ones the
// Telegram bot will call, so both agree on who took what.

var statusNames = map[string]string{
	leads.StatusNew: "Новая", leads.StatusInProgress: "В работе", leads.StatusWaitingClient: "Ждём клиента",
	leads.StatusDone: "Завершена", leads.StatusRejected: "Отклонена", leads.StatusSpam: "Спам",
}

// groupNames name the tabs and the columns of the board: a group of requests, not one of them.
var groupNames = map[string]string{
	leads.StatusNew: "Новые", leads.StatusInProgress: "В работе", leads.StatusWaitingClient: "Ждём клиента",
	leads.StatusDone: "Завершено", leads.StatusRejected: "Отклонено", leads.StatusSpam: "Спам",
}

var methodNames = map[string]string{leads.MethodEmail: "почта", leads.MethodTelegram: "Telegram", leads.MethodPhone: "телефон"}

// directionName gives the Russian label of a direction from content/site.yaml.
func (h *Handler) directionName(id string) string {
	if option, ok := h.opts.Form().Direction(id); ok {
		if label := option.Label.In("ru"); label != "" {
			return label
		}
	}
	return id
}

// --- the messenger and the board ------------------------------------------------------------------

// messengerData is the «Заявки» screen (messenger.go): the list, and the conversation chosen.
type messengerData struct {
	List listData
	Chat *leadData // nil — none chosen: the month in numbers instead
	// Funnel of the last 30 days, while no conversation is open.
	Funnel *leads.Funnel
	Period string
}

type boardColumn struct {
	Status, Name string
	Cards        []leads.Summary
}

// boardData is the board: the requests somebody still has to do something about, by status.
type boardData struct {
	Board  []boardColumn
	Search string
	Funnel *leads.Funnel
	Period string
}

func (h *Handler) leadsList(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("view") == "board" {
		h.leadsBoard(w, r)
		return
	}
	ctx := r.Context()
	list, err := h.conversationList(ctx, listStateFrom(r), 0)
	if err != nil {
		h.fail(w, r, "cannot list the requests", err)
		return
	}
	data := messengerData{List: list, Period: "за 30 дней"}
	now := time.Now()
	if data.Funnel, err = h.opts.Leads.Funnel(ctx, now.AddDate(0, 0, -30), now.Add(time.Minute)); err != nil {
		h.fail(w, r, "cannot compute the funnel", err)
		return
	}
	h.render(w, r, http.StatusOK, "leads", view{Title: "Заявки", Nav: "leads", Full: true, Data: data})
}

func (h *Handler) leadsBoard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := boardData{Search: strings.TrimSpace(r.URL.Query().Get("q")), Period: "за 30 дней"}
	for _, status := range []string{leads.StatusNew, leads.StatusInProgress, leads.StatusWaitingClient, leads.StatusDone, leads.StatusRejected} {
		cards, _, err := h.opts.Leads.List(ctx, leads.Filter{Status: status, Query: data.Search, Limit: 30})
		if err != nil {
			h.fail(w, r, "cannot list the requests", err)
			return
		}
		data.Board = append(data.Board, boardColumn{Status: status, Name: groupNames[status], Cards: cards})
	}
	now := time.Now()
	var err error
	if data.Funnel, err = h.opts.Leads.Funnel(ctx, now.AddDate(0, 0, -30), now.Add(time.Minute)); err != nil {
		h.fail(w, r, "cannot compute the funnel", err)
		return
	}
	h.render(w, r, http.StatusOK, "board", view{Title: "Доска заявок", Nav: "leads", Data: data})
}

// --- the card ------------------------------------------------------------------------------------

type leadData struct {
	Card      *leads.Card
	Direction string
	Contact   string // a link: mailto:, https://t.me/…, tel:
	// ClientBotLink is the «continue in Telegram» link of this request, for the owner to send to a
	// client who left a contact and never opened the bot: answers wait for that (telegram.ClientPrefix).
	ClientBotLink string
	Next          []string
	// Suggestions are the answers in the client's language, the best for this conversation first;
	// Top are the first three of them (leads.Rank), Groups the rest by what they answer.
	Suggestions []leads.Suggestion
	Top         []leads.Suggestion
	Groups      []suggestionGroup
	Rejects     []leads.Template
	Draft       string // the answer being written: a chosen template, or what was typed before an error
	Refusal     string // the same for the letter that goes with a refusal
	// Chosen are the templates the draft was made from; Attached the files of theirs that go along.
	Chosen   []int64
	Attached []leads.Media
	// Filled are the templates filled in for this client, for the script that puts them into the
	// answer without a reload.
	Filled   map[int64]filledTemplate
	VisitID  string
	CanReply bool
	// ByPhone: nothing delivers an answer (a phone, no account) — the text is the record of a call.
	ByPhone bool
	// Client is the personal account of the request, as the list of clients shows it; nil — none.
	Client *clients.Row
	// Template is the ready-made answer chosen with ?template=…
	Template int64
	// Files: the answer may carry files (not a call); ByMail: some go as attachments of a letter.
	Files  bool
	ByMail bool

	// The messenger (messenger.go): the list the conversation is open in, its line there (orders,
	// what was unread), the conversation laid out, the moves of the header, where the answer goes.
	List     listData
	Conv     leads.Conversation
	Priority int
	Items    []feedItem
	Primary  statusAction
	Others   []statusAction
	Channels []channelChoice
	Via      string // «почта · Telegram · кабинет»: where an answer goes unless a channel is left out
	Facts    string // «$1–3k · 1–2 weeks · −10%»
	Owed     int    // the client's messages since the last answer, while the request is open
}

// filledTemplate is a template as the page's script gets it (JSON inside the page).
type filledTemplate struct {
	Body  string      `json:"body"`
	Media []mediaInfo `json:"media,omitempty"`
}

type mediaInfo struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size string `json:"size"`
}

type suggestionGroup struct {
	Category, Name string
	Items          []leads.Suggestion
}

// draftOf is what a failed answer leaves in the form: its text, templates and files of templates.
type draftOf struct {
	Text      string
	Templates []int64
	Media     []int64
}

func leadID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (h *Handler) leadCard(w http.ResponseWriter, r *http.Request) {
	h.showLead(w, r, http.StatusOK, "", draftOf{})
}

func (h *Handler) showLead(w http.ResponseWriter, r *http.Request, status int, problem string, draft draftOf) {
	id, ok := leadID(r)
	if !ok {
		h.notFound(w, r, "Заявка не найдена")
		return
	}
	ctx := r.Context()
	seen := time.Now() // what the client wrote up to now is on the screen
	card, err := h.opts.Leads.Card(ctx, id)
	if errors.Is(err, leads.ErrNotFound) {
		h.notFound(w, r, "Заявка не найдена: возможно, она удалена по запросу клиента.")
		return
	}
	if err != nil {
		h.fail(w, r, "cannot read the request", err)
		return
	}
	lead := card.Lead
	// The line of the list as it was before this look: what the client wrote since the last one is
	// marked in the conversation — and is read from now on.
	conv, err := h.opts.Leads.Conversation(ctx, id)
	if err != nil {
		h.fail(w, r, "cannot read the request", err)
		return
	}
	if r.Method == http.MethodGet && conv.Unread > 0 {
		if err := h.opts.Leads.MarkStaffSeen(ctx, id, seen); err != nil {
			h.opts.Log.Warn("cannot mark a conversation as read", "lead", lead.Number(), "error", err)
		}
	}
	list, err := h.conversationList(ctx, listStateFrom(r), id)
	if err != nil {
		h.fail(w, r, "cannot list the requests", err)
		return
	}
	data := leadData{Card: card, Direction: h.directionName(lead.Direction), Next: leads.NextStatuses(lead.Status), Draft: draft.Text,
		CanReply: lead.Status != leads.StatusSpam && !card.AnonymizedAt.Valid, ByPhone: card.ReplyVia == leads.MethodPhone,
		Chosen: draft.Templates, List: list, Conv: conv, Priority: h.priority(conv), Channels: channelChoices(card.Reach)}
	data.Files = !data.ByPhone && h.opts.Leads.Files() != nil
	data.ByMail = card.Reach.Has(leads.ChannelEmail)
	data.Primary, data.Others = statusActions(lead.Status)
	if data.Items, err = h.feed(ctx, card, sessionOf(r).User.Login, conv.Unread); err != nil {
		h.fail(w, r, "cannot read the deliveries of the answers", err)
		return
	}
	var via []string
	for _, choice := range data.Channels {
		via = append(via, choice.Name)
	}
	if card.Reach.Account {
		via = append(via, "кабинет")
	}
	data.Via = strings.Join(via, " · ")
	var facts []string
	for _, fact := range []string{lead.Budget, lead.Timeline} {
		if fact != "" {
			facts = append(facts, fact)
		}
	}
	if lead.Discount.Percent > 0 {
		facts = append(facts, fmt.Sprintf("−%d%%", lead.Discount.Percent))
	}
	data.Facts = strings.Join(facts, " · ")
	if lead.Status == leads.StatusNew || lead.Status == leads.StatusInProgress || lead.Status == leads.StatusWaitingClient {
		for i := len(card.Feed) - 1; i >= 0; i-- {
			if entry := card.Feed[i]; entry.Kind == "message" {
				if entry.Direction != "in" {
					break
				}
				data.Owed++
			}
		}
	}
	switch lead.ContactMethod {
	case leads.MethodEmail:
		data.Contact = "mailto:" + lead.ContactValue
	case leads.MethodTelegram:
		data.Contact = "https://t.me/" + strings.TrimPrefix(lead.ContactValue, "@")
	case leads.MethodPhone:
		data.Contact = "tel:" + lead.ContactValue
	}
	if lead.Session.Known {
		data.VisitID = lead.Session.SessionHex()
	}
	if lead.ClientID > 0 && h.opts.Clients != nil {
		if data.Client, err = h.opts.Clients.Summary(ctx, lead.ClientID); err != nil && !errors.Is(err, clients.ErrNotFound) {
			h.fail(w, r, "cannot read the client of the request", err)
			return
		}
	}
	data.Template, _ = strconv.ParseInt(r.URL.Query().Get("template"), 10, 64)
	if h.opts.BotStatus != nil && data.CanReply && lead.PublicToken != "" {
		if state, _ := h.opts.BotStatus(); state.Username != "" {
			data.ClientBotLink = "https://t.me/" + state.Username + "?start=" + telegram.ClientPrefix + lead.PublicToken
		}
	}

	templates, err := h.opts.Leads.Templates(ctx, "")
	if err != nil {
		h.fail(w, r, "cannot read the templates", err)
		return
	}
	sent, err := h.opts.Leads.UsedTemplates(ctx, lead.ID)
	if err != nil {
		h.fail(w, r, "cannot read the templates of the request", err)
		return
	}
	links := leads.LinksFor(h.siteURL(), lead)
	data.Filled = map[int64]filledTemplate{}
	media := map[int64]leads.Media{}
	for _, item := range templates {
		if item.Lang != lead.Lang {
			continue
		}
		filled := filledTemplate{Body: leads.FillTemplate(item.Body, lead, links)}
		for _, file := range item.Media {
			media[file.ID] = file
			filled.Media = append(filled.Media, mediaInfo{ID: file.ID, Name: file.Filename, Kind: file.Kind, Size: formatBytes(file.Size)})
		}
		data.Filled[item.ID] = filled
		// «?template=7» puts a ready-made text into its form — without any script.
		chosen := strconv.FormatInt(item.ID, 10) == r.URL.Query().Get("template")
		switch {
		case item.Kind == "reject":
			data.Rejects = append(data.Rejects, item)
			if chosen {
				data.Refusal = data.Filled[item.ID].Body
			}
		case chosen && draft.Text == "":
			data.Draft = data.Filled[item.ID].Body
			data.Chosen = []int64{item.ID}
			draft.Media = nil
			for _, file := range item.Media {
				draft.Media = append(draft.Media, file.ID)
			}
		}
	}
	for _, id := range draft.Media {
		if file, ok := media[id]; ok {
			data.Attached = append(data.Attached, file)
		}
	}
	data.Suggestions = leads.Rank(templates, leads.SituationOf(card, sent, time.Now()))
	groups := map[string]*suggestionGroup{}
	var order []string
	for _, item := range data.Suggestions {
		if item.Top {
			data.Top = append(data.Top, item)
			continue
		}
		category := item.Template.Category
		if groups[category] == nil {
			groups[category] = &suggestionGroup{Category: category, Name: categoryNames[category]}
			order = append(order, category)
		}
		groups[category].Items = append(groups[category].Items, item)
	}
	// The rest in the order of categories, the way the editor lists them.
	for _, category := range append(append([]string(nil), leads.Categories...), "") {
		if group := groups[category]; group != nil {
			data.Groups = append(data.Groups, *group)
			delete(groups, category)
		}
	}
	for _, category := range order {
		if group := groups[category]; group != nil {
			data.Groups = append(data.Groups, *group)
		}
	}
	title := "Заявка #"
	if lead.Kind == leads.KindInquiry {
		title = "Обращение #"
	}
	h.render(w, r, status, "leads", view{Title: title + lead.Number(), Nav: "leads", Full: true, Error: problem,
		Data: messengerData{List: list, Chat: &data}})
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request, message string) {
	h.render(w, r, http.StatusNotFound, "error", view{Title: "Не найдено", Nav: "leads", Error: message})
}

func (h *Handler) backToLead(w http.ResponseWriter, r *http.Request, id int64, flash string) {
	http.Redirect(w, r, fmt.Sprintf("%s/leads/%d?%s", h.opts.Prefix, id, withFlash(listStateFrom(r), flash)), http.StatusSeeOther)
}

// withFlash is the query of a page of the messenger: the state of its list, and what just happened.
func withFlash(state listState, flash string) string {
	query := state.Query()
	if query != "" {
		query += "&"
	}
	return query + "ok=" + url.QueryEscape(flash)
}

func (h *Handler) auditLead(r *http.Request, action string, id int64, details string) {
	h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, action, leads.Number(id), details, h.attemptMeta(r).IPPrefix)
}

// wantsJSON: the board's script moves cards without reloading the page.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// leadStatus changes the status. «new → in progress» is a «take»: whoever is first gets the
// request, the second is told who that was (brief B10.4).
func (h *Handler) leadStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := leadID(r)
	if !ok {
		h.notFound(w, r, "Заявка не найдена")
		return
	}
	actor, status, reason := sessionOf(r).User.Login, r.PostFormValue("status"), r.PostFormValue("reason")
	card, err := h.opts.Leads.Card(r.Context(), id)
	if err == nil && card.Lead.Status == leads.StatusNew && status == leads.StatusInProgress {
		var takenBy string
		if takenBy, err = h.opts.Leads.Take(r.Context(), id, actor); errors.Is(err, leads.ErrAlreadyTaken) {
			h.answerStatus(w, r, id, http.StatusConflict, "Заявку уже взял(а) "+takenBy+".")
			return
		}
	} else if err == nil {
		// A refusal may go to the client first: a polite letter from a template, then the status.
		if letter := strings.TrimSpace(r.PostFormValue("letter")); status == leads.StatusRejected && letter != "" {
			if _, err = h.opts.Leads.Reply(r.Context(), id, actor, letter); err == nil {
				h.opts.Kick()
			}
		}
		if err == nil {
			err = h.opts.Leads.SetStatus(r.Context(), id, actor, status, reason)
		}
	}
	switch {
	case err == nil:
		h.auditLead(r, "lead.status", id, status)
		h.opts.Kick() // a request rescued from spam is announced at once
		h.answerStatus(w, r, id, http.StatusOK, "")
	case errors.Is(err, leads.ErrNotFound):
		h.answerStatus(w, r, id, http.StatusNotFound, "Заявка не найдена.")
	case errors.Is(err, leads.ErrBadTransition):
		h.answerStatus(w, r, id, http.StatusConflict, "Из текущего статуса так перейти нельзя — обновите страницу: возможно, заявку уже изменили.")
	default:
		h.opts.Log.Error("cannot change the status of a request", "error", err)
		h.answerStatus(w, r, id, http.StatusInternalServerError, "Не получилось изменить статус.")
	}
}

func (h *Handler) answerStatus(w http.ResponseWriter, r *http.Request, id int64, status int, problem string) {
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"ok":%t,"error":%q}`, problem == "", problem)
		return
	}
	if problem == "" {
		h.backToLead(w, r, id, "lead-status")
		return
	}
	h.showLead(w, r, status, problem, draftOf{})
}

func (h *Handler) leadNote(w http.ResponseWriter, r *http.Request) {
	id, ok := leadID(r)
	if !ok {
		h.notFound(w, r, "Заявка не найдена")
		return
	}
	switch err := h.opts.Leads.AddNote(r.Context(), id, sessionOf(r).User.Login, r.PostFormValue("text")); {
	case err == nil:
		h.backToLead(w, r, id, "lead-note")
	case errors.Is(err, leads.ErrEmptyText):
		h.showLead(w, r, http.StatusBadRequest, "Заметка пустая.", draftOf{})
	case errors.Is(err, leads.ErrNotFound):
		h.notFound(w, r, "Заявка не найдена")
	default:
		h.fail(w, r, "cannot store a note", err)
	}
}

func (h *Handler) leadReply(w http.ResponseWriter, r *http.Request) {
	id, ok := leadID(r)
	if !ok {
		h.notFound(w, r, "Заявка не найдена")
		return
	}
	if r.PostFormValue("mode") == "note" {
		h.leadNote(w, r) // the composer's «Заметка»: the text only, files are not kept with notes
		return
	}
	answer := leads.Answer{Text: r.PostFormValue("text"), Templates: formIDs(r.PostForm["template"]), Media: formIDs(r.PostForm["media"]),
		Channels: chosenChannels(r)}
	draft := draftOf{Text: answer.Text, Templates: answer.Templates, Media: answer.Media}
	// Files attached by hand: each is checked by what it is; one that does not pass stops the answer.
	if r.MultipartForm != nil {
		for _, header := range r.MultipartForm.File["files"] {
			if header.Filename == "" && header.Size == 0 {
				continue // a file field nobody touched
			}
			file, err := header.Open()
			var upload leads.Upload
			if err == nil {
				upload, err = leads.SaveOutgoing(h.opts.Leads.Files(), header.Filename, header.Size, file)
				_ = file.Close()
			}
			if err != nil {
				h.discardUploads(answer.Files)
				if !errors.Is(err, leads.ErrFileType) && !errors.Is(err, leads.ErrFileTooBig) {
					h.opts.Log.Error("cannot keep a file of an answer", "error", err)
				}
				h.showLead(w, r, http.StatusBadRequest, fileProblem(leads.CleanFilename(header.Filename), err)+" Ответ не отправлен.", draft)
				return
			}
			answer.Files = append(answer.Files, upload)
		}
	}
	_, err := h.opts.Leads.ReplyWith(r.Context(), id, sessionOf(r).User.Login, answer)
	switch {
	case err == nil:
		details := ""
		if files := len(answer.Media) + len(answer.Files); files > 0 {
			details = fmt.Sprintf("файлов: %d", files)
		}
		h.auditLead(r, "lead.reply", id, details)
		h.opts.Kick()
		h.backToLead(w, r, id, "lead-reply")
	case errors.Is(err, leads.ErrEmptyText):
		h.showLead(w, r, http.StatusBadRequest, "Ответ пустой.", draft)
	case errors.Is(err, leads.ErrTooManyFiles):
		h.showLead(w, r, http.StatusBadRequest, fmt.Sprintf("Не больше %d файлов в одном ответе — столько Telegram показывает альбомом.", leads.MaxOutgoingFiles), draft)
	case errors.Is(err, leads.ErrNoFilesByPhone):
		h.showLead(w, r, http.StatusBadRequest, "Клиент оставил телефон: файлы по звонку не уходят. Запишите итог разговора без них.", draft)
	case errors.Is(err, leads.ErrNoChannel):
		h.showLead(w, r, http.StatusBadRequest, "Ответ никуда не уйдёт: отметьте хотя бы один канал — у клиента нет личного кабинета.", draft)
	case errors.Is(err, leads.ErrNotFound):
		h.notFound(w, r, "Заявка не найдена")
	default:
		h.opts.Log.Error("cannot store an answer", "error", err)
		h.showLead(w, r, http.StatusInternalServerError, "Не получилось сохранить ответ — текст ниже, попробуйте ещё раз.", draft)
	}
}

// chosenChannels reads the picker of the composer: the channels left checked. A form without the
// picker (one way to the client, or a script of its own) sends the answer everywhere; a picker with
// everything unchecked keeps the answer in the personal account alone.
func chosenChannels(r *http.Request) []string {
	if r.PostFormValue("channels") != "1" {
		return nil
	}
	out := []string{leads.ChannelSite}
	for _, channel := range r.PostForm["channel"] {
		if (channel == leads.ChannelEmail || channel == leads.ChannelTelegram) && !slices.Contains(out, channel) {
			out = append(out, channel)
		}
	}
	return out
}

// formIDs reads the ids of a form: the ones that are numbers.
func formIDs(values []string) []int64 {
	var out []int64
	for _, value := range values {
		if id, err := strconv.ParseInt(value, 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// discardUploads removes files of an answer that was not stored.
func (h *Handler) discardUploads(files []leads.Upload) {
	for _, file := range files {
		if err := h.opts.Leads.Files().Remove(file.StoredAs); err != nil {
			h.opts.Log.Warn("cannot remove a file of an answer that was not stored; the daily sweep will", "error", err)
		}
	}
}

// leadDelete removes a client's data on their request (brief B10.6). The number typed by hand
// is the confirmation: this cannot be undone.
func (h *Handler) leadDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := leadID(r)
	if !ok {
		h.notFound(w, r, "Заявка не найдена")
		return
	}
	typed := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(r.PostFormValue("confirm"))), "#")
	if typed != leads.Number(id) {
		h.showLead(w, r, http.StatusBadRequest, "Для удаления введите номер заявки: "+leads.Number(id), draftOf{})
		return
	}
	err := h.opts.Leads.Delete(r.Context(), id)
	if errors.Is(err, leads.ErrFilesLeft) {
		// The request is gone; a file that could not be removed now goes with the daily sweep.
		h.opts.Log.Warn("a deleted request left files behind", "lead", leads.Number(id), "error", err)
		err = nil
	}
	switch {
	case err == nil:
		// The journal keeps the number and who deleted it — nothing about the person.
		h.auditLead(r, "lead.delete", id, "данные клиента удалены по запросу")
		http.Redirect(w, r, h.opts.Prefix+"/leads?"+withFlash(listStateFrom(r), "lead-deleted"), http.StatusSeeOther)
	case errors.Is(err, leads.ErrNotFound):
		h.notFound(w, r, "Заявка не найдена")
	default:
		h.fail(w, r, "cannot delete a request", err)
	}
}

// leadFile hands out a file that came with a request (brief B10.7): to a signed-in administrator
// only, and only as a download — whatever is inside, the browser is told to save it, not to
// show or run it.
func (h *Handler) leadFile(w http.ResponseWriter, r *http.Request) {
	id, ok := leadID(r)
	fileID, err := strconv.ParseInt(r.PathValue("file"), 10, 64)
	if !ok || err != nil || fileID <= 0 {
		h.notFound(w, r, "Файл не найден")
		return
	}
	file, content, err := h.opts.Leads.OpenAttachment(r.Context(), id, fileID)
	if errors.Is(err, leads.ErrNotFound) {
		h.notFound(w, r, "Файл не найден: возможно, заявка удалена или обезличена.")
		return
	}
	if err != nil {
		h.fail(w, r, "cannot open an attachment", err)
		return
	}
	defer content.Close()

	header := w.Header()
	header.Set("Content-Type", "application/octet-stream")
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": file.Filename})
	if disposition == "" {
		disposition = "attachment" // a name the header cannot carry: the browser makes one up
	}
	header.Set("Content-Disposition", disposition)
	header.Set("Content-Length", strconv.FormatInt(file.Size, 10))
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if _, err := io.Copy(w, content); err != nil {
		h.opts.Log.Warn("an attachment was not sent completely", "lead", leads.Number(id), "error", err)
	}
}

func (h *Handler) leadsExport(w http.ResponseWriter, r *http.Request) {
	h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, "admin.export", "leads", "", h.attemptMeta(r).IPPrefix)
	header := w.Header()
	header.Set("Content-Type", "text/csv; charset=utf-8")
	header.Set("Content-Disposition", `attachment; filename="krokosha-leads-`+time.Now().UTC().Format(time.DateOnly)+`.csv"`)
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))
	out := csv.NewWriter(&limitedWriter{writer: w, left: exportMaxBytes})
	err := h.opts.Leads.Export(r.Context(), func(row []string) error {
		for i, cell := range row {
			row[i] = spreadsheetSafe(cell)
		}
		return out.Write(row)
	})
	out.Flush()
	if err != nil {
		h.opts.Log.Error("export of requests was cut short", "error", err)
	}
}
