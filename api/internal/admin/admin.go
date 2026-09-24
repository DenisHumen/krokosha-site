// Package admin is the hidden admin area of the site (brief B6): server-rendered pages behind a
// secret path, a login with optional two-factor authentication, and — on top of that — the
// dashboards. Everything it serves is private: no caching, no indexing, a strict CSP.
package admin

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/achievements"
	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/auth"
	"github.com/DenisHumen/krokosha-site/api/internal/clients"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/geo"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/loyalty"
	"github.com/DenisHumen/krokosha-site/api/internal/mailboxes"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram"
)

//go:embed templates/*.html static/*
var assets embed.FS

// The fonts of static/fonts: Go knows the type of .woff2 only from /etc/mime.types, which a minimal
// server may not have — and a font served as application/octet-stream is refused under nosniff.
func init() {
	_ = mime.AddExtensionType(".woff2", "font/woff2")
}

// CookieName uses the __Host- prefix: browsers then insist on Secure, Path=/ and no Domain,
// so no sub-domain and no plain-HTTP page can ever set or read it.
const CookieName = "__Host-ks"

const cookieName = CookieName

// Options configure the admin area.
type Options struct {
	// Prefix is the secret path, e.g. /_k7f3a9 (ADMIN_PATH).
	Prefix   string
	SiteHost string
	Auth     *auth.Service
	Log      *slog.Logger
	Version  string

	// Reports read the statistics; Location is the owner's time zone — every time on screen is theirs.
	Reports  *analytics.Reports
	Location *time.Location
	// Feed subscribes to accepted analytics events (analytics.Service.Subscribe).
	Feed func() (feed <-chan analytics.Live, cancel func())
	// Active answers «how many visitors were seen within the window».
	Active func(ctx context.Context, window time.Duration) int

	// Traffic reads what nginx served; System knows how the server is doing and passes the
	// «rebuild now» button on. LogPolled says when nginx's log was last read.
	Traffic   TrafficReports
	System    SystemStatus
	LogPolled func() time.Time

	// Leads are the requests from the site's form; Form gives the names of their directions.
	// Kick tells the outbox worker that there is something to deliver right now.
	Leads *leads.Store
	Form  func() config.Form
	Kick  func()

	// BotAccess knows who may use the Telegram bot; BotStatus says how the bot is doing, and
	// false when there is no token (invitations can be prepared before there is a bot).
	BotAccess *telegram.Access
	BotStatus func() (telegram.Status, bool)
	// BotRemindAfter: the bot reminds about a request nobody took after this long (0 — never);
	// BotDigestAt is the time of its morning summary, "" — none.
	BotRemindAfter time.Duration
	BotDigestAt    string

	// Inbox reads the service mailbox; nil — answers by mail are not read (no IMAP_ADDR).
	// Mailbox is its address, KeepLettersDays how long a letter without a request waits.
	Inbox           Inbox
	Mailbox         string
	KeepLettersDays int

	// Geo describes the GeoIP database; nil — geolocation is off. Its maker is credited at the
	// bottom of every page, as the licence of the data asks (DB-IP: CC BY 4.0).
	Geo func() geo.Info

	// Clients are the personal accounts; Loyalty the rules of discounts (content/site.yaml → loyalty);
	// Achievements the statistics of the easter eggs.
	Clients      *clients.Service
	Loyalty      func() config.Loyalty
	Achievements *achievements.Service
	// Mailboxes of the site's own mail server (the «Почта» screen); nil — the screen is not there.
	Mailboxes *mailboxes.Service
	// The mail the site sends: its From, the submission server, and the queue of what goes out
	// by mail and to Telegram (outbox.Recent, outbox.SentSince); nil — not shown.
	MailFrom   string
	SMTPAddr   string
	Deliveries func(ctx context.Context, limit int) ([]outbox.Entry, error)
	SentSince  func(ctx context.Context, channel string, since time.Time) (int, error)
}

