package clients

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DenisHumen/krokosha-site/api/internal/achievements"
	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/loyalty"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
)

// The API of the personal account: JSON in, JSON out, for the account's page of the site
// (web/src/scripts/account.ts). A request that changes something must come from the site's own
// pages (Origin, Sec-Fetch-Site), as JSON (a form of another site cannot send it without asking),
// and — once signed in — with the session's CSRF token in a header. The cookies are __Host- and
// SameSite=Strict.

const maxBody = 32 << 10 // the longest thing a client sends is an inquiry: 4000 characters

// Handler serves /api/account/*.
type Handler struct {
	s *Service
	// Rules returns the loyalty rules in force (content/site.yaml → loyalty).
	rules func() config.Loyalty
}

// NewHandler builds the API on top of the service.
func NewHandler(s *Service, rules func() config.Loyalty) *Handler {
	if rules == nil {
		rules = func() config.Loyalty { return config.Loyalty{} }
	}
	return &Handler{s: s, rules: rules}
}

// Register adds the routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/account/login", h.public(h.startLogin))
	mux.HandleFunc("POST /api/account/login/code", h.public(h.verifyCode))
	mux.HandleFunc("POST /api/account/login/link", h.public(h.verifyLink))
	mux.HandleFunc("GET /api/account/me", h.whoever(h.me))
	mux.HandleFunc("POST /api/account/logout", h.private(h.logout))
	mux.HandleFunc("POST /api/account/profile", h.private(h.profile))
	mux.HandleFunc("POST /api/account/contacts", h.private(h.addContact))
	mux.HandleFunc("POST /api/account/contacts/remove", h.private(h.removeContact))
	mux.HandleFunc("POST /api/account/email", h.private(h.addEmail))
	mux.HandleFunc("POST /api/account/telegram", h.private(h.addTelegram))
	mux.HandleFunc("POST /api/account/telegram/unlink", h.private(h.unlinkTelegram))
	mux.HandleFunc("POST /api/account/sessions/end", h.private(h.endSessions))
	mux.HandleFunc("GET /api/account/leads", h.private(h.leadList))
	mux.HandleFunc("GET /api/account/leads/{number}", h.private(h.leadView))
	mux.HandleFunc("POST /api/account/leads/{number}/messages", h.private(h.leadMessage))
	mux.HandleFunc("POST /api/account/inquiries", h.private(h.inquiry))
	mux.HandleFunc("POST /api/account/eggs", h.private(h.eggs))
	mux.HandleFunc("POST /api/account/achievements/seen", h.private(h.achievementsSeen))
	mux.HandleFunc("POST /api/account/delete", h.private(h.deleteAccount))
}

type sessionKey struct{}

func sessionOf(r *http.Request) *Session {
	session, _ := r.Context().Value(sessionKey{}).(*Session)
	return session
}

func fail(w http.ResponseWriter, status int, code string) {
	server.WriteJSON(w, status, map[string]any{"ok": false, "error": code})
}

// fromSite refuses what another site's page sends: the browser tells where a request comes from.
func fromSite(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		if parsed, err := url.Parse(origin); err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			return false
		}
	}
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin"
}

// guard is what every route checks: the site's own page, JSON for anything that changes, no cache.
func guard(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if !fromSite(r) {
		fail(w, http.StatusForbidden, "cross_origin")
		return false
	}
	if r.Method == http.MethodPost {
		if media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); media != "application/json" {
			fail(w, http.StatusUnsupportedMediaType, "json_required")
			return false
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	}
	return true
}

func (h *Handler) public(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if guard(w, r) {
			next(w, r)
		}
	}
}

func (h *Handler) private(next http.HandlerFunc) http.HandlerFunc {
	return h.signedIn(next, http.StatusUnauthorized)
}

// whoever is private for the one question the account's page asks on every visit — who is signed
// in: nobody is an answer, not an error, so it comes with 200 and leaves the console of every
// signed-out visitor clean.
func (h *Handler) whoever(next http.HandlerFunc) http.HandlerFunc {
	return h.signedIn(next, http.StatusOK)
}

