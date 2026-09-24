package admin

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram"
)

func testForm() config.Form {
	return config.Form{Enabled: true, Directions: []config.Option{
		{ID: "networks", Label: config.Localized{"en": "Networks & hardware", "ru": "Сети и оборудование"}},
		{ID: "devops", Label: config.Localized{"": "DevOps"}},
	}}
}

// addLead stores a request the way the form handler does.
func (s *site) addLead(change func(*leads.Submission)) *leads.Lead {
	s.t.Helper()
	sub := leads.Submission{
		Name: "Иван Петров", ContactMethod: leads.MethodEmail, ContactValue: "ivan@company.com", Direction: "networks",
		Description: "Нужно перестроить сеть офиса на 40 мест: MikroTik и два VLAN.", Budget: "$1–3k", Lang: "ru",
	}
	if change != nil {
		change(&sub)
	}
	session := analytics.SessionSummary{Known: true, SessionID: []byte{0xAA, 0xBB, 0xCC, 0xDD, 1, 2, 3, 4}, Source: "ads", UTMSource: "google",
		UTMCampaign: "mikrotik-kyiv", Device: "mobile", Browser: "Safari", OS: "iOS", Sections: []string{"hero", "skills", "contacts"}, TimeOnSiteMs: 183000}
	lead, err := s.leads.Create(context.Background(), sub, leads.Verdict{}, session, "203.0.113.0/24")
	if err != nil {
		s.t.Fatal(err)
	}
	return lead
}

func (s *site) post(path string, form url.Values) reply {
	s.t.Helper()
	form.Set("csrf", s.csrf())
	return s.do(http.MethodPost, prefix+path, form, nil)
}

