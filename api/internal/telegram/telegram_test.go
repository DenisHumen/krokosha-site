package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram/tgtest"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

var (
	quiet  = slog.New(slog.DiscardHandler)
	noon   = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	secret = []byte("0123456789abcdef0123456789abcdef")

	denis  = User{ID: 1001, FirstName: "Денис", LastName: "Гумен", Username: "DenisHumen", LanguageCode: "ru"}
	olena  = User{ID: 1002, FirstName: "Олена", Username: "olena_k", LanguageCode: "uk"}
	client = User{ID: 2001, FirstName: "John", LanguageCode: "en"}
)

type fixture struct {
	t       *testing.T
	db      *sql.DB
	api     *tgtest.Server
	access  *Access
	bot     *Bot
	now     time.Time
	audit   []string
	update  int64
	kicks   int // how many times the bot told the outbox there is something to send
	hurried int // how many times it made waiting cards due at once
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
	limits, err := cache.New(ctx, "", quiet)
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{t: t, db: pool, api: tgtest.Start(t), now: noon}
	clock := func() time.Time { return f.now }
	limits.SetClock(clock)
	f.access = NewAccess(pool, clock)
	f.bot = New(Options{
		API: NewClient(tgtest.Token, f.api.URL), Access: f.access, Cache: limits, Log: quiet, SiteURL: "https://krokosha.xyz", Now: clock,
		Audit: func(_ context.Context, actor, action, subject, details string) {
			f.audit = append(f.audit, strings.TrimSpace(actor+" "+action+" "+subject+" "+details))
		},
	})
	f.bot.opts.Hurry = func(context.Context) { f.hurried++ }
	me := User{ID: 123456, IsBot: true, Username: "krokosha_test_bot"}
	f.bot.me.Store(&me)
	return f
}

// says sends a private message to the bot and returns what the bot answered in that chat.
func (f *fixture) says(who User, text string) []tgtest.Call {
	f.t.Helper()
	f.api.Forget()
	f.update++
	f.bot.Handle(context.Background(), Update{UpdateID: f.update, Message: &Message{
		MessageID: f.update, From: &who, Chat: Chat{ID: who.ID, Type: "private"}, Text: text, Date: f.now.Unix(),
	}})
	return f.api.Sent(who.ID)
}

// presses presses an inline button and returns all the calls the bot made.
func (f *fixture) presses(who User, data string) []tgtest.Call {
	f.t.Helper()
	f.api.Forget()
	f.update++
	f.bot.Handle(context.Background(), Update{UpdateID: f.update, CallbackQuery: &CallbackQuery{
		ID: fmt.Sprint("cb", f.update), From: who, Data: data, Message: &Message{MessageID: 500, Chat: Chat{ID: who.ID, Type: "private"}},
	}})
	return f.api.Calls("")
}

// join lets a person in with a fresh invitation.
func (f *fixture) join(who User, role string) {
	f.t.Helper()
	code, _, err := f.access.Invite(context.Background(), role, "test")
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.access.Redeem(context.Background(), code, who); err != nil {
		f.t.Fatal(err)
	}
}

func oneText(t *testing.T, calls []tgtest.Call) string {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("messages sent: %d, want 1: %+v", len(calls), calls)
	}
	return calls[0].Text()
}

// --- the API client -----------------------------------------------------------------------------

