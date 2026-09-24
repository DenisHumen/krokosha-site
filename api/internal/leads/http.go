package leads

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/achievements"
	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/loyalty"
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

// Accounts tells whose personal account the browser is signed in to (clients.Handler): the
// request joins the account, and gets the account's discount. 0 — nobody is signed in.
type Accounts interface {
	Account(r *http.Request) (clientID int64, everyEgg bool)
}

type noAccounts struct{}

func (noAccounts) Account(*http.Request) (int64, bool) { return 0, false }

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
	// Files is where attachments are written when the form accepts them; nil — never.
	Files *Files
	// WWWDir is where the built site lives: the «thank you» page of the release is filled in
	// and served to visitors who sent the form without JavaScript.
	WWWDir string
	// TelegramURL returns the «continue in Telegram» link for a request, or "" without a bot.
	TelegramURL func(lead *Lead) string
	// OnCreated is called after a request was stored: the outbox worker is told to hurry.
	OnCreated func(lead *Lead)
	// Accounts: whose personal account sends the form; nil — the site has no accounts.
	Accounts Accounts
	Now      func() time.Time
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
	if opts.Accounts == nil {
		opts.Accounts = noAccounts{}
	}
	return &Handler{opts: opts}
}

// Register adds the routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/leads/challenge", h.challenge)
	mux.HandleFunc("POST /api/leads/offer", h.offer)
	mux.HandleFunc("POST /api/leads", h.submit)
	mux.HandleFunc("GET /api/leads/thanks", h.thanks)
	mux.HandleFunc("GET /api/leads/telegram", h.telegram)
}

// TelegramPath is the site's own address that leads on to the bot, followed by the request's
// token: what letters link to instead of the bot itself (see telegram).
const TelegramPath = "/api/leads/telegram?t="

// telegram sends a client who clicked «continue in Telegram» in a letter on to the bot. Letters
// link here, on the site's own domain, and not to t.me: mail filters judge a letter by where its
// links lead, and a young domain whose letters send people to Telegram looks like the spam that
// does exactly that. A request that is gone, is spam, or has no bot to go to brings the visitor
// to the site instead — never anywhere a link could be made to point.
func (h *Handler) telegram(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Robots-Tag", "noindex")
	lead, err := h.opts.Store.ByToken(r.Context(), r.URL.Query().Get("t"))
	if err != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	target := langPrefix(lead.Lang)
	if lead.Status != StatusSpam {
		if bot := h.opts.TelegramURL(lead); bot != "" {
			target = bot
		}
	}
	http.Redirect(w, r, target, http.StatusFound)
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

	// Files make a request big; without them a form is a few kilobytes, and no more is read.
	acceptsFiles := form.Attachments && h.opts.Files != nil
	limit := int64(maxFormBytes)
	if acceptsFiles {
		limit = maxUploadBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	var parseErr error
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType == "multipart/form-data" {
		// Files over a quarter of a megabyte wait in temporary files (the service has a /tmp of its own).
		parseErr = r.ParseMultipartForm(256 << 10) //nolint:gosec // the body is capped by MaxBytesReader above
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()
	} else {
		parseErr = r.ParseForm()
	}
	if parseErr != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(parseErr, &tooLarge) {
			var fields FieldErrors
			if acceptsFiles {
				fields = FieldErrors{"files": ErrFileTooBig.Error()} // nothing else makes a form this big
			}
			fail(http.StatusRequestEntityTooLarge, "invalid", fields)
			return
		}
		fail(http.StatusBadRequest, "invalid", nil)
		return
	}
	if l := r.PostFormValue("lang"); languages[l] {
		lang = l
	}

	sub, fieldErrors := Parse(r.PostFormValue, form)
	// The receipt of every easter egg claims the one-time discount; a receipt that does not verify
	// claims nothing, and nobody is told why.
	if receipt, err := achievements.Verify(h.opts.Secret, strings.TrimSpace(r.PostFormValue("eggs"))); err == nil && receipt.ID == achievements.All {
		sub.EggsReceipt, sub.EggsSpan = strings.TrimSpace(r.PostFormValue("eggs")), receipt.Span
	}
	// A signed-in client: the request joins the account.
	if clientID, everyEgg := h.opts.Accounts.Account(r); clientID > 0 {
		sub.ClientID, sub.Trusted, sub.EggsByAccount = clientID, true, everyEgg
	}
	files, fileErr := incomingFiles(r, acceptsFiles)
	if fileErr != nil {
		if fieldErrors == nil {
			fieldErrors = FieldErrors{}
		}
		fieldErrors["files"] = fileErr.Error()
	}
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

	// A robot's files are not worth the disk; everybody else's are written now, and belong to
	// nobody until the request is stored.
	if verdict.IsSpam() {
		sub.FilesDropped = len(files)
	} else if sub.Files, err = h.saveFiles(files); err != nil {
		h.opts.Log.Error("cannot store the files of a request", "error", err)
		fail(http.StatusInternalServerError, "server_error", nil)
		return
	}
	lead, err := h.opts.Store.Create(r.Context(), sub, verdict, session, prefix)
	if err != nil {
		h.discard(sub.Files)
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
		if discount, ok := shownDiscount(lead); ok {
			body["discount"] = discount
		}
		server.WriteJSON(w, http.StatusCreated, body)
		return
	}
	// Post, redirect, get: reloading the «thank you» page must not send the form again.
	http.Redirect(w, r, "/api/leads/thanks?t="+url.QueryEscape(lead.PublicToken), http.StatusSeeOther)
}

