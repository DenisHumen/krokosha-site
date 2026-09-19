package leads

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
)

const (
	maxFormBytes  = 48 << 10 // the description is at most 4000 characters, 16 KB in the worst case
	leadsPerHour  = 3        // per address (brief B10.1)
	challengesMin = 30       // challenges per address and minute: a form asks for one
)

// Sessions tells what is known about the visit a request comes from (analytics.Service).
type Sessions interface {
	CurrentSession(ctx context.Context, ip net.IP, userAgent string) (analytics.SessionSummary, error)
}

// Options configure the public side of the requests.
type Options struct {
	Store    *Store
	Cache    *cache.Cache
	Sessions Sessions
	Log      *slog.Logger
	Secret   []byte // signs proof-of-work challenges
	// Form returns the current settings of the form (content/site.yaml is re-read by the caller
	// when it changes).
	Form func() config.Form
	// WWWDir is where the built site lives: the «thank you» page of the release is filled in
	// and served to visitors who sent the form without JavaScript.
	WWWDir string
	// TelegramURL returns the «continue in Telegram» link for a request, or "" without a bot.
	TelegramURL func(lead *Lead) string
	// OnCreated is called after a request was stored: the outbox worker is told to hurry.
	OnCreated func(lead *Lead)
	Now       func() time.Time
}

// Handler is the public API of the contact form.
type Handler struct {
	opts Options
}

// NewHandler builds the handler.
func NewHandler(opts Options) *Handler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.TelegramURL == nil {
		opts.TelegramURL = func(*Lead) string { return "" }
	}
	if opts.OnCreated == nil {
		opts.OnCreated = func(*Lead) {}
	}
	return &Handler{opts: opts}
}

// Register adds the routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/leads/challenge", h.challenge)
	mux.HandleFunc("POST /api/leads", h.submit)
	mux.HandleFunc("GET /api/leads/thanks", h.thanks)
}

// addressKey keeps addresses out of the cache: the counters are keyed by a keyed hash.
func (h *Handler) addressKey(r *http.Request) string {
	mac := hmac.New(sha256.New, h.opts.Secret)
	if ip := server.ClientIP(r.Context()); ip != nil {
		mac.Write(ip)
	}
	return hex.EncodeToString(mac.Sum(nil)[:8])
}

func (h *Handler) challenge(w http.ResponseWriter, r *http.Request) {
	if !h.opts.Cache.Allow(r.Context(), "lead-challenge:"+h.addressKey(r), challengesMin, time.Minute) {
		server.WriteJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "rate_limited"})
		return
	}
	challenge, err := NewChallenge(h.opts.Secret, h.opts.Now())
	if err != nil {
		h.opts.Log.Error("cannot make a proof-of-work challenge", "error", err)
		server.WriteJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "server_error"})
		return
	}
	server.WriteJSON(w, http.StatusOK, challenge)
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	wantsJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	lang := "en" // until the form says otherwise
	fail := func(status int, code string, fields FieldErrors) {
		if wantsJSON {
			body := map[string]any{"ok": false, "error": code}
			if fields != nil {
				body["errors"] = fields
			}
			server.WriteJSON(w, status, body)
			return
		}
		// Without JavaScript: back to the form, where a block waiting for this very anchor explains
		// what went wrong (the site shows it with :target, no script needed).
		anchor := map[string]string{"rate_limited": "form-error-rate", "invalid": "form-error-invalid"}[code]
		if anchor == "" {
			anchor = "form-error-server"
		}
		// Not an open redirect: the prefix is one of three known ones, the anchor one of three above.
		http.Redirect(w, r, langPrefix(lang)+"#"+anchor, http.StatusSeeOther) //nolint:gosec // see the comment
	}

	form := h.opts.Form()
	if !form.Enabled {
		fail(http.StatusServiceUnavailable, "disabled", nil)
		return
	}
	// Browsers say where a form was sent from; other sites have no business posting here.
	if origin := r.Header.Get("Origin"); origin != "" {
		if parsed, err := url.Parse(origin); err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			server.WriteJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "cross-origin request"})
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	var parseErr error
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType == "multipart/form-data" {
		parseErr = r.ParseMultipartForm(maxFormBytes) //nolint:gosec // the body is capped by MaxBytesReader above
	} else {
		parseErr = r.ParseForm()
	}
	if parseErr != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(parseErr, &tooLarge) {
			fail(http.StatusRequestEntityTooLarge, "invalid", nil)
			return
		}
		fail(http.StatusBadRequest, "invalid", nil)
		return
	}
	if l := r.PostFormValue("lang"); languages[l] {
		lang = l
	}

	sub, fieldErrors := Parse(r.PostFormValue, form)
	if fieldErrors != nil {
		fail(http.StatusUnprocessableEntity, "invalid", fieldErrors)
		return
	}
	// Counted only now: a typo in the email must not use up the three requests an hour.
	if !h.opts.Cache.Allow(r.Context(), "lead:"+h.addressKey(r), leadsPerHour, time.Hour) {
		fail(http.StatusTooManyRequests, "rate_limited", nil)
		return
	}

	now := h.opts.Now()
	signals := Signals{}
	switch proof, err := VerifyProof(h.opts.Secret, sub.Proof, now); {
	case err == nil:
		// One proof, one request: a solved challenge cannot be replayed.
		if h.opts.Cache.Allow(r.Context(), "lead-proof:"+proof.Challenge, 1, proof.ExpiresAt.Sub(now)+time.Minute) {
			signals.ProofOK, signals.FillTime = true, now.Sub(proof.IssuedAt)
		} else {
			signals.ProofError = "решение уже использовано"
		}
	case errors.Is(err, ErrNoProof):
	case errors.Is(err, ErrProofExpired):
		signals.ProofError = "срок задачи истёк"
	default:
		signals.ProofError = "неверное решение"
	}
	verdict := Judge(sub, signals)

	ip := server.ClientIP(r.Context())
	session, err := h.opts.Sessions.CurrentSession(r.Context(), ip, r.UserAgent())
	if err != nil {
		// The request matters more than its statistics.
		h.opts.Log.Warn("cannot read the visit of a request", "error", err)
	}
	prefix := "unknown"
	if ip != nil {
		prefix = analytics.TruncateIP(ip)
	}

	lead, err := h.opts.Store.Create(r.Context(), sub, verdict, session, prefix)
	if err != nil {
		h.opts.Log.Error("cannot store a request", "error", err)
		fail(http.StatusInternalServerError, "server_error", nil)
		return
	}
	h.opts.Log.Info("request received", "lead", lead.Number(), "status", lead.Status, "spam_score", verdict.Score)
	h.opts.OnCreated(lead)

	telegram := ""
	if lead.Status != StatusSpam {
		telegram = h.opts.TelegramURL(lead)
	}
	if wantsJSON {
		body := map[string]any{"ok": true, "id": lead.Number()}
		if hours := form.ReplyWithinHours(); hours > 0 {
			body["reply_within_hours"] = hours
		}
		if telegram != "" {
			body["telegram_url"] = telegram
		}
		server.WriteJSON(w, http.StatusCreated, body)
		return
	}
	// Post, redirect, get: reloading the «thank you» page must not send the form again.
	http.Redirect(w, r, "/api/leads/thanks?t="+url.QueryEscape(lead.PublicToken), http.StatusSeeOther)
}