func (h *Handler) signedIn(next http.HandlerFunc, signedOut int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		session, err := h.session(r)
		if err != nil && !errors.Is(err, ErrNoSession) {
			// The database did not answer: the session may be fine, and stays.
			h.s.opts.Log.Error("account: cannot check a session", "error", err)
			fail(w, http.StatusServiceUnavailable, "server_error")
			return
		}
		if err != nil {
			clearCookie(w, SessionCookie)
			fail(w, signedOut, "signed_out")
			return
		}
		if r.Method == http.MethodPost && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRFToken)) != 1 {
			fail(w, http.StatusForbidden, "csrf")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, session)))
	}
}

func (h *Handler) session(r *http.Request) (*Session, error) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, ErrNoSession
	}
	return h.s.Authenticate(r.Context(), cookie.Value)
}

// Account is what the contact form asks: whose browser sends it. 0 — nobody is signed in.
func (h *Handler) Account(r *http.Request) (clientID int64, everyEgg bool) {
	session, err := h.session(r)
	if err != nil {
		return 0, false
	}
	everyEgg, err = h.s.HasEveryEgg(r.Context(), session.Client.ID)
	if err != nil {
		h.s.opts.Log.Warn("account: cannot read the eggs of an account", "error", err)
	}
	return session.Client.ID, everyEgg
}

func setCookie(w http.ResponseWriter, name, value string, lifetime time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: int(lifetime.Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// read decodes the JSON body; a malformed one is the caller's 400.
func read(r *http.Request, into any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}

// attempt describes who asks: for the rate limits and for the list of sessions.
func (h *Handler) attempt(r *http.Request, lang string) Attempt {
	client := analytics.ParseUserAgent(r.UserAgent())
	a := Attempt{Lang: lang, Network: "unknown", Device: client.Browser + " · " + client.OS}
	if ip := server.ClientIP(r.Context()); ip != nil {
		a.Network = analytics.TruncateIP(ip)
		a.Key = h.s.keyOf(LimitKey(ip))
	}
	return a
}

// --- signing in ------------------------------------------------------------------------------------

func (h *Handler) startLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Method string `json:"method"`
		Email  string `json:"email"`
		Lang   string `json:"lang"`
	}
	if !read(r, &body) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	var started Started
	var err error
	switch body.Method {
	case MethodEmail:
		started, err = h.s.StartEmail(r.Context(), body.Email, h.attempt(r, body.Lang), 0)
	case MethodTelegram:
		started, err = h.s.StartTelegram(r.Context(), h.attempt(r, body.Lang), 0)
	default:
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	h.answerStart(w, started, err)
}

func (h *Handler) answerStart(w http.ResponseWriter, started Started, err error) {
	switch {
	case errors.Is(err, ErrBadContact):
		fail(w, http.StatusUnprocessableEntity, "bad_email")
	case errors.Is(err, ErrThrottled):
		fail(w, http.StatusTooManyRequests, "throttled")
	case errors.Is(err, ErrNoBot):
		fail(w, http.StatusServiceUnavailable, "no_bot")
	case err != nil:
		h.s.opts.Log.Error("account: cannot start a login", "error", err)
		fail(w, http.StatusInternalServerError, "server_error")
	default:
		setCookie(w, loginCookie, started.Browser, LoginLifetime)
		answer := map[string]any{"ok": true}
		if started.SentTo != "" {
			answer["sent_to"] = started.SentTo
		}
		if started.BotURL != "" {
			answer["bot_url"] = started.BotURL
		}
		server.WriteJSON(w, http.StatusOK, answer)
	}
}

func (h *Handler) verifyCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !read(r, &body) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	browser := ""
	if cookie, err := r.Cookie(loginCookie); err == nil {
		browser = cookie.Value
	}
	result, err := h.s.VerifyCode(r.Context(), browser, body.Code, h.attempt(r, ""))
	h.answerVerified(w, result, err)
}

