package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram"
)

// The Telegram bot in the admin area (brief B10.3): how it is doing, who has access, and
// invitations. Everything here can also be done by the owner inside the bot (/invite, /users)
// and with `krokosha-cli bot`; all three end in the same telegram.Access.

type botData struct {
	Configured bool // a token is set
	Status     telegram.Status
	Members    []telegram.Member
	Invites    []telegram.Invite
	// NewInvite is shown once, right after it was made: the database keeps only a hash of the code.
	NewInvite *newInvite
	// Messages of clients and answers in Telegram over the last week; accounts that sign in with
	// Telegram, of all accounts; the bot's reminders; its commands.
	Messages    int
	ByTelegram  int
	Accounts    int
	HasAccounts bool
	RemindAfter time.Duration
	DigestAt    string
	Commands    []telegram.Command
	Roles       []botRole
}

// botRole is a role in the words of the admin area.
type botRole struct{ ID, Name, Hint string }

var botRoles = []botRole{
	{telegram.RoleMember, "участник", "получает заявки и работает с ними"},
	{telegram.RoleOwner, "владелец", "ещё и управляет доступом"},
	{telegram.RoleNotify, "только уведомления", "получает карточки, но ничего не меняет"},
}

func botRoleName(id string) string {
	for _, role := range botRoles {
		if role.ID == id {
			return role.Name
		}
	}
	return id
}

type newInvite struct {
	Role, Code, Link string
	Expires          time.Time
}

func (h *Handler) botPage(w http.ResponseWriter, r *http.Request) {
	h.showBot(w, r, http.StatusOK, "", nil)
}

func (h *Handler) showBot(w http.ResponseWriter, r *http.Request, status int, problem string, invite *newInvite) {
	data := botData{NewInvite: invite}
	if h.opts.BotStatus != nil {
		data.Status, data.Configured = h.opts.BotStatus()
	}
	var err error
	if data.Members, err = h.opts.BotAccess.Members(r.Context()); err != nil {
		h.fail(w, r, "cannot list the members of the bot", err)
		return
	}
	if data.Invites, err = h.opts.BotAccess.Invites(r.Context()); err != nil {
		h.fail(w, r, "cannot list the invitations", err)
		return
	}
	data.RemindAfter, data.DigestAt, data.Commands, data.Roles = h.opts.BotRemindAfter, h.opts.BotDigestAt, telegram.Commands(), botRoles
	if h.opts.Leads != nil {
		if data.Messages, err = h.opts.Leads.ChannelMessages(r.Context(), leads.MethodTelegram, time.Now().AddDate(0, 0, -7)); err != nil {
			h.fail(w, r, "cannot count the messages in the bot", err)
			return
		}
	}
	if h.opts.Clients != nil {
		totals, err := h.opts.Clients.Totals(r.Context(), time.Now(), time.Now())
		if err != nil {
			h.fail(w, r, "cannot count the accounts", err)
			return
		}
		data.ByTelegram, data.Accounts, data.HasAccounts = totals.Telegram, totals.Clients, true
	}
	h.render(w, r, status, "bot", view{Title: "Бот", Nav: "bot", Error: problem, Data: data})
}

func (h *Handler) botInvite(w http.ResponseWriter, r *http.Request) {
	actor := sessionOf(r).User.Login
	role := r.PostFormValue("role")
	code, expires, err := h.opts.BotAccess.Invite(r.Context(), role, actor)
	if errors.Is(err, telegram.ErrBadRole) {
		h.showBot(w, r, http.StatusBadRequest, badRole, nil)
		return
	}
	if err != nil {
		h.fail(w, r, "cannot make an invitation", err)
		return
	}
	h.opts.Auth.Audit(r.Context(), actor, "bot.invite", role, "", h.attemptMeta(r).IPPrefix)
	invite := &newInvite{Role: role, Code: code, Expires: expires}
	if h.opts.BotStatus != nil {
		if state, _ := h.opts.BotStatus(); state.Username != "" {
			invite.Link = "https://t.me/" + state.Username + "?start=" + code
		}
	}
	// Shown in the answer to the form itself, not after a redirect: the code exists in readable
	// form only here and now.
	h.showBot(w, r, http.StatusOK, "", invite)
}

