package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

func letterFrom(from, messageID, subject, body string) []byte {
	return []byte(strings.ReplaceAll("From: "+from+"\nTo: "+testMailbox+"\nSubject: "+subject+"\nMessage-ID: <"+messageID+">\n"+
		"Content-Type: text/plain; charset=utf-8\n\n"+body+"\n", "\n", "\r\n"))
}

// TestLettersWithoutARequest: brief B10.5 — letters nobody could place wait for a person, who
// puts them into a request's conversation or throws them away.
func TestLettersWithoutARequest(t *testing.T) {
	s := newSiteWith(t, true)
	ctx := context.Background()
	lead := s.addLead(nil)
	s.mailbox.Deliver(letterFrom(`"<img src=x onerror=alert(1)>" <olena@else.test>`, "one@else.test", "Вопрос <script>alert(2)</script>",
		"Добрый день! Пишу по рекомендации.\n<b>Нужна</b> помощь с сетью."))
	s.mailbox.Deliver(letterFrom("shop@spam.test", "two@spam.test", "Купите трафик", "Дёшево."))
	if err := s.letters.CheckOnce(ctx); err != nil {
		t.Fatal(err)
	}
	s.signIn()

	page := s.do(http.MethodGet, prefix+"/inbox", nil, nil)
	if page.status != http.StatusOK {
		t.Fatalf("the page: %d", page.status)
	}
	for _, want := range []string{
		"Входящие без заявки", "olena@else.test", "Пишу по рекомендации.", "shop@spam.test", testMailbox, "30 дней",
		`<span class="tick-k">Входящие</span><span class="tick-v">2</span>`, // in the header
		`<option value="K-0001">Иван Петров · Сети и оборудование</option>`,
		"&lt;b&gt;Нужна&lt;/b&gt; помощь",
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page.body, "<img src=x") || strings.Contains(page.body, "<script>alert") || strings.Contains(page.body, "<b>Нужна") {
		t.Error("what a stranger wrote reached the page unescaped")
	}

	// Into the conversation of a request: the number is typed the way people type it.
	if got := s.post("/inbox/1/attach", url.Values{"lead": {"к-1"}}); got.status != http.StatusBadRequest || !strings.Contains(got.body, "Введите номер заявки") {
		t.Errorf("a number that is none: %d", got.status)
	}
	if got := s.post("/inbox/1/attach", url.Values{"lead": {"#K-0042"}}); got.status != http.StatusBadRequest || !strings.Contains(got.body, "Заявки K-0042 нет") {
		t.Errorf("a request that does not exist: %d", got.status)
	}
	got := s.post("/inbox/1/attach", url.Values{"lead": {" #k-0001 "}})
	if got.status != http.StatusSeeOther || got.location != prefix+"/leads/1?ok=letter-attached" {
		t.Fatalf("attaching: %d → %s", got.status, got.location)
	}
	card := s.do(http.MethodGet, got.location, nil, nil)
	for _, want := range []string{"Письмо перенесено в переписку заявки.", "Пишу по рекомендации.", "привязано вручную: denis", "с адреса olena@else.test"} {
		if !strings.Contains(card.body, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	if stored, _ := s.leads.Card(ctx, lead.ID); len(stored.Feed) == 0 || stored.Lead.Status != leads.StatusNew {
		t.Errorf("the request after a letter: %+v", stored.Lead.Status)
	}
	if again := s.post("/inbox/1/attach", url.Values{"lead": {"1"}}); again.status != http.StatusConflict {
		t.Errorf("attaching twice: %d", again.status)
	}

	// Away with the other one.
	if got := s.post("/inbox/2/discard", url.Values{}); got.status != http.StatusSeeOther || got.location != prefix+"/inbox?ok=letter-discarded" {
		t.Fatalf("discarding: %d → %s", got.status, got.location)
	}
	empty := s.do(http.MethodGet, prefix+"/inbox?ok=letter-discarded", nil, nil)
	if !strings.Contains(empty.body, "Писем без заявки нет.") || !strings.Contains(empty.body, "Письмо удалено.") || !strings.Contains(empty.body, `<span class="tick-k">Входящие</span><span class="tick-v">пусто</span>`) {
		t.Error("the page after both letters were dealt with")
	}
	if left := s.mailbox.Messages(); len(left) != 0 {
		t.Errorf("letters left in the mailbox: %d", len(left))
	}
	var actions []string
	rows, err := s.db.Query(`SELECT CONCAT(action, ' ', COALESCE(subject, '')) FROM audit_log WHERE action LIKE 'inbox.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var action string
		_ = rows.Scan(&action)
		actions = append(actions, action)
	}
	if strings.Join(actions, ", ") != "inbox.attach K-0001, inbox.discard " {
		t.Errorf("the audit log: %q", actions)
	}

	// The status screen knows about the mailbox.
	status := s.do(http.MethodGet, prefix+"/status", nil, nil)
	if !strings.Contains(status.body, "Входящая почта") || !strings.Contains(status.body, testMailbox) {
		t.Error("the status screen says nothing about incoming mail")
	}

	// A forged form changes nothing.
	if got := s.do(http.MethodPost, prefix+"/inbox/1/discard", url.Values{"csrf": {"made-up"}}, nil); got.status != http.StatusForbidden {
		t.Errorf("a form without the token: %d", got.status)
	}
}

func TestWithoutAMailboxThereIsNoInbox(t *testing.T) {
	s := newSite(t)
	s.signIn()
	if got := s.do(http.MethodGet, prefix+"/inbox", nil, nil); got.status != http.StatusNotFound {
		t.Errorf("the page without a mailbox: %d", got.status)
	}
	overview := s.do(http.MethodGet, prefix+"/", nil, nil)
	if strings.Contains(overview.body, ">Входящие") {
		t.Error("the menu offers letters nobody reads")
	}
	status := s.do(http.MethodGet, prefix+"/status", nil, nil)
	if !strings.Contains(status.body, "не настроена — ответы клиентов письмом не читаются") {
		t.Error("the status screen does not say that mail is not read")
	}
}