func (h *Handler) verifyLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		// Peek: only say whose account the link opens — the page asks the visitor before it signs in.
		Peek bool `json:"peek"`
	}
	if !read(r, &body) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if body.Peek {
		account, err := h.s.PeekLink(r.Context(), body.Token, h.attempt(r, ""))
		if err != nil {
			h.answerVerified(w, Result{}, err)
			return
		}
		server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "account": account})
		return
	}
	result, err := h.s.VerifyLink(r.Context(), body.Token, h.attempt(r, ""))
	h.answerVerified(w, result, err)
}

func (h *Handler) answerVerified(w http.ResponseWriter, result Result, err error) {
	switch {
	case errors.Is(err, ErrBadCode):
		fail(w, http.StatusUnprocessableEntity, "bad_code")
	case errors.Is(err, ErrThrottled):
		fail(w, http.StatusTooManyRequests, "throttled")
	case errors.Is(err, ErrTaken):
		fail(w, http.StatusConflict, "taken")
	case errors.Is(err, ErrDisabled):
		fail(w, http.StatusForbidden, "disabled")
	case err != nil:
		h.s.opts.Log.Error("account: cannot finish a login", "error", err)
		fail(w, http.StatusInternalServerError, "server_error")
	default:
		clearCookie(w, loginCookie)
		if result.Token != "" {
			setCookie(w, SessionCookie, result.Token, SessionLifetime)
		}
		server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "lang": result.Lang, "created": result.Created, "linked": result.Linked})
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Logout(r.Context(), sessionOf(r)); err != nil {
		h.s.opts.Log.Error("account: cannot end a session", "error", err)
	}
	clearCookie(w, SessionCookie)
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- the account ---------------------------------------------------------------------------------