func TestTheTokenNeverShowsInErrors(t *testing.T) {
	api := tgtest.Start(t)
	ctx := context.Background()

	if me, err := NewClient(tgtest.Token, api.URL).GetMe(ctx); err != nil || me.Username != "krokosha_test_bot" {
		t.Fatalf("getMe: %+v %v", me, err)
	}
	// Telegram says no.
	_, err := NewClient("999:wrong-token-wrong-token-wrong-token", api.URL).GetMe(ctx)
	var refused *APIError
	if !errors.As(err, &refused) || refused.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong token: %v", err)
	}
	// Nobody answers at all: net/http quotes the address — with the token in it — in its errors.
	_, err = NewClient(tgtest.Token, "http://127.0.0.1:1").GetMe(ctx)
	if err == nil || strings.Contains(err.Error(), tgtest.Token) || !strings.Contains(err.Error(), "<token>") {
		t.Errorf("an unreachable API: %v", err)
	}
	// «Wait and come again» carries the pause.
	api.Refuse("sendMessage", 1, http.StatusTooManyRequests, "Too Many Requests: retry after 7", 7)
	_, err = NewClient(tgtest.Token, api.URL).Send(ctx, Outgoing{ChatID: 1, Text: "x"})
	if !errors.As(err, &refused) || refused.RetryAfter != 7*time.Second || refused.Gone() {
		t.Errorf("429: %+v", err)
	}
	api.Refuse("sendMessage", 1, http.StatusForbidden, "Forbidden: bot was blocked by the user", 0)
	if _, err = NewClient(tgtest.Token, api.URL).Send(ctx, Outgoing{ChatID: 1, Text: "x"}); !errors.As(err, &refused) || !refused.Gone() {
		t.Errorf("a blocked bot: %+v", err)
	}
	// An edit that changes nothing is not a failure.
	api.Refuse("editMessageText", 1, http.StatusBadRequest, "Bad Request: message is not modified", 0)
	if err := NewClient(tgtest.Token, api.URL).Edit(ctx, 5, Outgoing{ChatID: 1, Text: "x"}); err != nil {
		t.Errorf("an edit without changes: %v", err)
	}
}

func TestMessagesAreHTMLWithoutPreviews(t *testing.T) {
	api := tgtest.Start(t)
	client := NewClient(tgtest.Token, api.URL)
	sent, err := client.Send(context.Background(), Outgoing{ChatID: 42, Text: "<b>" + Escape("Tom & <Jerry>") + "</b>",
		Buttons: Keyboard{{{Text: "Взять", Data: "l:take:1"}, {Text: "В админке", URL: "https://krokosha.xyz/_x/leads/1"}}}})
	if err != nil || sent.MessageID == 0 {
		t.Fatalf("send: %+v %v", sent, err)
	}
	call := api.Calls("sendMessage")[0]
	if call.Text() != "<b>Tom &amp; &lt;Jerry&gt;</b>" || call.Params["parse_mode"] != "HTML" {
		t.Errorf("the message: %+v", call.Params)
	}
	if preview, _ := call.Params["link_preview_options"].(map[string]any); preview["is_disabled"] != true {
		t.Error("links would unfold into previews")
	}
	labels, data := call.Buttons()
	if fmt.Sprint(labels) != "[Взять В админке]" || data["Взять"] != "l:take:1" || data["В админке"] != "https://krokosha.xyz/_x/leads/1" {
		t.Errorf("buttons: %v %v", labels, data)
	}
}

// --- access -------------------------------------------------------------------------------------

func TestAnInvitationLetsOnePersonInOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	code, expires, err := f.access.Invite(ctx, RoleMember, "denis")
	if err != nil || !strings.HasPrefix(code, InvitePrefix) || len(code) != len(InvitePrefix)+20 || !expires.Equal(noon.Add(24*time.Hour)) {
		t.Fatalf("invite: %q %v %v", code, expires, err)
	}
	// The database cannot give the code away: it holds a hash.
	var stored []byte
	if err := f.db.QueryRow(`SELECT code_hash FROM bot_invites`).Scan(&stored); err != nil || bytes.Contains(stored, []byte(code)) || len(stored) != 32 {
		t.Errorf("what is stored of the code: %x %v", stored, err)
	}
	if _, _, err := f.access.Invite(ctx, "admin", "denis"); !errors.Is(err, ErrBadRole) {
		t.Errorf("a made-up role: %v", err)
	}

	// Two people race with the same code: one gets in.
	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = f.access.Redeem(ctx, strings.ToUpper(code)+" ", User{ID: int64(3000 + i), FirstName: fmt.Sprint("Racer ", i)})
		}()
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrBadInvite) {
			t.Errorf("a loser of the race: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("people let in by one code: %d", winners)
	}

	for name, tc := range map[string]struct {
		code string
		who  User
	}{
		"used":          {code, olena},
		"made up":       {InvitePrefix + "aaaaaaaaaaaaaaaaaaaa", olena},
		"a client link": {ClientPrefix + "AAAAAAAAAAAAAAAAAAAAAA", olena},
		"empty":         {"", olena},
		"another bot":   {mustInvite(t, f), User{ID: 77, IsBot: true, FirstName: "Bot"}},
	} {
		if _, err := f.access.Redeem(ctx, tc.code, tc.who); !errors.Is(err, ErrBadInvite) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A day later the code is worth nothing.
	late := mustInvite(t, f)
	f.now = f.now.Add(24*time.Hour + time.Second)
	if _, err := f.access.Redeem(ctx, late, olena); !errors.Is(err, ErrBadInvite) {
		t.Errorf("an expired invitation: %v", err)
	}
	if pending, err := f.access.Invites(ctx); err != nil || len(pending) != 0 {
		t.Errorf("invitations that can still be used: %+v %v", pending, err)
	}
}

func mustInvite(t *testing.T, f *fixture) string {
	t.Helper()
	code, _, err := f.access.Invite(context.Background(), RoleMember, "test")
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestPeopleAreBoundByIDNotByName(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.join(denis, RoleOwner)
	f.join(olena, RoleMember)

	// Somebody else takes the user name: it opens nothing.
	impostor := User{ID: 6666, FirstName: "Денис", LastName: "Гумен", Username: denis.Username}
	if _, err := f.access.Member(ctx, impostor.ID); !errors.Is(err, ErrNoAccess) {
		t.Errorf("an impostor with the owner's user name: %v", err)
	}
	member, err := f.access.Member(ctx, olena.ID)
	if err != nil || member.Name != "Олена" || member.Username != "olena_k" || member.Owner() {
		t.Fatalf("a member: %+v %v", member, err)
	}

	// Revoking works at once; the last owner stays.
	if _, err := f.access.SetDisabled(ctx, member.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.access.Member(ctx, olena.ID); !errors.Is(err, ErrNoAccess) {
		t.Errorf("after revoking: %v", err)
	}
	owner, _ := f.access.Member(ctx, denis.ID)
	if _, err := f.access.SetDisabled(ctx, owner.ID, true); !errors.Is(err, ErrLastOwner) {
		t.Errorf("switching the only owner off: %v", err)
	}
	if recipients, _ := f.access.Recipients(ctx); len(recipients) != 1 || recipients[0].TelegramID != denis.ID {
		t.Errorf("who gets requests: %+v", recipients)
	}
	// A new invitation gives access back — the owner sent it, after all — with its role.
	f.join(olena, RoleOwner)
	if member, err = f.access.Member(ctx, olena.ID); err != nil || !member.Owner() {
		t.Errorf("after a new invitation: %+v %v", member, err)
	}
	if all, _ := f.access.Members(ctx); len(all) != 2 {
		t.Errorf("members: %d, want 2 (nobody is doubled)", len(all))
	}
}

// --- the bot ------------------------------------------------------------------------------------

func TestStrangersLearnNothing(t *testing.T) {
	f := newFixture(t)
	f.join(denis, RoleOwner)

	// A passer-by is greeted in the language of their Telegram — once in ten minutes.
	text := oneText(t, f.says(client, "/start"))
	if !strings.Contains(text, "Continue in Telegram") || !strings.Contains(text, "https://krokosha.xyz/#contacts") {
		t.Errorf("the greeting: %q", text)
	}
	for _, attempt := range []string{"/users", "/invite", "/help", "/lead K-0001", "hello?", "/start c_AAAAAAAAAAAAAAAAAAAAAA"} {
		if got := f.says(client, attempt); len(got) != 0 {
			t.Errorf("%q was answered: %+v", attempt, got)
		}
	}
	f.now = f.now.Add(11 * time.Minute)
	if text := oneText(t, f.says(User{ID: client.ID, LanguageCode: "uk"}, "привіт")); !strings.Contains(text, "Продовжити в Telegram") {
		t.Errorf("the greeting in Ukrainian: %q", text)
	}
	// Buttons of somebody else's card do nothing for them.
	calls := f.presses(client, "u:off:1")
	if len(calls) != 1 || calls[0].Method != "answerCallbackQuery" || calls[0].Params["text"] != "Нет доступа." {
		t.Errorf("a stranger pressed a button: %+v", calls)
	}
	// In a group the bot is silent even for the owner: a card there would be shown to everybody.
	f.api.Forget()
	f.bot.Handle(context.Background(), Update{UpdateID: 900, Message: &Message{From: &denis, Chat: Chat{ID: -100500, Type: "group"}, Text: "/users"}})
	if got := f.api.Calls(""); len(got) != 0 {
		t.Errorf("the bot spoke in a group: %+v", got)
	}

	// Guessing codes: five tries an hour, then silence.
	for i := range 7 {
		got := f.says(User{ID: 4040}, "/start "+InvitePrefix+fmt.Sprintf("%020d", i))
		if want := 1; i >= 5 {
			if len(got) != 0 {
				t.Errorf("try %d was answered", i+1)
			}
		} else if len(got) != want || !strings.Contains(got[0].Text(), "не подходит") {
			t.Errorf("try %d: %+v", i+1, got)
		}
	}
}

func TestJoiningAndManagingAccess(t *testing.T) {
	f := newFixture(t)
	f.join(denis, RoleOwner)

	// The owner makes an invitation in the bot…
	text := oneText(t, f.says(denis, "/invite"))
	link := "https://t.me/krokosha_test_bot?start=" + InvitePrefix
	at := strings.Index(text, link)
	if at < 0 || !strings.Contains(text, "24 часа") {
		t.Fatalf("the invitation: %q", text)
	}
	code := text[at+len(link)-len(InvitePrefix):][:len(InvitePrefix)+20]

	// …a colleague opens the link.
	welcome := oneText(t, f.says(olena, "/start "+code))
	if !strings.Contains(welcome, "Доступ открыт, Олена") || strings.Contains(welcome, "/invite") {
		t.Errorf("the welcome of a member: %q", welcome)
	}
	// Requests that came while nobody could receive them go out now, not at the queue's next look.
	if f.hurried != 1 {
		t.Errorf("waiting cards were hurried %d times, want once", f.hurried)
	}
	if text := oneText(t, f.says(olena, "/help@krokosha_test_bot")); strings.Contains(text, "/users") {
		t.Errorf("a member is offered the owner's commands: %q", text)
	}
	for _, command := range []string{"/invite", "/users"} {
		if text := oneText(t, f.says(olena, command)); !strings.Contains(text, "владел") {
			t.Errorf("%s by a member: %q", command, text)
		}
	}

	// The owner sees everybody and switches the colleague off with a button.
	list := f.says(denis, "/users")
	labels, data := list[0].Buttons()
	if !strings.Contains(oneText(t, list), "Денис Гумен (@DenisHumen) — владелец") || len(labels) != 2 || data["Отключить: Олена"] == "" {
		t.Fatalf("the list: %q %v", list[0].Text(), labels)
	}
	if calls := f.presses(olena, data["Отключить: Олена"]); len(calls) != 1 || calls[0].Params["show_alert"] != true {
		t.Errorf("a member pressed the owner's button: %+v", calls)
	}
	calls := f.presses(denis, data["Отключить: Олена"])
	if len(calls) != 2 || calls[0].Params["text"] != "Доступ отключён: Олена" || calls[1].Method != "editMessageText" ||
		!strings.Contains(calls[1].Text(), "Олена (@olena_k) — <i>доступ отключён</i>") {
		t.Fatalf("switching a member off: %+v", calls)
	}
	// From this moment she is a stranger.
	if text := oneText(t, f.says(olena, "/help")); !strings.Contains(text, "Вітаю") {
		t.Errorf("after revoking: %q", text)
	}
	_, data = calls[1].Buttons()
	if calls := f.presses(denis, data["Вернуть доступ: Олена"]); calls[0].Params["text"] != "Доступ возвращён: Олена" {
		t.Errorf("giving access back: %+v", calls)
	}
	owner, _ := f.access.Member(context.Background(), denis.ID)
	if calls := f.presses(denis, fmt.Sprint("u:off:", owner.ID)); !strings.Contains(fmt.Sprint(calls[0].Params["text"]), "Единственного владельца") {
		t.Errorf("the only owner switched themselves off: %+v", calls)
	}
	if calls := f.presses(denis, "x:unknown"); calls[0].Params["text"] != "Эта кнопка больше не работает." {
		t.Errorf("an unknown button: %+v", calls)
	}

	want := []string{
		"bot:Денис Гумен bot.invite member", "bot:Олена bot.join 1002 member, пригласил(а) bot:Денис Гумен",
		"bot:Денис Гумен bot.disable 1002 Олена", "bot:Денис Гумен bot.enable 1002 Олена",
	}
	if fmt.Sprint(f.audit) != fmt.Sprint(want) {
		t.Errorf("the journal:\n%q\nwant\n%q", f.audit, want)
	}
	// Names from Telegram are text, not markup.
	f.join(User{ID: 5005, FirstName: "<b>Bold</b> & Co"}, RoleMember)
	if text := oneText(t, f.says(denis, "/users")); !strings.Contains(text, "&lt;b&gt;Bold&lt;/b&gt; &amp; Co") {
		t.Errorf("a name with markup: %q", text)
	}
}

// --- receiving updates --------------------------------------------------------------------------

func (f *fixture) runner(t *testing.T, mode string) (*Runner, http.Handler) {
	t.Helper()
	runner := NewRunner(f.bot, RunnerOptions{Mode: mode, SiteURL: "https://krokosha.xyz/", Secret: secret, Log: quiet, RetryPause: 5 * time.Millisecond})
	mux := http.NewServeMux()
	runner.Register(mux)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return runner, mux
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if condition() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWebhook(t *testing.T) {
	f := newFixture(t)
	f.join(denis, RoleOwner)
	f.api.Refuse("getMe", 2, http.StatusBadGateway, "Bad Gateway", 0) // Telegram is unwell at first: the runner keeps trying
	runner, handler := f.runner(t, ModeWebhook)
	waitFor(t, "the connection", func() bool { return runner.Status().Username == "krokosha_test_bot" })

	pathSecret, headerSecret := Secrets(secret)
	hook := f.api.Calls("setWebhook")
	if len(hook) != 1 || hook[0].Params["url"] != "https://krokosha.xyz/api/telegram/"+pathSecret || hook[0].Params["secret_token"] != headerSecret ||
		fmt.Sprint(hook[0].Params["allowed_updates"]) != "[message callback_query]" {
		t.Fatalf("setWebhook: %+v", hook)
	}
	if len(pathSecret) != 32 || len(headerSecret) != 64 || strings.Contains(headerSecret, pathSecret) {
		t.Errorf("the secrets: %q %q", pathSecret, headerSecret)
	}
	if status := runner.Status(); status.LastError != "" || status.ConnectedAt.IsZero() {
		t.Errorf("status after connecting: %+v", status)
	}

	deliver := func(path, header string, body any) int {
		raw, _ := json.Marshal(body)
		if text, ok := body.(string); ok {
			raw = []byte(text)
		}
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		if header != "" {
			request.Header.Set("X-Telegram-Bot-Api-Secret-Token", header)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	update := Update{UpdateID: 77, Message: &Message{MessageID: 1, From: &denis, Chat: Chat{ID: denis.ID, Type: "private"}, Text: "/help"}}
	good := WebhookPrefix + pathSecret

	for name, tc := range map[string]struct {
		path, header string
		want         int
	}{
		"a guessed address":      {WebhookPrefix + strings.Repeat("0", 32), headerSecret, http.StatusNotFound},
		"without the header":     {good, "", http.StatusForbidden},
		"with a wrong header":    {good, strings.Repeat("a", 64), http.StatusForbidden},
		"the path as the header": {good, pathSecret, http.StatusForbidden},
	} {
		if got := deliver(tc.path, tc.header, update); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
	if sent := f.api.Sent(denis.ID); len(sent) != 0 {
		t.Fatalf("a forged update was handled: %+v", sent)
	}

	if got := deliver(good, headerSecret, update); got != http.StatusOK {
		t.Fatalf("the genuine update: %d", got)
	}
	waitFor(t, "the answer", func() bool { return len(f.api.Sent(denis.ID)) == 1 })
	// Telegram repeats a delivery it believes has failed: the person must not get two answers.
	if got := deliver(good, headerSecret, update); got != http.StatusOK {
		t.Errorf("the repeated update: %d", got)
	}
	if got := deliver(good, headerSecret, "{not json"); got != http.StatusOK {
		t.Errorf("rubbish must be confirmed, or Telegram repeats it for days: %d", got)
	}
	update.UpdateID = 78
	_ = deliver(good, headerSecret, update)
	waitFor(t, "the second answer", func() bool { return len(f.api.Sent(denis.ID)) == 2 })
	if runner.Status().LastUpdateAt.IsZero() {
		t.Error("the status does not say when Telegram was last heard from")
	}
}

func TestLongPolling(t *testing.T) {
	f := newFixture(t)
	f.join(denis, RoleOwner)
	runner, _ := f.runner(t, ModePolling)
	waitFor(t, "the connection", func() bool { return runner.Status().Username != "" })
	if len(f.api.Calls("deleteWebhook")) != 1 || len(f.api.Calls("setWebhook")) != 0 {
		t.Fatalf("polling must switch the webhook off: %+v", f.api.Calls(""))
	}

	f.api.Push(Update{UpdateID: 501, Message: &Message{MessageID: 1, From: &denis, Chat: Chat{ID: denis.ID, Type: "private"}, Text: "/help"}})
	f.api.Push(Update{UpdateID: 502, Message: &Message{MessageID: 2, From: &denis, Chat: Chat{ID: denis.ID, Type: "private"}, Text: "/users"}})
	waitFor(t, "both answers", func() bool { return len(f.api.Sent(denis.ID)) == 2 })
	// The next request confirms what was handled: the offset moves past it.
	waitFor(t, "the confirmation", func() bool {
		calls := f.api.Calls("getUpdates")
		return len(calls) > 0 && calls[len(calls)-1].Params["offset"] == float64(503)
	})
	sent := f.api.Sent(denis.ID)
	if !strings.Contains(sent[0].Text(), "/help") || !strings.Contains(sent[1].Text(), "Доступ к боту") {
		t.Errorf("the answers came out of order: %q, %q", sent[0].Text(), sent[1].Text())
	}
	// Telegram hiccups: polling goes on.
	f.api.Refuse("getUpdates", 2, http.StatusBadGateway, "Bad Gateway", 0)
	f.api.Push(Update{UpdateID: 503, Message: &Message{MessageID: 3, From: &denis, Chat: Chat{ID: denis.ID, Type: "private"}, Text: "/help"}})
	waitFor(t, "the answer after the hiccup", func() bool { return len(f.api.Sent(denis.ID)) == 3 })
}

// TestAStoppingServiceLosesNoUpdate: what was taken is handled before the service stops — Telegram
// counts it as delivered —, and what comes after is refused, for the next process to get.
func TestAStoppingServiceLosesNoUpdate(t *testing.T) {
	f := newFixture(t)
	f.join(denis, RoleOwner)
	runner := NewRunner(f.bot, RunnerOptions{Mode: ModeWebhook, SiteURL: "https://krokosha.xyz/", Secret: secret, Log: quiet})
	mux := http.NewServeMux()
	runner.Register(mux)

	f.api.Forget()
	f.update++
	if !runner.accept(Update{UpdateID: f.update, Message: &Message{MessageID: 1, From: &denis, Chat: Chat{ID: denis.ID, Type: "private"}, Text: "/help", Date: f.now.Unix()}}) {
		t.Fatal("the update was not taken")
	}
	runner.drain()
	if len(f.api.Sent(denis.ID)) == 0 {
		t.Error("an update taken before the stop was not handled")
	}

	pathSecret, headerSecret := Secrets(secret)
	request := httptest.NewRequest(http.MethodPost, WebhookPrefix+pathSecret, strings.NewReader(`{"update_id": 99}`))
	request.Header.Set("X-Telegram-Bot-Api-Secret-Token", headerSecret)
	answer := httptest.NewRecorder()
	mux.ServeHTTP(answer, request)
	if answer.Code != http.StatusServiceUnavailable {
		t.Errorf("an update after the stop: %d, want 503 for Telegram to try the next process", answer.Code)
	}

	// Polling: Telegram is told what was handled, or the next start would get it again.
	polling := NewRunner(f.bot, RunnerOptions{Mode: ModePolling, SiteURL: "https://krokosha.xyz/", Secret: secret, Log: quiet})
	polling.offset = 43
	close(polling.polled)
	f.api.Forget()
	polling.drain()
	calls := f.api.Calls("getUpdates")
	if len(calls) != 1 || calls[0].Params["offset"] != float64(43) {
		t.Errorf("the confirmation: %+v", calls)
	}
}