// Handler serves the admin area.
type Handler struct {
	opts      Options
	templates map[string]*template.Template
	static    http.Handler

	headerMu sync.Mutex
	header   headerCache // the slower numbers of the ticker (frame.go)
}

// New parses the embedded templates.
func New(opts Options) (*Handler, error) {
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	if opts.LogPolled == nil {
		opts.LogPolled = func() time.Time { return time.Time{} }
	}
	if opts.Kick == nil {
		opts.Kick = func() {}
	}
	if opts.Form == nil {
		opts.Form = func() config.Form { return config.Form{} }
	}
	if opts.Loyalty == nil {
		opts.Loyalty = func() config.Loyalty { return config.Loyalty{} }
	}
	if opts.Active == nil {
		opts.Active = func(context.Context, time.Duration) int { return 0 }
	}
	h := &Handler{opts: opts, templates: map[string]*template.Template{}}
	funcs := template.FuncMap{
		"path": func(parts ...string) string { return opts.Prefix + strings.Join(parts, "") },
		// href builds a link with a query made by this package (periodQuery): html/template would
		// otherwise escape its «=» and «&» as if they were data.
		"href": func(path, query string) template.URL {
			if query == "" {
				return template.URL(opts.Prefix + path) //nolint:gosec // our own prefix and route
			}
			return template.URL(opts.Prefix + path + "?" + query) //nolint:gosec // query comes from url.Values.Encode
		},
		"time": func(t time.Time) string { return t.In(opts.Location).Format("02.01.2006 15:04") },
		// «23 сен 14:12», and the year when it is not this one
		"when": func(t time.Time) string {
			local := t.In(opts.Location)
			text := fmt.Sprintf("%d %s", local.Day(), shortMonths[local.Month()])
			if local.Year() != time.Now().In(opts.Location).Year() {
				text += fmt.Sprintf(" %d", local.Year())
			}
			return text + local.Format(" 15:04")
		},
		"dayMonth": func(t time.Time) string {
			local := t.In(opts.Location)
			return fmt.Sprintf("%d %s", local.Day(), shortMonths[local.Month()])
		},
		"clock":      func(t time.Time) string { return t.In(opts.Location).Format("15:04:05") },
		"day":        func(t time.Time) string { return t.In(opts.Location).Format("02.01") },
		"duration":   func(ms any) string { return duration(toInt64(ms)) },
		"elapsed":    func(d time.Duration) string { return duration(d.Milliseconds()) },
		"bytes":      func(value any) string { return formatBytes(toInt64(value)) },
		"count":      func(value any) string { return formatCount(toInt64(value)) },
		"ago":        func(t time.Time) string { return ago(time.Since(t)) },
		"meter":      meter,
		"usage":      usage,
		"statusName": named(statusNames, "—"),
		"methodName": named(methodNames, "—"),
		"direction":  func(id string) string { return h.directionName(id) },
		"safeURL": func(link string) template.URL { // links built by this package from validated contacts
			return template.URL(link) //nolint:gosec // see leadCard: mailto:, https://t.me/, tel: of a validated value
		},
		"pct":      formatPercent,
		"ring":     ring,
		"bar":      bar,
		"describe": describe,
		"section":  named(sectionNames, "—"),
		"source":   named(sourceNames, "—"),
		"device":   named(deviceNames, "—"),
		"language": named(languageNames, "не указан"),
		"contact":  named(contactNames, "—"),
		"country":  countryName,
		"plural":   plural,
		"tone": func(index int) string { // the accents of the reference dashboard, in turn
			return [...]string{"accent", "cyan", "pink"}[index%3]
		},
		"discount":    func(offer loyalty.Offer) string { return h.discountText(offer) },
		"tierName":    func(id string) string { return h.tierName(id) },
		"money":       func(value any) string { return h.money(value) },
		"contactLink": contactLink,
		"kindName":    named(clients.KindNames, "—"),
		"leadKind":    named(map[string]string{leads.KindRequest: "Заявка", leads.KindInquiry: "Обращение"}, "Заявка"),
		"eggName":     named(eggNames, "—"),
		"orderName":   named(orderNames, "—"),
		"percentOf":   func(part, whole float64) float64 { return 100 * part / max(whole, 1) },
		"icon":        icon,
		"step":        step,
		"initials":    initials,
		"botRole":     botRoleName,
		"lasting":     lasting,
		"spanOf":      spanOf,
		"sub":         func(a, b any) int64 { return toInt64(a) - toInt64(b) },
		// share: part of whole in percent, whatever numbers the report uses; 0 of nothing
		"share": func(part, whole any) float64 {
			if w := toNumber(whole); w > 0 {
				return toNumber(part) * 100 / w
			}
			return 0
		},
		"shortDuration": shortDuration,
		"minsec":        func(ms any) string { return minutesSeconds(toInt64(ms)) },
		"average": func(total, count any) int64 { // total ÷ count, and 0 when there is nothing to divide by
			if n := toInt64(count); n > 0 {
				return toInt64(total) / n
			}
			return 0
		},
		"decimalPct": func(value float64) string { return decimal(value) + "%" },
		"short":      named(shortNames, "—"),
		"since":      func(t time.Time) string { return ago(time.Since(t)) },
	}
	for _, page := range []string{"login", "overview", "visits", "visit", "traffic", "status", "leads", "lead", "inbox", "templates", "bot", "account", "error",
		"clients", "client", "mail", "achievements"} {
		parsed, err := template.New("layout.html").Funcs(funcs).ParseFS(assets, "templates/layout.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		h.templates[page] = parsed
	}
	staticFiles, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	h.static = http.StripPrefix(opts.Prefix+"/static/", http.FileServerFS(staticFiles))
	return h, nil
}

// Register adds the routes under the secret prefix. Anything else under the prefix is a 404
// that looks like every other 404 of the API.
func (h *Handler) Register(mux *http.ServeMux) {
	p := h.opts.Prefix
	mux.Handle("GET "+p+"/static/", h.headers(h.static))
	mux.Handle("GET "+p+"/login", h.headers(http.HandlerFunc(h.loginPage)))
	mux.Handle("POST "+p+"/login", h.headers(h.sameOrigin(http.HandlerFunc(h.login))))
	mux.Handle("POST "+p+"/login/code", h.headers(h.sameOrigin(http.HandlerFunc(h.loginCode))))
	mux.Handle("POST "+p+"/logout", h.private(h.logout))

	mux.Handle("GET "+p+"/{$}", h.private(h.overview))
	mux.Handle("GET "+p+"/live", h.private(h.live))
	mux.Handle("GET "+p+"/visits", h.private(h.visits))
	mux.Handle("GET "+p+"/visits/{id}", h.private(h.visit))
	mux.Handle("GET "+p+"/export/{table}", h.private(h.export))
	mux.Handle("GET "+p+"/leads", h.private(h.leadsList))
	mux.Handle("GET "+p+"/leads/export.csv", h.private(h.leadsExport))
	mux.Handle("GET "+p+"/leads/{id}", h.private(h.leadCard))
	mux.Handle("GET "+p+"/leads/{id}/files/{file}", h.private(h.leadFile))
	mux.Handle("POST "+p+"/leads/{id}/status", h.private(h.leadStatus))
	mux.Handle("POST "+p+"/leads/{id}/note", h.private(h.leadNote))
	mux.Handle("POST "+p+"/leads/{id}/reply", h.private(h.leadReply))
	mux.Handle("POST "+p+"/leads/{id}/delete", h.private(h.leadDelete))
	mux.Handle("POST "+p+"/leads/{id}/discount", h.private(h.leadDiscount))
	mux.Handle("POST "+p+"/leads/{id}/amount", h.private(h.leadAmount))
	if h.opts.Clients != nil {
		mux.Handle("POST "+p+"/leads/{id}/client", h.private(h.leadClient))
	}
	if h.opts.Achievements != nil || h.opts.Clients != nil {
		mux.Handle("GET "+p+"/achievements", h.private(h.achievementsPage))
	}
	if h.opts.Mailboxes != nil {
		mux.Handle("GET "+p+"/mail", h.private(h.mailPage))
		mux.Handle("POST "+p+"/mail", h.private(h.mailAdd))
		mux.Handle("POST "+p+"/mail/password", h.private(h.mailPassword))
		mux.Handle("POST "+p+"/mail/delete", h.private(h.mailDelete))
	}
	if h.opts.Clients != nil {
		mux.Handle("GET "+p+"/clients", h.private(h.clientsList))
		mux.Handle("POST "+p+"/clients", h.private(h.clientCreate))
		mux.Handle("GET "+p+"/clients/{id}", h.private(h.clientCard))
		mux.Handle("POST "+p+"/clients/{id}/profile", h.private(h.clientProfile))
		mux.Handle("POST "+p+"/clients/{id}/note", h.private(h.clientNote))
		mux.Handle("POST "+p+"/clients/{id}/contacts", h.private(h.clientContactAdd))
		mux.Handle("POST "+p+"/clients/{id}/contacts/{contact}/remove", h.private(h.clientContactRemove))
		mux.Handle("POST "+p+"/clients/{id}/discount", h.private(h.clientDiscount))
		mux.Handle("POST "+p+"/clients/{id}/email", h.private(h.clientEmail))
		mux.Handle("POST "+p+"/clients/{id}/telegram/unlink", h.private(h.clientUnlinkTelegram))
		mux.Handle("POST "+p+"/clients/{id}/sessions/end", h.private(h.clientEndSessions))
		mux.Handle("POST "+p+"/clients/{id}/link", h.private(h.clientSendLink))
		mux.Handle("POST "+p+"/clients/{id}/block", h.private(h.clientBlock))
		mux.Handle("POST "+p+"/clients/{id}/merge", h.private(h.clientMerge))
		mux.Handle("POST "+p+"/clients/{id}/attach", h.private(h.clientAttach))
		mux.Handle("POST "+p+"/clients/{id}/delete", h.private(h.clientDelete))
	}
	mux.Handle("GET "+p+"/inbox", h.private(h.inboxPage))
	mux.Handle("POST "+p+"/inbox/{id}/attach", h.private(h.inboxAttach))
	mux.Handle("POST "+p+"/inbox/{id}/discard", h.private(h.inboxDiscard))
	mux.Handle("GET "+p+"/templates", h.private(h.templatesPage))
	mux.Handle("POST "+p+"/templates", h.private(h.templateSave))
	mux.Handle("GET "+p+"/bot", h.private(h.botPage))
	mux.Handle("POST "+p+"/bot/invite", h.private(h.botInvite))
	mux.Handle("POST "+p+"/bot/invite/revoke", h.private(h.botInviteRevoke))
	mux.Handle("POST "+p+"/bot/member", h.private(h.botMember))
	mux.Handle("POST "+p+"/bot/add", h.private(h.botAdd))
	mux.Handle("POST "+p+"/bot/role", h.private(h.botRole))
	mux.Handle("GET "+p+"/traffic", h.private(h.traffic))
	mux.Handle("GET "+p+"/status", h.private(h.status))
	mux.Handle("POST "+p+"/status/rebuild", h.private(h.rebuild))
	mux.Handle("POST "+p+"/status/backup", h.private(h.backupNow))
	mux.Handle("GET "+p+"/account", h.private(h.account))
	mux.Handle("POST "+p+"/account/password", h.private(h.changePassword))
	mux.Handle("POST "+p+"/account/totp/begin", h.private(h.totpBegin))
	mux.Handle("POST "+p+"/account/totp/confirm", h.private(h.totpConfirm))
	mux.Handle("POST "+p+"/account/totp/disable", h.private(h.totpDisable))
	mux.Handle("GET "+p+"/account/totp/qr.png", h.private(h.totpQR))
	mux.Handle("POST "+p+"/account/sessions/revoke", h.private(h.revokeSession))
	mux.Handle("GET "+p, http.RedirectHandler(p+"/", http.StatusMovedPermanently))
}

type contextKey int

const keySession contextKey = iota

func sessionOf(r *http.Request) *auth.Session {
	session, _ := r.Context().Value(keySession).(*auth.Session)
	return session
}

// headers are sent with everything under the prefix.
func (h *Handler) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Cache-Control", "no-store")
		// No inline scripts or styles anywhere in the admin area; charts are server-rendered SVG.
		header.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; "+
				"connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin refuses state-changing requests that a browser marks as coming from elsewhere.
func (h *Handler) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			if parsed, err := url.Parse(origin); err != nil || !strings.EqualFold(parsed.Host, r.Host) {
				http.Error(w, "cross-origin request", http.StatusForbidden)
				return
			}
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "cross-site request", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// private wraps a page that needs a session; unsafe methods also need the CSRF token.
func (h *Handler) private(next http.HandlerFunc) http.Handler {
	return h.headers(h.sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err != nil {
			h.toLogin(w, r)
			return
		}
		// The live feed polls by itself: it must not keep an abandoned session alive.
		authenticate := h.opts.Auth.Authenticate
		if strings.HasSuffix(r.URL.Path, "/live") {
			authenticate = h.opts.Auth.Check
		}
		session, err := authenticate(r.Context(), cookie.Value)
		if err != nil {
			if !errors.Is(err, auth.ErrNoSession) {
				h.opts.Log.Error("cannot check the session", "error", err)
			}
			h.clearCookie(w)
			h.toLogin(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(session.CSRFToken)) != 1 {
				http.Error(w, "the form has expired, reload the page", http.StatusForbidden)
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), keySession, session)))
	})))
}