type offerJSON struct {
	Percent int    `json:"percent"`
	Reason  string `json:"reason,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

func offerOf(offer loyalty.Offer) offerJSON {
	out := offerJSON{Percent: offer.Percent, Reason: offer.Reason}
	// The tier's id, the owner's note to the client; never anything else.
	if offer.Reason == loyalty.ReasonTier || offer.Reason == loyalty.ReasonPersonal || offer.Reason == loyalty.ReasonManual {
		out.Detail = offer.Detail
	}
	return out
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session := sessionOf(r)
	client := session.Client
	// Whatever the orders have earned since — a request linked by a new address, say — is given now.
	if err := h.s.AwardOrders(ctx, client.ID); err != nil {
		h.s.opts.Log.Warn("account: cannot award the achievements of orders", "error", err)
	}
	orders, err := h.s.Orders(ctx, client.ID)
	if err != nil {
		h.internal(w, "cannot read the achievements of orders", err)
		return
	}
	shares, err := h.s.OrderShares(ctx)
	if err != nil {
		h.internal(w, "cannot read the shares of the achievements", err)
		return
	}
	contacts, err := h.s.Contacts(ctx, client.ID)
	if err != nil {
		h.internal(w, "cannot read the contacts", err)
		return
	}
	eggs, err := h.s.Eggs(ctx, client.ID)
	if err != nil {
		h.internal(w, "cannot read the eggs", err)
		return
	}
	sessions, err := h.s.Sessions(ctx, client.ID, session)
	if err != nil {
		h.internal(w, "cannot list the sessions", err)
		return
	}
	history, personal, err := h.s.opts.Leads.History(ctx, client.ID)
	if err != nil {
		h.internal(w, "cannot read the history", err)
		return
	}
	// Every egg in the account: the next request gets the eggs' discount if it is the largest.
	offer, err := h.s.opts.Leads.Preview(ctx, client.ID, len(eggs) == len(achievements.Eggs))
	if err != nil {
		h.internal(w, "cannot price the next request", err)
		return
	}

	rules := h.rules()
	loyaltyJSON := map[string]any{
		"enabled": rules.Enabled, "currency": rules.Currency, "orders": history.Orders, "spent": history.Spent,
		"offer": offerOf(offer), "eggs_used": history.EggsUsed, "welcome_used": history.Earlier > 0 || history.Orders > 0,
	}
	if tier, ok := loyalty.TierOf(rules, history); ok {
		loyaltyJSON["tier"] = tier.ID
	}
	if next, ok := loyalty.NextTier(rules, history); ok {
		loyaltyJSON["next"] = map[string]any{"tier": next.Tier.ID, "orders": next.Orders, "spent": next.Spent, "progress": next.Progress}
	}
	if personal.Valid(h.today()) {
		value := map[string]any{"percent": personal.Percent, "note": personal.Note, "once": personal.Once}
		if !personal.Until.IsZero() {
			value["until"] = personal.Until.Format(time.DateOnly)
		}
		loyaltyJSON["personal"] = value
	}
	telegram := ""
	if client.TelegramID != 0 {
		telegram = "@" + client.TelegramUsername
		if client.TelegramUsername == "" {
			telegram = "Telegram"
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"csrf": session.CSRFToken,
		"client": map[string]any{
			"id": client.ID, "name": client.Name, "company": client.Company, "lang": client.Lang, "email": client.Email,
			"telegram": telegram, "preferred": client.Preferred, "since": client.CreatedAt,
		},
		"contacts": nonNil(contacts),
		"loyalty":  loyaltyJSON,
		"eggs":     nonNil(eggs),
		// The achievements of orders: what the account has (new — not shown to the client yet), how
		// many of the accounts have each, and the sum a big order starts with.
		"orders":   map[string]any{"earned": nonNil(orders), "shares": shares, "big_order": rules.BigOrder},
		"sessions": nonNil(sessions),
		"bot":      h.s.opts.BotUsername() != "",
	})
}

func nonNil[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

func (h *Handler) today() time.Time {
	now := h.s.opts.Now().In(h.s.opts.Location)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func (h *Handler) internal(w http.ResponseWriter, what string, err error) {
	h.s.opts.Log.Error("account: "+what, "error", err)
	fail(w, http.StatusInternalServerError, "server_error")
}

func (h *Handler) profile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Company   string `json:"company"`
		Lang      string `json:"lang"`
		Preferred string `json:"preferred"`
	}
	if !read(r, &body) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := h.s.SetProfile(r.Context(), sessionOf(r).Client.ID, Profile(body)); err != nil {
		h.internal(w, "cannot save a profile", err)
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) addContact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if !read(r, &body) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	contact, err := h.s.AddContact(r.Context(), sessionOf(r).Client.ID, body.Kind, body.Value)
	switch {
	case errors.Is(err, ErrBadContact):
		fail(w, http.StatusUnprocessableEntity, "bad_contact")
	case errors.Is(err, ErrTooMany):
		fail(w, http.StatusUnprocessableEntity, "too_many")
	case err != nil:
		h.internal(w, "cannot add a contact", err)
	default:
		server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "contact": contact})
	}
}

func (h *Handler) removeContact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID int64 `json:"id"`
	}
	if !read(r, &body) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	switch err := h.s.RemoveContact(r.Context(), sessionOf(r).Client.ID, body.ID); {
	case errors.Is(err, ErrNotFound):
		fail(w, http.StatusNotFound, "not_found")
	case err != nil:
		h.internal(w, "cannot remove a contact", err)
	default:
		server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (h *Handler) addEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if !read(r, &body) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	client := sessionOf(r).Client
	started, err := h.s.StartEmail(r.Context(), body.Email, h.attempt(r, client.Lang), client.ID)
	h.answerStart(w, started, err)
}

func (h *Handler) addTelegram(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !read(r, &body) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	client := sessionOf(r).Client
	started, err := h.s.StartTelegram(r.Context(), h.attempt(r, client.Lang), client.ID)
	h.answerStart(w, started, err)
}

func (h *Handler) unlinkTelegram(w http.ResponseWriter, r *http.Request) {
	client := sessionOf(r).Client
	if client.Email == "" {
		fail(w, http.StatusConflict, "last_way") // without Telegram there would be no way in
		return
	}
	if err := h.s.UnlinkTelegram(r.Context(), client.ID); err != nil {
		h.internal(w, "cannot unlink Telegram", err)
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) endSessions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"` // "" — every session but this one
	}
	if !read(r, &body) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	session := sessionOf(r)
	var err error
	if body.ID == "" {
		_, err = h.s.EndSessions(r.Context(), session.Client.ID, session)
	} else {
		err = h.s.EndSession(r.Context(), session.Client.ID, body.ID)
	}
	switch {
	case errors.Is(err, ErrNotFound):
		fail(w, http.StatusNotFound, "not_found")
	case err != nil:
		h.internal(w, "cannot end sessions", err)
	default:
		server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// --- requests and inquiries --------------------------------------------------------------------------

type summaryJSON struct {
	leads.ClientSummary
	Discount offerJSON `json:"discount"`
}

func (h *Handler) leadList(w http.ResponseWriter, r *http.Request) {
	list, err := h.s.opts.Leads.ClientLeads(r.Context(), sessionOf(r).Client.ID)
	if err != nil {
		h.internal(w, "cannot list requests", err)
		return
	}
	out := make([]summaryJSON, 0, len(list))
	for _, item := range list {
		out = append(out, summaryJSON{ClientSummary: item, Discount: offerOf(item.Discount)})
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "leads": out})
}

