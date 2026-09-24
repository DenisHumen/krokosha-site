package clients

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/achievements"
	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/mail"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

var (
	quiet  = slog.New(slog.DiscardHandler)
	secret = []byte("0123456789abcdef0123456789abcdef")
	noon   = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
)

func rules() config.Loyalty {
	return config.Loyalty{
		Enabled: true, Currency: "USD", Welcome: 10, Eggs: 20,
		Tiers: []config.LoyaltyTier{
			{ID: "silver", Orders: 1, Spent: 1000, Discount: 5},
			{ID: "gold", Orders: 3, Spent: 5000, Discount: 10},
			{ID: "platinum", Orders: 6, Spent: 15000, Discount: 15},
		},
	}
}

func testForm() config.Form {
	return config.Form{Enabled: true, Directions: []config.Option{{ID: "networks"}, {ID: "other"}}}
}

type fixture struct {
	t       *testing.T
	db      *sql.DB
	now     time.Time
	service *Service
	leads   *leads.Store
	handler http.Handler
	bot     string
	rules   config.Loyalty
	// submitted counts the forms sent, for their addresses.
	submitted int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := testenv.MySQL(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, cfg, migrations.Files, quiet); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, cfg, 10*time.Second, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store, err := cache.New(ctx, "", quiet)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: pool, now: noon, bot: "krokosha_bot", rules: rules()}
	clock := func() time.Time { return f.now }
	store.SetClock(clock)
	f.leads = leads.NewStore(pool, clock)
	f.leads.UseLoyalty(func() config.Loyalty { return f.rules }, time.UTC)
	f.service = New(Options{
		DB: pool, Cache: store, Leads: f.leads, Log: quiet, Secret: secret, SiteURL: "https://krokosha.com",
		BotUsername: func() string { return f.bot }, Now: clock,
	})
	api := NewHandler(f.service, func() config.Loyalty { return f.rules })
	srv := server.New(server.Deps{Env: &config.Env{Listen: "127.0.0.1:0"}, DB: pool, Cache: store, Log: quiet, Started: time.Now()})
	api.Register(srv.Mux())
	leads.NewHandler(leads.Options{
		Store: f.leads, Cache: store, Sessions: sessionsOf{}, Log: quiet, Secret: secret, Form: testForm, WWWDir: t.TempDir(),
		Accounts: api, Now: clock,
	}).Register(srv.Mux())
	f.handler = srv.Handler()
	return f
}

// sessionsOf satisfies leads.Sessions with an unknown visit.
type sessionsOf struct{}

func (sessionsOf) CurrentSession(context.Context, net.IP, string) (analytics.SessionSummary, error) {
	return analytics.SessionSummary{}, nil
}

// --- a browser -------------------------------------------------------------------------------------

type browser struct {
	f       *fixture
	ip      string
	cookies map[string]string
	csrf    string
}

func (f *fixture) browser(ip string) *browser {
	return &browser{f: f, ip: ip, cookies: map[string]string{}}
}

type answer struct {
	status int
	body   map[string]any
	raw    string
}

func (b *browser) send(method, path string, body any, edit ...func(*http.Request)) answer {
	b.f.t.Helper()
	var reader *strings.Reader
	contentType := "application/json"
	switch value := body.(type) {
	case nil:
		reader = strings.NewReader("")
	case url.Values:
		reader, contentType = strings.NewReader(value.Encode()), "application/x-www-form-urlencoded"
	default:
		raw, _ := json.Marshal(value)
		reader = strings.NewReader(string(raw))
	}
	request := httptest.NewRequest(method, path, reader)
	request.Host = "krokosha.com"
	request.RemoteAddr = "127.0.0.1:40000"
	request.Header.Set("X-Real-IP", b.ip)
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36")
	request.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("Origin", "https://krokosha.com")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		if b.csrf != "" {
			request.Header.Set("X-CSRF-Token", b.csrf)
		}
	}
	for name, value := range b.cookies {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	for _, change := range edit {
		change(request)
	}
	recorder := httptest.NewRecorder()
	b.f.handler.ServeHTTP(recorder, request)
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(b.cookies, cookie.Name)
		} else {
			if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
				b.f.t.Errorf("cookie %s is not locked down: %+v", cookie.Name, cookie)
			}
			b.cookies[cookie.Name] = cookie.Value
		}
	}
	out := answer{status: recorder.Code, raw: recorder.Body.String()}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out.body)
	return out
}

// lastLogin reads the newest login, for the code the letter or the bot would carry.
func (f *fixture) lastLogin() *login {
	f.t.Helper()
	l, err := scanLogin(f.db.QueryRow(`SELECT ` + loginColumns + ` FROM client_logins ORDER BY id DESC LIMIT 1`))
	if err != nil {
		f.t.Fatal(err)
	}
	return l
}

// signInByEmail goes the whole way: asks for a code, reads it «from the letter», types it in.
func (b *browser) signInByEmail(email string) {
	b.f.t.Helper()
	if got := b.send(http.MethodPost, "/api/account/login", map[string]string{"method": "email", "email": email, "lang": "ru"}); got.status != http.StatusOK {
		b.f.t.Fatalf("login: %d %s", got.status, got.raw)
	}
	code := b.f.service.code(b.f.lastLogin())
	if got := b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": code}); got.status != http.StatusOK {
		b.f.t.Fatalf("code: %d %s", got.status, got.raw)
	}
	b.me()
}

func (b *browser) me() map[string]any {
	b.f.t.Helper()
	got := b.send(http.MethodGet, "/api/account/me", nil)
	if got.status != http.StatusOK || got.body["ok"] != true {
		b.f.t.Fatalf("me: %d %s", got.status, got.raw)
	}
	b.csrf, _ = got.body["csrf"].(string)
	return got.body
}

