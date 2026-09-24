package clients

import (
	"context"
	"net/http"
	"strings"
	"testing"
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
