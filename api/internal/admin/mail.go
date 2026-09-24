package admin

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/clients"
	"github.com/DenisHumen/krokosha-site/api/internal/inbox"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/mailboxes"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram"
)

// «Почта»: the mailboxes of the site's own mail server — a mailbox for a colleague with a password
// made here or typed by hand, a new password, a mailbox that is no longer needed. The admin area
// only asks: a root helper applies the request a second later (internal/mailboxes).

type mailData struct {
	Installed bool
	Domain    string
	Host      string // mail.<domain>: what mail programs connect to
	Mailboxes []mailboxes.Mailbox
	Log       []mailboxes.Outcome
	Waiting   bool // requests wait for the helper: the page refreshes itself
	Stuck     bool // …for more than a minute: the helper does not run, the page stops refreshing
	// Issued is the password just made, shown once: nothing keeps it after this page.
	Issued        string
	IssuedAddress string

	// The mail of the site itself: where letters come from and through what, how many went out
	// this week, how reading the service mailbox goes, and the queue of deliveries.
	From      string
	SMTP      string
	SentWeek  int
	Mailbox   string
	Inbox     *inbox.Status
	ReplyTo   string // what the address of the answers to a request looks like
	Queue     []queueRow
	HasQueue  bool
	QueueNote string
}

// queueRow is a delivery of the outbox in words.
type queueRow struct {
	outbox.Entry
	What  string
	State string
	Tone  string // ok | warn | wait
}

func queueRowOf(entry outbox.Entry, location *time.Location) queueRow {
	row := queueRow{Entry: entry, What: deliveryNames[entry.Kind]}
	if row.What == "" {
		row.What = entry.Kind
	}
	if entry.Kind == leads.TaskNotify && entry.Channel == outbox.ChannelTelegram {
		row.What = "Карточка заявки — в бот"
	}
	if entry.LeadID > 0 {
		row.What += " · #" + leads.Number(entry.LeadID)
	}
	clock := func(t time.Time) string { return t.In(location).Format("15:04") }
	switch {
	case entry.Status == "sent" && entry.SentAt.Valid:
		row.State, row.Tone = "отправлено "+clock(entry.SentAt.Time), "ok"
	case entry.Status == "sent":
		row.State, row.Tone = "отправлено", "ok"
	case entry.Status == "failed":
		row.State, row.Tone = "не доставлено", "warn"
	case entry.Attempts > 0:
		row.State, row.Tone = "ошибка · повтор "+clock(entry.NextAttempt), "warn"
	case entry.Status == "sending":
		row.State, row.Tone = "отправляется", "wait"
	default:
		row.State, row.Tone = "в очереди", "wait"
	}
	return row
}

// deliveryNames say what a task of the outbox carries.
var deliveryNames = map[string]string{
	leads.TaskNotify:        "Новая заявка — вам",
	leads.TaskAutoReply:     "Подтверждение клиенту",
	leads.TaskReply:         "Ответ клиенту",
	leads.TaskSiteReply:     "Ответ в кабинете — уведомление клиенту",
	leads.TaskClientMessage: "Сообщение клиента — сотрудникам",
	leads.TaskUndelivered:   "Письмо не дошло — сотрудникам",
	clients.TaskLogin:       "Код входа в кабинет",
	telegram.TaskStranger:   "Сообщение в бот без заявки — сотрудникам",
	outbox.KindAlert:        "Предупреждение сервера",
}

func (h *Handler) mailPage(w http.ResponseWriter, r *http.Request) {
	h.showMail(w, r, http.StatusOK, "", "", "")
}

func (h *Handler) showMail(w http.ResponseWriter, r *http.Request, status int, problem, issuedAddress, issued string) {
	m := h.opts.Mailboxes
	data := mailData{Installed: m.Installed(), Domain: m.Domain(), Host: "mail." + m.Domain(), Issued: issued, IssuedAddress: issuedAddress,
		SMTP: h.opts.SMTPAddr, Mailbox: h.opts.Mailbox}
	if from, err := mail.ParseAddress(h.opts.MailFrom); err == nil {
		data.From = from.Address
	}
	if local, domain, ok := strings.Cut(h.opts.Mailbox, "@"); ok {
		data.ReplyTo = local + "+0042.a8f3@" + domain
	}
	if h.opts.Inbox != nil {
		state := h.opts.Inbox.Status(r.Context())
		data.Inbox = &state
	}
	ctx := r.Context()
	if h.opts.SentSince != nil {
		sent, err := h.opts.SentSince(ctx, outbox.ChannelEmail, time.Now().AddDate(0, 0, -7))
		if err != nil {
			h.fail(w, r, "cannot count the letters sent", err)
			return
		}
		data.SentWeek = sent
	}
	if h.opts.Deliveries != nil {
		entries, err := h.opts.Deliveries(ctx, 12)
		if err != nil {
			h.fail(w, r, "cannot read the queue of deliveries", err)
			return
		}
		data.HasQueue = true
		for _, entry := range entries {
			data.Queue = append(data.Queue, queueRowOf(entry, h.opts.Location))
		}
	}
	if data.Installed {
		var err error
		if data.Mailboxes, err = m.List(); err != nil {
			h.fail(w, r, "cannot list the mailboxes", err)
			return
		}
		if data.Log, err = m.Log(); err != nil {
			h.fail(w, r, "cannot read the log of the mailboxes", err)
			return
		}
		for _, mailbox := range data.Mailboxes {
			data.Waiting = data.Waiting || mailbox.Pending != ""
		}
		// The helper answers within seconds. A request older than a minute means it does not run:
		// the page says so and stops reloading itself (a reload keeps the session awake).
		if pending, err := m.Pending(); err == nil && len(pending) > 0 && time.Since(pending[0].At) > time.Minute {
			data.Stuck = true
		}
	}
	h.render(w, r, status, "mail", view{Title: "Почта", Nav: "mail", Error: problem, Data: data})
}

