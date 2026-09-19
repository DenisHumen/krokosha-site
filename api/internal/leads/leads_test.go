package leads

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

var (
	quiet  = slog.New(slog.DiscardHandler)
	secret = []byte("0123456789abcdef0123456789abcdef")
	noon   = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
)

func testForm() config.Form {
	return config.Form{
		Enabled:    true,
		Directions: []config.Option{{ID: "networks"}, {ID: "servers"}, {ID: "devops"}, {ID: "other"}},
		Budgets:    []config.Localized{{"en": "up to $500", "ru": "до $500"}, {"": "$1–3k"}},
		Timelines:  []config.Localized{{"en": "urgent", "ru": "срочно"}, {"en": "a month", "ru": "месяц"}},
	}
}

func validValues() url.Values {
	return url.Values{
		"name": {"  Иван Петров "}, "contact_method": {"email"}, "contact_value": {"Ivan.Petrov@Company.COM"},
		"direction": {"networks"}, "description": {"Нужно перестроить сеть офиса на 40 мест: MikroTik и два VLAN."},
		"budget": {"1"}, "timeline": {"0"}, "consent": {"on"}, "lang": {"ru"},
	}
}

func TestParseAcceptsAndNormalizes(t *testing.T) {
	sub, errs := Parse(validValues().Get, testForm())
	if errs != nil {
		t.Fatalf("errors: %v", errs)
	}
	if sub.Name != "Иван Петров" || sub.ContactValue != "Ivan.Petrov@company.com" || sub.Budget != "$1–3k" || sub.Timeline != "urgent" || sub.Lang != "ru" {
		t.Errorf("submission: %+v", sub)
	}

	for method, cases := range map[string]map[string]string{
		"telegram": {"@krokosha_dev": "@krokosha_dev", "krokosha_dev": "@krokosha_dev", "https://t.me/krokosha_dev": "@krokosha_dev", "t.me/krokosha_dev": "@krokosha_dev"},
		"phone":    {"+380 (67) 123-45-67": "+380671234567", "067 123 45 67": "0671234567"},
	} {
		for input, want := range cases {
			values := validValues()
			values.Set("contact_method", method)
			values.Set("contact_value", input)
			if sub, errs := Parse(values.Get, testForm()); errs != nil || sub.ContactValue != want {
				t.Errorf("%s %q → %q (%v), want %q", method, input, sub.ContactValue, errs, want)
			}
		}
	}

	// Optional fields may stay empty; an unknown language falls back to English.
	values := validValues()
	values.Del("budget")
	values.Del("timeline")
	values.Set("lang", "de")
	if sub, errs := Parse(values.Get, testForm()); errs != nil || sub.Budget != "" || sub.Lang != "en" {
		t.Errorf("optional fields: %+v %v", sub, errs)
	}
}

func TestParseRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		field, value, errField, code string
	}{
		"no name":                {"name", "  ", "name", ErrRequired},
		"no contact":             {"contact_value", "", "contact_value", ErrRequired},
		"not an email":           {"contact_value", "ivan@", "contact_value", ErrInvalidEmail},
		"email with a name":      {"contact_value", "Ivan <ivan@company.com>", "contact_value", ErrInvalidEmail},
		"two emails":             {"contact_value", "a@b.com, c@d.com", "contact_value", ErrInvalidEmail},
		"email header injection": {"contact_value", "ivan@company.com\r\nBcc: all@example.com", "contact_value", ErrInvalidEmail},
		"unknown method":         {"contact_method", "fax", "contact_method", ErrInvalidChoice},
		"unknown direction":      {"direction", "astrology", "direction", ErrInvalidChoice},
		"no direction":           {"direction", "", "direction", ErrRequired},
		"short description":      {"description", "Позвоните мне", "description", ErrDescriptionLength},
		"huge description":       {"description", strings.Repeat("я", MaxDescription+1), "description", ErrDescriptionLength},
		"budget out of range":    {"budget", "7", "budget", ErrInvalidChoice},
		"timeline not a number":  {"timeline", "asap", "timeline", ErrInvalidChoice},
		"no consent":             {"consent", "", "consent", ErrConsentRequired},
	} {
		values := validValues()
		values.Set(tc.field, tc.value)
		if _, errs := Parse(values.Get, testForm()); errs[tc.errField] != tc.code || len(errs) != 1 {
			t.Errorf("%s: %v, want %s=%s", name, errs, tc.errField, tc.code)
		}
	}

	values := validValues()
	values.Set("contact_method", "telegram")
	for _, bad := range []string{"@abc", "@1name_x", "имя_пользователя", "https://evil.example/krokosha_dev", "@" + strings.Repeat("a", 33)} {
		values.Set("contact_value", bad)
		if _, errs := Parse(values.Get, testForm()); errs["contact_value"] != ErrInvalidTelegram {
			t.Errorf("telegram %q: %v", bad, errs)
		}
	}
	values.Set("contact_method", "phone")
	for _, bad := range []string{"12345", "+38067abc4567", "++380671234567", strings.Repeat("9", 16)} {
		values.Set("contact_value", bad)
		if _, errs := Parse(values.Get, testForm()); errs["contact_value"] != ErrInvalidPhone {
			t.Errorf("phone %q: %v", bad, errs)
		}
	}
}

