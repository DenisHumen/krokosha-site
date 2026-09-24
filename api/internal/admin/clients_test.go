package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

func testLoyalty() config.Loyalty {
	return config.Loyalty{Enabled: true, Currency: "USD", Welcome: 10, Eggs: 20, BigOrder: 3000, Tiers: []config.LoyaltyTier{
		{ID: "silver", Name: config.Localized{"en": "Silver", "ru": "Серебряный"}, Orders: 1, Spent: 1000, Discount: 5},
		{ID: "gold", Name: config.Localized{"en": "Gold", "ru": "Золотой"}, Orders: 3, Spent: 5000, Discount: 10},
	}}
}

func TestClientsListAndCard(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	id, err := s.clients.Create(ctx, `Ольга <script>alert(1)</script>`, "olga@company.com", "+380 67 123 45 67")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.AddContact(ctx, id, "linkedin", "https://www.linkedin.com/in/olga-k/"); err != nil {
		t.Fatal(err)
	}
	lead := s.addLead(func(sub *leads.Submission) { sub.ContactValue = "olga@company.com" }) // joins the account by its address
	s.signIn()

	list := s.do(http.MethodGet, prefix+"/clients", nil, nil)
	if list.status != http.StatusOK || !strings.Contains(list.body, "olga@company.com") || strings.Contains(list.body, "<script>alert(1)") {
		t.Fatalf("list: %d\n%s", list.status, list.body)
	}
	if found := s.do(http.MethodGet, prefix+"/clients?q=%2B380671234567", nil, nil); !strings.Contains(found.body, "olga@company.com") {
		t.Error("search by the phone found nothing")
	}
	card := s.do(http.MethodGet, prefix+"/clients/"+strconv.FormatInt(id, 10), nil, nil)
	// The request got the first-request discount; the next one will not.
	for _, want := range []string{`href="tel:&#43;380671234567"`, `https://www.linkedin.com/in/olga-k`, "#" + lead.Number(), "<td>10%</td>",
		"<dt>Скидка следующей заявки</dt><dd>нет</dd>", "&lt;script&gt;"} {
		if !strings.Contains(card.body, want) {
			t.Errorf("the card has no %q", want)
		}
	}
	if s.do(http.MethodGet, prefix+"/clients/999999", nil, nil).status != http.StatusNotFound {
		t.Error("a client who is not there")
	}
}