// password is the password of a form: made here («generate»), or typed twice by hand.
func (h *Handler) password(r *http.Request) (password string, generated bool, problem string) {
	if r.PostFormValue("mode") != "custom" {
		password, err := mailboxes.Generate()
		if err != nil {
			return "", false, "Не получилось сделать пароль."
		}
		return password, true, ""
	}
	password = r.PostFormValue("password")
	switch {
	case password != r.PostFormValue("again"):
		return "", false, "Пароли не совпадают."
	case len([]rune(password)) < mailboxes.MinPassword:
		return "", false, "Пароль — не короче 12 символов."
	}
	return password, false, ""
}

func (h *Handler) mailAdd(w http.ResponseWriter, r *http.Request) {
	password, generated, problem := h.password(r)
	if problem != "" {
		h.showMail(w, r, http.StatusBadRequest, problem, "", "")
		return
	}
	address, err := h.opts.Mailboxes.Add(r.PostFormValue("address"), password)
	if h.mailFailed(w, r, err) {
		return
	}
	h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, "mail.add", address, "", h.attemptMeta(r).IPPrefix)
	h.mailDone(w, r, address, password, generated)
}

func (h *Handler) mailPassword(w http.ResponseWriter, r *http.Request) {
	password, generated, problem := h.password(r)
	if problem != "" {
		h.showMail(w, r, http.StatusBadRequest, problem, "", "")
		return
	}
	address := strings.ToLower(strings.TrimSpace(r.PostFormValue("address")))
	if h.mailFailed(w, r, h.opts.Mailboxes.SetPassword(address, password)) {
		return
	}
	h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, "mail.password", address, "", h.attemptMeta(r).IPPrefix)
	h.mailDone(w, r, address, password, generated)
}

// mailDone answers a request that went through. A password made here is shown on this very page and
// nowhere else: a redirect would have to carry it in the address.
func (h *Handler) mailDone(w http.ResponseWriter, r *http.Request, address, password string, generated bool) {
	if generated {
		h.showMail(w, r, http.StatusOK, "", address, password)
		return
	}
	http.Redirect(w, r, h.opts.Prefix+"/mail?ok=mail-request", http.StatusSeeOther)
}

func (h *Handler) mailDelete(w http.ResponseWriter, r *http.Request) {
	address := strings.ToLower(strings.TrimSpace(r.PostFormValue("address")))
	if strings.ToLower(strings.TrimSpace(r.PostFormValue("confirm"))) != address {
		h.showMail(w, r, http.StatusBadRequest, "Для удаления введите адрес ящика целиком: "+address, "", "")
		return
	}
	if h.mailFailed(w, r, h.opts.Mailboxes.Delete(address)) {
		return
	}
	h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, "mail.delete", address, "", h.attemptMeta(r).IPPrefix)
	http.Redirect(w, r, h.opts.Prefix+"/mail?ok=mail-request", http.StatusSeeOther)
}

// mailFailed explains a refused request; it reports whether there was one.
func (h *Handler) mailFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	messages := map[error]string{
		mailboxes.ErrNoMail:     "Почтового сервера здесь нет: сайт установлен с --no-mail.",
		mailboxes.ErrBadAddress: "Адрес — латинские буквы, цифры, точка, дефис и подчёркивание, до 64 символов, в домене сайта.",
		mailboxes.ErrShort:      "Пароль — не короче 12 символов и в одну строку.",
		mailboxes.ErrExists:     "Такой ящик уже есть: чтобы сменить пароль, нажмите «Новый пароль» в его строке.",
		mailboxes.ErrNoMailbox:  "Такого ящика нет.",
		mailboxes.ErrService:    "Это служебный ящик сайта: его пароль хранится в настройках сервиса и меняется установщиком.",
		mailboxes.ErrPending:    "Запрос по этому ящику ещё выполняется — подождите несколько секунд.",
	}
	for known, message := range messages {
		if errors.Is(err, known) {
			h.showMail(w, r, http.StatusBadRequest, message, "", "")
			return true
		}
	}
	h.fail(w, r, "cannot ask for a mailbox change", err)
	return true
}
