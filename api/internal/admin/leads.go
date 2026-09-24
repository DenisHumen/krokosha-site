package admin

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram"
)

// Requests in the admin area (brief B10.6): the list, the board, the card with the conversation,
// ready-made answers. Everything here ends in a method of leads.Store — the same ones the
// Telegram bot will call, so both agree on who took what.

const leadsPerPage = 50

var statusNames = map[string]string{
	leads.StatusNew: "Новая", leads.StatusInProgress: "В работе", leads.StatusWaitingClient: "Ждём клиента",
	leads.StatusDone: "Завершена", leads.StatusRejected: "Отклонена", leads.StatusSpam: "Спам",
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

// --- the list and the board ----------------------------------------------------------------------

type statusTab struct {
	ID, Name string
	Count    int
	Current  bool
	Query    string
}

type boardColumn struct {
	Status, Name string
	Cards        []leads.Summary
}

type leadsData struct {
	View    string // list | board
	Tabs    []statusTab
	Items   []leads.Summary
	Board   []boardColumn
	Total   int
	Search  string
	Status  string
	Page    int
	Prev    string // queries of the neighbouring pages, "" = none
	Next    string
	ListQ   string // the same filter in the other view
	BoardQ  string
	Funnel  *leads.Funnel
	Period  string
	Waiting int // the client wrote last: somebody should answer
}

func (h *Handler) leadsList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	data := leadsData{View: "list", Search: strings.TrimSpace(query.Get("q")), Status: query.Get("status"), Period: "за 30 дней"}
	if query.Get("view") == "board" {
		data.View = "board"
	}
	if _, known := statusNames[data.Status]; !known && data.Status != "all" {
		data.Status = ""
	}
	data.Page, _ = strconv.Atoi(query.Get("page"))
	data.Page = max(data.Page, 1)

	ctx := r.Context()
	counts, err := h.opts.Leads.Counts(ctx)
	if err != nil {
		h.fail(w, r, "cannot count the requests", err)
		return
	}
	link := func(status, view string, page int) string {
		values := url.Values{}
		if status != "" {
			values.Set("status", status)
		}
		if data.Search != "" {
			values.Set("q", data.Search)
		}
		if view == "board" {
			values.Set("view", "board")
		}
		if page > 1 {
			values.Set("page", strconv.Itoa(page))
		}
		return values.Encode()
	}
	open := 0
	for status, count := range counts {
		if status != leads.StatusSpam {
			open += count
		}
	}
	data.Tabs = append(data.Tabs, statusTab{ID: "", Name: "Все", Count: open, Current: data.Status == "", Query: link("", data.View, 1)})
	for _, status := range leads.Statuses {
		data.Tabs = append(data.Tabs, statusTab{ID: status, Name: statusNames[status], Count: counts[status], Current: data.Status == status, Query: link(status, data.View, 1)})
	}
	data.ListQ, data.BoardQ = link(data.Status, "list", 1), link(data.Status, "board", 1)

	if data.View == "board" {
		// The board shows the requests somebody still has to do something about, column by column.
		for _, status := range []string{leads.StatusNew, leads.StatusInProgress, leads.StatusWaitingClient, leads.StatusDone, leads.StatusRejected} {
			cards, _, err := h.opts.Leads.List(ctx, leads.Filter{Status: status, Query: data.Search, Limit: 30})
			if err != nil {
				h.fail(w, r, "cannot list the requests", err)
				return
			}
			data.Board = append(data.Board, boardColumn{Status: status, Name: statusNames[status], Cards: cards})
		}
	} else {
		items, total, err := h.opts.Leads.List(ctx, leads.Filter{Status: data.Status, Query: data.Search, Limit: leadsPerPage, Offset: (data.Page - 1) * leadsPerPage})
		if err != nil {
			h.fail(w, r, "cannot list the requests", err)
			return
		}
		data.Items, data.Total = items, total
		if data.Page > 1 {
			data.Prev = link(data.Status, "list", data.Page-1)
		}
		if data.Page*leadsPerPage < total {
			data.Next = link(data.Status, "list", data.Page+1)
		}
		for _, item := range items {
			if item.LastFromUser {
				data.Waiting++
			}
		}
	}

	now := time.Now()
	if data.Funnel, err = h.opts.Leads.Funnel(ctx, now.AddDate(0, 0, -30), now.Add(time.Minute)); err != nil {
		h.fail(w, r, "cannot compute the funnel", err)
		return
	}
	h.render(w, r, http.StatusOK, "leads", view{Title: "Заявки", Nav: "leads", Data: data})
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
	Replies       []leads.Template // in the client's language
	Rejects       []leads.Template
	Draft         string // the answer being written: a chosen template, or what was typed before an error
	Refusal       string // the same for the letter that goes with a refusal
	VisitID       string
	CanReply      bool
	ByPhone       bool
}

func leadID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (h *Handler) leadCard(w http.ResponseWriter, r *http.Request) {
	h.showLead(w, r, http.StatusOK, "", "")
}