func (f *fixture) tasks(kind string) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE kind = ?`, kind).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// --- signing in --------------------------------------------------------------------------------------

func TestSignInByEmail(t *testing.T) {
	f := newFixture(t)
	// A request left before the account existed joins it at the first sign-in.
	f.submit(f.browser("198.51.100.1"), "Ivan@Company.com", nil)

	b := f.browser("203.0.113.1")
	got := b.send(http.MethodPost, "/api/account/login", map[string]string{"method": "email", "email": "Ivan@COMPANY.com", "lang": "ru"})
	if got.status != http.StatusOK || got.body["sent_to"] != "I***n@company.com" || b.cookies[loginCookie] == "" {
		t.Fatalf("login: %d %v %v", got.status, got.body, b.cookies)
	}
	if f.tasks(TaskLogin) != 1 {
		t.Fatalf("letters queued: %d", f.tasks(TaskLogin))
	}
	l := f.lastLogin()
	// Neither the code nor the link is in the database.
	var stored string
	_ = f.db.QueryRow(`SELECT CONCAT_WS('|', HEX(nonce), HEX(browser_hash)) FROM client_logins WHERE id = ?`, l.id).Scan(&stored)
	if strings.Contains(stored, f.service.code(l)) {
		t.Fatal("the code is stored")
	}

	// Another browser cannot use the code, even the right one.
	thief := f.browser("203.0.113.66")
	if got := thief.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": f.service.code(l)}); got.status != http.StatusUnprocessableEntity {
		t.Errorf("a code from another browser: %d", got.status)
	}
	if got := b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": "000000"}); got.status != http.StatusUnprocessableEntity || got.body["error"] != "bad_code" {
		t.Errorf("a wrong code: %d %v", got.status, got.body)
	}
	got = b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": " " + f.service.code(l)[:3] + " " + f.service.code(l)[3:]})
	if got.status != http.StatusOK || got.body["created"] != true || got.body["linked"] != 1.0 || b.cookies[SessionCookie] == "" || b.cookies[loginCookie] != "" {
		t.Fatalf("the right code (with spaces): %d %v %v", got.status, got.body, b.cookies)
	}
	me := b.me()
	client := me["client"].(map[string]any)
	if client["email"] != "Ivan@company.com" || client["name"] != "Иван Петров" {
		t.Errorf("the new account: %v", client)
	}
	// The code is spent.
	if got := b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": f.service.code(l)}); got.status != http.StatusUnprocessableEntity {
		t.Errorf("the code twice: %d", got.status)
	}
	leadsList := b.send(http.MethodGet, "/api/account/leads", nil)
	if list, _ := leadsList.body["leads"].([]any); len(list) != 1 {
		t.Errorf("the request left before: %v", leadsList.body)
	}
}

func TestCodeGuessingStops(t *testing.T) {
	f := newFixture(t)
	b := f.browser("203.0.113.2")
	b.send(http.MethodPost, "/api/account/login", map[string]string{"method": "email", "email": "anna@example.com", "lang": "en"})
	l := f.lastLogin()
	for range loginTries {
		b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": "111111"})
	}
	if got := b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": f.service.code(l)}); got.status != http.StatusUnprocessableEntity {
		t.Errorf("the right code after %d wrong ones: %d", loginTries, got.status)
	}

	// An expired code does not work either.
	b.send(http.MethodPost, "/api/account/login", map[string]string{"method": "email", "email": "anna@example.com", "lang": "en"})
	l = f.lastLogin()
	f.now = f.now.Add(LoginLifetime + time.Second)
	if got := b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": f.service.code(l)}); got.status != http.StatusUnprocessableEntity {
		t.Errorf("an expired code: %d", got.status)
	}
}

func TestSignInByLink(t *testing.T) {
	f := newFixture(t)
	b := f.browser("203.0.113.3")
	b.send(http.MethodPost, "/api/account/login", map[string]string{"method": "email", "email": "olga@example.com", "lang": "uk"})
	l := f.lastLogin()
	token := f.service.linkToken(l)
	if link := f.service.LinkURL(l.lang, token); link != "https://krokosha.com/uk/account/#login="+token {
		t.Errorf("link: %s", link)
	}
	// The link works in any browser: opening the letter proves the address.
	other := f.browser("198.51.100.3")
	if got := other.send(http.MethodPost, "/api/account/login/link", map[string]string{"token": token}); got.status != http.StatusOK || other.cookies[SessionCookie] == "" {
		t.Fatalf("link: %d %v", got.status, got.body)
	}
	if got := f.browser("198.51.100.4").send(http.MethodPost, "/api/account/login/link", map[string]string{"token": token}); got.status != http.StatusUnprocessableEntity {
		t.Errorf("the link twice: %d", got.status)
	}
	forged := strings.Replace(token, token[len(token)-4:], "AAAA", 1)
	if got := f.browser("198.51.100.5").send(http.MethodPost, "/api/account/login/link", map[string]string{"token": forged}); got.status != http.StatusUnprocessableEntity {
		t.Errorf("a forged link: %d", got.status)
	}
}

func TestSignInWithTelegram(t *testing.T) {
	f := newFixture(t)
	b := f.browser("203.0.113.4")
	got := b.send(http.MethodPost, "/api/account/login", map[string]string{"method": "telegram", "lang": "ru"})
	botURL, _ := got.body["bot_url"].(string)
	if got.status != http.StatusOK || !strings.HasPrefix(botURL, "https://t.me/krokosha_bot?start=l_") {
		t.Fatalf("telegram: %d %v", got.status, got.body)
	}
	token := strings.TrimPrefix(botURL, "https://t.me/krokosha_bot?start=")
	// Before anybody opened the bot there is no code to type.
	if got := b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": "123456"}); got.status != http.StatusUnprocessableEntity {
		t.Errorf("a code before the bot: %d", got.status)
	}
	code, err := f.service.TelegramStart(context.Background(), token, 777, "Олег", "oleg_net")
	if err != nil || len(code.Code) != 6 || code.Lang != "ru" || !strings.Contains(code.LinkURL, "/ru/account/#login=") {
		t.Fatalf("TelegramStart: %+v %v", code, err)
	}
	// The same person may open it again; somebody else may not.
	if again, err := f.service.TelegramStart(context.Background(), token, 777, "Олег", "oleg_net"); err != nil || again.Code != code.Code {
		t.Errorf("the same person again: %+v %v", again, err)
	}
	if _, err := f.service.TelegramStart(context.Background(), token, 888, "Кто-то", "someone"); !errors.Is(err, ErrUsed) {
		t.Errorf("another Telegram account: %v", err)
	}
	if got := b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": code.Code}); got.status != http.StatusOK {
		t.Fatalf("code: %d %v", got.status, got.body)
	}
	client := b.me()["client"].(map[string]any)
	if client["telegram"] != "@oleg_net" || client["name"] != "Олег" || client["email"] != "" {
		t.Errorf("the account: %v", client)
	}

	f.bot = ""
	if got := f.browser("203.0.113.5").send(http.MethodPost, "/api/account/login", map[string]string{"method": "telegram", "lang": "ru"}); got.status != http.StatusServiceUnavailable {
		t.Errorf("without a bot: %d", got.status)
	}
}

func TestAddingAnAddress(t *testing.T) {
	f := newFixture(t)
	taken := f.browser("203.0.113.6")
	taken.signInByEmail("taken@example.com")

	b := f.browser("203.0.113.7")
	b.signInByEmail("first@example.com")
	// The address of another account: whoever types its code holds it, so that account is theirs too
	// and joins this one (TestTwoAccountsOfOnePersonBecomeOne).
	b.send(http.MethodPost, "/api/account/email", map[string]string{"email": "taken@example.com"})
	if got := b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": f.service.code(f.lastLogin())}); got.status != http.StatusOK {
		t.Errorf("an address of another account: %d %v", got.status, got.body)
	}
	if email := b.me()["client"].(map[string]any)["email"]; email != "taken@example.com" {
		t.Errorf("email after joining the other account: %v", email)
	}
	if got := taken.send(http.MethodGet, "/api/account/me", nil); got.body["ok"] != false {
		t.Errorf("the joined account is still signed in: %s", got.raw)
	}
	// A new address replaces the old one; the session stays.
	b.send(http.MethodPost, "/api/account/email", map[string]string{"email": "second@example.com"})
	if got := b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": f.service.code(f.lastLogin())}); got.status != http.StatusOK {
		t.Fatalf("a new address: %d %v", got.status, got.body)
	}
	if email := b.me()["client"].(map[string]any)["email"]; email != "second@example.com" {
		t.Errorf("email: %v", email)
	}
	// The only way in cannot go.
	if got := b.send(http.MethodPost, "/api/account/telegram/unlink", map[string]string{}); got.status != http.StatusOK {
		t.Errorf("unlinking a Telegram that is not there, with an address: %d", got.status)
	}
}

// letterOf writes the letter of a login the way the outbox sends it.
func (f *fixture) letterOf(l *login) mail.Message {
	f.t.Helper()
	var sent mail.Message
	mailer := &Mailer{Service: f.service, SiteHost: "krokosha.com", Deliver: func(_ context.Context, message mail.Message) error {
		sent = message
		return nil
	}}
	payload, _ := json.Marshal(loginPayload{LoginID: l.id})
	if err := mailer.Send(context.Background(), outbox.Task{Kind: TaskLogin, Payload: payload}); err != nil {
		f.t.Fatal(err)
	}
	return sent
}

// Adding an address or a Telegram account is the code's alone: a link would work in the browser of
// whoever got the letter, and give their address — with their requests — to the account that asked.
func TestAddingTakesTheCode(t *testing.T) {
	f := newFixture(t)
	f.submit(f.browser("198.51.100.20"), "victim@example.com", nil) // a request of the victim's, in nobody's account
	attacker := f.browser("203.0.113.20")
	attacker.signInByEmail("attacker@example.com")
	attacker.send(http.MethodPost, "/api/account/email", map[string]string{"email": "victim@example.com"})
	l := f.lastLogin()
	letter := f.letterOf(l)
	if !strings.Contains(letter.Text, f.service.code(l)) || strings.Contains(letter.Text, "#login=") || strings.Contains(letter.HTML, "#login=") {
		t.Errorf("the letter that adds an address:\n%s", letter.Text)
	}
	victim := f.browser("198.51.100.21")
	for _, body := range []map[string]any{{"token": f.service.linkToken(l)}, {"token": f.service.linkToken(l), "peek": true}} {
		if got := victim.send(http.MethodPost, "/api/account/login/link", body); got.status != http.StatusUnprocessableEntity {
			t.Errorf("the link of a login that adds an address %v: %d %v", body, got.status, got.body)
		}
	}
	if email := attacker.me()["client"].(map[string]any)["email"]; email != "attacker@example.com" {
		t.Errorf("the attacker's account took %v", email)
	}
	if list, _ := attacker.send(http.MethodGet, "/api/account/leads", nil).body["leads"].([]any); len(list) != 0 {
		t.Errorf("the victim's requests in the attacker's account: %v", list)
	}

	// Telegram: the bot hands out the code, and no button that would sign in anywhere.
	got := attacker.send(http.MethodPost, "/api/account/telegram", map[string]string{})
	botURL, _ := got.body["bot_url"].(string)
	code, err := f.service.TelegramStart(context.Background(), strings.TrimPrefix(botURL, "https://t.me/krokosha_bot?start="), 555, "Жертва", "victim")
	if err != nil || !code.Adding || code.Code == "" || code.LinkURL != "" {
		t.Errorf("TelegramStart of a login that adds: %+v %v", code, err)
	}
}

// Whoever opens a link to sign in is asked first: the page learns whose account it opens, half
// hidden, without spending the link.
func TestPeekingAtALink(t *testing.T) {
	f := newFixture(t)
	f.browser("203.0.113.21").send(http.MethodPost, "/api/account/login", map[string]string{"method": "email", "email": "olena@example.com", "lang": "uk"})
	token := f.service.linkToken(f.lastLogin())
	other := f.browser("198.51.100.22")
	got := other.send(http.MethodPost, "/api/account/login/link", map[string]any{"token": token, "peek": true})
	if got.status != http.StatusOK || got.body["account"] != "o***a@example.com" || other.cookies[SessionCookie] != "" {
		t.Fatalf("peek: %d %v %v", got.status, got.body, other.cookies)
	}
	if got := other.send(http.MethodPost, "/api/account/login/link", map[string]any{"token": token}); got.status != http.StatusOK || other.cookies[SessionCookie] == "" {
		t.Errorf("the link after a peek: %d %v", got.status, got.body)
	}
}

// A look-alike of an address is not the address. The database's collation takes «ánna@» for
// «anna@»: a code sent to the look-alike must not open the real account, nor pull its requests.
func TestLookAlikeAddresses(t *testing.T) {
	f := newFixture(t)
	owner := f.browser("203.0.113.22")
	owner.signInByEmail("anna@example.com")
	id := owner.me()["client"].(map[string]any)["id"]
	stranger := f.browser("198.51.100.23")
	for _, lookAlike := range []string{"ánna@example.com", "anna@exámple.com", "аnna@example.com"} { // the last «а» is Cyrillic
		if got := stranger.send(http.MethodPost, "/api/account/login", map[string]string{"method": "email", "email": lookAlike, "lang": "en"}); got.status != http.StatusUnprocessableEntity {
			t.Errorf("a code for %q: %d %v", lookAlike, got.status, got.body)
		}
	}
	// The same address in capitals is the same account.
	same := f.browser("198.51.100.24")
	same.signInByEmail("ANNA@Example.com")
	if other := same.me()["client"].(map[string]any)["id"]; other != id {
		t.Errorf("the address in capitals opened account %v, not %v", other, id)
	}
	// A request left with a look-alike joins no account.
	lead, err := f.leads.Get(context.Background(), mustNumber(t, f.submit(f.browser("198.51.100.25"), "ánna@example.com", nil)["id"].(string)))
	if err != nil || lead.ClientID != 0 {
		t.Errorf("a request with a look-alike joined account %d (%v)", lead.ClientID, err)
	}
}

// However many addresses and networks a stranger has, the site sends no more than lettersPerHour
// letters with codes an hour; and «+tags» and Gmail's dots are one mailbox.
func TestLettersPerHour(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := range lettersPerHour {
		if _, err := f.service.StartEmail(ctx, fmt.Sprintf("person%d@example.com", i), Attempt{Lang: "en", Key: fmt.Sprint("net", i)}, 0); err != nil {
			t.Fatalf("letter %d: %v", i+1, err)
		}
	}
	if _, err := f.service.StartEmail(ctx, "one.more@example.com", Attempt{Lang: "en", Key: "another net"}, 0); !errors.Is(err, ErrThrottled) {
		t.Errorf("letter %d in an hour: %v", lettersPerHour+1, err)
	}
	f.now = f.now.Add(time.Hour + time.Minute)
	for i, address := range []string{"john.smith@gmail.com", "johnsmith@gmail.com", "john.smith+a@gmail.com", "JohnSmith+b@googlemail.com", "j.ohn.smith@gmail.com"} {
		if _, err := f.service.StartEmail(ctx, address, Attempt{Lang: "en", Key: fmt.Sprint("gmail", i)}, 0); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	if _, err := f.service.StartEmail(ctx, "johnsmith+c@gmail.com", Attempt{Lang: "en", Key: "gmail, again"}, 0); !errors.Is(err, ErrThrottled) {
		t.Errorf("a sixth letter to one Gmail mailbox: %v", err)
	}
}

func TestTheAPIRefusesStrangers(t *testing.T) {
	f := newFixture(t)
	b := f.browser("203.0.113.8")
	b.signInByEmail("guard@example.com")

	crossOrigin := b.send(http.MethodPost, "/api/account/profile", map[string]string{"name": "x"}, func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example")
	})
	crossSite := b.send(http.MethodPost, "/api/account/profile", map[string]string{"name": "x"}, func(r *http.Request) {
		r.Header.Del("Origin")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	form := b.send(http.MethodPost, "/api/account/profile", url.Values{"name": {"x"}})
	noToken := b.send(http.MethodPost, "/api/account/profile", map[string]string{"name": "x"}, func(r *http.Request) { r.Header.Del("X-CSRF-Token") })
	if crossOrigin.status != http.StatusForbidden || crossSite.status != http.StatusForbidden || form.status != http.StatusUnsupportedMediaType || noToken.status != http.StatusForbidden {
		t.Errorf("guards: origin %d, site %d, form %d, csrf %d", crossOrigin.status, crossSite.status, form.status, noToken.status)
	}
	// Nobody signed in is an answer to «who is it», not an error; any other route refuses.
	stranger := f.browser("203.0.113.9")
	if got := stranger.send(http.MethodGet, "/api/account/me", nil); got.status != http.StatusOK || got.body["ok"] != false || got.body["error"] != "signed_out" {
		t.Errorf("signed out: %d %v", got.status, got.body)
	}
	if got := stranger.send(http.MethodGet, "/api/account/leads", nil); got.status != http.StatusUnauthorized {
		t.Errorf("signed out, the requests: %d", got.status)
	}

	// Somebody else's request does not exist for this account.
	f.submit(f.browser("198.51.100.10"), "someone@example.com", nil)
	var id int64
	_ = f.db.QueryRow(`SELECT MAX(id) FROM leads`).Scan(&id)
	if got := b.send(http.MethodGet, "/api/account/leads/"+leads.Number(id), nil); got.status != http.StatusNotFound {
		t.Errorf("somebody else's request: %d", got.status)
	}
	if got := b.send(http.MethodPost, "/api/account/leads/"+leads.Number(id)+"/messages", map[string]string{"text": "hello"}); got.status != http.StatusNotFound {
		t.Errorf("writing into somebody else's request: %d", got.status)
	}

	if got := b.send(http.MethodPost, "/api/account/logout", map[string]string{}); got.status != http.StatusOK || b.cookies[SessionCookie] != "" {
		t.Errorf("logout: %d %v", got.status, b.cookies)
	}
}

// --- the account: the conversation, inquiries, contacts --------------------------------------------------

func TestTheConversationInTheAccount(t *testing.T) {
	f := newFixture(t)
	b := f.browser("203.0.113.11")
	b.signInByEmail("maria@example.com")
	number := f.submit(b, "maria@example.com", nil)["id"].(string)
	id := mustNumber(t, number)

	ctx := context.Background()
	if _, err := f.leads.Take(ctx, id, "denis"); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	if err := f.leads.AddNote(ctx, id, "denis", "внутренняя заметка: клиент торопится"); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	if _, err := f.leads.Reply(ctx, id, "denis", "Здравствуйте! Нужны детали."); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	list := b.send(http.MethodGet, "/api/account/leads", nil).body["leads"].([]any)
	if item := list[0].(map[string]any); item["status"] != leads.StatusWaitingClient || item["unread"] != true {
		t.Errorf("the list: %v", item)
	}
	view := b.send(http.MethodGet, "/api/account/leads/"+number, nil)
	raw := view.raw
	if strings.Contains(raw, "внутренняя заметка") || strings.Contains(raw, "denis") || strings.Contains(raw, "spam") {
		t.Errorf("the client sees what is not theirs: %s", raw)
	}
	feed := view.body["lead"].(map[string]any)["feed"].([]any)
	if len(feed) < 3 {
		t.Fatalf("feed: %v", feed)
	}
	// Opened: the answer is no longer new.
	if item := b.send(http.MethodGet, "/api/account/leads", nil).body["leads"].([]any)[0].(map[string]any); item["unread"] != false {
		t.Errorf("unread after opening: %v", item)
	}
	// The client answers in the account: back to work, and the staff is told.
	f.now = f.now.Add(time.Minute)
	if got := b.send(http.MethodPost, "/api/account/leads/"+number+"/messages", map[string]string{"text": "Вот детали: 40 мест, два этажа."}); got.status != http.StatusOK {
		t.Fatalf("message: %d %s", got.status, got.raw)
	}
	card, err := f.leads.Card(ctx, id)
	if err != nil || card.Lead.Status != leads.StatusInProgress {
		t.Fatalf("after the client's message: %v %+v", err, card)
	}
	last := leads.Entry{}
	for _, entry := range card.Feed {
		if entry.Kind == "message" {
			last = entry
		}
	}
	if last.Channel != leads.ChannelSite || last.Direction != "in" {
		t.Errorf("the client's message in the card: %+v", last)
	}
	if f.tasks(leads.TaskClientMessage) != 2 {
		t.Errorf("the staff is not told: %d tasks", f.tasks(leads.TaskClientMessage))
	}
}

func TestInquiry(t *testing.T) {
	f := newFixture(t)
	b := f.browser("203.0.113.12")
	b.signInByEmail("petr@example.com")
	parent := f.submit(b, "petr@example.com", nil)["id"].(string)
	got := b.send(http.MethodPost, "/api/account/inquiries", map[string]string{"subject": "Счёт", "text": "Пришлите, пожалуйста, счёт на оплату.", "parent": parent})
	if got.status != http.StatusCreated {
		t.Fatalf("inquiry: %d %s", got.status, got.raw)
	}
	lead, err := f.leads.Get(context.Background(), mustNumber(t, got.body["number"].(string)))
	if err != nil || lead.Kind != leads.KindInquiry || lead.Subject != "Счёт" || lead.ParentID != mustNumber(t, parent) || lead.ContactValue != "petr@example.com" ||
		lead.Discount.Percent != 0 || lead.Status != leads.StatusNew {
		t.Errorf("inquiry: %+v %v", lead, err)
	}
	// Somebody else's request cannot be the parent.
	f.submit(f.browser("198.51.100.20"), "other@example.com", nil)
	var other int64
	_ = f.db.QueryRow(`SELECT MAX(id) FROM leads WHERE contact_value = 'other@example.com'`).Scan(&other)
	if got := b.send(http.MethodPost, "/api/account/inquiries", map[string]string{"text": "Вопрос по чужой заявке", "parent": leads.Number(other)}); got.status != http.StatusUnprocessableEntity {
		t.Errorf("somebody else's parent: %d", got.status)
	}
}

func TestContacts(t *testing.T) {
	for _, tc := range []struct{ kind, in, want, link string }{
		{"whatsapp", "+380 (67) 123-45-67", "+380671234567", "https://wa.me/380671234567"},
		{"viber", "067 123 45 67", "0671234567", "viber://chat?number=%2B0671234567"},
		{"telegram", "https://t.me/ivan_petrov", "@ivan_petrov", "https://t.me/ivan_petrov"},
		{"linkedin", "https://www.linkedin.com/in/ivan-petrov/", "in/ivan-petrov", "https://www.linkedin.com/in/ivan-petrov"},
		{"linkedin", "ivan-petrov", "in/ivan-petrov", "https://www.linkedin.com/in/ivan-petrov"},
		{"instagram", "@ivan.petrov", "ivan.petrov", "https://www.instagram.com/ivan.petrov"},
		{"x", "https://twitter.com/ivan_p", "ivan_p", "https://x.com/ivan_p"},
		{"facebook", "facebook.com/ivan.petrov.1", "ivan.petrov.1", "https://www.facebook.com/ivan.petrov.1"},
		{"github", "github.com/DenisHumen", "DenisHumen", "https://github.com/DenisHumen"},
		{"discord", "Ivan.Petrov", "ivan.petrov", ""},
		{"website", "company.com.ua/about", "https://company.com.ua/about", "https://company.com.ua/about"},
	} {
		got, err := NormalizeContact(tc.kind, tc.in)
		if err != nil || got != tc.want || ContactLink(tc.kind, got) != tc.link {
			t.Errorf("%s %q → %q (%v), link %q; want %q, %q", tc.kind, tc.in, got, err, ContactLink(tc.kind, got), tc.want, tc.link)
		}
	}
	for kind, bad := range map[string]string{
		"phone": "call me maybe", "linkedin": "https://evil.example/in/x", "instagram": "javascript:alert(1)",
		"website": "javascript:alert(1)", "github": "https://github.com/../../etc", "fax": "123", "x": "",
	} {
		if got, err := NormalizeContact(kind, bad); err == nil {
			t.Errorf("%s %q accepted as %q", kind, bad, got)
		}
	}

	f := newFixture(t)
	b := f.browser("203.0.113.13")
	b.signInByEmail("contacts@example.com")
	got := b.send(http.MethodPost, "/api/account/contacts", map[string]string{"kind": "whatsapp", "value": "+380671234567"})
	if got.status != http.StatusOK {
		t.Fatalf("add: %d %s", got.status, got.raw)
	}
	if got := b.send(http.MethodPost, "/api/account/contacts", map[string]string{"kind": "website", "value": "javascript:alert(1)"}); got.status != http.StatusUnprocessableEntity {
		t.Errorf("a bad contact: %d", got.status)
	}
	contacts := b.me()["contacts"].([]any)
	if len(contacts) != 1 {
		t.Fatalf("contacts: %v", contacts)
	}
	id := contacts[0].(map[string]any)["id"]
	if got := b.send(http.MethodPost, "/api/account/contacts/remove", map[string]any{"id": id}); got.status != http.StatusOK || len(b.me()["contacts"].([]any)) != 0 {
		t.Errorf("remove: %d", got.status)
	}
}

// --- discounts, end to end ---------------------------------------------------------------------------------

// submit sends the contact form from a browser (signed in or not), with the eggs' receipt if given.
func (f *fixture) submit(b *browser, email string, eggs *string) map[string]any {
	f.t.Helper()
	values := url.Values{
		"name": {"Иван Петров"}, "contact_method": {"email"}, "contact_value": {email}, "direction": {"networks"},
		"description": {"Нужно перестроить сеть офиса на 40 мест: MikroTik и два VLAN."}, "consent": {"on"}, "lang": {"ru"},
	}
	if eggs != nil {
		values.Set("eggs", *eggs)
	}
	// The form takes three requests an hour from an address; the account is the cookie, not the address.
	f.submitted++
	got := b.send(http.MethodPost, "/api/leads", values, func(r *http.Request) {
		r.Header.Set("X-Real-IP", fmt.Sprintf("192.0.2.%d", f.submitted))
	})
	if got.status != http.StatusCreated {
		f.t.Fatalf("form: %d %s", got.status, got.raw)
	}
	f.now = f.now.Add(time.Minute) // what happens next happens later
	return got.body
}

func discountOf(body map[string]any) (percent float64, reason string) {
	discount, _ := body["discount"].(map[string]any)
	percent, _ = discount["percent"].(float64)
	reason, _ = discount["reason"].(string)
	return percent, reason
}

func mustNumber(t *testing.T, number string) int64 {
	t.Helper()
	var id int64
	if _, err := fmt.Sscanf(number, "K-%d", &id); err != nil {
		t.Fatalf("number %q: %v", number, err)
	}
	return id
}

func (f *fixture) complete(id int64, amount float64) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.leads.SetStatus(ctx, id, "denis", leads.StatusInProgress, ""); err != nil && !errors.Is(err, leads.ErrBadTransition) {
		f.t.Fatal(err)
	}
	if err := f.leads.SetStatus(ctx, id, "denis", leads.StatusDone, ""); err != nil {
		f.t.Fatal(err)
	}
	if err := f.leads.SetAmount(ctx, id, "denis", &amount); err != nil {
		f.t.Fatal(err)
	}
}

func TestDiscountsOfASignedInClient(t *testing.T) {
	f := newFixture(t)
	b := f.browser("203.0.113.20")
	b.signInByEmail("loyal@example.com")

	first := f.submit(b, "loyal@example.com", nil)
	if percent, reason := discountOf(first); percent != 10 || reason != "welcome" {
		t.Fatalf("first request: %v", first)
	}
	// The second, while the first is open: no welcome again, no level yet.
	second := f.submit(b, "loyal@example.com", nil)
	if percent, _ := discountOf(second); percent != 0 {
		t.Errorf("second request: %v", second)
	}
	f.complete(mustNumber(t, first["id"].(string)), 1200)
	loyaltyJSON := b.me()["loyalty"].(map[string]any)
	if loyaltyJSON["tier"] != "silver" || loyaltyJSON["orders"] != 1.0 || loyaltyJSON["offer"].(map[string]any)["percent"] != 5.0 {
		t.Errorf("after the first order: %v", loyaltyJSON)
	}
	if next := loyaltyJSON["next"].(map[string]any); next["tier"] != "gold" || next["orders"] != 2.0 || next["spent"] != 3800.0 {
		t.Errorf("next: %v", next)
	}
	third := f.submit(b, "loyal@example.com", nil)
	if percent, reason := discountOf(third); percent != 5 || reason != "tier" || third["discount"].(map[string]any)["detail"] != "silver" {
		t.Errorf("third request: %v", third)
	}

	// Every egg, once: 20% beats silver, then silver again.
	eggs := allReceipt(t)
	withEggs := f.submit(b, "loyal@example.com", &eggs)
	if percent, reason := discountOf(withEggs); percent != 20 || reason != "eggs" {
		t.Errorf("with the eggs: %v", withEggs)
	}
	other := allReceipt(t) // another receipt of the same account: the account used its reward
	if percent, _ := discountOf(f.submit(b, "loyal@example.com", &other)); percent != 5 {
		t.Errorf("the eggs twice: %v", percent)
	}

	// A personal discount «once» wins, and is spent.
	var clientID int64
	_ = f.db.QueryRow(`SELECT id FROM clients WHERE email = 'loyal@example.com'`).Scan(&clientID)
	if err := f.service.SetPersonal(context.Background(), clientID, Personal{Percent: 30, Note: "партнёр", Once: true}); err != nil {
		t.Fatal(err)
	}
	personal := f.submit(b, "loyal@example.com", nil)
	if percent, reason := discountOf(personal); percent != 30 || reason != "personal" || personal["discount"].(map[string]any)["detail"] != "партнёр" {
		t.Errorf("personal: %v", personal)
	}
	if percent, _ := discountOf(f.submit(b, "loyal@example.com", nil)); percent != 5 {
		t.Errorf("the personal discount once was not spent: %v", percent)
	}
}

func allReceipt(t *testing.T) string {
	t.Helper()
	var receipts []string
	for i, egg := range achievements.Eggs {
		receipts = append(receipts, achievements.Sign(secret, egg, noon.Add(time.Duration(i)*time.Hour)))
	}
	first, last, err := achievements.Complete(secret, receipts)
	if err != nil {
		t.Fatal(err)
	}
	// Two receipts made in the same second would be the same: make each one of its own.
	return achievements.SignAll(secret, noon.Add(time.Duration(time.Now().UnixNano()%1e6)*time.Second), last.Sub(first))
}

func TestDiscountsOfAStranger(t *testing.T) {
	f := newFixture(t)
	stranger := f.browser("198.51.100.30")
	first := f.submit(stranger, "new@example.com", nil)
	if percent, reason := discountOf(first); percent != 10 || reason != "welcome" {
		t.Errorf("a first request: %v", first)
	}
	eggs := allReceipt(t)
	withEggs := f.submit(stranger, "new@example.com", &eggs)
	if percent, reason := discountOf(withEggs); percent != 20 || reason != "eggs" {
		t.Errorf("the eggs: %v", withEggs)
	}
	// The same receipt twice pays once — whatever address comes with it.
	if percent, _ := discountOf(f.submit(stranger, "else@example.com", &eggs)); percent != 10 {
		t.Errorf("a used receipt: %v", percent)
	}
	// A forged receipt claims nothing.
	forged := "all.x.0.AAAAAAAAAAAAAAAAAAAA"
	if percent, _ := discountOf(f.submit(stranger, "forger@example.com", &forged)); percent != 10 {
		t.Errorf("a forged receipt: %v", percent)
	}

	// A regular client's address, typed by somebody who is not signed in: the request gets the
	// client's level, but the page does not say so.
	gold := f.browser("203.0.113.31")
	gold.signInByEmail("gold@example.com")
	for range 3 {
		f.complete(mustNumber(t, f.submit(gold, "gold@example.com", nil)["id"].(string)), 100)
	}
	typed := f.submit(f.browser("198.51.100.32"), "gold@example.com", nil)
	if _, shown := typed["discount"]; shown {
		t.Errorf("a stranger learns the level of an address: %v", typed)
	}
	lead, _ := f.leads.Get(context.Background(), mustNumber(t, typed["id"].(string)))
	if lead.Discount.Percent != 10 || lead.Discount.Reason != "tier" || lead.ClientID == 0 {
		t.Errorf("the request of a known address: %+v", lead.Discount)
	}
}

func TestLoginLetter(t *testing.T) {
	f := newFixture(t)
	b := f.browser("203.0.113.40")
	b.send(http.MethodPost, "/api/account/login", map[string]string{"method": "email", "email": "letter@example.com", "lang": "uk"})
	l := f.lastLogin()
	var sent []mail.Message
	mailer := &Mailer{Service: f.service, SiteHost: "krokosha.com", Deliver: func(_ context.Context, message mail.Message) error {
		sent = append(sent, message)
		return nil
	}}
	var task outbox.Task
	_ = f.db.QueryRow(`SELECT id, kind, payload FROM outbox WHERE kind = ?`, TaskLogin).Scan(&task.ID, &task.Kind, &task.Payload)
	if err := mailer.Send(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	letter := sent[0]
	code, link := f.service.code(l), f.service.LinkURL("uk", f.service.linkToken(l))
	if letter.To.Address != "letter@example.com" || !strings.Contains(letter.Subject, code) || !strings.Contains(letter.Text, link) ||
		!strings.Contains(letter.HTML, code) || !strings.Contains(letter.Text, "15 хвилин") || letter.Headers["Auto-Submitted"] != "auto-generated" {
		t.Errorf("letter: %+v", letter)
	}
	// A code that ran out is not sent at all.
	f.now = f.now.Add(LoginLifetime)
	if err := mailer.Send(context.Background(), task); !outbox.IsPermanent(err) {
		t.Errorf("an expired code: %v", err)
	}
}

func TestTheOwnersTools(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.browser("203.0.113.50")
	a.signInByEmail("twin@example.com")
	var emailID, telegramID int64
	_ = f.db.QueryRow(`SELECT id FROM clients WHERE email = 'twin@example.com'`).Scan(&emailID)
	f.submit(a, "twin@example.com", nil)

	// Blocking ends the sessions at once, and signing in no longer works.
	if err := f.service.SetDisabled(ctx, emailID, true); err != nil {
		t.Fatal(err)
	}
	if got := a.send(http.MethodGet, "/api/account/me", nil); got.body["ok"] != false || got.body["error"] != "signed_out" {
		t.Errorf("a blocked account: %d %v", got.status, got.body)
	}
	a.send(http.MethodPost, "/api/account/login", map[string]string{"method": "email", "email": "twin@example.com", "lang": "ru"})
	if f.tasks(TaskLogin) != 1 {
		t.Errorf("a blocked account got a code: %d letters", f.tasks(TaskLogin))
	}
	if err := f.service.SetDisabled(ctx, emailID, false); err != nil {
		t.Fatal(err)
	}

	// Another account of the same person (Telegram) is merged into the first.
	b := f.browser("203.0.113.51")
	started := b.send(http.MethodPost, "/api/account/login", map[string]string{"method": "telegram", "lang": "ru"})
	code, err := f.service.TelegramStart(ctx, strings.TrimPrefix(started.body["bot_url"].(string), "https://t.me/krokosha_bot?start="), 4242, "Twin", "twin_tg")
	if err != nil {
		t.Fatal(err)
	}
	if got := b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": code.Code}); got.status != http.StatusOK {
		t.Fatalf("telegram sign-in: %d %s", got.status, got.raw)
	}
	_ = f.db.QueryRow(`SELECT id FROM clients WHERE telegram_id = 4242`).Scan(&telegramID)
	if err := f.service.SetPersonal(ctx, telegramID, Personal{Percent: 12, Note: "старый клиент"}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Merge(ctx, emailID, telegramID); err != nil {
		t.Fatal(err)
	}
	merged, err := f.service.Get(ctx, emailID)
	if err != nil || merged.TelegramID != 4242 || merged.Email != "twin@example.com" || merged.Personal.Percent != 12 {
		t.Errorf("merged: %+v %v", merged, err)
	}
	if _, err := f.service.Get(ctx, telegramID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the merged account is still there: %v", err)
	}

	// The owner changes the address of the account; the sign-in link goes there.
	if err := f.service.SetEmail(ctx, emailID, "new-twin@example.com"); err != nil {
		t.Fatal(err)
	}
	if masked, err := f.service.SendLink(ctx, emailID); err != nil || masked != "n***n@example.com" {
		t.Errorf("SendLink: %q %v", masked, err)
	}
	if err := f.service.Delete(ctx, emailID); err != nil {
		t.Fatal(err)
	}
	var orphans int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM leads WHERE contact_value = 'twin@example.com' AND client_id IS NULL`).Scan(&orphans)
	if orphans != 1 {
		t.Errorf("the requests of a deleted account: %d left, unlinked", orphans)
	}
}

