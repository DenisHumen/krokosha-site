package admin

import (
	"bytes"
	"errors"
	"image/png"
	"net/http"

	"rsc.io/qr"

	"github.com/DenisHumen/krokosha-site/api/internal/auth"
)

// --- signing in ---------------------------------------------------------------------------------

type loginData struct {
	Login   string
	Pending string // set on the second step: the form asks for the one-time code only
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(cookieName); err == nil {
		if _, err := h.opts.Auth.Authenticate(r.Context(), cookie.Value); err == nil {
			http.Redirect(w, r, h.opts.Prefix+"/", http.StatusSeeOther)
			return
		}
	}
	h.render(w, r, http.StatusOK, "login", view{Title: "Вход", Data: loginData{}})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	attempt := h.attemptMeta(r)
	attempt.Login, attempt.Password = r.PostFormValue("login"), r.PostFormValue("password")

	result, err := h.opts.Auth.Login(r.Context(), attempt)
	h.finishLogin(w, r, result, err, loginData{Login: attempt.Login})
}

func (h *Handler) loginCode(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	pending := r.PostFormValue("pending")
	result, err := h.opts.Auth.CompleteLogin(r.Context(), pending, r.PostFormValue("code"), h.attemptMeta(r))
	h.finishLogin(w, r, result, err, loginData{Pending: result.Pending})
}

func (h *Handler) finishLogin(w http.ResponseWriter, r *http.Request, result auth.LoginResult, err error, data loginData) {
	switch {
	case err == nil:
		h.setCookie(w, result.Token)
		http.Redirect(w, r, h.opts.Prefix+"/", http.StatusSeeOther)
	case errors.Is(err, auth.ErrCodeRequired):
		h.render(w, r, http.StatusOK, "login", view{Title: "Вход", Data: loginData{Pending: result.Pending}})
	case errors.Is(err, auth.ErrBadCode):
		h.render(w, r, http.StatusUnauthorized, "login", view{Title: "Вход", Error: "Неверный код. Попробуйте ещё раз.", Data: data})
	case errors.Is(err, auth.ErrThrottled):
		h.render(w, r, http.StatusTooManyRequests, "login", view{Title: "Вход", Error: "Слишком много попыток. Подождите 15 минут.", Data: loginData{}})
	case errors.Is(err, auth.ErrBadCredentials):
		h.render(w, r, http.StatusUnauthorized, "login", view{Title: "Вход", Error: "Неверный логин или пароль.", Data: loginData{Login: data.Login}})
	default:
		h.opts.Log.Error("login failed unexpectedly", "error", err)
		h.render(w, r, http.StatusInternalServerError, "login", view{Title: "Вход", Error: "Внутренняя ошибка. Подробности — в журнале сервиса.", Data: loginData{}})
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.opts.Auth.Logout(r.Context(), sessionOf(r)); err != nil {
		h.opts.Log.Error("cannot end the session", "error", err)
	}
	h.clearCookie(w)
	http.Redirect(w, r, h.opts.Prefix+"/login", http.StatusSeeOther)
}

// --- the administrator's own account -------------------------------------------------------------

type accountData struct {
	Sessions   []auth.SessionInfo
	TOTPSecret string // shown while an enrolment is pending
}

func (h *Handler) accountView(r *http.Request, v view) view {
	session := sessionOf(r)
	data := accountData{}
	if sessions, err := h.opts.Auth.Sessions(r.Context(), session); err == nil {
		data.Sessions = sessions
	} else {
		h.opts.Log.Error("cannot list the sessions", "error", err)
	}
	if !session.User.TOTPEnabled {
		if secret, err := h.opts.Auth.PendingTOTP(r.Context(), session); err == nil && len(secret) > 0 {
			data.TOTPSecret = auth.TOTPSecretText(secret)
		}
	}
	v.Title, v.Nav, v.Data = "Аккаунт", "account", data
	return v
}

func (h *Handler) account(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "account", h.accountView(r, view{}))
}

func (h *Handler) accountError(w http.ResponseWriter, r *http.Request, message string) {
	h.render(w, r, http.StatusBadRequest, "account", h.accountView(r, view{Error: message}))
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	next := r.PostFormValue("new")
	if next != r.PostFormValue("repeat") {
		h.accountError(w, r, "Новый пароль и повтор не совпадают.")
		return
	}
	switch err := h.opts.Auth.ChangePassword(r.Context(), sessionOf(r), r.PostFormValue("current"), next); {
	case err == nil:
		http.Redirect(w, r, h.opts.Prefix+"/account?ok=password", http.StatusSeeOther)
	case errors.Is(err, auth.ErrBadCredentials):
		h.accountError(w, r, "Текущий пароль введён неверно.")
	default:
		h.accountError(w, r, "Пароль не изменён: нужно не меньше 12 символов.")
	}
}

func (h *Handler) totpBegin(w http.ResponseWriter, r *http.Request) {
	if _, err := h.opts.Auth.BeginTOTP(r.Context(), sessionOf(r)); err != nil {
		h.accountError(w, r, "Двухфакторная аутентификация уже включена.")
		return
	}
	http.Redirect(w, r, h.opts.Prefix+"/account#totp", http.StatusSeeOther)
}

func (h *Handler) totpConfirm(w http.ResponseWriter, r *http.Request) {
	if err := h.opts.Auth.ConfirmTOTP(r.Context(), sessionOf(r), r.PostFormValue("code")); err != nil {
		h.accountError(w, r, "Код не подошёл. Проверьте время на телефоне и попробуйте следующий код.")
		return
	}
	http.Redirect(w, r, h.opts.Prefix+"/account?ok=totp-on", http.StatusSeeOther)
}

func (h *Handler) totpDisable(w http.ResponseWriter, r *http.Request) {
	if err := h.opts.Auth.DisableTOTP(r.Context(), sessionOf(r), r.PostFormValue("password")); err != nil {
		h.accountError(w, r, "Пароль введён неверно — двухфакторная аутентификация осталась включённой.")
		return
	}
	http.Redirect(w, r, h.opts.Prefix+"/account?ok=totp-off", http.StatusSeeOther)
}

// totpQR draws the enrolment QR code on the server: nothing about the secret ever reaches a
// third-party «QR generator».
func (h *Handler) totpQR(w http.ResponseWriter, r *http.Request) {
	session := sessionOf(r)
	secret, err := h.opts.Auth.PendingTOTP(r.Context(), session)
	if err != nil || len(secret) == 0 {
		http.NotFound(w, r)
		return
	}
	code, err := qr.Encode(auth.TOTPURI(secret, h.opts.SiteHost, session.User.Login), qr.M)
	if err != nil {
		http.Error(w, "cannot draw the code", http.StatusInternalServerError)
		return
	}
	code.Scale = 6
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, code.Image()); err != nil {
		http.Error(w, "cannot draw the code", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(buffer.Bytes())
}

func (h *Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	if err := h.opts.Auth.Revoke(r.Context(), sessionOf(r), r.PostFormValue("id")); err != nil {
		h.accountError(w, r, "Такого сеанса нет.")
		return
	}
	http.Redirect(w, r, h.opts.Prefix+"/account?ok=revoked", http.StatusSeeOther)
}