func TestWorkingWithAClient(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	id, err := s.clients.Create(ctx, "Пётр", "petr@company.com", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.clients.Create(ctx, "Пётр (Telegram)", "petr.tg@company.com", "")
	if err != nil {
		t.Fatal(err)
	}
	s.signIn()
	base := "/clients/" + strconv.FormatInt(id, 10)

	if got := s.post(base+"/discount", url.Values{"percent": {"25"}, "note": {"партнёрская"}, "once": {"on"}}); got.status != http.StatusSeeOther {
		t.Fatalf("discount: %d %s", got.status, got.body)
	}
	client, _ := s.clients.Get(ctx, id)
	if client.Personal.Percent != 25 || !client.Personal.Once || client.Personal.Note != "партнёрская" {
		t.Errorf("personal discount: %+v", client.Personal)
	}
	// The client's address typed by somebody who is not signed in: the request joins the account, but
	// the personal discount is not theirs to spend…
	typed := s.addLead(func(sub *leads.Submission) { sub.ContactValue = "petr@company.com" })
	if typed.ClientID != id || typed.Discount.Reason == "personal" {
		t.Errorf("a request with the client's address, signed out: #%d %+v", typed.ClientID, typed.Discount)
	}
	// …the client spends it, signed in.
	lead := s.addLead(func(sub *leads.Submission) {
		sub.ContactValue, sub.ClientID, sub.Trusted = "petr@company.com", id, true
	})
	if lead.Discount.Percent != 25 || lead.Discount.Reason != "personal" {
		t.Errorf("the next request: %+v", lead.Discount)
	}
	if got := s.post(base+"/email", url.Values{"email": {"petr.tg@company.com"}}); got.status != http.StatusConflict {
		t.Errorf("an address of another client: %d", got.status)
	}
	if got := s.post(base+"/block", url.Values{"block": {"1"}}); got.status != http.StatusSeeOther {
		t.Errorf("block: %d", got.status)
	}
	if card := s.do(http.MethodGet, prefix+base, nil, nil); !strings.Contains(card.body, "Заблокирован") {
		t.Error("the card does not say the account is blocked")
	}
	if got := s.post(base+"/merge", url.Values{"other": {"#" + strconv.FormatInt(other, 10)}}); got.status != http.StatusSeeOther {
		t.Errorf("merge: %d %s", got.status, got.body)
	}
	if got := s.post(base+"/delete", url.Values{"confirm": {strconv.FormatInt(id+100, 10)}}); got.status != http.StatusBadRequest {
		t.Errorf("delete with a wrong confirmation: %d", got.status)
	}
	if got := s.post(base+"/delete", url.Values{"confirm": {strconv.FormatInt(id, 10)}}); got.status != http.StatusSeeOther {
		t.Errorf("delete: %d", got.status)
	}
	var audit int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action LIKE 'client.%'`).Scan(&audit)
	if audit < 4 {
		t.Errorf("the journal has %d lines about the client", audit)
	}
}

func TestDiscountAndAmountOfARequest(t *testing.T) {
	s := newSite(t)
	lead := s.addLead(nil)
	s.signIn()
	path := "/leads/" + strconv.FormatInt(lead.ID, 10)
	if got := s.post(path+"/amount", url.Values{"amount": {"1 500,50"}}); got.status != http.StatusSeeOther {
		t.Fatalf("amount: %d %s", got.status, got.body)
	}
	if got := s.post(path+"/amount", url.Values{"amount": {"-5"}}); got.status != http.StatusBadRequest {
		t.Errorf("a negative sum: %d", got.status)
	}
	if got := s.post(path+"/discount", url.Values{"percent": {"25"}, "note": {"за отзыв"}}); got.status != http.StatusSeeOther {
		t.Fatalf("discount: %d %s", got.status, got.body)
	}
	if got := s.post(path+"/discount", url.Values{"percent": {"101"}}); got.status != http.StatusBadRequest {
		t.Errorf("101%%: %d", got.status)
	}
	stored, _ := s.leads.Get(context.Background(), lead.ID)
	if !stored.HasAmount || stored.Amount != 1500.5 || stored.Discount.Percent != 25 || stored.Discount.Reason != "manual" {
		t.Errorf("stored: amount %v %v, discount %+v", stored.HasAmount, stored.Amount, stored.Discount)
	}
	card := s.do(http.MethodGet, prefix+path, nil, nil)
	for _, want := range []string{"25% — вручную (за отзыв)", "1 500,50 $", "сумма заказа: 1500.5"} {
		if !strings.Contains(card.body, want) {
			t.Errorf("the card has no %q", want)
		}
	}
}

func TestAchievementsPage(t *testing.T) {
	s := newSite(t)
	for id, n := range map[string]int{"players": 40, "konami": 10, "sudo": 2} {
		if _, err := s.db.Exec(`INSERT INTO achievement_daily (day, id, n) VALUES ('2026-09-19', ?, ?)`, id, n); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.eggs.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.signIn()
	page := s.do(http.MethodGet, prefix+"/achievements", nil, nil)
	for _, want := range []string{"Пасхалки · редкость", "25.0%", "5.0%", "Игроков: 40", "За заказы · редкость", "Крупный проект",
		`<script type="module" src="/_test1234/static/achievements.js">`, `href="/_test1234/achievements" aria-current="page"`} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the page has no %q", want)
		}
	}
	// Every kind of banner, with the shares of the site where there are enough players.
	buttons := regexp.MustCompile(`data-toast="([^"]+)"`).FindAllStringSubmatch(page.body, -1)
	if len(buttons) != 7 {
		t.Fatalf("buttons: %d", len(buttons))
	}
	var common struct {
		Rarity string `json:"rarity"`
		Sound  string `json:"sound"`
		Rare   bool   `json:"rare"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(buttons[0][1])), &common); err != nil || common.Sound != "egg" || common.Rare {
		t.Errorf("the common banner: %+v %v", common, err)
	}
	for i, sound := range map[int]string{1: "rare", 2: "epic", 5: "epic"} {
		if !strings.Contains(html.UnescapeString(buttons[i][1]), `"sound":"`+sound+`"`) {
			t.Errorf("button %d does not sound %q: %s", i, sound, buttons[i][1])
		}
	}
	// The script and the banner it shows are served, as JavaScript.
	for _, file := range []string{"achievements.js", "achievement.js"} {
		got := s.do(http.MethodGet, prefix+"/static/"+file, nil, nil)
		if got.status != http.StatusOK || !strings.Contains(got.header.Get("Content-Type"), "javascript") {
			t.Errorf("%s: %d %s", file, got.status, got.header.Get("Content-Type"))
		}
	}
}

// The banner of the admin area's bench is the site's: the file is copied, and must stay the same.
func TestTheBenchShowsTheSitesBanner(t *testing.T) {
	site, err := os.ReadFile("../../../design/components/eggs/achievement.js")
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile("static/achievement.js")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(site, copied) {
		t.Fatal("api/internal/admin/static/achievement.js differs from design/components/eggs/achievement.js: copy it again")
	}
}

// A request is given to a client by the client's number or whole address — a part of an address
// may be somebody else's — and a request in another client's account stays there until detached.
func TestAttachingARequest(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	anna, err := s.clients.Create(ctx, "Анна", "anna@company.com", "")
	if err != nil {
		t.Fatal(err)
	}
	joanna, err := s.clients.Create(ctx, "Иоанна", "joanna@company.com", "")
	if err != nil {
		t.Fatal(err)
	}
	s.signIn()
	lead := s.addLead(func(sub *leads.Submission) { sub.ContactValue = "boris@company.com" })
	path := "/leads/" + strconv.FormatInt(lead.ID, 10) + "/client"
	owner := func() int64 {
		t.Helper()
		got, err := s.leads.Get(ctx, lead.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.ClientID
	}
	if got := s.post(path, url.Values{"client": {"oanna@company.com"}}); got.status != http.StatusBadRequest || owner() != 0 {
		t.Errorf("a part of an address: %d, the request is #%d's", got.status, owner())
	}
	if got := s.post(path, url.Values{"client": {"Anna@Company.com"}}); got.status != http.StatusSeeOther || owner() != anna {
		t.Fatalf("the whole address: %d, the request is #%d's", got.status, owner())
	}
	moveTo := url.Values{"client": {"#" + strconv.FormatInt(joanna, 10)}}
	if got := s.post(path, moveTo); got.status != http.StatusConflict || owner() != anna {
		t.Errorf("into another account at once: %d, the request is #%d's", got.status, owner())
	}
	if got := s.post(path, url.Values{"detach": {"1"}}); got.status != http.StatusSeeOther || owner() != 0 {
		t.Errorf("detach: %d, the request is #%d's", got.status, owner())
	}
	if got := s.post(path, moveTo); got.status != http.StatusSeeOther || owner() != joanna {
		t.Errorf("a detached request given to another client: %d, the request is #%d's", got.status, owner())
	}
}
