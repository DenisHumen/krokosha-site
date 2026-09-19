// Package admin is the hidden admin area of the site (brief B6): server-rendered pages behind a
// secret path, a login with optional two-factor authentication, and — on top of that — the
// dashboards. Everything it serves is private: no caching, no indexing, a strict CSP.
package admin

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/auth"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
)

//go:embed templates/*.html static/*
var assets embed.FS

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
}

// Handler serves the admin area.
type Handler struct {
	opts      Options
	templates map[string]*template.Template
	static    http.Handler
}

// New parses the embedded templates.
func New(opts Options) (*Handler, error) {
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	if opts.LogPolled == nil {
		opts.LogPolled = func() time.Time { return time.Time{} }
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
		"time":     func(t time.Time) string { return t.In(opts.Location).Format("02.01.2006 15:04") },
		"clock":    func(t time.Time) string { return t.In(opts.Location).Format("15:04:05") },
		"day":      func(t time.Time) string { return t.In(opts.Location).Format("02.01") },
		"duration": func(ms any) string { return duration(toInt64(ms)) },
		"elapsed":  func(d time.Duration) string { return duration(d.Milliseconds()) },
		"bytes":    func(value any) string { return formatBytes(toInt64(value)) },
		"count":    func(value any) string { return formatCount(toInt64(value)) },
		"ago":      func(t time.Time) string { return ago(time.Since(t)) },
		"meter":    meter,
		"usage":    usage,
		"pct":      formatPercent,
		"ring":     ring,
		"bar":      bar,
		"describe": describe,
		"section":  named(sectionNames, "—"),
		"source":   named(sourceNames, "—"),
		"device":   named(deviceNames, "—"),
		"language": named(languageNames, "не указан"),
		"contact":  named(contactNames, "—"),
		"country":  named(map[string]string{}, "не определена"),
		"plural":   plural,
		"tone": func(index int) string { // the accents of the reference dashboard, in turn
			return [...]string{"accent", "cyan", "pink"}[index%3]
		},
	}
	for _, page := range []string{"login", "overview", "visits", "visit", "traffic", "status", "account", "error"} {
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
	mux.Handle("GET "+p+"/traffic", h.private(h.traffic))
	mux.Handle("GET "+p+"/status", h.private(h.status))
	mux.Handle("POST "+p+"/status/rebuild", h.private(h.rebuild))
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
	Title   string
	Nav     string
	Session *auth.Session
	Version string
	Flash   string // a message about what just happened
	Error   string
	Data    any
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, page string, v view) {
	v.Session = sessionOf(r)
	v.Version = h.opts.Version
	if v.Flash == "" {
		v.Flash = flashText[r.URL.Query().Get("ok")]
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
}

func (h *Handler) attemptMeta(r *http.Request) auth.Attempt {
	client := analytics.ParseUserAgent(r.UserAgent())
	prefix := "unknown"
	if ip := server.ClientIP(r.Context()); ip != nil {
		prefix = analytics.TruncateIP(ip)
	}
	return auth.Attempt{IPPrefix: prefix, Client: client.Browser + " · " + client.OS}
}