func TestOrdersSurviveAnonymisation(t *testing.T) {
	f := newFixture(t)
	b := f.browser("203.0.113.60")
	b.signInByEmail("old@example.com")
	id := mustNumber(t, f.submit(b, "old@example.com", nil)["id"].(string))
	f.complete(id, 2500)
	if err := f.leads.Anonymize(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	loyaltyJSON := b.me()["loyalty"].(map[string]any)
	if loyaltyJSON["orders"] != 1.0 || loyaltyJSON["spent"] != 2500.0 || loyaltyJSON["tier"] != "silver" {
		t.Errorf("after anonymisation: %v", loyaltyJSON)
	}
	if list := b.send(http.MethodGet, "/api/account/leads", nil).body["leads"].([]any); len(list) != 0 {
		t.Errorf("an anonymised request is still in the account: %v", list)
	}
}

func TestAchievementsOfOrders(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.rules.BigOrder = 3000
	// As main.go wires it: every change of a request gives its account what its orders earned.
	f.leads.OnChange(func(leadID int64) {
		if err := f.service.AwardOrdersOfLead(ctx, leadID); err != nil {
			t.Errorf("award: %v", err)
		}
	})
	b := f.browser("203.0.113.21")
	b.signInByEmail("nina@example.com")
	earned := func() map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, item := range b.me()["orders"].(map[string]any)["earned"].([]any) {
			entry := item.(map[string]any)
			out[entry["id"].(string)] = entry["new"].(bool)
		}
		return out
	}
	complete := func(amount *float64) int64 {
		t.Helper()
		id := mustNumber(t, f.submit(b, "nina@example.com", nil)["id"].(string))
		if _, err := f.leads.Take(ctx, id, "denis"); err != nil {
			t.Fatal(err)
		}
		if amount != nil {
			if err := f.leads.SetAmount(ctx, id, "denis", amount); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.leads.SetStatus(ctx, id, "denis", leads.StatusDone, ""); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Minute)
		return id
	}

	if got := earned(); len(got) != 0 {
		t.Fatalf("before any order: %v", got)
	}
	small := 1200.0
	first := complete(&small)
	if got := earned(); len(got) != 1 || !got[FirstOrder] {
		t.Fatalf("after the first order: %v", got)
	}
	// The banner was shown: no longer new. Unknown ids are no business of this endpoint.
	if got := b.send(http.MethodPost, "/api/account/achievements/seen", map[string][]string{"ids": {FirstOrder, "konami"}}); got.status != http.StatusOK {
		t.Fatalf("seen: %d %s", got.status, got.raw)
	}
	if got := earned(); got[FirstOrder] {
		t.Errorf("still new after being shown: %v", got)
	}

	// A second order, a big one: the second, the big and — all three there — the golden one.
	big := 3500.0
	complete(&big)
	got := earned()
	if len(got) != 4 || got[FirstOrder] || !got[SecondOrder] || !got[BigOrder] || !got[AllOrders] {
		t.Fatalf("after a second, big order: %v", got)
	}

	// An order that goes back to work keeps what it earned.
	if err := f.leads.SetStatus(ctx, first, "denis", leads.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	if got := earned(); len(got) != 4 {
		t.Errorf("an order back in work took an achievement away: %v", got)
	}

	// The shares are of all the accounts, and only from ten of them.
	shares := b.me()["orders"].(map[string]any)["shares"].(map[string]any)
	if len(shares) != 0 {
		t.Errorf("shares of one account: %v", shares)
	}
	for i := range 9 {
		if _, err := f.service.Create(ctx, "", fmt.Sprintf("client%d@example.com", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	shares = b.me()["orders"].(map[string]any)["shares"].(map[string]any)
	if shares[FirstOrder] != 10.0 || shares[AllOrders] != 10.0 {
		t.Errorf("shares of ten accounts: %v", shares)
	}
	if b.me()["orders"].(map[string]any)["big_order"] != 3000.0 {
		t.Error("the page is not told what a big order is")
	}
}