func TestRequestsListAndBoard(t *testing.T) {
	s := newSite(t)
	first := s.addLead(nil)
	s.addLead(func(sub *leads.Submission) {
		sub.Name, sub.ContactMethod, sub.ContactValue, sub.Direction = `<img src=x onerror=alert(1)>`, leads.MethodTelegram, "@olena_sh", "devops"
		sub.Description = `<script>alert("xss")</script> Kubernetes для небольшой команды.`
	})
	s.signIn()

	list := s.do(http.MethodGet, prefix+"/leads", nil, nil)
	if list.status != http.StatusOK {
		t.Fatalf("list: %d", list.status)
	}
	for _, want := range []string{
		"2 заявки", "#K-0001", "#K-0002", "Иван Петров", "Сети и оборудование", "DevOps", "mikrotik-kyiv", "Реклама",
		`<span class="badge">2</span>`, // new requests, next to «Заявки» in the menu
		`<span class="status status-new">Новая</span>`,
		`href="` + prefix + `/leads/1"`,
	} {
		if !strings.Contains(list.body, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	if strings.Contains(list.body, "<img src=x") || strings.Contains(list.body, "<script>alert") {
		t.Error("what a visitor typed reached the page unescaped")
	}
	// Inside tags only: the visitor's «onerror=» above is on the page too — as harmless text.
	if regexp.MustCompile(`(?i)<[^>]*\s(?:style|on[a-z]+)=`).MatchString(list.body) {
		t.Error("inline styles or handlers: the CSP would block them")
	}

	if got := s.do(http.MethodGet, prefix+"/leads?q=olena", nil, nil); !strings.Contains(got.body, "1 заявка") || strings.Contains(got.body, "Иван Петров") {
		t.Error("search by contact")
	}
	if got := s.do(http.MethodGet, prefix+"/leads?status=done", nil, nil); !strings.Contains(got.body, "Заявок нет.") {
		t.Error("an empty status tab")
	}
	if got := s.do(http.MethodGet, prefix+"/leads?status=%27%22%3E&page=-5", nil, nil); got.status != http.StatusOK {
		t.Errorf("nonsense in the query: %d", got.status)
	}

	board := s.do(http.MethodGet, prefix+"/leads?view=board", nil, nil)
	for _, want := range []string{`data-board`, `data-status="new"`, `data-status="in_progress"`, fmt.Sprintf(`data-lead="%d"`, first.ID), `draggable="true"`} {
		if !strings.Contains(board.body, want) {
			t.Errorf("the board lacks %q", want)
		}
	}

	s.cookie = ""
	for _, path := range []string{"/leads", "/leads/1", "/leads/export.csv", "/templates"} {
		if got := s.do(http.MethodGet, prefix+path, nil, nil); got.status != http.StatusSeeOther {
			t.Errorf("anonymous %s: %d", path, got.status)
		}
	}
}

func TestWorkingOnARequest(t *testing.T) {
	s := newSite(t)
	lead := s.addLead(nil)
	s.signIn()
	path := fmt.Sprintf("/leads/%d", lead.ID)

	card := s.do(http.MethodGet, prefix+path, nil, nil)
	for _, want := range []string{
		"Заявка #K-0001", "Иван Петров", `href="mailto:ivan@company.com"`, "Сети и оборудование", "$1–3k", "Взять в работу",
		"Первый экран → Навыки → Контакты", "3 мин 03 с", "google / mikrotik-kyiv", "203.0.113.0/24",
		`href="` + prefix + `/visits/aabbccdd01020304"`, // the path through the site
		"форма на сайте", "Изучу и отвечу сегодня",      // a ready-made answer in the client's language
	} {
		if !strings.Contains(card.body, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	if strings.Contains(card.body, "I'll look into it today") {
		t.Error("templates of another language are offered")
	}
	if strings.Contains(card.body, "Ссылка в бот для клиента") {
		t.Error("a link into a bot that is not there")
	}
	// With the bot: the «continue in Telegram» link of the request, for a client who never opened it.
	s.bot = telegram.Status{Mode: telegram.ModeWebhook, Username: "krokosha_bot", ConnectedAt: time.Now()}
	card = s.do(http.MethodGet, prefix+path, nil, nil)
	if link := "https://t.me/krokosha_bot?start=" + telegram.ClientPrefix + lead.PublicToken; !strings.Contains(card.body, link) || !strings.Contains(card.body, "ещё не открывал") {
		t.Errorf("the card lacks the client's link into the bot %s", link)
	}

	// Take it. A second «take» — the colleague was a moment late — is told who has it.
	if got := s.post(path+"/status", url.Values{"status": {leads.StatusInProgress}}); got.status != http.StatusSeeOther {
		t.Fatalf("take: %d %s", got.status, got.body)
	}
	if _, err := s.leads.Take(context.Background(), lead.ID, "colleague"); err == nil {
		t.Error("the request was taken twice")
	}
	if card = s.do(http.MethodGet, prefix+path, nil, nil); !strings.Contains(card.body, "denis взял(а) в работу") || !strings.Contains(card.body, `status-in_progress`) {
		t.Error("the card does not show who took the request")
	}

	// A ready-made answer is put into the form without any script, then sent.
	picked := regexp.MustCompile(`<option value="(\d+)">Нужны детали</option>`).FindStringSubmatch(card.body)
	if picked == nil {
		t.Fatal("no template to pick")
	}
	draft := s.do(http.MethodGet, prefix+path+"?template="+picked[1], nil, nil)
	if !strings.Contains(draft.body, "Здравствуйте, Иван Петров!") || !strings.Contains(draft.body, "Спасибо за заявку #K-0001.") {
		t.Error("the chosen template is not filled in")
	}
	if got := s.post(path+"/reply", url.Values{"text": {"Здравствуйте! Уточните, пожалуйста, сколько коммутаторов уже есть."}}); got.status != http.StatusSeeOther {
		t.Fatalf("reply: %d", got.status)
	}
	if got := s.post(path+"/reply", url.Values{"text": {"   "}}); got.status != http.StatusBadRequest {
		t.Errorf("an empty answer: %d", got.status)
	}
	if got := s.post(path+"/note", url.Values{"text": {"Клиент из рекламы, просил перезвонить после 18:00 <b>важно</b>"}}); got.status != http.StatusSeeOther {
		t.Fatalf("note: %d", got.status)
	}

	card = s.do(http.MethodGet, prefix+path, nil, nil)
	for _, want := range []string{
		"status-waiting_client", "ответ клиенту, почта", "ждёт отправки", "сколько коммутаторов уже есть",
		"заметка, клиент её не видит", "&lt;b&gt;важно&lt;/b&gt;", "denis ответил(а) клиенту",
	} {
		if !strings.Contains(card.body, want) {
			t.Errorf("after the answer the card lacks %q", want)
		}
	}
	var queued int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE kind = 'lead.reply' AND lead_id = ?`, lead.ID).Scan(&queued); err != nil || queued != 1 {
		t.Errorf("answers queued for delivery: %d, %v", queued, err)
	}

	// A refusal with a letter to the client — also from a template —, then a change of mind.
	refusal := regexp.MustCompile(`<option value="(\d+)">Бюджет не подходит</option>`).FindStringSubmatch(card.body)
	if refusal == nil {
		t.Fatal("no refusal template to pick")
	}
	if draft = s.do(http.MethodGet, prefix+path+"?template="+refusal[1], nil, nil); !regexp.MustCompile(`(?s)<textarea id="reject-letter"[^>]*>Здравствуйте, Иван Петров!`).MatchString(draft.body) ||
		regexp.MustCompile(`(?s)<textarea id="reply-text"[^>]*>Здравствуйте`).MatchString(draft.body) {
		t.Error("the refusal template must fill the refusal letter, and only it")
	}
	if got := s.post(path+"/status", url.Values{"status": {leads.StatusRejected}, "reason": {"бюджет не подходит"}, "letter": {"К сожалению, в этот бюджет не уложиться."}}); got.status != http.StatusSeeOther {
		t.Fatalf("reject: %d", got.status)
	}
	if card = s.do(http.MethodGet, prefix+path, nil, nil); !strings.Contains(card.body, "бюджет не подходит") || !strings.Contains(card.body, "в этот бюджет не уложиться") {
		t.Error("the refusal and its letter are not on the card")
	}
	if got := s.post(path+"/status", url.Values{"status": {leads.StatusDone}}); got.status != http.StatusConflict {
		t.Errorf("rejected → done: %d, want a refusal", got.status)
	}
	// The board's script gets the same answers as JSON.
	form := url.Values{"status": {leads.StatusSpam}, "csrf": {s.csrf()}}
	if got := s.do(http.MethodPost, prefix+path+"/status", form, map[string]string{"Accept": "application/json"}); got.status != http.StatusConflict || !strings.Contains(got.body, `"ok":false`) {
		t.Errorf("a forbidden move from the board: %d %s", got.status, got.body)
	}
	form.Set("status", leads.StatusInProgress)
	if got := s.do(http.MethodPost, prefix+path+"/status", form, map[string]string{"Accept": "application/json"}); got.status != http.StatusOK || !strings.Contains(got.body, `"ok":true`) {
		t.Errorf("an allowed move from the board: %d %s", got.status, got.body)
	}

	// Everything is in the journal, under the number of the request.
	var actions string
	if err := s.db.QueryRow(`SELECT GROUP_CONCAT(action ORDER BY id SEPARATOR ' ') FROM audit_log WHERE subject = 'K-0001'`).Scan(&actions); err != nil ||
		actions != "lead.status lead.reply lead.status lead.status" {
		t.Errorf("audit log: %q %v", actions, err)
	}
	if got := s.do(http.MethodPost, prefix+path+"/note", url.Values{"text": {"forged"}, "csrf": {"nope"}}, nil); got.status != http.StatusForbidden {
		t.Errorf("a forged form: %d", got.status)
	}
	if got := s.do(http.MethodGet, prefix+"/leads/999", nil, nil); got.status != http.StatusNotFound {
		t.Errorf("a request that does not exist: %d", got.status)
	}
	if got := s.do(http.MethodGet, prefix+"/leads/abc", nil, nil); got.status != http.StatusNotFound {
		t.Errorf("a request with a made-up number: %d", got.status)
	}
}

// TestTheClientCameToTheBot: once the client opened the link, the card says so — and that answers
// now go to Telegram, until the client writes a letter.
func TestTheClientCameToTheBot(t *testing.T) {
	s := newSite(t)
	lead := s.addLead(nil)
	s.signIn()
	s.bot = telegram.Status{Mode: telegram.ModeWebhook, Username: "krokosha_bot", ConnectedAt: time.Now()}
	if err := s.leads.ClientLinked(context.Background(), lead.ID); err != nil {
		t.Fatal(err)
	}
	card := s.do(http.MethodGet, prefix+fmt.Sprintf("/leads/%d", lead.ID), nil, nil)
	if !strings.Contains(card.body, "Клиент её уже открыл") || !strings.Contains(card.body, "Ответ уйдёт клиенту в Telegram через бота") {
		t.Error("the card does not say the client came to the bot")
	}
}

func TestDeletingOnRequestAndExport(t *testing.T) {
	s := newSite(t)
	lead := s.addLead(nil)
	s.addLead(func(sub *leads.Submission) {
		sub.Name, sub.Description = "=cmd|' /C calc'!A1", "Another client with a spreadsheet formula for a name."
	})
	s.signIn()
	path := fmt.Sprintf("/leads/%d", lead.ID)

	export := s.do(http.MethodGet, prefix+"/leads/export.csv", nil, nil)
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(export.body, "\xEF\xBB\xBF"))).ReadAll()
	if err != nil || export.status != http.StatusOK || len(rows) != 3 || rows[1][0] != "K-0001" || rows[1][4] != "Иван Петров" {
		t.Fatalf("export: %d, %d rows, %v", export.status, len(rows), err)
	}
	if rows[2][4] != "'=cmd|' /C calc'!A1" {
		t.Errorf("a formula was exported as is: %q", rows[2][4])
	}

	// The number typed by hand is the confirmation.
	if got := s.post(path+"/delete", url.Values{"confirm": {"K-0002"}}); got.status != http.StatusBadRequest {
		t.Errorf("deleting with a wrong number: %d", got.status)
	}
	if got := s.post(path+"/delete", url.Values{"confirm": {" #k-0001 "}}); got.status != http.StatusSeeOther || !strings.Contains(got.location, "ok=lead-deleted") {
		t.Fatalf("delete: %d → %q", got.status, got.location)
	}
	if got := s.do(http.MethodGet, prefix+path, nil, nil); got.status != http.StatusNotFound {
		t.Errorf("the deleted request still opens: %d", got.status)
	}
	var left int
	if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM leads WHERE id = ?) + (SELECT COUNT(*) FROM lead_messages WHERE lead_id = ?) + (SELECT COUNT(*) FROM outbox WHERE lead_id = ?)`,
		lead.ID, lead.ID, lead.ID).Scan(&left); err != nil || left != 0 {
		t.Errorf("rows left of the deleted request: %d, %v", left, err)
	}
	// The journal remembers that it happened — and nothing about the person.
	var details string
	if err := s.db.QueryRow(`SELECT CONCAT(actor, ' ', action, ' ', subject, ' ', details) FROM audit_log WHERE action = 'lead.delete'`).Scan(&details); err != nil ||
		details != "denis lead.delete K-0001 данные клиента удалены по запросу" || strings.Contains(details, "Петров") {
		t.Errorf("audit of the deletion: %q %v", details, err)
	}
}

func TestTemplatesEditor(t *testing.T) {
	s := newSite(t)
	s.signIn()

	page := s.do(http.MethodGet, prefix+"/templates", nil, nil)
	for _, want := range []string{"Ответы", "Отказы", "Русский", "Українська", "English", "Нужны детали", "Not my field", "{name}"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the editor lacks %q", want)
		}
	}
	if got := s.post("/templates", url.Values{"kind": {"reply"}, "lang": {"ru"}, "title": {"Счёт выставлен"}, "body": {"Здравствуйте, {name}! Счёт по заявке {id} во вложении."}}); got.status != http.StatusSeeOther {
		t.Fatalf("new template: %d", got.status)
	}
	page = s.do(http.MethodGet, prefix+"/templates", nil, nil)
	id := regexp.MustCompile(`name="id" value="(\d+)" />\s*<input type="hidden" name="kind" value="reply" />\s*<input type="hidden" name="lang" value="ru" />\s*<label[^>]*>Название</label>\s*<input[^>]*value="Счёт выставлен"`).FindStringSubmatch(page.body)
	if id == nil {
		t.Fatal("the new template is not in the editor")
	}
	if got := s.post("/templates", url.Values{"id": {id[1]}, "kind": {"reply"}, "lang": {"ru"}, "title": {""}, "body": {"x"}}); got.status != http.StatusBadRequest {
		t.Errorf("a template without a title: %d", got.status)
	}
	if got := s.post("/templates", url.Values{"id": {id[1]}, "delete": {"1"}}); got.status != http.StatusSeeOther {
		t.Fatalf("delete: %d", got.status)
	}
	if page = s.do(http.MethodGet, prefix+"/templates", nil, nil); strings.Contains(page.body, "Счёт выставлен") {
		t.Error("the deleted template is still there")
	}
}

// Files of a request: handed out to a signed-in administrator only, and only as downloads.
func TestFilesOfARequest(t *testing.T) {
	s := newSite(t)
	files := leads.NewFiles(t.TempDir())
	s.leads.UseFiles(files)
	page := []byte("<html><script>alert(document.cookie)</script></html> — what a «text file» may well contain")
	lead := s.addLead(func(sub *leads.Submission) {
		upload, err := files.Save("Схема сети & план.txt", leads.KindTXT, bytes.NewReader(page))
		if err != nil {
			t.Fatal(err)
		}
		sub.Files = []leads.Upload{upload}
	})
	other := s.addLead(nil)
	link := fmt.Sprintf("%s/leads/%d/files/1", prefix, lead.ID)

	if got := s.do(http.MethodGet, link, nil, nil); got.status != http.StatusSeeOther || got.location != prefix+"/login" {
		t.Fatalf("a file without signing in: %d → %q", got.status, got.location)
	}
	s.signIn()

	card := s.do(http.MethodGet, fmt.Sprintf("%s/leads/%d", prefix, lead.ID), nil, nil)
	if !strings.Contains(card.body, `href="`+link+`" download>Схема сети &amp; план.txt</a>`) || !strings.Contains(card.body, fmt.Sprintf("txt · %d Б", len(page))) {
		t.Errorf("the card does not offer the file:\n%s", card.body)
	}

	got := s.do(http.MethodGet, link, nil, nil)
	if got.status != http.StatusOK || got.body != string(page) {
		t.Fatalf("the download: %d, %d bytes", got.status, len(got.body))
	}
	// Whatever is inside, the browser saves it; it never shows or runs it.
	for header, want := range map[string]string{
		"Content-Type":            "application/octet-stream",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; sandbox",
		"Cache-Control":           "no-store",
	} {
		if value := got.header.Get(header); !strings.Contains(value, want) {
			t.Errorf("%s: %q, want %q", header, value, want)
		}
	}
	if disposition := got.header.Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment; filename*=utf-8''") {
		t.Errorf("Content-Disposition: %q", disposition)
	}

	for name, path := range map[string]string{
		"under another request": fmt.Sprintf("%s/leads/%d/files/1", prefix, other.ID),
		"a file that is not":    fmt.Sprintf("%s/leads/%d/files/99", prefix, lead.ID),
		"a made-up number":      fmt.Sprintf("%s/leads/%d/files/..%%2f..%%2fetc%%2fpasswd", prefix, lead.ID),
	} {
		if got := s.do(http.MethodGet, path, nil, nil); got.status != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", name, got.status)
		}
	}

	// Deleting the client's data takes the file along.
	if got := s.post(fmt.Sprintf("/leads/%d/delete", lead.ID), url.Values{"confirm": {lead.Number()}}); got.status != http.StatusSeeOther {
		t.Fatalf("delete: %d", got.status)
	}
	if got := s.do(http.MethodGet, link, nil, nil); got.status != http.StatusNotFound {
		t.Errorf("the file of a deleted request: %d", got.status)
	}
}