func TestParseCleansText(t *testing.T) {
	values := validValues()
	values.Set("name", "Иван\r\nПетров\x00\x07")
	values.Set("description", "Первая строка\r\nВторая строка\tс табуляцией\x00 и нулём внутри текста")
	sub, errs := Parse(values.Get, testForm())
	if errs != nil {
		t.Fatal(errs)
	}
	if sub.Name != "Иван Петров" {
		t.Errorf("name = %q: a name is one line", sub.Name)
	}
	if sub.Description != "Первая строка\nВторая строка с табуляцией и нулём внутри текста" {
		t.Errorf("description = %q", sub.Description)
	}
}

// solve does what the form's script does in the browser.
func solve(t *testing.T, challenge Challenge) string {
	t.Helper()
	for number := 0; number <= challenge.MaxNumber; number++ {
		sum := sha256.Sum256([]byte(challenge.Salt + strconv.Itoa(number)))
		if hex.EncodeToString(sum[:]) == challenge.Challenge {
			payload, _ := json.Marshal(altchaPayload{Algorithm: challenge.Algorithm, Challenge: challenge.Challenge, Number: number, Salt: challenge.Salt, Signature: challenge.Signature})
			return base64.StdEncoding.EncodeToString(payload)
		}
	}
	t.Fatal("the challenge has no solution")
	return ""
}