// number reads «K-0042» (or «42») from the path.
func number(r *http.Request) (int64, bool) {
	text := strings.TrimPrefix(strings.ToUpper(r.PathValue("number")), "K-")
	id, err := strconv.ParseInt(text, 10, 64)
	return id, err == nil && id > 0
}

func (h *Handler) leadView(w http.ResponseWriter, r *http.Request) {
	id, ok := number(r)
	if !ok {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	client := sessionOf(r).Client
	view, err := h.s.opts.Leads.ClientView(r.Context(), client.ID, id)
	switch {
	case errors.Is(err, leads.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found")
		return
	case err != nil:
		h.internal(w, "cannot read a request", err)
		return
	}
	if err := h.s.opts.Leads.MarkSeen(r.Context(), client.ID, id); err != nil {
		h.s.opts.Log.Warn("account: cannot mark a request as seen", "error", err)
	}
	if view.Feed == nil {
		view.Feed = []leads.ClientEntry{}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "lead": struct {
		*leads.ClientView
		Discount offerJSON `json:"discount"`
	}{view, offerOf(view.Discount)}})
}

func (h *Handler) leadMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := number(r)
	var body struct {
		Text string `json:"text"`
	}
	if !ok || !read(r, &body) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if utf8.RuneCountInString(body.Text) > leads.MaxDescription {
		fail(w, http.StatusUnprocessableEntity, "too_long")
		return
	}
	client := sessionOf(r).Client
	if !h.s.allow(r.Context(), "message", strconv.FormatInt(client.ID, 10), 30) {
		fail(w, http.StatusTooManyRequests, "throttled")
		return
	}
	switch _, err := h.s.opts.Leads.ClientPost(r.Context(), client.ID, id, body.Text); {
	case errors.Is(err, leads.ErrNotFound):
		fail(w, http.StatusNotFound, "not_found")
	case errors.Is(err, leads.ErrEmptyText):
		fail(w, http.StatusUnprocessableEntity, "empty")
	case err != nil:
		h.internal(w, "cannot store a message", err)
	default:
		h.s.opts.Kick()
		server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// inquiry is a question written in the account: it becomes a request of its own kind, with the same
// statuses, the same conversation and the same notifications to the staff — but no discount, as it
// is not an order.
func (h *Handler) inquiry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Subject string `json:"subject"`
		Text    string `json:"text"`
		Parent  string `json:"parent"`
	}
	if !read(r, &body) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	ctx := r.Context()
	client := sessionOf(r).Client
	text := leads.Clean(body.Text, true)
	switch length := utf8.RuneCountInString(text); {
	case length < 2:
		fail(w, http.StatusUnprocessableEntity, "empty")
		return
	case length > leads.MaxDescription:
		fail(w, http.StatusUnprocessableEntity, "too_long")
		return
	}
	if !h.s.allow(ctx, "inquiry", strconv.FormatInt(client.ID, 10), 5) {
		fail(w, http.StatusTooManyRequests, "throttled")
		return
	}
	sub := leads.Submission{
		Kind: leads.KindInquiry, ClientID: client.ID, Trusted: true, Subject: cut(leads.Clean(body.Subject, false), 150),
		Name: client.Name, Description: text, Lang: client.Lang, Direction: "other",
	}
	if sub.Name == "" {
		sub.Name = "—"
	}
	if body.Parent != "" {
		parentID, _ := strconv.ParseInt(strings.TrimPrefix(strings.ToUpper(body.Parent), "K-"), 10, 64)
		parent, err := h.s.opts.Leads.ClientView(ctx, client.ID, parentID)
		if err != nil {
			fail(w, http.StatusUnprocessableEntity, "bad_parent")
			return
		}
		sub.ParentID, sub.Direction = parentID, parent.Direction
	}
	// The way back: the address the account proved, else its Telegram.
	switch {
	case client.Email != "" && (client.Preferred != MethodTelegram || client.TelegramID == 0):
		sub.ContactMethod, sub.ContactValue = leads.MethodEmail, client.Email
	case client.TelegramID != 0:
		sub.ContactMethod = leads.MethodTelegram
		if client.TelegramUsername != "" {
			sub.ContactValue = "@" + client.TelegramUsername
		}
	default:
		fail(w, http.StatusConflict, "no_way_back")
		return
	}
	prefix := "unknown"
	if ip := server.ClientIP(ctx); ip != nil {
		prefix = analytics.TruncateIP(ip)
	}
	lead, err := h.s.opts.Leads.Create(ctx, sub, leads.Verdict{}, analytics.SessionSummary{}, prefix)
	if err != nil {
		h.internal(w, "cannot store an inquiry", err)
		return
	}
	if sub.ContactMethod == leads.MethodTelegram {
		// The client's Telegram is proven: what they write to the bot joins this inquiry, and answers go there.
		if _, err := h.s.opts.DB.ExecContext(ctx, `INSERT IGNORE INTO bot_clients (lead_id, telegram_id, linked_at) VALUES (?, ?, ?)`,
			lead.ID, client.TelegramID, h.s.now()); err != nil {
			h.s.opts.Log.Warn("account: cannot link an inquiry to the client's Telegram", "error", err)
		}
	}
	h.s.opts.Kick()
	server.WriteJSON(w, http.StatusCreated, map[string]any{"ok": true, "number": lead.Number()})
}

