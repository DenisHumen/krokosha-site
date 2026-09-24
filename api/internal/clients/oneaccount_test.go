package clients

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/DenisHumen/krokosha-site/api/internal/mail"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// signInByTelegram goes the whole way with the bot: asks for the link, «opens» it as a Telegram
// account, types the code the bot would show.
func (b *browser) signInByTelegram(telegramID int64, name, username string) {
	b.f.t.Helper()
	got := b.send(http.MethodPost, "/api/account/login", map[string]string{"method": "telegram", "lang": "ru"})
	b.telegramCode(got, telegramID, name, username)
	b.me()
}

// addTelegram links a Telegram account to the signed-in account; the answer of the code is returned.
func (b *browser) addTelegram(telegramID int64, name, username string) answer {
	b.f.t.Helper()
	got := b.send(http.MethodPost, "/api/account/telegram", map[string]string{})
	return b.telegramCode(got, telegramID, name, username)
}

func (b *browser) telegramCode(started answer, telegramID int64, name, username string) answer {
	b.f.t.Helper()
	botURL, _ := started.body["bot_url"].(string)
	if started.status != http.StatusOK || botURL == "" {
		b.f.t.Fatalf("the link into the bot: %d %s", started.status, started.raw)
	}
	code, err := b.f.service.TelegramStart(context.Background(), strings.TrimPrefix(botURL, "https://t.me/krokosha_bot?start="), telegramID, name, username)
	if err != nil {
		b.f.t.Fatalf("the bot: %v", err)
	}
	return b.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": code.Code})
}

func idOf(t *testing.T, me map[string]any) float64 {
	t.Helper()
	client, _ := me["client"].(map[string]any)
	id, _ := client["id"].(float64)
	if id == 0 {
		t.Fatalf("no account: %v", me)
	}
	return id
}