func (h *Handler) botInviteRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	if err != nil {
		h.showBot(w, r, http.StatusBadRequest, "Такого приглашения нет.", nil)
		return
	}
	if err := h.opts.BotAccess.RevokeInvite(r.Context(), id); err != nil {
		h.fail(w, r, "cannot revoke an invitation", err)
		return
	}
	h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, "bot.invite-revoke", strconv.FormatInt(id, 10), "", h.attemptMeta(r).IPPrefix)
	http.Redirect(w, r, h.opts.Prefix+"/bot?ok=bot-invite-revoked", http.StatusSeeOther)
}

// botMember revokes access or gives it back. It works at once: the bot asks the database about
// every single update.
func (h *Handler) botMember(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	off := r.PostFormValue("action") == "disable"
	if err != nil || (!off && r.PostFormValue("action") != "enable") {
		h.showBot(w, r, http.StatusBadRequest, "Непонятное действие.", nil)
		return
	}
	member, err := h.opts.BotAccess.SetDisabled(r.Context(), id, off)
	switch {
	case errors.Is(err, telegram.ErrLastOwner):
		h.showBot(w, r, http.StatusConflict, "Единственного владельца отключить нельзя: некому будет приглашать людей из бота.", nil)
	case errors.Is(err, telegram.ErrNoAccess):
		h.showBot(w, r, http.StatusNotFound, "Такого участника нет.", nil)
	case err != nil:
		h.fail(w, r, "cannot change access to the bot", err)
	default:
		action, flash := "bot.enable", "bot-enabled"
		if off {
			action, flash = "bot.disable", "bot-disabled"
		}
		h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, action, strconv.FormatInt(member.TelegramID, 10), member.Name, h.attemptMeta(r).IPPrefix)
		http.Redirect(w, r, h.opts.Prefix+"/bot?ok="+flash, http.StatusSeeOther)
	}
}

const badRole = "Роль — «участник», «владелец» или «только уведомления»."

// botAdd lets a person in by their numeric Telegram id, without an invitation.
func (h *Handler) botAdd(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("telegram_id")), 10, 64)
	if err != nil || id <= 0 {
		h.showBot(w, r, http.StatusBadRequest, "Telegram ID — число, например 123456789. Его показывает, например, @userinfobot.", nil)
		return
	}
	actor := sessionOf(r).User.Login
	member, err := h.opts.BotAccess.Add(r.Context(), id, r.PostFormValue("role"), r.PostFormValue("name"), actor)
	switch {
	case errors.Is(err, telegram.ErrBadRole):
		h.showBot(w, r, http.StatusBadRequest, badRole, nil)
	case errors.Is(err, telegram.ErrLastOwner):
		h.showBot(w, r, http.StatusConflict, "Это единственный владелец: сначала сделайте владельцем кого-то ещё.", nil)
	case err != nil:
		h.fail(w, r, "cannot add a member of the bot", err)
	default:
		h.opts.Auth.Audit(r.Context(), actor, "bot.add", strconv.FormatInt(id, 10), member.Role, h.attemptMeta(r).IPPrefix)
		// Cards that waited for somebody to receive them go out now.
		h.opts.Kick()
		http.Redirect(w, r, h.opts.Prefix+"/bot?ok=bot-added", http.StatusSeeOther)
	}
}

// botRole changes what a person may do in the bot; the last owner stays one.
func (h *Handler) botRole(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	if err != nil {
		h.showBot(w, r, http.StatusBadRequest, "Такого участника нет.", nil)
		return
	}
	member, err := h.opts.BotAccess.SetRole(r.Context(), id, r.PostFormValue("role"))
	switch {
	case errors.Is(err, telegram.ErrBadRole):
		h.showBot(w, r, http.StatusBadRequest, badRole, nil)
	case errors.Is(err, telegram.ErrLastOwner):
		h.showBot(w, r, http.StatusConflict, "Это единственный владелец: сначала сделайте владельцем кого-то ещё.", nil)
	case errors.Is(err, telegram.ErrNoAccess):
		h.showBot(w, r, http.StatusNotFound, "Такого участника нет.", nil)
	case err != nil:
		h.fail(w, r, "cannot change a role in the bot", err)
	default:
		h.opts.Auth.Audit(r.Context(), sessionOf(r).User.Login, "bot.role", strconv.FormatInt(member.TelegramID, 10), member.Role, h.attemptMeta(r).IPPrefix)
		http.Redirect(w, r, h.opts.Prefix+"/bot?ok=bot-role", http.StatusSeeOther)
	}
}
