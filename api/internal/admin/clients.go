package admin

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/achievements"
	"github.com/DenisHumen/krokosha-site/api/internal/clients"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/loyalty"
)

// Clients in the admin area (docs/architecture.md, 2026-09-24): who has a personal account, how to
// write to them, what they ordered, their level and discounts — and what the owner can do about an
// account: give a personal discount, change the address it signs in with, send a link to sign in,
// end its sessions, block, merge two accounts of one person, delete.

const clientsPerPage = 50

type clientsData struct {
	Items  []clientRow
	Total  int
	Search string
	Page   int
	Pages  int
	Prev   string
	Next   string
	Rules  config.Loyalty
	Totals clients.Totals // accounts, new in 30 days, repeat clients, revenue of the year
}

type clientRow struct {
	clients.Row
	Tier    string // the level's name
	HasTier bool
}

func (h *Handler) clientsList(w http.ResponseWriter, r *http.Request) {
	h.showClients(w, r, http.StatusOK, "")
}

func (h *Handler) showClients(w http.ResponseWriter, r *http.Request, status int, problem string) {
	query := r.URL.Query()
	data := clientsData{Search: strings.TrimSpace(query.Get("q")), Rules: h.opts.Loyalty()}
	data.Page, _ = strconv.Atoi(query.Get("page"))
	data.Page = max(data.Page, 1)
	rows, total, err := h.opts.Clients.List(r.Context(), clients.Filter{Query: data.Search, Limit: clientsPerPage, Offset: (data.Page - 1) * clientsPerPage})
	if err != nil {
		h.fail(w, r, "cannot list the clients", err)
		return
	}
	for _, row := range rows {
		item := clientRow{Row: row}
		if tier, ok := loyalty.TierOf(data.Rules, loyalty.History{Orders: row.Orders, Spent: row.Spent}); ok {
			item.Tier, item.HasTier = tier.Name.In("ru"), true
		}
		data.Items = append(data.Items, item)
	}
	data.Total, data.Pages = total, max(1, (total+clientsPerPage-1)/clientsPerPage)
	now := time.Now().In(h.opts.Location)
	if data.Totals, err = h.opts.Clients.Totals(r.Context(), now.AddDate(0, 0, -30), time.Date(now.Year(), 1, 1, 0, 0, 0, 0, h.opts.Location)); err != nil {
		h.fail(w, r, "cannot count the clients", err)
		return
	}
	link := func(page int) string {
		values := url.Values{}
		if data.Search != "" {
			values.Set("q", data.Search)
		}
		if page > 1 {
			values.Set("page", strconv.Itoa(page))
		}
		return values.Encode()
	}
	if data.Page > 1 {
		data.Prev = link(data.Page - 1)
	}
	if data.Page*clientsPerPage < total {
		data.Next = link(data.Page + 1)
	}
	h.render(w, r, status, "clients", view{Title: "Клиенты", Nav: "clients", Error: problem, Data: data})
}

// clientCreate makes an account for a client who came by phone or in person.
func (h *Handler) clientCreate(w http.ResponseWriter, r *http.Request) {
	id, err := h.opts.Clients.Create(r.Context(), r.PostFormValue("name"), r.PostFormValue("email"), r.PostFormValue("phone"))
	switch {
	case errors.Is(err, clients.ErrTaken):
		h.showClients(w, r, http.StatusConflict, "Этот адрес уже у другого клиента — найдите его поиском.")
	case errors.Is(err, clients.ErrBadContact):
		h.showClients(w, r, http.StatusBadRequest, "Проверьте адрес почты и телефон.")
	case err != nil:
		h.fail(w, r, "cannot create a client", err)
	default:
		h.auditClient(r, "client.create", id, "")
		h.backToClient(w, r, id, "client-created")
	}
}

// --- the card --------------------------------------------------------------------------------------

type clientData struct {
	Client       *clients.Client
	Contacts     []clients.Contact
	Sessions     []clients.SessionInfo
	Eggs         []clients.Found
	EggsTotal    int
	Orders       []clients.Earned // the achievements of orders
	OrdersTotal  int
	Leads        []leads.ClientSummary
	History      loyalty.History
	Tier         config.LoyaltyTier
	HasTier      bool
	Next         loyalty.Next
	HasNext      bool
	Offer        loyalty.Offer
	Rules        config.Loyalty
	Kinds        []string
	TelegramLink string
	Today        string
}

func clientID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (h *Handler) clientCard(w http.ResponseWriter, r *http.Request) {
	h.showClient(w, r, http.StatusOK, "")
}

