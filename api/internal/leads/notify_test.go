package leads

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/mail"
	"github.com/DenisHumen/krokosha-site/api/internal/mail/mailtest"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

func formWithLabels() config.Form {
	form := testForm()
	form.Directions[0].Label = config.Localized{"en": "Networks & hardware", "uk": "Мережі та обладнання", "ru": "Сети и оборудование"}
	return form
}

// parts returns the decoded plain-text and HTML bodies of a received message.
func parts(t *testing.T, received mailtest.Received) (header netmail.Header, text, html string) {
	t.Helper()
	message, err := received.Message()
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(message.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(part)
		if strings.HasPrefix(part.Header.Get("Content-Type"), "text/html") {
			html = string(body)
		} else {
			text = string(body)
		}
	}
	return message.Header, text, html
}

func subject(t *testing.T, header netmail.Header) string {
	t.Helper()
	decoded, err := new(mime.WordDecoder).DecodeHeader(header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

// TestFromFormToMailbox is the path brief B10.7 asks to test: the form is sent → the request is
// in the database → the outbox has tasks → the (mock) mail server receives both letters.
func TestFromFormToMailbox(t *testing.T) {
	f := newFixture(t)
	smtp := mailtest.Start(t)
	sender := &mail.Sender{Addr: smtp.Addr, Hello: "krokosha.xyz"}
	mailer := &Mailer{
		Store: NewStore(f.db, nil), Deliver: sender.Send, SiteHost: "krokosha.xyz", AdminURL: "https://krokosha.xyz/_secret1/",
		From:     netmail.Address{Name: "Denis Humen", Address: "denis@krokosha.xyz"},
		NotifyTo: netmail.Address{Address: "owner@krokosha.xyz"},
		Form:     formWithLabels, Location: time.UTC,
		TelegramURL: func(lead *Lead) string { return "https://krokosha.xyz" + TelegramPath + lead.PublicToken },
	}
	worker := outbox.NewWorker(f.db, quiet)
	worker.SetClock(func() time.Time { return f.now })
	worker.Register(outbox.ChannelEmail, mailer)

	values := validValues()
	values.Set("altcha", f.proof())
	f.now = f.now.Add(3 * time.Minute)
	if got := f.do(http.MethodPost, "/api/leads", values, asJSON); got.status != http.StatusCreated {
		t.Fatalf("the form: %d %s", got.status, got.body)
	}

	// The mail server is down at first: nothing is lost, the tasks wait.
	smtp.RejectWith("451 4.3.0 try again later")
	if _, err := worker.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waiting := f.count(`SELECT COUNT(*) FROM outbox WHERE channel = 'email' AND status = 'pending' AND attempts = 1`); waiting != 2 {
		t.Fatalf("tasks waiting for the mail server: %d, want 2", waiting)
	}
	smtp.RejectWith("")
	f.now = f.now.Add(time.Hour)
	if _, err := worker.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}

	received := smtp.Messages()
	if len(received) != 2 {
		t.Fatalf("letters received: %d, want 2", len(received))
	}
	var toOwner, toClient mailtest.Received
	for _, letter := range received {
		if letter.To[0] == "owner@krokosha.xyz" {
			toOwner = letter
		} else {
			toClient = letter
		}
	}

	header, text, html := parts(t, toOwner)
	if got := subject(t, header); got != "Заявка #K-0001 · Сети и оборудование · Иван Петров" {
		t.Errorf("subject of the notification: %q", got)
	}
	// «Reply» in the owner's mail program goes to the client.
	if replyTo, err := header.AddressList("Reply-To"); err != nil || len(replyTo) != 1 || replyTo[0].Address != "Ivan.Petrov@company.com" {
		t.Errorf("Reply-To of the notification: %v %v", replyTo, err)
	}
	for _, want := range []string{"Иван Петров", "Ivan.Petrov@company.com", "$1–3k", "urgent", "MikroTik и два VLAN", "https://krokosha.xyz/_secret1/leads/1", "телефон Safari · iOS"} {
		if !strings.Contains(text, want) {
			t.Errorf("the plain notification lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(html, `href="mailto:Ivan.Petrov@company.com"`) || !strings.Contains(html, `href="https://krokosha.xyz/_secret1/leads/1"`) {
		t.Errorf("the HTML notification lacks its links:\n%s", html)
	}

	header, text, html = parts(t, toClient)
	if toClient.To[0] != "Ivan.Petrov@company.com" || subject(t, header) != "Заявка #K-0001 принята — krokosha.xyz" {
		t.Errorf("the confirmation: to %v, subject %q", toClient.To, subject(t, header))
	}
	if header.Get("Auto-Submitted") != "auto-replied" {
		t.Error("the confirmation does not say it is automatic: mail robots may answer it")
	}
	// What was chosen, in the client's language; the link to the bot on the site's own address;
	// signed by a person.
	for _, want := range []string{"Здравствуйте, Иван Петров!", "#K-0001", "Ваша заявка", "Сети и оборудование", "срочно", "$1–3k",
		"Ivan.Petrov@company.com", "сохранено вместе с заявкой", "https://krokosha.xyz/api/leads/telegram?t=", "Denis Humen"} {
		if !strings.Contains(text, want) || !strings.Contains(html, strings.ReplaceAll(want, "&", "&amp;")) {
			t.Errorf("the confirmation lacks %q:\n%s", want, text)
		}
	}
	// …and nothing the visitor typed freely: the letter goes to whatever address was entered.
	if strings.Contains(text+html, "MikroTik") || strings.Contains(text+html, "t.me/") {
		t.Errorf("the confirmation repeats the description or links to t.me:\n%s", text)
	}
	if strings.Contains(text, "_secret1") || strings.Contains(html, "_secret1") || strings.Contains(text, "203.0.113") {
		t.Error("the client's letter leaks the admin area or an address")
	}
	if !strings.Contains(html, `<html lang="ru"><head>`) || !strings.Contains(html, "<title>Заявка #K-0001 принята — krokosha.xyz</title>") {
		t.Errorf("the HTML of the confirmation has no language or title:\n%s", html[:min(len(html), 400)])
	}
	if sent := f.count(`SELECT COUNT(*) FROM outbox WHERE channel = 'email' AND status = 'sent'`); sent != 2 {
		t.Errorf("tasks marked as sent: %d", sent)
	}
	// Telegram has no sender yet: its task waits, untouched by the mail server's moods.
	if waiting := f.count(`SELECT COUNT(*) FROM outbox WHERE channel = 'telegram' AND status = 'pending'`); waiting != 1 {
		t.Errorf("telegram tasks waiting: %d", waiting)
	}
}

// Everything a visitor types is hostile until shown otherwise (brief B10.7).
func TestLettersEscapeWhatVisitorsType(t *testing.T) {
	f := newFixture(t)
	var letters []mail.Message
	mailer := &Mailer{
		Store: NewStore(f.db, nil), SiteHost: "krokosha.xyz", AdminURL: "https://krokosha.xyz/_secret1",
		From: netmail.Address{Address: "denis@krokosha.xyz"}, NotifyTo: netmail.Address{Address: "owner@krokosha.xyz"}, Form: formWithLabels,
		Deliver: func(_ context.Context, message mail.Message) error { letters = append(letters, message); return nil },
	}
	worker := outbox.NewWorker(f.db, quiet)
	worker.SetClock(func() time.Time { return f.now })
	worker.Register(outbox.ChannelEmail, mailer)

	values := validValues()
	values.Set("name", `<img src=x onerror=alert(1)>"Бобби"`)
	values.Set("description", `<script>alert("xss")</script> Нужна сеть на 40 мест 🙂 </td></table><a href="javascript:alert(1)">клик</a> `+strings.Repeat("очень длинный текст ", 150))
	values.Set("altcha", f.proof())
	f.now = f.now.Add(time.Minute)
	if got := f.do(http.MethodPost, "/api/leads", values, asJSON); got.status != http.StatusCreated {
		t.Fatalf("the form: %d %s", got.status, got.body)
	}
	if _, err := worker.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(letters) != 2 {
		t.Fatalf("letters: %d", len(letters))
	}
	for _, letter := range letters {
		if strings.Contains(letter.HTML, "<script>") || strings.Contains(letter.HTML, "<img src=x") || strings.Contains(letter.HTML, `href="javascript:`) || strings.Contains(letter.HTML, "</td></table><a") {
			t.Errorf("markup typed by a visitor reached the HTML of a letter to %s", letter.To.Address)
		}
		if strings.ContainsAny(letter.Subject, "\r\n") {
			t.Error("a line break in a subject")
		}
		switch letter.To.Address {
		case "owner@krokosha.xyz": // the owner reads the text whole, only harmless
			if !strings.Contains(letter.HTML, "&lt;script&gt;") || !strings.Contains(letter.HTML, "🙂") || !strings.Contains(letter.Text, `<script>alert("xss")</script>`) {
				t.Error("the text must reach the owner whole, only harmless")
			}
		default: // the confirmation repeats none of it, and a «name» that is no name is left out
			if strings.Contains(letter.Text+letter.HTML, "xss") || strings.Contains(letter.Text+letter.HTML, "Бобби") || strings.Contains(letter.Text+letter.HTML, "onerror") {
				t.Errorf("the confirmation repeats what the visitor typed:\n%s", letter.Text)
			}
			if letter.To.Name != "" || !strings.HasPrefix(letter.Text, "Здравствуйте!") {
				t.Errorf("the confirmation greets %q, addressed to %q", strings.SplitN(letter.Text, "\n", 2)[0], letter.To.Name)
			}
		}
	}

	// A phone number becomes a tel: link — the template's URL filter must not eat it.
	phone := validValues()
	phone.Set("contact_method", "phone")
	phone.Set("contact_value", "+380 67 123 45 67")
	if got := f.do(http.MethodPost, "/api/leads", phone, asJSON); got.status != http.StatusCreated {
		t.Fatalf("the form: %d", got.status)
	}
	letters = nil
	if _, err := worker.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(letters) != 1 || !strings.Contains(letters[0].HTML, `href="tel:&#43;380671234567"`) && !strings.Contains(letters[0].HTML, `href="tel:+380671234567"`) {
		t.Errorf("the notification about a phone request: %d letters", len(letters))
	}
	if letters[0].ReplyTo != nil {
		t.Error("Reply-To without a client's email")
	}
}

func TestMailerRefusesWhatItCannotDo(t *testing.T) {
	f := newFixture(t)
	mailer := &Mailer{Store: NewStore(f.db, nil), Form: formWithLabels, Deliver: func(context.Context, mail.Message) error { return nil }}
	for name, task := range map[string]outbox.Task{
		"a request that is gone": {Kind: TaskNotify, Payload: []byte(`{"lead_id": 999}`)},
		"garbage":                {Kind: TaskNotify, Payload: []byte(`not json`)},
		"an unknown task":        {Kind: "lead.fax", Payload: []byte(`{"lead_id": 1}`)},
	} {
		// Retrying would not help any of these: they fail at once instead of clogging the queue.
		if err := mailer.Send(context.Background(), task); !outbox.IsPermanent(err) {
			t.Errorf("%s: %v, want a permanent error", name, err)
		}
	}
}

func TestAnswersReachTheClientInOneThread(t *testing.T) {
	f := newFixture(t)
	smtp := mailtest.Start(t)
	sender := &mail.Sender{Addr: smtp.Addr, Hello: "krokosha.xyz"}
	store := NewStore(f.db, func() time.Time { return f.now })
	mailer := &Mailer{
		Store: store, Deliver: sender.Send, SiteHost: "krokosha.xyz", AdminURL: "https://krokosha.xyz/_secret1",
		From: netmail.Address{Name: "Denis Humen", Address: "denis@krokosha.xyz"}, NotifyTo: netmail.Address{Address: "owner@krokosha.xyz"},
		Form: formWithLabels,
	}
	worker := outbox.NewWorker(f.db, quiet)
	worker.SetClock(func() time.Time { return f.now })
	worker.Register(outbox.ChannelEmail, mailer)
	ctx := context.Background()

	id := f.seed(nil)
	first, err := store.Reply(ctx, id, "denis", "Спасибо, изучу и отвечу до конца дня.\n\n<b>Без разметки</b>")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	var answer mailtest.Received
	for _, letter := range smtp.Messages() {
		if message, _ := letter.Message(); strings.HasPrefix(subject(t, message.Header), "Re: ") {
			answer = letter
		}
	}
	if len(answer.To) != 1 || answer.To[0] != "Ivan.Petrov@company.com" {
		t.Fatalf("the answer went to %v", answer.To)
	}
	header, text, html := parts(t, answer)
	if got := subject(t, header); got != "Re: Заявка #K-0001 принята — krokosha.xyz" {
		t.Errorf("subject = %q", got)
	}
	// It continues the thread the confirmation started.
	if header.Get("In-Reply-To") != "<lead-1.autoreply@krokosha.xyz>" || header.Get("Message-Id") != fmt.Sprintf("<lead-1.reply-%d@krokosha.xyz>", first) {
		t.Errorf("thread headers: In-Reply-To %q, Message-ID %q", header.Get("In-Reply-To"), header.Get("Message-Id"))
	}
	if !strings.Contains(text, "Спасибо, изучу и отвечу до конца дня.") || !strings.Contains(text, "Denis Humen") ||
		!strings.Contains(html, "&lt;b&gt;Без разметки&lt;/b&gt;") || strings.Contains(html, "<b>Без") {
		t.Errorf("the answer:\n%s\n%s", text, html)
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM lead_messages WHERE id = %d AND delivery = 'sent' AND email_message_id IS NOT NULL`, first)); n != 1 {
		t.Error("the answer is not marked as sent")
	}

	// The next answer points at the previous one; an address that bounces for good marks the answer failed.
	second, _ := store.Reply(ctx, id, "denis", "Коммерческое предложение во вложении.")
	smtp.RejectWith("550 5.1.1 no such user")
	if _, err := worker.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM lead_messages WHERE id = %d AND delivery = 'failed'`, second)); n != 1 {
		t.Error("a bounced answer is not marked as failed")
	}
	smtp.RejectWith("")
	third, _ := store.Reply(ctx, id, "denis", "Пробую ещё раз.")
	if _, err := worker.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	letters := smtp.Messages()
	last, _ := letters[len(letters)-1].Message()
	if want := fmt.Sprintf("<lead-1.reply-%d@krokosha.xyz>", first); last.Header.Get("In-Reply-To") != want || !strings.Contains(last.Header.Get("References"), "<lead-1.autoreply@krokosha.xyz>") {
		t.Errorf("the third answer: In-Reply-To %q, References %q", last.Header.Get("In-Reply-To"), last.Header.Get("References"))
	}
	_ = third
}

// Files stay on the server; the letters say which ones came.
func TestLettersNameTheFiles(t *testing.T) {
	f := newFixture(t)
	f.acceptFiles = true
	smtp := mailtest.Start(t)
	sender := &mail.Sender{Addr: smtp.Addr, Hello: "krokosha.xyz"}
	store := NewStore(f.db, nil)
	worker := outbox.NewWorker(f.db, quiet)
	worker.SetClock(func() time.Time { return f.now })
	worker.Register(outbox.ChannelEmail, &Mailer{
		Store: store, Deliver: sender.Send, SiteHost: "krokosha.xyz", AdminURL: "https://krokosha.xyz/_secret1/",
		From:     netmail.Address{Name: "Denis Humen", Address: "denis@krokosha.xyz"},
		NotifyTo: netmail.Address{Address: "owner@krokosha.xyz"},
		Form:     formWithLabels, Location: time.UTC,
	})

	files := []testFile{{"Схема & <план>.pdf", pdfFile()}, {"plan.png", append(pngFile(), make([]byte, 2<<20)...)}}
	if got := f.upload(validValues(), files, asJSON); got.status != http.StatusCreated {
		t.Fatalf("the form: %d %s", got.status, got.body)
	}
	if _, err := worker.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	received := smtp.Messages()
	if len(received) != 2 {
		t.Fatalf("letters received: %d, want 2", len(received))
	}
	for _, letter := range received {
		_, text, html := parts(t, letter)
		if letter.To[0] == "owner@krokosha.xyz" {
			if !strings.Contains(text, "Схема & план.pdf (77 Б), plan.png (2,0 МБ)") {
				t.Errorf("the plain notification does not name the files:\n%s", text)
			}
			if !strings.Contains(html, "Схема &amp; план.pdf (77 Б), plan.png (2,0 МБ)") {
				t.Errorf("the HTML notification: files missing or not escaped:\n%s", html)
			}
		} else if !strings.Contains(text, "Файлы: 2") || strings.Contains(text+html, "plan.png") {
			// The client is told how many; names of files are the visitor's text too.
			t.Errorf("the confirmation about the files:\n%s", text)
		}
		if strings.Contains(strings.ToLower(text+html), "content-disposition: attachment") {
			t.Error("a file travelled by mail")
		}
	}
}

// An answer with files: they go with the letter while they fit, the rest wait in the account.
func TestAnAnswerCarriesItsFilesByMail(t *testing.T) {
	f := newFixture(t)
	smtp := mailtest.Start(t)
	sender := &mail.Sender{Addr: smtp.Addr, Hello: "krokosha.xyz"}
	store := NewStore(f.db, func() time.Time { return f.now })
	store.UseFiles(f.files)
	mailer := &Mailer{
		Store: store, Deliver: sender.Send, SiteHost: "krokosha.xyz", AdminURL: "https://krokosha.xyz/_secret1/",
		From:     netmail.Address{Name: "Denis Humen", Address: "denis@krokosha.xyz"},
		NotifyTo: netmail.Address{Address: "owner@krokosha.xyz"},
		Form:     formWithLabels, Location: time.UTC,
	}
	ctx := context.Background()
	lead := f.seed(nil)
	save := func(name string, content []byte) Upload {
		upload, err := SaveOutgoing(f.files, name, int64(len(content)), bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		return upload
	}
	video := append(mp4File("isom"), make([]byte, 16<<20)...)
	answer, err := store.ReplyWith(ctx, lead, "denis", Answer{Text: "Смета и видео объекта.", Files: []Upload{save("Смета.pdf", pdfFile()), save("tour.mp4", video)}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(TaskPayload{LeadID: lead, MessageID: answer})
	if err := mailer.Send(ctx, outbox.Task{Channel: outbox.ChannelEmail, Kind: TaskReply, LeadID: lead, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	received := smtp.Messages()
	message, err := received[len(received)-1].Message()
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, _ := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if mediaType != "multipart/mixed" {
		t.Fatalf("the letter is %s", mediaType)
	}
	reader := multipart.NewReader(message.Body, params["boundary"])
	var text string
	var files []string
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, disposition, err := mime.ParseMediaType(part.Header.Get("Content-Disposition")); err == nil {
			content, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
			files = append(files, fmt.Sprintf("%s %s %d", disposition["filename"], part.Header.Get("Content-Type"), len(content)))
			continue
		}
		_, inner, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		alternatives := multipart.NewReader(part, inner["boundary"])
		plain, err := alternatives.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(plain)
		text = string(body)
	}
	if len(files) != 1 || !strings.HasPrefix(files[0], "Смета.pdf application/pdf") || !strings.HasSuffix(files[0], fmt.Sprintf(" %d", len(pdfFile()))) {
		t.Errorf("the files of the letter: %q", files)
	}
	if !strings.Contains(text, "Не поместились в письмо: tour.mp4 (16,0 МБ).") || !strings.Contains(text, fmt.Sprintf("https://krokosha.xyz/ru/account/#%s", Number(lead))) {
		t.Errorf("the letter about the video that did not fit:\n%s", text)
	}
	var delivery string
	_ = f.db.QueryRow(`SELECT delivery FROM lead_messages WHERE id = ?`, answer).Scan(&delivery)
	if delivery != "sent" {
		t.Errorf("the answer: %s", delivery)
	}
}

// The owner hears by mail too when a client writes again — in the thread of the request.
func TestTheOwnerIsToldWhenTheClientWritesAgain(t *testing.T) {
	f := newFixture(t)
	smtp := mailtest.Start(t)
	sender := &mail.Sender{Addr: smtp.Addr, Hello: "krokosha.xyz"}
	store := NewStore(f.db, func() time.Time { return f.now })
	worker := outbox.NewWorker(f.db, quiet)
	worker.SetClock(func() time.Time { return f.now })
	worker.Register(outbox.ChannelEmail, &Mailer{
		Store: store, Deliver: sender.Send, SiteHost: "krokosha.xyz", AdminURL: "https://krokosha.xyz/_secret1/",
		From:     netmail.Address{Name: "Denis Humen", Address: "denis@krokosha.xyz"},
		NotifyTo: netmail.Address{Address: "owner@krokosha.xyz"},
		Form:     formWithLabels, Location: time.UTC,
	})
	lead := f.seed(nil)
	if _, err := worker.Deliver(context.Background()); err != nil { // the notification and the confirmation
		t.Fatal(err)
	}
	if _, err := store.ClientMessage(context.Background(), lead, ChannelTelegram, "Забыл сказать: нужен ещё <b>гостевой</b> Wi-Fi."); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	received := smtp.Messages()
	if len(received) != 3 {
		t.Fatalf("letters received: %d, want 3", len(received))
	}
	letter := received[2]
	header, text, html := parts(t, letter)
	if letter.To[0] != "owner@krokosha.xyz" || subject(t, header) != "Re: Заявка #K-0001 · Сети и оборудование · Иван Петров" {
		t.Errorf("the letter: to %v, subject %q", letter.To, subject(t, header))
	}
	if header.Get("In-Reply-To") != "<lead-1.notify@krokosha.xyz>" || header.Get("Message-Id") != fmt.Sprintf("<lead-1.client-%d@krokosha.xyz>", 2) {
		t.Errorf("threading: In-Reply-To %q, Message-Id %q", header.Get("In-Reply-To"), header.Get("Message-Id"))
	}
	if !strings.Contains(text, "Иван Петров пишет по заявке #K-0001 (в Telegram)") || !strings.Contains(text, "нужен ещё <b>гостевой</b> Wi-Fi") {
		t.Errorf("the plain letter:\n%s", text)
	}
	if !strings.Contains(html, "&lt;b&gt;гостевой&lt;/b&gt;") || !strings.Contains(html, `href="https://krokosha.xyz/_secret1/leads/1"`) {
		t.Errorf("the HTML letter:\n%s", html)
	}
}

// Letters to a client ask for the answer to come back to the service mailbox, with the request's
// number signed into the address — and leave the server under the service's own envelope.
func TestAnswersComeBackToTheServiceMailbox(t *testing.T) {
	f := newFixture(t)
	smtp := mailtest.Start(t)
	sender := &mail.Sender{Addr: smtp.Addr, Hello: "krokosha.xyz", Envelope: "leads@krokosha.xyz"}
	store := NewStore(f.db, func() time.Time { return f.now })
	worker := outbox.NewWorker(f.db, quiet)
	worker.SetClock(func() time.Time { return f.now })
	worker.Register(outbox.ChannelEmail, &Mailer{
		Store: store, Deliver: sender.Send, SiteHost: "krokosha.xyz", AdminURL: "https://krokosha.xyz/_secret1/",
		From:     netmail.Address{Name: "Denis Humen", Address: "denis@krokosha.xyz"},
		NotifyTo: netmail.Address{Address: "owner@krokosha.xyz"},
		Form:     formWithLabels, Location: time.UTC, Inbox: "leads@krokosha.xyz", Secret: secret,
	})
	lead := f.seed(nil)
	if _, err := store.Reply(context.Background(), lead, "denis", "Спасибо, изучу и отвечу сегодня."); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	received := smtp.Messages()
	if len(received) != 3 {
		t.Fatalf("letters received: %d, want 3", len(received))
	}
	want := ReplyAddress(secret, "leads@krokosha.xyz", lead)
	for _, letter := range received {
		header, _, _ := parts(t, letter)
		if letter.From != "leads@krokosha.xyz" {
			t.Errorf("the envelope sender of the letter to %v: %q", letter.To, letter.From)
		}
		if from, _ := header.AddressList("From"); len(from) != 1 || from[0].Address != "denis@krokosha.xyz" {
			t.Errorf("the From header of the letter to %v: %v", letter.To, from)
		}
		replyTo, _ := header.AddressList("Reply-To")
		switch letter.To[0] {
		case "owner@krokosha.xyz": // «reply» in the owner's mail program goes to the client
			if len(replyTo) != 1 || replyTo[0].Address != "Ivan.Petrov@company.com" {
				t.Errorf("Reply-To of the notification: %v", replyTo)
			}
		default: // the confirmation and the answer: back to the request
			if len(replyTo) != 1 || replyTo[0].Address != want {
				t.Errorf("Reply-To of a letter to the client: %v, want %s", replyTo, want)
			}
			if id, ok := ParseReplyAddress(secret, replyTo[0].Address); !ok || id != lead {
				t.Errorf("the address does not lead back to the request: %d %v", id, ok)
			}
		}
	}
}

// A client's letter with a file, a robot's note, a letter of ours that came back (brief B10.5):
// what the owner hears about, and what stays quiet.
func TestLettersOfAClientAndLettersThatCameBack(t *testing.T) {
	f := newFixture(t)
	smtp := mailtest.Start(t)
	sender := &mail.Sender{Addr: smtp.Addr, Hello: "krokosha.xyz"}
	store := NewStore(f.db, func() time.Time { return f.now })
	store.UseFiles(f.files)
	worker := outbox.NewWorker(f.db, quiet)
	worker.SetClock(func() time.Time { return f.now })
	worker.Register(outbox.ChannelEmail, &Mailer{
		Store: store, Deliver: sender.Send, SiteHost: "krokosha.xyz", AdminURL: "https://krokosha.xyz/_secret1/",
		From:     netmail.Address{Name: "Denis Humen", Address: "denis@krokosha.xyz"},
		NotifyTo: netmail.Address{Address: "owner@krokosha.xyz"},
		Form:     formWithLabels, Location: time.UTC,
	})
	ctx := context.Background()
	lead := f.seed(nil)
	if _, err := store.Take(ctx, lead, "denis"); err != nil {
		t.Fatal(err)
	}
	answer, err := store.Reply(ctx, lead, "denis", "Сколько коммутаторов уже есть?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Deliver(ctx); err != nil { // the notification, the confirmation, the answer
		t.Fatal(err)
	}
	before := len(smtp.Messages())

	// A robot's note: kept, and nothing else happens.
	if _, err := store.ClientWrote(ctx, lead, Incoming{Channel: ChannelEmail, Text: "[автоответ] Я в отпуске.", Automatic: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.text(`SELECT status FROM leads WHERE id = ?`, lead); got != StatusWaitingClient {
		t.Errorf("a robot's note moved the request to %s", got)
	}

	// The client's own letter, with a file.
	upload, err := f.files.Save("схема.pdf", KindPDF, strings.NewReader("%PDF-1.7 the scheme"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClientWrote(ctx, lead, Incoming{Channel: ChannelEmail, Text: "Два, схема во вложении.", Files: []Upload{upload}, EmailMessageID: "abc@company.com"}); err != nil {
		t.Fatal(err)
	}
	if got := f.text(`SELECT status FROM leads WHERE id = ?`, lead); got != StatusInProgress {
		t.Errorf("the client answered, the request is %s", got)
	}
	// …and the answer that did not arrive.
	sentAs := f.text(`SELECT email_message_id FROM lead_messages WHERE id = ?`, answer)
	if err := store.Undelivered(ctx, lead, sentAs, "ivan@compny.test 5.4.4 Host or domain name <not> found", nil); err != nil {
		t.Fatal(err)
	}
	if got := f.text(`SELECT delivery FROM lead_messages WHERE id = ?`, answer); got != "failed" {
		t.Errorf("the returned answer is marked %q", got)
	}
	if err := store.Undelivered(ctx, 4242, "", "whatever", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("a report about a request that does not exist: %v", err)
	}

	if _, err := worker.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	received := smtp.Messages()[before:]
	if len(received) != 2 {
		t.Fatalf("letters to the owner: %d, want 2 (the robot's note is not announced)", len(received))
	}
	_, text, html := parts(t, received[0])
	if !strings.Contains(text, "пишет по заявке #K-0001 (письмом)") || !strings.Contains(text, "Файлы (в админке): схема.pdf (19 Б)") || !strings.Contains(html, "схема.pdf") {
		t.Errorf("the letter about the client's letter:\n%s", text)
	}
	header, text, html := parts(t, received[1])
	if subject(t, header) != "Не доставлено: заявка #K-0001 · Иван Петров" || header.Get("In-Reply-To") != "<lead-1.notify@krokosha.xyz>" {
		t.Errorf("the letter about the returned letter: %q, In-Reply-To %q", subject(t, header), header.Get("In-Reply-To"))
	}
	if !strings.Contains(text, "5.4.4 Host or domain name <not> found") || !strings.Contains(html, "name &lt;not&gt; found") {
		t.Errorf("the reason:\n%s\n%s", text, html)
	}

	// A letter from an address nobody left: no request to put it to.
	if _, err := store.ByEmail(ctx, "stranger@else.test"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a stranger's address: %v", err)
	}
	if id, err := store.ByEmail(ctx, "IVAN.PETROV@company.com"); err != nil || id != lead {
		t.Errorf("the client's address in another case: %d, %v", id, err)
	}
}

// An alert about the server is a letter to the owner: what is wrong, and a link to the status screen.
func TestAnAlertBecomesALetterToTheOwner(t *testing.T) {
	f := newFixture(t)
	smtp := mailtest.Start(t)
	sender := &mail.Sender{Addr: smtp.Addr, Hello: "krokosha.xyz"}
	worker := outbox.NewWorker(f.db, quiet)
	worker.SetClock(func() time.Time { return f.now })
	worker.Register(outbox.ChannelEmail, &Mailer{
		Store: NewStore(f.db, nil), Deliver: sender.Send, SiteHost: "krokosha.xyz", AdminURL: "https://krokosha.xyz/_secret1/",
		From:     netmail.Address{Name: "Denis Humen", Address: "denis@krokosha.xyz"},
		NotifyTo: netmail.Address{Address: "owner@krokosha.xyz"}, Form: formWithLabels, Location: time.UTC,
	})
	if err := outbox.EnqueueAlert(context.Background(), f.db, f.now, "cert:2026-09-19",
		outbox.Alert{Subject: "Сертификат mail.krokosha.xyz истекает через 9 дней", Text: "certbot renew:\n<connection refused>"}); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	received := smtp.Messages()
	if len(received) != 1 || received[0].To[0] != "owner@krokosha.xyz" {
		t.Fatalf("letters: %+v", received)
	}
	header, text, html := parts(t, received[0])
	if subject(t, header) != "[krokosha.xyz] Сертификат mail.krokosha.xyz истекает через 9 дней" || header.Get("Auto-Submitted") != "auto-generated" {
		t.Errorf("subject %q, Auto-Submitted %q", subject(t, header), header.Get("Auto-Submitted"))
	}
	if !strings.Contains(text, "<connection refused>") || !strings.Contains(text, "https://krokosha.xyz/_secret1/status") || !strings.Contains(html, "&lt;connection refused&gt;") {
		t.Errorf("the letter:\n%s\n%s", text, html)
	}
}

// A letter to the address typed into the form may call the visitor by name only if the name reads
// as one.
func TestGreetingName(t *testing.T) {
	for name, want := range map[string]string{
		"Иван Петров":                "Иван Петров",
		"  Олена   Коваль-Шевченко ": "Олена Коваль-Шевченко",
		"O'Brien":                      "O'Brien",
		"José":                         "José",
		"":                             "",
		"visit spam.example":           "",
		"Win $1000 now":                "",
		"http://spam.example":          "",
		"Denis @krokosha":              "",
		"one two three four five":      "",
		strings.Repeat("Александр", 5): "",
		`<img src=x onerror=alert(1)>"Бобби"`: "",
	} {
		if got := greetingName(name); got != want {
			t.Errorf("greetingName(%q) = %q, want %q", name, got, want)
		}
	}
}

// IDs of letters stay the same for every attempt, but are this installation's own: requests are
// numbered from one again after a reinstall.
func TestLetterIDsAreSignedForThisInstallation(t *testing.T) {
	here := &Mailer{SiteHost: "krokosha.xyz", Secret: []byte("secret of this installation")}
	there := &Mailer{SiteHost: "krokosha.xyz", Secret: []byte("secret of the installation before")}
	id := here.messageID(2, "autoreply")
	if !strings.HasPrefix(id, "lead-2.autoreply.") || !strings.HasSuffix(id, "@krokosha.xyz") || len(id) != len("lead-2.autoreply.")+8+len("@krokosha.xyz") {
		t.Errorf("id = %q", id)
	}
	if here.messageID(2, "autoreply") != id {
		t.Error("the id changes between attempts")
	}
	if there.messageID(2, "autoreply") == id || here.messageID(3, "autoreply") == id || here.messageID(2, "notify") == id {
		t.Error("ids repeat across installations or letters")
	}
	if got := (&Mailer{SiteHost: "krokosha.xyz"}).messageID(2, "notify"); got != "lead-2.notify@krokosha.xyz" {
		t.Errorf("without a secret: %q", got)
	}
}

// «Continue in Telegram» in a letter leads through the site's own address to the bot — and never
// anywhere else.
func TestTheLetterLinkLeadsToTheBot(t *testing.T) {
	f := newFixture(t)
	lead := f.seed(nil)
	token := f.created[0].PublicToken

	got := f.do(http.MethodGet, TelegramPath+token, nil, nil)
	if got.status != http.StatusFound || got.location != "https://t.me/krokosha_bot?start=c_"+token {
		t.Errorf("a request's link: %d → %q", got.status, got.location)
	}
	for _, token := range []string{"", "not-a-token", "AAAAAAAAAAAAAAAAAAAAAA", token + "x", "https://evil.example/"} {
		if got := f.do(http.MethodGet, TelegramPath+token, nil, nil); got.status != http.StatusFound || got.location != "/" {
			t.Errorf("token %q: %d → %q, want the site", token, got.status, got.location)
		}
	}
	if _, err := f.db.Exec(`UPDATE leads SET status = 'spam' WHERE id = ?`, lead); err != nil {
		t.Fatal(err)
	}
	if got := f.do(http.MethodGet, TelegramPath+token, nil, nil); got.status != http.StatusFound || got.location != "/ru/" {
		t.Errorf("a request taken for spam: %d → %q, want the site", got.status, got.location)
	}
}
