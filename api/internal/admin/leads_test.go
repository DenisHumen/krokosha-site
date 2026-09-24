package admin

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	}, Budgets: []config.Localized{
		{"en": "up to $500"}, {"": "$500–1k"}, {"": "$1–3k"}, {"": "$3–10k"}, {"en": "over $10k"}, {"en": "not sure yet"},
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
		"2 заявки · ждут ответа: 2", ">K-0001<", ">K-0002<", "Иван Петров", "Сети и оборудование", "DevOps", "mikrotik-kyiv", "Реклама",
		`<span class="tick-v">2 новые</span>`, // new requests, in the header
		`<span class="st st-new">новая</span>`,
		`href="` + prefix + `/leads/1"`,
		`data-prio="2"`, // a budget of $1–3k and no orders: the middle of the scale
		`<span class="c-unread" title="Непрочитанных сообщений: 1">1</span>`, // nobody has opened them yet
		"Выберите разговор слева",
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

	if got := s.do(http.MethodGet, prefix+"/leads?q=olena", nil, nil); !strings.Contains(got.body, "<span>1 из 2</span>") || strings.Contains(got.body, "Иван Петров") {
		t.Error("search by contact")
	}
	if got := s.do(http.MethodGet, prefix+"/leads?tab=closed", nil, nil); !strings.Contains(got.body, "Заявок нет.") {
		t.Error("an empty tab")
	}
	if got := s.do(http.MethodGet, prefix+"/leads?tab=%27%22%3E&sort=x&q=%3Cb%3E", nil, nil); got.status != http.StatusOK || strings.Contains(got.body, "<b>") {
		t.Errorf("nonsense in the query: %d", got.status)
	}
	// Opened, the list keeps its tab and its order in every link; the conversation is read.
	opened := s.do(http.MethodGet, prefix+"/leads/1?tab=work&sort=recent", nil, nil)
	for _, want := range []string{
		`href="` + prefix + `/leads/2?sort=recent&amp;tab=work"`, `name="list" value="sort=recent&amp;tab=work"`,
		`aria-current="page"`, `<div class="talk-unread" id="unread"><span>новые</span></div>`,
	} {
		if !strings.Contains(opened.body, want) {
			t.Errorf("an open conversation lacks %q", want)
		}
	}
	if again := s.do(http.MethodGet, prefix+"/leads", nil, nil); strings.Count(again.body, `class="c-unread"`) != 1 {
		t.Error("the conversation that was opened is still unread")
	}
	// The pulse of the open page: JSON, for the signed-in only.
	pulse := s.do(http.MethodGet, prefix+"/leads/pulse?lead=1", nil, map[string]string{"Accept": "application/json"})
	if pulse.status != http.StatusOK || !strings.Contains(pulse.body, `"unread":1`) || !strings.Contains(pulse.body, `"lead_last":1`) {
		t.Errorf("the pulse: %d %s", pulse.status, pulse.body)
	}

	board := s.do(http.MethodGet, prefix+"/leads?view=board", nil, nil)
	for _, want := range []string{`data-board`, `data-status="new"`, `data-status="in_progress"`, fmt.Sprintf(`data-lead="%d"`, first.ID), `draggable="true"`} {
		if !strings.Contains(board.body, want) {
			t.Errorf("the board lacks %q", want)
		}
	}

	s.cookie = ""
	for _, path := range []string{"/leads", "/leads/1", "/leads/export.csv", "/templates", "/leads/pulse"} {
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
	if card = s.do(http.MethodGet, prefix+path, nil, nil); !strings.Contains(card.body, "denis взял(а) в работу") || !strings.Contains(card.body, `st-in_progress`) {
		t.Error("the card does not show who took the request")
	}

	// A ready-made answer is put into the form without any script, then sent.
	picked := regexp.MustCompile(`href="` + prefix + `/leads/\d+\?template=(\d+)#reply"[^>]*>Нужны детали`).FindStringSubmatch(card.body)
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
		"st-waiting_client", "Вы · почта", "ждёт отправки", "сколько коммутаторов уже есть",
		"Заметка · клиент не видит", "&lt;b&gt;важно&lt;/b&gt;", "В работе → Ждём клиента",
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
	if draft = s.do(http.MethodGet, prefix+path+"?template="+refusal[1], nil, nil); !regexp.MustCompile(`(?s)<textarea[^>]* id="reject-letter"[^>]*>Здравствуйте, Иван Петров!`).MatchString(draft.body) ||
		regexp.MustCompile(`(?s)<textarea[^>]* id="reply-text"[^>]*>Здравствуйте`).MatchString(draft.body) {
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
	if _, err := s.db.Exec(`INSERT INTO bot_clients (lead_id, telegram_id, linked_at) VALUES (?, 777, NOW())`, lead.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.leads.ClientLinked(context.Background(), lead.ID); err != nil {
		t.Fatal(err)
	}
	card := s.do(http.MethodGet, prefix+fmt.Sprintf("/leads/%d", lead.ID), nil, nil)
	for _, want := range []string{
		"Клиент её уже открыл", "ответ уйдёт: почта · Telegram", `name="channels" value="1"`,
		`name="channel" value="email" checked`, `name="channel" value="telegram" checked`, "ivan@company.com",
	} {
		if !strings.Contains(card.body, want) {
			t.Errorf("the card of a client in the bot lacks %q", want)
		}
	}
	// Telegram alone: one delivery, by the bot.
	path := fmt.Sprintf("/leads/%d", lead.ID)
	if got := s.post(path+"/reply", url.Values{"text": {"Только в Telegram"}, "channels": {"1"}, "channel": {"telegram"}}); got.status != http.StatusSeeOther {
		t.Fatalf("an answer to Telegram alone: %d", got.status)
	}
	var channels string
	if err := s.db.QueryRow(`SELECT GROUP_CONCAT(channel ORDER BY id) FROM lead_deliveries WHERE lead_id = ?`, lead.ID).Scan(&channels); err != nil || channels != "telegram" {
		t.Errorf("deliveries of an answer to Telegram alone: %q %v", channels, err)
	}
	// Nothing checked and no account: the answer would go nowhere.
	if got := s.post(path+"/reply", url.Values{"text": {"В никуда"}, "channels": {"1"}}); got.status != http.StatusBadRequest ||
		!strings.Contains(got.body, "Ответ никуда не уйдёт") || !strings.Contains(got.body, ">В никуда</textarea>") {
		t.Errorf("an answer to nowhere: %d", got.status)
	}
	// «Заметка» of the same composer is a note, whatever else the form carries.
	if got := s.post(path+"/reply", url.Values{"text": {"Внутреннее"}, "mode": {"note"}, "channels": {"1"}}); got.status != http.StatusSeeOther || !strings.Contains(got.location, "ok=lead-note") {
		t.Errorf("a note from the composer: %d %s", got.status, got.location)
	}
	var notes int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM lead_messages WHERE lead_id = ? AND direction = 'note' AND body = 'Внутреннее'`, lead.ID).Scan(&notes); err != nil || notes != 1 {
		t.Errorf("notes: %d %v", notes, err)
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
	for _, want := range []string{"Ответы", "Отказы", "Русский", "Українська", "English", "Нужны детали", "Приветствие", "Стоимость", "{name}", "{account}", "первый ответ"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the editor lacks %q", want)
		}
	}
	if strings.Contains(page.body, "Pricing") {
		t.Error("the list of Russian answers shows English ones")
	}
	if page = s.do(http.MethodGet, prefix+"/templates?kind=reject&lang=en", nil, nil); !strings.Contains(page.body, "Not my field") {
		t.Error("the English refusals are not there")
	}
	if page = s.do(http.MethodGet, prefix+"/templates?q=NDA", nil, nil); !strings.Contains(page.body, "Конфиденциальность") || strings.Contains(page.body, "Приветствие") {
		t.Error("the search of templates")
	}

	got := s.post("/templates", url.Values{
		"kind": {"reply"}, "lang": {"ru"}, "title": {"Счёт выставлен"}, "body": {"Здравствуйте, {name}! Счёт по заявке {id} во вложении."},
		"category": {"payment"}, "moment": {"talk"}, "keywords": {"счёт, Оплата\nреквизиты"}, "direction": {"networks", "devops"},
	})
	id := regexp.MustCompile(`/templates/(\d+)\?ok=template-saved$`).FindStringSubmatch(got.location)
	if got.status != http.StatusSeeOther || id == nil {
		t.Fatalf("new template: %d → %q", got.status, got.location)
	}
	page = s.do(http.MethodGet, prefix+"/templates/"+id[1], nil, nil)
	for _, want := range []string{`value="Счёт выставлен"`, "счет, оплата, реквизиты", `<option value="payment" selected>`, `<option value="talk" selected>`,
		`name="direction" value="networks" checked`, `name="direction" value="devops" checked`, `action="` + prefix + `/upload/templates/` + id[1] + `/media"`} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the template's page lacks %q", want)
		}
	}
	if got := s.post("/templates", url.Values{"id": {id[1]}, "kind": {"reply"}, "lang": {"ru"}, "title": {""}, "body": {"x"}}); got.status != http.StatusBadRequest {
		t.Errorf("a template without a title: %d", got.status)
	}
	if got := s.post("/templates", url.Values{"id": {id[1]}, "kind": {"reply"}, "lang": {"ru"}, "title": {"a"}, "body": {"b"}, "moment": {"someday"}}); got.status != http.StatusBadRequest {
		t.Errorf("a template of an unknown moment: %d", got.status)
	}
	if got := s.post("/templates", url.Values{"id": {id[1]}, "delete": {"1"}}); got.status != http.StatusSeeOther {
		t.Fatalf("delete: %d", got.status)
	}
	if page = s.do(http.MethodGet, prefix+"/templates", nil, nil); strings.Contains(page.body, "Счёт выставлен") {
		t.Error("the deleted template is still there")
	}
	if got := s.do(http.MethodGet, prefix+"/templates/"+id[1], nil, nil); got.status != http.StatusNotFound {
		t.Errorf("the page of a deleted template: %d", got.status)
	}
}

// upload sends a form with files, the way a browser does.
func (s *site) upload(path string, fields url.Values, files map[string][]byte) reply {
	s.t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("csrf", s.csrf())
	for key, values := range fields {
		for _, value := range values {
			_ = writer.WriteField(key, value)
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		part, _ := writer.CreateFormFile("files", name)
		_, _ = part.Write(files[name])
	}
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, prefix+path, &body)
	request.Host = "krokosha.xyz"
	request.RemoteAddr = "127.0.0.1:40000"
	request.Header.Set("X-Real-IP", "203.0.113.7")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", "https://krokosha.xyz")
	request.AddCookie(&http.Cookie{Name: cookieName, Value: s.cookie})
	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, request)
	result := recorder.Result()
	defer result.Body.Close()
	raw, _ := io.ReadAll(result.Body)
	return reply{status: result.StatusCode, header: result.Header, body: string(raw), location: result.Header.Get("Location")}
}

var (
	jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, bytes.Repeat([]byte{7}, 300)...)
	mp4Bytes  = append([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0}, bytes.Repeat([]byte{1}, 300)...)
	pdfBytes  = []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")
)

// A template carries photos and videos; the card suggests what fits and sends them with the text.
func TestQuickAnswersWithFiles(t *testing.T) {
	s := newSite(t)
	files := leads.NewFiles(filepath.Join(t.TempDir(), "attachments"))
	s.leads.UseFiles(files)
	s.leads.UseMedia(leads.NewFiles(filepath.Join(t.TempDir(), "templates")))
	lead := s.addLead(func(sub *leads.Submission) {
		sub.Description = "Здравствуйте! Сколько будет стоить перестроить сеть офиса и когда сможете начать?"
	})
	s.signIn()

	// The pool: the price, the time and a greeting come first for this client, and say why.
	path := fmt.Sprintf("/leads/%d", lead.ID)
	card := s.do(http.MethodGet, prefix+path, nil, nil)
	top := regexp.MustCompile(`(?s)<span class="quick">(.*?)<details class="quick-all"`).FindStringSubmatch(card.body)
	if top == nil {
		t.Fatal("no suggestions on the card")
	}
	for _, want := range []string{"Стоимость", "Сроки", "«сколько будет»", "«когда»", "первый ответ"} {
		if !strings.Contains(top[1], want) {
			t.Errorf("the first three lack %q: %s", want, top[1])
		}
	}
	if !strings.Contains(card.body, `id="templates-data"`) || !strings.Contains(card.body, `enctype="multipart/form-data"`) {
		t.Error("the composer has no data for its script or cannot send files")
	}

	// A template with a photo and a video.
	var portfolio int64
	all, _ := s.leads.Templates(context.Background(), "reply")
	for _, item := range all {
		if item.Lang == "ru" && item.Category == "portfolio" {
			portfolio = item.ID
		}
	}
	templatePath := fmt.Sprintf("/templates/%d", portfolio)
	got := s.upload("/upload"+templatePath+"/media", nil, map[string][]byte{"стойка.jpg": jpegBytes, "обзор.mp4": mp4Bytes})
	if got.status != http.StatusSeeOther || !strings.Contains(got.location, "ok=media-added") {
		t.Fatalf("files for a template: %d %s", got.status, got.location)
	}
	if got = s.upload("/upload"+templatePath+"/media", nil, map[string][]byte{"setup.exe.jpg": []byte("MZ\x90\x00 not a photo at all")}); got.status != http.StatusBadRequest ||
		!strings.Contains(got.body, "«setup.exe.jpg»: не фото") {
		t.Errorf("a program for a template: %d", got.status)
	}
	template, err := s.leads.Template(context.Background(), portfolio)
	if err != nil || len(template.Media) != 2 {
		t.Fatalf("the files of the template: %+v %v", template, err)
	}
	photo := template.Media[1] // the form sends them in the order of their names: «обзор», then «стойка»
	if photo.Kind != leads.KindJPG {
		photo = template.Media[0]
	}
	preview := s.do(http.MethodGet, fmt.Sprintf("%s/templates/media/%d", prefix, photo.ID), nil, nil)
	if preview.status != http.StatusOK || preview.header.Get("Content-Type") != "image/jpeg" || !strings.HasPrefix(preview.header.Get("Content-Disposition"), "inline") ||
		preview.header.Get("X-Content-Type-Options") != "nosniff" || preview.body != string(jpegBytes) {
		t.Errorf("the preview of a photo: %d %v", preview.status, preview.header)
	}
	if page := s.do(http.MethodGet, prefix+templatePath, nil, nil); !strings.Contains(page.body, fmt.Sprintf(`src="%s/templates/media/%d"`, prefix, photo.ID)) || !strings.Contains(page.body, "<video") {
		t.Error("the template's page does not show its files")
	}

	// Chosen on the card without a script: its text in the form, its files checked.
	draft := s.do(http.MethodGet, fmt.Sprintf("%s%s?template=%d", prefix, path, portfolio), nil, nil)
	if !strings.Contains(draft.body, fmt.Sprintf(`name="template" value="%d"`, portfolio)) || !strings.Contains(draft.body, fmt.Sprintf(`name="media" value="%d" checked`, photo.ID)) ||
		!strings.Contains(draft.body, "https://krokosha.xyz/ru/#projects") {
		t.Error("the chosen template is not in the form with its files and links")
	}

	// Sent with the template's photo (the video unchecked) and a document of one's own.
	got = s.upload("/upload"+path+"/reply", url.Values{
		"text": {"Вот примеры."}, "template": {strconv.FormatInt(portfolio, 10)}, "media": {strconv.FormatInt(photo.ID, 10)},
	}, map[string][]byte{"смета.pdf": pdfBytes})
	if got.status != http.StatusSeeOther || !strings.Contains(got.location, "ok=lead-reply") {
		t.Fatalf("an answer with files: %d %s", got.status, got.body)
	}
	card = s.do(http.MethodGet, prefix+path, nil, nil)
	for _, want := range []string{"Вот примеры.", "стойка.jpg", "смета.pdf", "уже отправлен"} {
		if !strings.Contains(card.body, want) {
			t.Errorf("after the answer the card lacks %q", want)
		}
	}
	if strings.Contains(card.body, "обзор.mp4</a>") {
		t.Error("the unchecked video went too")
	}
	if got = s.upload("/upload"+path+"/reply", url.Values{"text": {"x"}}, map[string][]byte{"a.exe": []byte("MZ")}); got.status != http.StatusBadRequest ||
		!strings.Contains(got.body, "Ответ не отправлен") || !strings.Contains(got.body, ">x</textarea>") {
		t.Errorf("an answer with a program: %d", got.status)
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
	if !strings.Contains(card.body, `href="`+link+`" download title="Скачать Схема сети &amp; план.txt">`) || !strings.Contains(card.body, fmt.Sprintf("%d Б · скачать", len(page))) {
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

// Every kind of conversation opens: spam, one whose data is gone, a question from an account about
// a request, a phone with an account — and no page carries inline styles or handlers (the CSP).
func TestEveryKindOfConversationOpens(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	first := s.addLead(nil)
	spam := s.addLead(func(sub *leads.Submission) { sub.Name = "Spammer" })
	if err := s.leads.SetStatus(ctx, spam.ID, "denis", leads.StatusSpam, ""); err != nil {
		t.Fatal(err)
	}
	gone := s.addLead(func(sub *leads.Submission) { sub.Name = "Ушедший клиент" })
	if err := s.leads.Anonymize(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	result, err := s.db.Exec(`INSERT INTO clients (created_at, updated_at, name, lang, email, telegram_id, orders_carried, spent_carried) VALUES (NOW(), NOW(), 'Олег', 'ru', 'oleg@client.test', 4242, 5, 9000)`)
	if err != nil {
		t.Fatal(err)
	}
	client, _ := result.LastInsertId()
	phone := s.addLead(func(sub *leads.Submission) {
		sub.Name, sub.ContactMethod, sub.ContactValue = "Олег", leads.MethodPhone, "+380671234567"
	})
	if _, err := s.db.Exec(`UPDATE leads SET client_id = ? WHERE id = ?`, client, phone.ID); err != nil {
		t.Fatal(err)
	}
	inquiry := s.addLead(func(sub *leads.Submission) {
		sub.Kind, sub.ClientID, sub.ParentID, sub.Subject, sub.Trusted = leads.KindInquiry, client, first.ID, "Вопрос по счёту", true
	})
	// Files and no words, from Telegram: the tile of the file, no made-up text.
	s.leads.UseFiles(leads.NewFiles(t.TempDir()))
	photo, err := leads.SaveFromClient(s.leads.Files(), "photo_2026-09-24.jpg", int64(len(jpegBytes)), bytes.NewReader(jpegBytes))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.leads.ClientWrote(ctx, first.ID, leads.Incoming{Channel: leads.ChannelTelegram, Files: []leads.Upload{photo}}); err != nil {
		t.Fatal(err)
	}
	s.signIn()
	if page := s.do(http.MethodGet, fmt.Sprintf("%s/leads/%d", prefix, first.ID), nil, nil); !strings.Contains(page.body, "photo_2026-09-24.jpg") ||
		strings.Contains(page.body, leads.FilesOnly) {
		t.Error("a message of files alone: the file is not shown, or words are made up for it")
	}

	inline := regexp.MustCompile(`(?i)<[^>]*\s(?:style|on[a-z]+)=`)
	for _, c := range []struct {
		lead  *leads.Lead
		wants []string
	}{
		{spam, []string{"st-spam", "Помечено как спам: ответить нельзя", "Не спам"}},
		{gone, []string{"Заявка обезличена", "Заявка обезличена: писать некому."}},
		{phone, []string{"ответ уйдёт: почта · Telegram · кабинет", `data-prio="3"`, "5 заказов", "Клиент #"}},
		{inquiry, []string{"Вопрос по счёту", "обращение", `/leads/` + strconv.FormatInt(first.ID, 10) + `"`, "кабинет"}},
	} {
		page := s.do(http.MethodGet, fmt.Sprintf("%s/leads/%d", prefix, c.lead.ID), nil, nil)
		if page.status != http.StatusOK {
			t.Errorf("#%d: %d", c.lead.ID, page.status)
			continue
		}
		for _, want := range c.wants {
			if !strings.Contains(page.body, want) {
				t.Errorf("#%d lacks %q", c.lead.ID, want)
			}
		}
		if inline.MatchString(page.body) {
			t.Errorf("#%d: inline styles or handlers — the CSP would block them", c.lead.ID)
		}
	}
	// The spam tab shows up once there is spam; the board still opens.
	if list := s.do(http.MethodGet, prefix+"/leads?tab=spam", nil, nil); !strings.Contains(list.body, "Spammer") || !strings.Contains(list.body, `aria-current="true">Спам`) {
		t.Error("the spam tab")
	}
	if board := s.do(http.MethodGet, prefix+"/leads?view=board", nil, nil); board.status != http.StatusOK || inline.MatchString(board.body) {
		t.Errorf("the board: %d", board.status)
	}
}
