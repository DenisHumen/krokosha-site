package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

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
	h.render(w, r, status, "bot", view{Title: "Бот", Nav: "bot", Error: problem, Data: data})
}

func (h *Handler) botInvite(w http.ResponseWriter, r *http.Request) {
	actor := sessionOf(r).User.Login
	role := r.PostFormValue("role")
	code, expires, err := h.opts.BotAccess.Invite(r.Context(), role, actor)
	if errors.Is(err, telegram.ErrBadRole) {
		h.showBot(w, r, http.StatusBadRequest, "Роль — «участник» или «владелец».", nil)
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
