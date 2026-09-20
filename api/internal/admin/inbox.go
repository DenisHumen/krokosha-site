package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/DenisHumen/krokosha-site/api/internal/imap"
	"github.com/DenisHumen/krokosha-site/api/internal/inbox"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// Letters that came to the service mailbox and belong to no request (brief B10.5): a person
// decides — into the conversation of a request, or away.

// Inbox is the part of the mail reader the admin area needs.
type Inbox interface {
	Waiting(ctx context.Context) ([]inbox.Waiting, error)
	Attach(ctx context.Context, id, leadID int64, actor string) error
	Discard(ctx context.Context, id int64) error
	Status(ctx context.Context) inbox.Status
}

type inboxData struct {
	Letters  []inbox.Waiting
	Open     []leads.Summary // requests somebody still works on: hints for the number field
	Mailbox  string
	KeepDays int
}

func (h *Handler) inboxPage(w http.ResponseWriter, r *http.Request) {
	h.showInbox(w, r, http.StatusOK, "")
}

func (h *Handler) showInbox(w http.ResponseWriter, r *http.Request, status int, problem string) {
	if h.opts.Inbox == nil {
		h.notFound(w, r, "Чтение входящей почты не настроено (IMAP_ADDR в настройках сервиса).")
		return
	}
	letters, err := h.opts.Inbox.Waiting(r.Context())
	if err != nil {
		h.fail(w, r, "cannot list the letters", err)
		return
	}
	data := inboxData{Letters: letters, Mailbox: h.opts.Mailbox, KeepDays: h.opts.KeepLettersDays}
	if h.opts.Leads != nil {
		if data.Open, _, err = h.opts.Leads.List(r.Context(), leads.Filter{Status: "open", Limit: 50}); err != nil {
			h.fail(w, r, "cannot list the requests", err)
			return
		}
	}
	h.render(w, r, status, "inbox", view{Title: "Входящие без заявки", Nav: "inbox", Error: problem, Data: data})
}

func letterID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// leadNumber reads «#K-0042», «k-42» and «42».
func leadNumber(typed string) (int64, bool) {
	typed = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(typed)), "#")
	typed = strings.TrimPrefix(typed, "K-")
	id, err := strconv.ParseInt(typed, 10, 64)
	return id, err == nil && id > 0
}

func (h *Handler) inboxAttach(w http.ResponseWriter, r *http.Request) {
	id, ok := letterID(r)
	if !ok || h.opts.Inbox == nil {
		h.notFound(w, r, "Письмо не найдено")
		return
	}
	leadID, ok := leadNumber(r.PostFormValue("lead"))
	if !ok {
		h.showInbox(w, r, http.StatusBadRequest, "Введите номер заявки: #K-0042.")
		return
	}
	err := h.opts.Inbox.Attach(r.Context(), id, leadID, sessionOf(r).User.Login)
	var refused *imap.Error
	switch {
	case err == nil:
		h.auditLead(r, "inbox.attach", leadID, "письмо без заявки отнесено к заявке вручную")
		h.opts.Kick()
		http.Redirect(w, r, h.opts.Prefix+"/leads/"+strconv.FormatInt(leadID, 10)+"?ok=letter-attached", http.StatusSeeOther)
	case errors.Is(err, inbox.ErrGone):
		h.showInbox(w, r, http.StatusConflict, "Это письмо уже разобрано — возможно, другим администратором.")
	case errors.Is(err, leads.ErrNotFound):
		h.showInbox(w, r, http.StatusBadRequest, "Заявки "+leads.Number(leadID)+" нет: она удалена, обезличена или номер введён с ошибкой.")
	case errors.Is(err, leads.ErrEmptyText):
		h.showInbox(w, r, http.StatusBadRequest, "В письме нет ни текста, ни файлов, которые можно принять: переносить нечего.")
	case errors.As(err, &refused):
		h.opts.Log.Error("the mailbox refused", "error", err)
		h.showInbox(w, r, http.StatusBadGateway, "Почтовый ящик не ответил как надо. Письмо осталось на месте — попробуйте позже; подробности на экране «Статус».")
	default:
		h.opts.Log.Error("cannot attach a letter", "error", err)
		h.showInbox(w, r, http.StatusBadGateway, "Не получилось перенести письмо: почтовый ящик или база недоступны. Письмо осталось на месте — попробуйте позже.")
	}
}

func (h *Handler) inboxDiscard(w http.ResponseWriter, r *http.Request) {
	id, ok := letterID(r)
	if !ok || h.opts.Inbox == nil {
		h.notFound(w, r, "Письмо не найдено")
		return
	}
	switch err := h.opts.Inbox.Discard(r.Context(), id); {
	case err == nil:
		h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, "inbox.discard", "", "письмо без заявки удалено", h.attemptMeta(r).IPPrefix)
		http.Redirect(w, r, h.opts.Prefix+"/inbox?ok=letter-discarded", http.StatusSeeOther)
	case errors.Is(err, inbox.ErrGone):
		http.Redirect(w, r, h.opts.Prefix+"/inbox", http.StatusSeeOther)
	default:
		h.opts.Log.Error("cannot discard a letter", "error", err)
		h.showInbox(w, r, http.StatusBadGateway, "Не получилось удалить письмо: почтовый ящик недоступен. Попробуйте позже.")
	}
}