func (h *Handler) showClient(w http.ResponseWriter, r *http.Request, status int, problem string) {
	id, ok := clientID(r)
	if !ok {
		h.clientNotFound(w, r)
		return
	}
	ctx := r.Context()
	client, err := h.opts.Clients.Get(ctx, id)
	if errors.Is(err, clients.ErrNotFound) {
		h.clientNotFound(w, r)
		return
	}
	if err != nil {
		h.fail(w, r, "cannot read a client", err)
		return
	}
	data := clientData{Client: client, Rules: h.opts.Loyalty(), Kinds: clients.Kinds, EggsTotal: len(achievements.Eggs),
		OrdersTotal: len(clients.OrderAchievements), Today: time.Now().In(h.opts.Location).Format(time.DateOnly)}
	// Orders completed before the achievements existed earn them here as well.
	if err := h.opts.Clients.AwardOrders(ctx, id); err != nil {
		h.opts.Log.Warn("cannot award the achievements of orders", "client", id, "error", err)
	}
	if data.Contacts, err = h.opts.Clients.Contacts(ctx, id); err == nil {
		if data.Sessions, err = h.opts.Clients.Sessions(ctx, id, nil); err == nil {
			if data.Eggs, err = h.opts.Clients.Eggs(ctx, id); err == nil {
				if data.Orders, err = h.opts.Clients.Orders(ctx, id); err == nil {
					data.Leads, err = h.opts.Leads.ClientLeads(ctx, id)
				}
			}
		}
	}
	if err != nil {
		h.fail(w, r, "cannot read a client", err)
		return
	}
	if data.History, _, err = h.opts.Leads.History(ctx, id); err != nil {
		h.fail(w, r, "cannot read a client's history", err)
		return
	}
	data.Tier, data.HasTier = loyalty.TierOf(data.Rules, data.History)
	data.Next, data.HasNext = loyalty.NextTier(data.Rules, data.History)
	if data.Offer, err = h.opts.Leads.Preview(ctx, id, len(data.Eggs) == len(achievements.Eggs)); err != nil {
		h.fail(w, r, "cannot price a client's next request", err)
		return
	}
	switch {
	case client.TelegramUsername != "":
		data.TelegramLink = "https://t.me/" + client.TelegramUsername
	case client.TelegramID != 0:
		data.TelegramLink = "tg://user?id=" + strconv.FormatInt(client.TelegramID, 10)
	}
	title := client.Name
	if title == "" {
		title = fmt.Sprintf("Клиент #%d", id)
	}
	h.render(w, r, status, "client", view{Title: title, Nav: "clients", Error: problem, Data: data})
}

func (h *Handler) clientNotFound(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusNotFound, "error", view{Title: "Не найдено", Nav: "clients", Error: "Клиент не найден: возможно, аккаунт удалён."})
}

func (h *Handler) backToClient(w http.ResponseWriter, r *http.Request, id int64, flash string) {
	http.Redirect(w, r, fmt.Sprintf("%s/clients/%d?ok=%s", h.opts.Prefix, id, flash), http.StatusSeeOther)
}

func (h *Handler) auditClient(r *http.Request, action string, id int64, details string) {
	h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, action, fmt.Sprintf("client #%d", id), details, h.attemptMeta(r).IPPrefix)
}

// clientAction runs one change of an account and answers the usual way: back to the card, or the
// card again with what went wrong.
func (h *Handler) clientAction(w http.ResponseWriter, r *http.Request, action, flash string, run func(id int64) (string, error)) {
	id, ok := clientID(r)
	if !ok {
		h.clientNotFound(w, r)
		return
	}
	details, err := run(id)
	switch {
	case err == nil:
		h.auditClient(r, action, id, details)
		h.backToClient(w, r, id, flash)
	case errors.Is(err, clients.ErrNotFound):
		h.clientNotFound(w, r)
	case errors.Is(err, clients.ErrTaken):
		h.showClient(w, r, http.StatusConflict, "Этот адрес уже у другого клиента. Если это один человек — объедините аккаунты.")
	case errors.Is(err, clients.ErrBadContact):
		h.showClient(w, r, http.StatusBadRequest, "Значение не похоже на контакт этого вида — проверьте его.")
	case errors.Is(err, clients.ErrBadDiscount), errors.Is(err, errBadForm):
		h.showClient(w, r, http.StatusBadRequest, "Проверьте значения формы.")
	case errors.Is(err, clients.ErrTooMany):
		h.showClient(w, r, http.StatusBadRequest, fmt.Sprintf("У клиента уже %d контактов.", clients.MaxContacts))
	case errors.Is(err, clients.ErrDisabled):
		h.showClient(w, r, http.StatusConflict, "Аккаунт заблокирован: сначала разблокируйте его.")
	case errors.Is(err, leads.ErrNotFound):
		h.showClient(w, r, http.StatusNotFound, "Заявки с таким номером нет.")
	default:
		h.fail(w, r, "cannot change a client", err)
	}
}