func TestProofOfWork(t *testing.T) {
	challenge, err := NewChallenge(secret, noon)
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Algorithm != "SHA-256" || challenge.MaxNumber != altchaMaxNumber || !strings.HasSuffix(challenge.Salt, "&") {
		t.Errorf("challenge: %+v", challenge)
	}
	payload := solve(t, challenge)

	proof, err := VerifyProof(secret, payload, noon.Add(90*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !proof.IssuedAt.Equal(noon) || proof.Challenge != challenge.Challenge {
		t.Errorf("proof: %+v", proof)
	}
	if _, err := VerifyProof(secret, payload, noon.Add(challengeTTL+time.Second)); !errors.Is(err, ErrProofExpired) {
		t.Errorf("an old proof: %v", err)
	}
	if _, err := VerifyProof([]byte("another key, another server....."), payload, noon); !errors.Is(err, ErrBadProof) {
		t.Errorf("a proof signed by someone else: %v", err)
	}
	if _, err := VerifyProof(secret, "", noon); !errors.Is(err, ErrNoProof) {
		t.Errorf("no proof: %v", err)
	}

	tamper := func(change func(*altchaPayload)) string {
		raw, _ := base64.StdEncoding.DecodeString(payload)
		var solution altchaPayload
		_ = json.Unmarshal(raw, &solution)
		change(&solution)
		out, _ := json.Marshal(solution)
		return base64.StdEncoding.EncodeToString(out)
	}
	// Digits moved from the number into the salt give the same text to hash — «…&» + «456» and
	// «…&4» + «56» — and with it a way to extend the expiry, if a salt did not have to end with
	// its delimiter. (Needs a number whose tail has no leading zero, or the texts would differ.)
	recut := ""
	for recut == "" {
		fresh, err := NewChallenge(secret, noon)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := base64.StdEncoding.DecodeString(solve(t, fresh))
		var solution altchaPayload
		_ = json.Unmarshal(raw, &solution)
		if digits := strconv.Itoa(solution.Number); len(digits) >= 2 && digits[1] != '0' {
			solution.Salt += digits[:1]
			solution.Number, _ = strconv.Atoi(digits[1:])
			forged, _ := json.Marshal(solution)
			recut = base64.StdEncoding.EncodeToString(forged)
		}
	}
	if _, err := VerifyProof(secret, recut, noon); !errors.Is(err, ErrBadProof) {
		t.Errorf("a re-cut salt: %v, want ErrBadProof", err)
	}

	for name, forged := range map[string]string{
		"a wrong number":         tamper(func(p *altchaPayload) { p.Number++ }),
		"another algorithm":      tamper(func(p *altchaPayload) { p.Algorithm = "SHA-1" }),
		"not base64":             "%%%",
		"not JSON":               base64.StdEncoding.EncodeToString([]byte("hello")),
		"a salt without its end": tamper(func(p *altchaPayload) { p.Salt = strings.TrimSuffix(p.Salt, "&") }),
	} {
		if _, err := VerifyProof(secret, forged, noon); !errors.Is(err, ErrBadProof) {
			t.Errorf("%s: %v, want ErrBadProof", name, err)
		}
	}
}

func TestJudge(t *testing.T) {
	sub, _ := Parse(validValues().Get, testForm())
	human := Signals{ProofOK: true, FillTime: 95 * time.Second}

	if verdict := Judge(sub, human); verdict.Score != 0 || verdict.IsSpam() {
		t.Errorf("an ordinary request: %+v", verdict)
	}
	// Without JavaScript there is no proof: suspicious, but a plain text passes.
	if verdict := Judge(sub, Signals{}); verdict.IsSpam() || verdict.Score == 0 {
		t.Errorf("without JavaScript: %+v", verdict)
	}

	for name, tc := range map[string]struct {
		change  func(*Submission)
		signals Signals
		spam    bool
	}{
		"honeypot":                  {func(s *Submission) { s.Honeypot = "https://spam.example" }, human, true},
		"filled in a second":        {nil, Signals{ProofOK: true, FillTime: time.Second}, false},
		"filled in a second + link": {func(s *Submission) { s.Description += " http://promo.example" }, Signals{ProofOK: true, FillTime: time.Second}, true},
		"one link":                  {func(s *Submission) { s.Description += " схема: https://drive.example/plan.pdf" }, human, false},
		"no JS + link":              {func(s *Submission) { s.Description += " see www.best-seo.top" }, Signals{}, true},
		"seo pitch": {func(s *Submission) {
			s.Description = "We offer SEO and backlinks for your website, first page of Google guaranteed! https://a.example https://b.example"
		}, human, true},
		"seo pitch in Russian": {func(s *Submission) {
			s.Description = "Продвижение сайта в топ, раскрутка недорого. Пишите: promo-agency.ru и best-links.com"
		}, human, true},
		"a forged proof":        {nil, Signals{ProofError: "неверное решение"}, false},
		"a forged proof + link": {func(s *Submission) { s.Description += " http://x.example" }, Signals{ProofError: "неверное решение"}, true},
		"link as a name":        {func(s *Submission) { s.Name = "www.cheap-traffic.biz" }, Signals{}, true},
		"honest mention of seoul": {func(s *Submission) {
			s.Description = "Office network in Seoul: two floors, forty seats, MikroTik routers."
		}, human, false},
	} {
		candidate := sub
		if tc.change != nil {
			tc.change(&candidate)
		}
		if verdict := Judge(candidate, tc.signals); verdict.IsSpam() != tc.spam {
			t.Errorf("%s: spam=%v, want %v (%d points: %v)", name, verdict.IsSpam(), tc.spam, verdict.Score, verdict.Reasons)
		}
	}
}

// --- the whole way: HTTP → checks → database → outbox --------------------------------------------

type noSessions struct{}

func (noSessions) CurrentSession(_ context.Context, _ net.IP, userAgent string) (analytics.SessionSummary, error) {
	client := analytics.ParseUserAgent(userAgent)
	return analytics.SessionSummary{Device: client.Device, Browser: client.Browser, OS: client.OS}, nil
}

type fixture struct {
	t       *testing.T
	db      *sql.DB
	handler http.Handler
	now     time.Time
	www     string
	created []*Lead
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

	f := &fixture{t: t, db: pool, now: noon, www: t.TempDir()}
	clock := func() time.Time { return f.now }
	store.SetClock(clock)
	handler := NewHandler(Options{
		Store: NewStore(pool, clock), Cache: store, Sessions: noSessions{}, Log: quiet, Secret: secret,
		Form: testForm, WWWDir: f.www, Now: clock,
		TelegramURL: func(lead *Lead) string { return "https://t.me/krokosha_bot?start=c_" + lead.PublicToken },
		OnCreated:   func(lead *Lead) { f.created = append(f.created, lead) },
	})
	srv := server.New(server.Deps{Env: &config.Env{Listen: "127.0.0.1:0"}, DB: pool, Cache: store, Log: quiet, Started: time.Now()})
	handler.Register(srv.Mux())
	f.handler = srv.Handler()
	return f
}

type reply struct {
	status   int
	location string
	body     string
	json     map[string]any
}

func (f *fixture) do(method, path string, values url.Values, headers map[string]string) reply {
	f.t.Helper()
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	request := httptest.NewRequest(method, path, body)
	request.Host = "krokosha.xyz"
	request.RemoteAddr = "127.0.0.1:40000"
	request.Header.Set("X-Real-IP", "203.0.113.7")
	request.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1")
	if values != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", "https://krokosha.xyz")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	out := reply{status: recorder.Code, location: recorder.Header().Get("Location"), body: recorder.Body.String()}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out.json)
	return out
}

var asJSON = map[string]string{"Accept": "application/json"}

func (f *fixture) proof() string {
	f.t.Helper()
	got := f.do(http.MethodGet, "/api/leads/challenge", nil, nil)
	var challenge Challenge
	if err := json.Unmarshal([]byte(got.body), &challenge); err != nil || got.status != http.StatusOK {
		f.t.Fatalf("challenge: %d %s", got.status, got.body)
	}
	return solve(f.t, challenge)
}

func (f *fixture) count(query string) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(query).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestARequestIsStoredWithItsNotifications(t *testing.T) {
	f := newFixture(t)
	values := validValues()
	values.Set("altcha", f.proof())
	f.now = f.now.Add(2 * time.Minute) // the time it takes to write a request

	got := f.do(http.MethodPost, "/api/leads", values, asJSON)
	if got.status != http.StatusCreated || got.json["ok"] != true || got.json["id"] != "K-0001" {
		t.Fatalf("response: %d %s", got.status, got.body)
	}
	if link, _ := got.json["telegram_url"].(string); !strings.HasPrefix(link, "https://t.me/krokosha_bot?start=c_") || len(link) != len("https://t.me/krokosha_bot?start=c_")+22 {
		t.Errorf("telegram_url = %q", got.json["telegram_url"])
	}
	if len(f.created) != 1 || f.created[0].Number() != "K-0001" {
		t.Errorf("OnCreated: %v", f.created)
	}

	lead, err := NewStore(f.db, nil).Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if lead.Status != StatusNew || lead.Name != "Иван Петров" || lead.ContactValue != "Ivan.Petrov@company.com" || lead.Budget != "$1–3k" ||
		lead.Lang != "ru" || lead.IPPrefix != "203.0.113.0/24" || lead.Session.Device != "mobile" || lead.Verdict.Score != 0 {
		t.Errorf("stored request: %+v", lead)
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM lead_messages WHERE lead_id = 1 AND direction = 'in' AND channel = 'form'`:      1,
		`SELECT COUNT(*) FROM lead_events WHERE lead_id = 1 AND action = 'created' AND to_status = 'new'`:     1,
		`SELECT COUNT(*) FROM outbox WHERE lead_id = 1 AND kind = 'lead.notify' AND channel = 'email'`:        1,
		`SELECT COUNT(*) FROM outbox WHERE lead_id = 1 AND kind = 'lead.notify' AND channel = 'telegram'`:     1,
		`SELECT COUNT(*) FROM outbox WHERE lead_id = 1 AND kind = 'lead.autoreply' AND channel = 'email'`:     1,
		`SELECT COUNT(*) FROM outbox WHERE status <> 'pending' OR JSON_EXTRACT(payload, '$.lead_id') <> 1`:    0,
		`SELECT COUNT(*) FROM leads WHERE ip_prefix LIKE '%203.0.113.7%' OR description LIKE '%203.0.113.7%'`: 0,
	} {
		if got := f.count(query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}

	// The same proof again: a bot replaying a solved challenge. Accepted — and filed as suspicious
	// only if the text gives a reason; here it is a second honest-looking request.
	again := f.do(http.MethodPost, "/api/leads", values, asJSON)
	if again.status != http.StatusCreated || again.json["id"] != "K-0002" {
		t.Fatalf("second request: %d %s", again.status, again.body)
	}
	if reasons := f.count(`SELECT COUNT(*) FROM leads WHERE id = 2 AND spam_reasons LIKE '%уже использовано%'`); reasons != 1 {
		t.Error("a replayed proof was not noticed")
	}

	// A client who left a phone gets no automatic email.
	phone := validValues()
	phone.Set("contact_method", "phone")
	phone.Set("contact_value", "+380 67 123 45 67")
	phone.Set("altcha", f.proof())
	f.now = f.now.Add(time.Minute)
	if got := f.do(http.MethodPost, "/api/leads", phone, asJSON); got.status != http.StatusCreated {
		t.Fatalf("third request: %d %s", got.status, got.body)
	}
	if n := f.count(`SELECT COUNT(*) FROM outbox WHERE lead_id = 3 AND kind = 'lead.autoreply'`); n != 0 {
		t.Error("an automatic reply was queued for a phone number")
	}

	// Three an hour from one address (brief B10.1).
	limited := f.do(http.MethodPost, "/api/leads", phone, asJSON)
	if limited.status != http.StatusTooManyRequests || limited.json["error"] != "rate_limited" {
		t.Errorf("fourth request within the hour: %d %s", limited.status, limited.body)
	}
	if other := f.do(http.MethodPost, "/api/leads", phone, map[string]string{"Accept": "application/json", "X-Real-IP": "198.51.100.20"}); other.status != http.StatusCreated {
		t.Errorf("another visitor is limited too: %d", other.status)
	}
}

func TestSpamIsStoredQuietly(t *testing.T) {
	f := newFixture(t)
	values := validValues()
	values.Set("website", "https://buy-traffic.example") // the honeypot
	got := f.do(http.MethodPost, "/api/leads", values, asJSON)
	// The bot learns nothing: same answer as for everybody…
	if got.status != http.StatusCreated || got.json["id"] != "K-0001" {
		t.Fatalf("response: %d %s", got.status, got.body)
	}
	if _, offered := got.json["telegram_url"]; offered {
		t.Error("a spammer is invited to the bot")
	}
	// …but nobody is woken up.
	if status := f.count(`SELECT COUNT(*) FROM leads WHERE status = 'spam' AND spam_score = 100 AND spam_reasons LIKE '%скрытое поле%'`); status != 1 {
		t.Error("the request is not filed as spam")
	}
	if tasks := f.count(`SELECT COUNT(*) FROM outbox`); tasks != 0 {
		t.Errorf("%d notifications queued for spam", tasks)
	}
	if events := f.count(`SELECT COUNT(*) FROM lead_events WHERE to_status = 'spam' AND details LIKE 'похоже на спам%'`); events != 1 {
		t.Error("the history does not say why")
	}
}

func TestInvalidAndForeignRequests(t *testing.T) {
	f := newFixture(t)

	values := validValues()
	values.Set("contact_value", "not-an-email")
	values.Del("consent")
	got := f.do(http.MethodPost, "/api/leads", values, asJSON)
	errs, _ := got.json["errors"].(map[string]any)
	if got.status != http.StatusUnprocessableEntity || errs["contact_value"] != ErrInvalidEmail || errs["consent"] != ErrConsentRequired {
		t.Errorf("invalid form: %d %s", got.status, got.body)
	}
	// Mistakes do not use up the hourly limit.
	for range 5 {
		f.do(http.MethodPost, "/api/leads", values, asJSON)
	}
	if got := f.do(http.MethodPost, "/api/leads", validValues(), asJSON); got.status != http.StatusCreated {
		t.Errorf("a correct request after five mistakes: %d", got.status)
	}

	if got := f.do(http.MethodPost, "/api/leads", validValues(), map[string]string{"Origin": "https://evil.example", "Accept": "application/json"}); got.status != http.StatusForbidden {
		t.Errorf("a form posted from another site: %d", got.status)
	}
	huge := validValues()
	huge.Set("description", strings.Repeat("я", 40_000))
	if got := f.do(http.MethodPost, "/api/leads", huge, asJSON); got.status != http.StatusRequestEntityTooLarge && got.status != http.StatusBadRequest {
		t.Errorf("an oversized form: %d", got.status)
	}
	if n := f.count(`SELECT COUNT(*) FROM leads`); n != 1 {
		t.Errorf("%d requests stored, want 1", n)
	}
}

func TestTheFormWorksWithoutJavaScript(t *testing.T) {
	f := newFixture(t)
	page := `<html lang="ru"><body><h1><span class="thanks-generic %%GENERIC_CLASS%%">Заявка принята</span>` +
		`<span class="thanks-numbered %%NUMBERED_CLASS%%">Заявка #%%LEAD_NUMBER%% принята</span></h1>` +
		`<p class="thanks-telegram %%TELEGRAM_CLASS%%"><a href="%%TELEGRAM_URL%%">Продолжить в Telegram</a></p></body></html>`
	if err := os.MkdirAll(filepath.Join(f.www, "current", "ru", "thanks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.www, "current", "ru", "thanks", "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}

	// A plain form post: redirected to a page of its own, so that «reload» does not send it twice.
	got := f.do(http.MethodPost, "/api/leads", validValues(), nil)
	if got.status != http.StatusSeeOther || !strings.HasPrefix(got.location, "/api/leads/thanks?t=") {
		t.Fatalf("plain POST: %d → %q", got.status, got.location)
	}
	thanks := f.do(http.MethodGet, got.location, nil, nil)
	if thanks.status != http.StatusOK || !strings.Contains(thanks.body, `<span class="thanks-numbered is-shown">Заявка #K-0001 принята</span>`) ||
		!strings.Contains(thanks.body, `<span class="thanks-generic is-hidden">`) || !strings.Contains(thanks.body, `class="thanks-telegram is-shown"`) ||
		!strings.Contains(thanks.body, `href="https://t.me/krokosha_bot?start=c_`) || strings.Contains(thanks.body, "%%") {
		t.Errorf("thank-you page: %d %s", thanks.status, thanks.body)
	}
	// It passed without a proof of work — noted, not punished.
	if n := f.count(`SELECT COUNT(*) FROM leads WHERE status = 'new' AND spam_reasons LIKE '%без JavaScript%'`); n != 1 {
		t.Error("a request without JavaScript must be accepted and marked")
	}

	// Nobody can leaf through other people's requests.
	for _, token := range []string{"", "AAAAAAAAAAAAAAAAAAAAAA", "../../etc/passwd"} {
		if got := f.do(http.MethodGet, "/api/leads/thanks?t="+url.QueryEscape(token), nil, nil); got.status != http.StatusSeeOther || got.location != "/thanks/" {
			t.Errorf("token %q: %d → %q", token, got.status, got.location)
		}
	}

	// Mistakes lead back to the form, to the block that explains them (shown by CSS :target).
	invalid := validValues()
	invalid.Set("description", "коротко")
	if got := f.do(http.MethodPost, "/api/leads", invalid, nil); got.status != http.StatusSeeOther || got.location != "/ru/#form-error-invalid" {
		t.Errorf("invalid plain POST: %d → %q", got.status, got.location)
	}

	// English has no page in this pretend release: the visitor still gets a «thank you».
	english := validValues()
	english.Set("lang", "en")
	english.Set("contact_method", "telegram")
	english.Set("contact_value", "@ivan_petrov")
	redirect := f.do(http.MethodPost, "/api/leads", english, nil)
	if got := f.do(http.MethodGet, redirect.location, nil, nil); got.status != http.StatusSeeOther || got.location != "/thanks/" {
		t.Errorf("without the built page: %d → %q", got.status, got.location)
	}
}

func TestDisabledForm(t *testing.T) {
	f := newFixture(t)
	disabled := NewHandler(Options{Store: NewStore(f.db, nil), Log: quiet, Secret: secret, Sessions: noSessions{},
		Form: func() config.Form { return config.Form{} }})
	mux := http.NewServeMux()
	disabled.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/leads", strings.NewReader(validValues().Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("a disabled form answered %d", recorder.Code)
	}
}