func (h *Handler) showLead(w http.ResponseWriter, r *http.Request, status int, problem, draft string) {
	id, ok := leadID(r)
	if !ok {
		h.notFound(w, r, "Заявка не найдена")
		return
	}
	card, err := h.opts.Leads.Card(r.Context(), id)
	if errors.Is(err, leads.ErrNotFound) {
		h.notFound(w, r, "Заявка не найдена: возможно, она удалена по запросу клиента.")
		return
	}
	if err != nil {
		h.fail(w, r, "cannot read the request", err)
		return
	}
	lead := card.Lead
	data := leadData{Card: card, Direction: h.directionName(lead.Direction), Next: leads.NextStatuses(lead.Status), Draft: draft,
		CanReply: lead.Status != leads.StatusSpam && !card.AnonymizedAt.Valid, ByPhone: card.ReplyVia == leads.MethodPhone}
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
	if h.opts.BotStatus != nil && data.CanReply && lead.PublicToken != "" {
		if state, _ := h.opts.BotStatus(); state.Username != "" {
			data.ClientBotLink = "https://t.me/" + state.Username + "?start=" + telegram.ClientPrefix + lead.PublicToken
		}
	}

	templates, err := h.opts.Leads.Templates(r.Context(), "")
	if err != nil {
		h.fail(w, r, "cannot read the templates", err)
		return
	}
	for _, item := range templates {
		if item.Lang != lead.Lang {
			continue
		}
		// «?template=7» puts a ready-made text into its form — without any script.
		chosen := strconv.FormatInt(item.ID, 10) == r.URL.Query().Get("template")
		if item.Kind == "reply" {
			data.Replies = append(data.Replies, item)
			if chosen && draft == "" {
				data.Draft = leads.FillTemplate(item.Body, lead)
			}
		} else {
			data.Rejects = append(data.Rejects, item)
			if chosen {
				data.Refusal = leads.FillTemplate(item.Body, lead)
			}
		}
	}
	h.render(w, r, status, "lead", view{Title: "Заявка #" + lead.Number(), Nav: "leads", Error: problem, Data: data})
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request, message string) {
	h.render(w, r, http.StatusNotFound, "error", view{Title: "Не найдено", Nav: "leads", Error: message})
}

func (h *Handler) backToLead(w http.ResponseWriter, r *http.Request, id int64, flash string) {
	http.Redirect(w, r, fmt.Sprintf("%s/leads/%d?ok=%s", h.opts.Prefix, id, flash), http.StatusSeeOther)
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
	h.showLead(w, r, status, problem, "")
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
		h.showLead(w, r, http.StatusBadRequest, "Заметка пустая.", "")
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
	text := r.PostFormValue("text")
	switch _, err := h.opts.Leads.Reply(r.Context(), id, sessionOf(r).User.Login, text); {
	case err == nil:
		h.auditLead(r, "lead.reply", id, "")
		h.opts.Kick()
		h.backToLead(w, r, id, "lead-reply")
	case errors.Is(err, leads.ErrEmptyText):
		h.showLead(w, r, http.StatusBadRequest, "Ответ пустой.", "")
	case errors.Is(err, leads.ErrNotFound):
		h.notFound(w, r, "Заявка не найдена")
	default:
		h.opts.Log.Error("cannot store an answer", "error", err)
		h.showLead(w, r, http.StatusInternalServerError, "Не получилось сохранить ответ — текст ниже, попробуйте ещё раз.", text)
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
		h.showLead(w, r, http.StatusBadRequest, "Для удаления введите номер заявки: "+leads.Number(id), "")
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
		http.Redirect(w, r, h.opts.Prefix+"/leads?ok=lead-deleted", http.StatusSeeOther)
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

// --- ready-made answers --------------------------------------------------------------------------

type templateGroup struct {
	Kind, Name string
	Langs      []templateLang
}

type templateLang struct {
	Lang, Name string
	Items      []leads.Template
}

func (h *Handler) templatesPage(w http.ResponseWriter, r *http.Request) {
	h.showTemplates(w, r, http.StatusOK, "")
}

func (h *Handler) showTemplates(w http.ResponseWriter, r *http.Request, status int, problem string) {
	all, err := h.opts.Leads.Templates(r.Context(), "")
	if err != nil {
		h.fail(w, r, "cannot read the templates", err)
		return
	}
	var groups []templateGroup
	for _, kind := range []struct{ id, name string }{{"reply", "Ответы"}, {"reject", "Отказы"}} {
		group := templateGroup{Kind: kind.id, Name: kind.name}
		for _, lang := range []string{"ru", "uk", "en"} {
			block := templateLang{Lang: lang, Name: languageNames[lang]}
			for _, item := range all {
				if item.Kind == kind.id && item.Lang == lang {
					block.Items = append(block.Items, item)
				}
			}
			group.Langs = append(group.Langs, block)
		}
		groups = append(groups, group)
	}
	h.render(w, r, status, "templates", view{Title: "Шаблоны", Nav: "leads", Error: problem, Data: groups})
}

func (h *Handler) templateSave(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	item := leads.Template{ID: id, Kind: r.PostFormValue("kind"), Lang: r.PostFormValue("lang"), Title: r.PostFormValue("title"), Body: r.PostFormValue("body")}
	if r.PostFormValue("delete") != "" && id > 0 {
		if err := h.opts.Leads.DeleteTemplate(r.Context(), id); err != nil {
			h.fail(w, r, "cannot delete a template", err)
			return
		}
		http.Redirect(w, r, h.opts.Prefix+"/templates?ok=template-deleted", http.StatusSeeOther)
		return
	}
	switch err := h.opts.Leads.SaveTemplate(r.Context(), item); {
	case err == nil:
		http.Redirect(w, r, h.opts.Prefix+"/templates?ok=template-saved", http.StatusSeeOther)
	case errors.Is(err, leads.ErrEmptyText):
		h.showTemplates(w, r, http.StatusBadRequest, "У шаблона должны быть название и текст.")
	case errors.Is(err, leads.ErrNotFound):
		h.showTemplates(w, r, http.StatusNotFound, "Такого шаблона уже нет.")
	default:
		h.showTemplates(w, r, http.StatusBadRequest, "Шаблон не сохранён: проверьте вид и язык.")
	}
}