func (h *Handler) eggs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Receipts []string `json:"receipts"`
	}
	if !read(r, &body) || len(body.Receipts) > len(achievements.Eggs)+1 {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	found, err := h.s.SyncEggs(r.Context(), sessionOf(r).Client.ID, body.Receipts)
	if err != nil {
		h.internal(w, "cannot keep the eggs", err)
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "eggs": nonNil(found)})
}

// achievementsSeen: the page has shown the banners of these achievements; they are no longer new.
func (h *Handler) achievementsSeen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !read(r, &body) || len(body.IDs) > len(OrderAchievements) {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	if err := h.s.MarkShown(r.Context(), sessionOf(r).Client.ID, body.IDs); err != nil {
		h.internal(w, "cannot mark achievements as seen", err)
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// deleteAccount removes the account itself. The requests stay with the owner — they have their own
// storage period, and deleting them is a request to the owner, as the privacy policy says.
func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if !read(r, &body) || !body.Confirm {
		fail(w, http.StatusBadRequest, "bad_request")
		return
	}
	client := sessionOf(r).Client
	if err := h.s.Delete(r.Context(), client.ID); err != nil {
		h.internal(w, "cannot delete an account", err)
		return
	}
	h.s.opts.Log.Info("account: a client deleted their account", "client", client.ID)
	clearCookie(w, SessionCookie)
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