func langPrefix(lang string) string {
	if lang == "uk" || lang == "ru" {
		return "/" + lang + "/"
	}
	return "/"
}

// Marks of the «thank you» page (web/src/pages/[...lang]/thanks.astro). The class marks switch
// its parts: as built, the page shows the plain «request received» and hides the rest, so it is
// presentable even when opened directly.
const (
	placeholderNumber        = "%%LEAD_NUMBER%%"
	placeholderTelegramURL   = "%%TELEGRAM_URL%%"
	placeholderGenericClass  = "%%GENERIC_CLASS%%"  // → is-hidden
	placeholderNumberedClass = "%%NUMBERED_CLASS%%" // → is-shown
	placeholderTelegramClass = "%%TELEGRAM_CLASS%%" // → is-shown, when there is a bot to continue in
)

// thanks is where a form sent without JavaScript ends up. The page is the site's own — built by
// Astro, designed like everything else — with the number of the request filled in. The link
// carries the request's random token, so nobody can leaf through other people's numbers. If
// anything is missing, the visitor gets the same page as it is: still a «thank you».
func (h *Handler) thanks(w http.ResponseWriter, r *http.Request) {
	lead, err := h.opts.Store.ByToken(r.Context(), r.URL.Query().Get("t"))
	if err != nil {
		http.Redirect(w, r, "/thanks/", http.StatusSeeOther)
		return
	}
	location := langPrefix(lead.Lang) + "thanks/"
	page, err := os.ReadFile(filepath.Join(h.opts.WWWDir, "current", filepath.FromSlash(strings.TrimPrefix(location, "/")), "index.html"))
	if err != nil || !bytes.Contains(page, []byte(placeholderNumber)) {
		if err != nil {
			h.opts.Log.Warn("cannot read the «thank you» page of the release", "error", err)
		}
		http.Redirect(w, r, location, http.StatusSeeOther)
		return
	}
	telegram, telegramClass := "", ""
	if lead.Status != StatusSpam {
		if telegram = h.opts.TelegramURL(lead); telegram != "" {
			telegramClass = "is-shown"
		}
	}
	for mark, value := range map[string]string{
		placeholderNumber:        html.EscapeString(lead.Number()),
		placeholderTelegramURL:   html.EscapeString(telegram),
		placeholderGenericClass:  "is-hidden",
		placeholderNumberedClass: "is-shown",
		placeholderTelegramClass: telegramClass,
	} {
		page = bytes.ReplaceAll(page, []byte(mark), []byte(value))
	}

	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(page)
}