var errBadForm = errors.New("bad form")

func (h *Handler) clientProfile(w http.ResponseWriter, r *http.Request) {
	h.clientAction(w, r, "client.profile", "client-saved", func(id int64) (string, error) {
		return "", h.opts.Clients.SetProfile(r.Context(), id, clients.Profile{
			Name: r.PostFormValue("name"), Company: r.PostFormValue("company"), Lang: r.PostFormValue("lang"), Preferred: r.PostFormValue("preferred"),
		})
	})
}

func (h *Handler) clientNote(w http.ResponseWriter, r *http.Request) {
	h.clientAction(w, r, "client.note", "client-saved", func(id int64) (string, error) {
		return "", h.opts.Clients.SetNote(r.Context(), id, r.PostFormValue("note"))
	})
}

func (h *Handler) clientContactAdd(w http.ResponseWriter, r *http.Request) {
	h.clientAction(w, r, "client.contact", "client-saved", func(id int64) (string, error) {
		_, err := h.opts.Clients.AddContact(r.Context(), id, r.PostFormValue("kind"), r.PostFormValue("value"))
		return r.PostFormValue("kind"), err
	})
}

func (h *Handler) clientContactRemove(w http.ResponseWriter, r *http.Request) {
	h.clientAction(w, r, "client.contact-remove", "client-saved", func(id int64) (string, error) {
		contact, err := strconv.ParseInt(r.PathValue("contact"), 10, 64)
		if err != nil {
			return "", clients.ErrNotFound
		}
		return "", h.opts.Clients.RemoveContact(r.Context(), id, contact)
	})
}

// clientDiscount gives the client a discount of their own: any percent, for good or until a day,
// for every request or once.
func (h *Handler) clientDiscount(w http.ResponseWriter, r *http.Request) {
	h.clientAction(w, r, "client.discount", "client-discount", func(id int64) (string, error) {
		percent, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(r.PostFormValue("percent")), "%"))
		if err != nil && strings.TrimSpace(r.PostFormValue("percent")) != "" {
			return "", errBadForm
		}
		personal := clients.Personal{Percent: percent, Note: r.PostFormValue("note"), Once: r.PostFormValue("once") == "on"}
		if until := strings.TrimSpace(r.PostFormValue("until")); until != "" {
			day, err := time.Parse(time.DateOnly, until)
			if err != nil {
				return "", errBadForm
			}
			personal.Until = sql.NullTime{Time: day, Valid: true}
		}
		details := fmt.Sprintf("%d%%", percent)
		if personal.Once {
			details += ", один раз"
		}
		if personal.Until.Valid {
			details += ", до " + personal.Until.Time.Format("02.01.2006")
		}
		return details, h.opts.Clients.SetPersonal(r.Context(), id, personal)
	})
}

func (h *Handler) clientEmail(w http.ResponseWriter, r *http.Request) {
	h.clientAction(w, r, "client.email", "client-saved", func(id int64) (string, error) {
		return "адрес входа изменён", h.opts.Clients.SetEmail(r.Context(), id, r.PostFormValue("email"))
	})
}

func (h *Handler) clientUnlinkTelegram(w http.ResponseWriter, r *http.Request) {
	h.clientAction(w, r, "client.telegram-unlink", "client-saved", func(id int64) (string, error) {
		return "", h.opts.Clients.UnlinkTelegram(r.Context(), id)
	})
}

func (h *Handler) clientEndSessions(w http.ResponseWriter, r *http.Request) {
	h.clientAction(w, r, "client.sessions-end", "client-sessions", func(id int64) (string, error) {
		ended, err := h.opts.Clients.EndSessions(r.Context(), id, nil)
		return fmt.Sprintf("завершено: %d", ended), err
	})
}

func (h *Handler) clientSendLink(w http.ResponseWriter, r *http.Request) {
	h.clientAction(w, r, "client.link", "client-link", func(id int64) (string, error) {
		masked, err := h.opts.Clients.SendLink(r.Context(), id)
		if err == nil {
			h.opts.Kick()
		}
		return masked, err
	})
}