// shownDiscount is what the sender is told of the discount their request got. A signed-in client
// learns everything; somebody else only what their own request proves — the first request, the
// eggs — and not a level or a personal discount of whoever's address they may have typed
// (loyalty.Public). Spam learns nothing.
func shownDiscount(lead *Lead) (map[string]any, bool) {
	offer := lead.Discount
	// Trusted is a sign-in; an account found by the typed address is not one.
	if lead.Status == StatusSpam || offer.Percent <= 0 || (!lead.Trusted && !loyalty.Public(offer.Reason)) {
		return nil, false
	}
	shown := map[string]any{"percent": offer.Percent, "reason": offer.Reason}
	if offer.Reason == loyalty.ReasonTier || offer.Reason == loyalty.ReasonPersonal || offer.Reason == loyalty.ReasonManual {
		shown["detail"] = offer.Detail
	}
	return shown, true
}

// offer tells the form what discount a request would get, before it is sent: the account's own for
// a signed-in client, else what a first request gets, with the eggs if the browser has their
// receipt. Nothing is spent; the request itself decides for good.
func (h *Handler) offer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if origin := r.Header.Get("Origin"); origin != "" {
		if parsed, err := url.Parse(origin); err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			server.WriteJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "cross-origin request"})
			return
		}
	}
	var body struct {
		Eggs string `json:"eggs"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_request"})
		return
	}
	ctx := r.Context()
	eggs := false
	if receipt, err := achievements.Verify(h.opts.Secret, body.Eggs); err == nil && receipt.ID == achievements.All {
		used, err := h.opts.Store.EggsReceiptUsed(ctx, body.Eggs)
		if err != nil {
			h.opts.Log.Error("cannot check a receipt of the eggs", "error", err)
		}
		eggs = err == nil && !used
	}
	rules := h.opts.Store.Rules()
	answer := map[string]any{"ok": true, "enabled": rules.Enabled, "signed_in": false}
	var offer loyalty.Offer
	if clientID, everyEgg := h.opts.Accounts.Account(r); clientID > 0 {
		var err error
		if offer, err = h.opts.Store.Preview(ctx, clientID, eggs || everyEgg); err != nil {
			h.opts.Log.Error("cannot price a request", "error", err)
			server.WriteJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "server_error"})
			return
		}
		answer["signed_in"] = true
	} else {
		offer = loyalty.Decide(rules, loyalty.History{}, loyalty.Claim{Eggs: eggs}, loyalty.Personal{}, h.opts.Store.today())
	}
	answer["percent"], answer["reason"] = offer.Percent, offer.Reason
	if offer.Reason == loyalty.ReasonTier || offer.Reason == loyalty.ReasonPersonal {
		answer["detail"] = offer.Detail
	}
	server.WriteJSON(w, http.StatusOK, answer)
}

// incomingFile is a file of the form that passed the inspection.
type incomingFile struct {
	header *multipart.FileHeader
	kind   string
}

// incomingFiles checks what came in the «files» field: how many, how big, and whether each is
// what its name says (Inspect). Nothing is written yet — a request may still turn out to be
// one too many this hour, or a robot's.
func incomingFiles(r *http.Request, accepted bool) ([]incomingFile, error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	var files []incomingFile
	for _, header := range r.MultipartForm.File["files"] {
		if header.Filename == "" && header.Size == 0 {
			continue // a file field nobody touched: browsers send it empty
		}
		files = append(files, incomingFile{header: header})
	}
	switch {
	case len(files) == 0:
		return nil, nil
	case !accepted:
		return nil, ErrNoFilesHere
	case len(files) > MaxAttachments:
		return nil, ErrTooManyFiles
	}
	for i, file := range files {
		content, err := file.header.Open()
		if err != nil {
			return nil, ErrFileType
		}
		kind, err := Inspect(file.header.Filename, file.header.Size, content)
		_ = content.Close()
		switch {
		case errors.Is(err, ErrFileTooBig), errors.Is(err, ErrFileType):
			return nil, err
		case err != nil:
			return nil, ErrFileType // unreadable is as good as unacceptable
		}
		files[i].kind = kind
	}
	return files, nil
}

// saveFiles writes inspected files to the attachments directory — all of them or none.
func (h *Handler) saveFiles(files []incomingFile) ([]Upload, error) {
	var saved []Upload
	for _, file := range files {
		content, err := file.header.Open()
		if err != nil {
			h.discard(saved)
			return nil, err
		}
		upload, err := h.opts.Files.Save(file.header.Filename, file.kind, content)
		_ = content.Close()
		if err != nil {
			h.discard(saved)
			return nil, err
		}
		saved = append(saved, upload)
	}
	return saved, nil
}

// discard removes files whose request was not stored after all.
func (h *Handler) discard(files []Upload) {
	for _, file := range files {
		if err := h.opts.Files.Remove(file.StoredAs); err != nil {
			h.opts.Log.Warn("cannot remove a file of a request that was not stored; the daily sweep will", "error", err)
		}
	}
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
	placeholderDiscountClass = "%%DISCOUNT_CLASS%%" // → is-shown, when the sender may be told of a discount
	placeholderDiscount      = "%%DISCOUNT%%"       // → the percent
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
	discountClass, discount := "", ""
	if shown, ok := shownDiscount(lead); ok {
		discountClass, discount = "is-shown", strconv.Itoa(shown["percent"].(int))
	}
	for mark, value := range map[string]string{
		placeholderNumber:        html.EscapeString(lead.Number()),
		placeholderTelegramURL:   html.EscapeString(telegram),
		placeholderGenericClass:  "is-hidden",
		placeholderNumberedClass: "is-shown",
		placeholderTelegramClass: telegramClass,
		placeholderDiscountClass: discountClass,
		placeholderDiscount:      discount,
	} {
		page = bytes.ReplaceAll(page, []byte(mark), []byte(value))
	}

	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(page)
}