func (h *Handler) toLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, h.opts.Prefix+"/login", http.StatusSeeOther)
}

func (h *Handler) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/",
		MaxAge: int(auth.SessionLifetime.Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

// view is what every template gets.
type view struct {
	Title    string
	Nav      string
	NewLeads int // requests nobody has taken yet: the number next to «Заявки» in the menu
	// HasInbox: the service reads a mailbox; Letters — how many letters wait for a decision.
	HasInbox bool
	Letters  int
	// HasClients: the site has personal accounts — the menu shows «Клиенты». HasMail: «Почта».
	HasClients bool
	HasMail    bool
	// Refresh: the page reloads itself in a few seconds (a mailbox request is being applied).
	Refresh bool
	// Script: a module of static/ the page needs besides admin.js («Ачивки» → achievements.js).
	Script string
	// HasAchievements: the menu shows «Ачивки».
	HasAchievements bool
	Session         *auth.Session
	Version         string
	// GeoSource is the maker of the GeoIP database whom the footer credits: «DB-IP», «MaxMind» or "".
	GeoSource string
	Flash     string // a message about what just happened
	Error     string
	Data      any

	// The frame (frame.go): the icons of the rail, the numbers of the ticker, the avatar's letters,
	// and the address of the site for «Открыть сайт».
	Rail     []navItem
	Ticker   [][]tick
	Initials string
	SiteURL  string
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, page string, v view) {
	v.Session = sessionOf(r)
	v.Version = h.opts.Version
	if v.Session != nil && h.opts.Leads != nil {
		if counts, err := h.opts.Leads.Counts(r.Context()); err == nil {
			v.NewLeads = counts[leads.StatusNew]
		}
	}
	v.HasClients = v.Session != nil && h.opts.Clients != nil
	v.HasMail = v.Session != nil && h.opts.Mailboxes != nil
	v.HasAchievements = v.Session != nil && (h.opts.Achievements != nil || h.opts.Clients != nil)
	if data, ok := v.Data.(mailData); ok && data.Waiting && !data.Stuck && data.Issued == "" {
		v.Refresh = true
	}
	if v.Session != nil && h.opts.Inbox != nil {
		v.HasInbox, v.Letters = true, h.opts.Inbox.Status(r.Context()).Unmatched
	}
	if v.Session != nil && h.opts.Geo != nil {
		v.GeoSource = h.opts.Geo().Source()
	}
	if v.Flash == "" {
		v.Flash = flashText[r.URL.Query().Get("ok")]
	}
	if v.Session != nil {
		v.Rail = h.nav(v)
		v.Ticker = h.ticker(r.Context(), v)
		v.Initials = initials(v.Session.User.Login)
		if h.opts.SiteHost != "" {
			v.SiteURL = "https://" + h.opts.SiteHost + "/"
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.templates[page].ExecuteTemplate(w, "layout.html", v); err != nil {
		h.opts.Log.Error("cannot render a page", "page", page, "error", err)
	}
}

// flashText maps ?ok=… to a message, so that a redirect after a POST can say what happened
// without putting free text into the URL.
var flashText = map[string]string{ //nolint:gosec // messages about a changed password, not a password
	"password": "Пароль изменён. Остальные сеансы завершены.",
	"totp-on":  "Двухфакторная аутентификация включена.",
	"totp-off": "Двухфакторная аутентификация выключена.",
	"revoked":  "Сеанс завершён.",
	"rebuild":  "Пересборка запрошена: она начнётся в течение нескольких секунд и займёт около минуты.",
	"backup":   "Резервная копия запрошена: она начнётся в течение нескольких секунд; итог появится здесь.",

	"lead-status":      "Статус изменён.",
	"lead-note":        "Заметка сохранена.",
	"lead-reply":       "Ответ сохранён и поставлен в очередь на отправку.",
	"lead-deleted":     "Данные клиента удалены. В журнале осталась только запись об удалении.",
	"letter-attached":  "Письмо перенесено в переписку заявки.",
	"letter-discarded": "Письмо удалено.",
	"template-saved":   "Шаблон сохранён.",
	"template-deleted": "Шаблон удалён.",
	"lead-discount":    "Скидка заявки изменена.",
	"lead-amount":      "Сумма заказа сохранена: она учитывается в уровне клиента.",
	"lead-client":      "Клиент заявки изменён.",

	"client-created":   "Клиент создан.",
	"client-saved":     "Сохранено.",
	"client-discount":  "Персональная скидка сохранена.",
	"client-sessions":  "Все сеансы клиента завершены.",
	"client-link":      "Ссылка для входа отправлена на адрес клиента: она действует сутки.",
	"client-blocked":   "Аккаунт заблокирован: сеансы завершены, войти нельзя.",
	"client-unblocked": "Аккаунт разблокирован.",
	"client-merged":    "Аккаунты объединены.",
	"client-attached":  "Заявка привязана к клиенту.",
	"client-deleted":   "Аккаунт удалён. Его заявки остались — без клиента.",
	"mail-request":     "Запрос отправлен: почтовый сервер применит его через несколько секунд.",

	"bot-invite-revoked": "Приглашение отозвано.",
	"bot-added":          "Доступ выдан: бот начнёт присылать этому человеку заявки, как только тот напишет боту /start.",
	"bot-role":           "Роль изменена.",
	"bot-disabled":       "Доступ отключён: бот больше не отвечает этому человеку и не присылает ему заявки.",
	"bot-enabled":        "Доступ возвращён.",
}

func (h *Handler) attemptMeta(r *http.Request) auth.Attempt {
	client := analytics.ParseUserAgent(r.UserAgent())
	prefix := "unknown"
	if ip := server.ClientIP(r.Context()); ip != nil {
		prefix = analytics.TruncateIP(ip)
	}
	return auth.Attempt{IPPrefix: prefix, Client: client.Browser + " · " + client.OS}
}