func (h *Handler) clientBlock(w http.ResponseWriter, r *http.Request) {
	block := r.PostFormValue("block") == "1"
	action, flash := "client.unblock", "client-unblocked"
	if block {
		action, flash = "client.block", "client-blocked"
	}
	h.clientAction(w, r, action, flash, func(id int64) (string, error) {
		return "", h.opts.Clients.SetDisabled(r.Context(), id, block)
	})
}

// clientMerge moves another account of the same person into this one.
func (h *Handler) clientMerge(w http.ResponseWriter, r *http.Request) {
	h.clientAction(w, r, "client.merge", "client-merged", func(id int64) (string, error) {
		other, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(r.PostFormValue("other")), "#"), 10, 64)
		if err != nil || other <= 0 || other == id {
			return "", errBadForm
		}
		return fmt.Sprintf("влит клиент #%d", other), h.opts.Clients.Merge(r.Context(), id, other)
	})
}

// clientAttach gives a request to the client: one left by phone, or with another address.
func (h *Handler) clientAttach(w http.ResponseWriter, r *http.Request) {
	h.clientAction(w, r, "client.attach", "client-attached", func(id int64) (string, error) {
		lead, ok := parseLeadNumber(r.PostFormValue("lead"))
		if !ok {
			return "", leads.ErrNotFound
		}
		err := h.opts.Leads.Attach(r.Context(), lead, id, sessionOf(r).User.Login)
		return leads.Number(lead), err
	})
}

func parseLeadNumber(text string) (int64, bool) {
	text = strings.TrimPrefix(strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(text), "#")), "K-")
	id, err := strconv.ParseInt(text, 10, 64)
	return id, err == nil && id > 0
}

// clientDelete removes the account; its requests stay, as requests of nobody.
func (h *Handler) clientDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := clientID(r)
	if !ok {
		h.clientNotFound(w, r)
		return
	}
	if strings.TrimPrefix(strings.TrimSpace(r.PostFormValue("confirm")), "#") != strconv.FormatInt(id, 10) {
		h.showClient(w, r, http.StatusBadRequest, fmt.Sprintf("Для удаления введите номер клиента: %d", id))
		return
	}
	switch err := h.opts.Clients.Delete(r.Context(), id); {
	case errors.Is(err, clients.ErrNotFound):
		h.clientNotFound(w, r)
	case err != nil:
		h.fail(w, r, "cannot delete a client", err)
	default:
		h.auditClient(r, "client.delete", id, "аккаунт удалён, заявки остались без клиента")
		http.Redirect(w, r, h.opts.Prefix+"/clients?ok=client-deleted", http.StatusSeeOther)
	}
}

// --- a request's discount, sum and client --------------------------------------------------------------

func (h *Handler) leadDiscount(w http.ResponseWriter, r *http.Request) {
	id, ok := leadID(r)
	if !ok {
		h.notFound(w, r, "Заявка не найдена")
		return
	}
	percent, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(r.PostFormValue("percent")), "%"))
	if err != nil || percent < 0 || percent > 100 {
		h.showLead(w, r, http.StatusBadRequest, "Скидка — целое число процентов от 0 до 100.", draftOf{})
		return
	}
	switch err := h.opts.Leads.SetDiscount(r.Context(), id, sessionOf(r).User.Login, percent, r.PostFormValue("note")); {
	case err == nil:
		h.auditLead(r, "lead.discount", id, fmt.Sprintf("%d%%", percent))
		h.backToLead(w, r, id, "lead-discount")
	case errors.Is(err, leads.ErrNotFound):
		h.notFound(w, r, "Заявка не найдена")
	default:
		h.fail(w, r, "cannot set a discount", err)
	}
}

func (h *Handler) leadAmount(w http.ResponseWriter, r *http.Request) {
	id, ok := leadID(r)
	if !ok {
		h.notFound(w, r, "Заявка не найдена")
		return
	}
	var amount *float64
	if text := strings.TrimSpace(strings.NewReplacer(" ", "", " ", "", ",", ".").Replace(r.PostFormValue("amount"))); text != "" {
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			h.showLead(w, r, http.StatusBadRequest, "Сумма — число, например 1500 или 1500.50.", draftOf{})
			return
		}
		amount = &value
	}
	switch err := h.opts.Leads.SetAmount(r.Context(), id, sessionOf(r).User.Login, amount); {
	case err == nil:
		h.auditLead(r, "lead.amount", id, "")
		h.backToLead(w, r, id, "lead-amount")
	case errors.Is(err, leads.ErrBadAmount):
		h.showLead(w, r, http.StatusBadRequest, "Сумма не может быть отрицательной.", draftOf{})
	case errors.Is(err, leads.ErrNotFound):
		h.notFound(w, r, "Заявка не найдена")
	default:
		h.fail(w, r, "cannot set the amount", err)
	}
}