// TestOneAccountWhicheverWayIn: an account with an address and a Telegram account is the same
// account whichever of the two a client signs in with, on any device.
func TestOneAccountWhicheverWayIn(t *testing.T) {
	f := newFixture(t)
	laptop := f.browser("203.0.113.20")
	laptop.signInByEmail("anna@northwind.example")
	anna := idOf(t, laptop.me())
	if got := laptop.addTelegram(555, "Анна", "anna_nw"); got.status != http.StatusOK || got.body["merged"] != false {
		t.Fatalf("linking Telegram: %d %s", got.status, got.raw)
	}

	phone := f.browser("203.0.113.21")
	phone.signInByTelegram(555, "Анна", "anna_nw")
	if id := idOf(t, phone.me()); id != anna {
		t.Errorf("signed in with Telegram: account %v, want %v", id, anna)
	}
	tablet := f.browser("203.0.113.22")
	tablet.signInByEmail("anna@northwind.example")
	if id := idOf(t, tablet.me()); id != anna {
		t.Errorf("signed in with the address: account %v, want %v", id, anna)
	}
	if client := phone.me()["client"].(map[string]any); client["email"] != "anna@northwind.example" || client["telegram"] != "@anna_nw" {
		t.Errorf("both ways on the account: %v", client)
	}
	var accounts int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM clients`).Scan(&accounts); err != nil || accounts != 1 {
		t.Errorf("accounts: %d (%v)", accounts, err)
	}
}

// TestTwoAccountsOfOnePersonBecomeOne: somebody who signed in once with the address and once with
// Telegram has two accounts; linking the second way to either of them proves both are theirs, and
// the other account joins — requests and all. A blocked account does not join.
func TestTwoAccountsOfOnePersonBecomeOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	phone := f.browser("203.0.113.30")
	phone.signInByTelegram(556, "Олег", "oleg_net")
	byTelegram := idOf(t, phone.me())
	f.submit(phone, "oleg@example.com", nil) // a request while signed in with Telegram

	laptop := f.browser("203.0.113.31")
	laptop.signInByEmail("oleg@example.com")
	byEmail := idOf(t, laptop.me())
	if byEmail == byTelegram {
		t.Fatal("the two sign-ins were one account from the start")
	}
	if got := laptop.addTelegram(556, "Олег", "oleg_net"); got.status != http.StatusOK || got.body["merged"] != true {
		t.Fatalf("linking the Telegram of the other account: %d %s", got.status, got.raw)
	}
	me := laptop.me()
	client := me["client"].(map[string]any)
	if idOf(t, me) != byEmail || client["email"] != "oleg@example.com" || client["telegram"] != "@oleg_net" {
		t.Errorf("the account after linking: %v", client)
	}
	var accounts, requests int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM clients`).Scan(&accounts); err != nil || accounts != 1 {
		t.Errorf("accounts after linking: %d (%v)", accounts, err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM leads WHERE client_id = ?`, int64(byEmail)).Scan(&requests); err != nil || requests != 1 {
		t.Errorf("the request of the other account: %d on this one (%v)", requests, err)
	}
	// The other account's session ended with it; Telegram now signs in to this one.
	if got := phone.send(http.MethodGet, "/api/account/me", nil); got.body["ok"] != false {
		t.Errorf("the session of the joined account: %s", got.raw)
	}
	again := f.browser("203.0.113.32")
	again.signInByTelegram(556, "Олег", "oleg_net")
	if id := idOf(t, again.me()); id != byEmail {
		t.Errorf("Telegram after linking: account %v, want %v", id, byEmail)
	}

	// The same the other way round: an account made with Telegram takes the address of an account
	// made with it.
	tg := f.browser("203.0.113.33")
	tg.signInByTelegram(557, "Ира", "ira_k")
	withTelegram := idOf(t, tg.me())
	mail := f.browser("203.0.113.34")
	mail.signInByEmail("ira@example.com")
	tg.send(http.MethodPost, "/api/account/email", map[string]string{"email": "ira@example.com"})
	if got := tg.send(http.MethodPost, "/api/account/login/code", map[string]string{"code": f.service.code(f.lastLogin())}); got.status != http.StatusOK {
		t.Fatalf("adding the address of the other account: %d %s", got.status, got.raw)
	}
	fresh := f.browser("203.0.113.35")
	fresh.signInByEmail("ira@example.com")
	if id := idOf(t, fresh.me()); id != withTelegram {
		t.Errorf("the address after adding it: account %v, want %v", id, withTelegram)
	}

	// A blocked account does not join anything.
	blocked := f.browser("203.0.113.36")
	blocked.signInByTelegram(558, "Спамер", "spammer")
	blockedID := int64(idOf(t, blocked.me()))
	if err := f.service.SetDisabled(ctx, blockedID, true); err != nil {
		t.Fatal(err)
	}
	someone := f.browser("203.0.113.37")
	someone.signInByEmail("someone@example.com")
	if got := someone.addTelegram(558, "Спамер", "spammer"); got.status != http.StatusConflict || got.body["error"] != "taken" {
		t.Errorf("the Telegram of a blocked account: %d %s", got.status, got.raw)
	}
	if _, err := f.service.Get(ctx, blockedID); err != nil {
		t.Errorf("the blocked account is gone: %v", err)
	}
}

// TestNoLinkAddsAWay: an address or a Telegram account is added to an account by its code only,
// typed in the browser that asked. Anybody signed in may ask to add somebody else's address — the
// letter goes to that address — and a link in it would work wherever it is clicked: the owner of the
// address, talked into clicking, would hand it, their requests and their own account over.
func TestNoLinkAddsAWay(t *testing.T) {
	f := newFixture(t)
	victim := f.browser("203.0.113.50")
	victim.signInByEmail("victim@example.com")
	victimID := idOf(t, victim.me())
	f.submit(victim, "victim@example.com", nil)

	attacker := f.browser("203.0.113.51")
	attacker.signInByEmail("attacker@example.com")
	if got := attacker.send(http.MethodPost, "/api/account/email", map[string]string{"email": "victim@example.com"}); got.status != http.StatusOK {
		t.Fatalf("asking to add an address: %d %s", got.status, got.raw)
	}
	l := f.lastLogin()

	// The letter has the code and says what the code does; it has no link.
	var sent []mail.Message
	mailer := &Mailer{Service: f.service, SiteHost: "krokosha.com", Deliver: func(_ context.Context, message mail.Message) error {
		sent = append(sent, message)
		return nil
	}}
	var task outbox.Task
	_ = f.db.QueryRow(`SELECT id, kind, payload FROM outbox WHERE kind = ? ORDER BY id DESC LIMIT 1`, TaskLogin).Scan(&task.ID, &task.Kind, &task.Payload)
	if err := mailer.Send(context.Background(), task); err != nil || len(sent) != 1 {
		t.Fatalf("the letter: %v, %d sent", err, len(sent))
	}
	letter := sent[0]
	for _, part := range []string{letter.Text, letter.HTML} {
		if strings.Contains(part, "#login=") || !strings.Contains(part, f.service.code(l)) {
			t.Errorf("a letter adding an address must have the code and no link:\n%s", part)
		}
		if !strings.Contains(part, loginTexts[l.lang]["joining"]) || !strings.Contains(part, loginTexts[l.lang]["ignore_add"]) {
			t.Errorf("the letter does not say that the code joins another account:\n%s", part)
		}
	}

	// Nor does a link made for it work, in the owner's browser or in the one that asked.
	token := f.service.linkToken(l)
	for _, b := range []*browser{victim, attacker, f.browser("203.0.113.52")} {
		if got := b.send(http.MethodPost, "/api/account/login/link", map[string]string{"token": token}); got.status != http.StatusUnprocessableEntity {
			t.Errorf("a link adding an address: %d %s", got.status, got.raw)
		}
	}
	if id := idOf(t, victim.me()); id != victimID {
		t.Errorf("the victim's account changed: %v, want %v", id, victimID)
	}
	if email := attacker.me()["client"].(map[string]any)["email"]; email != "attacker@example.com" {
		t.Errorf("the attacker's address: %v", email)
	}
	var theirs int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM leads WHERE client_id = ?`, int64(victimID)).Scan(&theirs); err != nil || theirs != 1 {
		t.Errorf("the victim's request: %d on their account (%v)", theirs, err)
	}

	// The bot gives the code of a Telegram account being linked without a link, and says whether
	// the code joins another account.
	owner := f.browser("203.0.113.53")
	owner.signInByTelegram(901, "Олег", "oleg_net")
	started := attacker.send(http.MethodPost, "/api/account/telegram", map[string]string{})
	botURL, _ := started.body["bot_url"].(string)
	code, err := f.service.TelegramStart(context.Background(), strings.TrimPrefix(botURL, "https://t.me/krokosha_bot?start="), 901, "Олег", "oleg_net")
	if err != nil || code.LinkURL != "" || !code.Adding || !code.Joining || len(code.Code) != 6 {
		t.Errorf("the bot's answer to a Telegram account being linked: %+v %v", code, err)
	}
	fresh := attacker.send(http.MethodPost, "/api/account/telegram", map[string]string{})
	botURL, _ = fresh.body["bot_url"].(string)
	if code, err := f.service.TelegramStart(context.Background(), strings.TrimPrefix(botURL, "https://t.me/krokosha_bot?start="), 902, "Ира", "ira_k"); err != nil || code.Joining {
		t.Errorf("a Telegram account of nobody: %+v %v", code, err)
	}
}