// leadClient attaches a request to a client by the client's number or address, or detaches it.
func (h *Handler) leadClient(w http.ResponseWriter, r *http.Request) {
	id, ok := leadID(r)
	if !ok {
		h.notFound(w, r, "Заявка не найдена")
		return
	}
	var client int64
	if text := strings.TrimSpace(r.PostFormValue("client")); text != "" && r.PostFormValue("detach") == "" {
		number, err := strconv.ParseInt(strings.TrimPrefix(text, "#"), 10, 64)
		if err != nil || number <= 0 {
			rows, _, listErr := h.opts.Clients.List(r.Context(), clients.Filter{Query: text, Limit: 2})
			if listErr != nil || len(rows) != 1 {
				h.showLead(w, r, http.StatusBadRequest, "Клиент не найден однозначно: укажите его номер (#12) или точный адрес.", draftOf{})
				return
			}
			number = rows[0].ID
		}
		client = number
	}
	switch err := h.opts.Leads.Attach(r.Context(), id, client, sessionOf(r).User.Login); {
	case err == nil:
		h.auditLead(r, "lead.client", id, fmt.Sprintf("client #%d", client))
		h.backToLead(w, r, id, "lead-client")
	case errors.Is(err, leads.ErrNoClient):
		h.showLead(w, r, http.StatusBadRequest, "Такого клиента нет.", draftOf{})
	case errors.Is(err, leads.ErrNotFound):
		h.notFound(w, r, "Заявка не найдена")
	default:
		h.fail(w, r, "cannot attach a request", err)
	}
}

// --- words for the templates -------------------------------------------------------------------------

// eggNames are the achievements as the owner reads them (the site's texts are content/site.yaml → eggs).
var eggNames = map[string]string{
	"konami": "↑↑↓↓←→←→BA — ночь в серверной", "sudo": "Суперпользователь — терминал по sudo", "croc": "Крокодил замечен",
	"croc5": "Настоящий Krokosha — 5 кликов по крокодилу", "cat": "Заклинатель котов", "reboot": "Выключи и включи — аватар",
	"console": "Почти коллега — krokosha.hello()", "lost_packet": "Пакет найден — игра на 404", "all": "root@krokosha — все пасхалки",
}

// discountText says why a request has its discount, the way the owner reads it.
func (h *Handler) discountText(offer loyalty.Offer) string {
	if offer.Percent <= 0 {
		return "нет"
	}
	return leads.DiscountWords(h.opts.Loyalty(), offer, "ru", true)
}

func (h *Handler) tierName(id string) string {
	if tier, ok := h.opts.Loyalty().Tier(id); ok {
		if name := tier.Name.In("ru"); name != "" {
			return name
		}
	}
	return id
}

// money writes a sum the way the owner reads it: «1 200 $».
func (h *Handler) money(value any) string {
	amount := toFloat(value)
	whole := strconv.FormatInt(int64(amount), 10)
	var grouped []string
	for len(whole) > 3 {
		grouped = append([]string{whole[len(whole)-3:]}, grouped...)
		whole = whole[:len(whole)-3]
	}
	text := strings.Join(append([]string{whole}, grouped...), " ")
	if cents := int64(amount*100+0.5) % 100; cents != 0 {
		text += fmt.Sprintf(",%02d", cents)
	}
	symbols := map[string]string{"USD": "$", "EUR": "€", "UAH": "₴", "GBP": "£"}
	currency := h.opts.Loyalty().Currency
	if symbol, ok := symbols[currency]; ok {
		currency = symbol
	}
	return strings.TrimSpace(text + " " + currency)
}

func toFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case *float64:
		if v != nil {
			return *v
		}
	case int:
		return float64(v)
	}
	return 0
}

// contactLink is a link to write to a client by a contact; "" — the kind has none.
func contactLink(contact clients.Contact) template.URL {
	return template.URL(contact.Link()) //nolint:gosec // built by clients.ContactLink from a value clients.NormalizeContact checked
}
