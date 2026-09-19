package mail

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/mail/mailtest"
)

var sentAt = time.Date(2026, 9, 19, 12, 0, 0, 0, time.FixedZone("EEST", 3*3600))

func sample() Message {
	return Message{
		From:      netmail.Address{Name: "Денис Гумен", Address: "denis@krokosha.xyz"},
		To:        netmail.Address{Name: "Иван Петров", Address: "ivan@company.com"},
		ReplyTo:   &netmail.Address{Address: "leads@krokosha.xyz"},
		Subject:   "Заявка #K-0042 принята — krokosha.xyz",
		Text:      "Здравствуйте, Иван!\n\nВаша заявка #K-0042 принята.\n.\nСтрока, начинающаяся с точки, не должна оборвать письмо.",
		HTML:      "<p>Здравствуйте, Иван!</p><p>Ваша заявка <b>#K-0042</b> принята.</p>",
		MessageID: "lead-42.autoreply.0011223344556677@krokosha.xyz",
		InReplyTo: "lead-42.form@krokosha.xyz",
		Headers:   map[string]string{"Auto-Submitted": "auto-replied", "X-Krokosha-Lead": "K-0042"},
	}
}

func TestMessageIsWellFormed(t *testing.T) {
	raw, err := sample().Bytes(sentAt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ReplaceAll(string(raw), "\r\n", ""), "\n") {
		t.Error("bare line feeds: every line of a message ends with CRLF")
	}
	parsed, err := netmail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	decoder := new(mime.WordDecoder)
	subject, _ := decoder.DecodeHeader(parsed.Header.Get("Subject"))
	from, _ := parsed.Header.AddressList("From")
	if subject != "Заявка #K-0042 принята — krokosha.xyz" || len(from) != 1 || from[0].Name != "Денис Гумен" {
		t.Errorf("subject %q, from %v", subject, from)
	}
	for header, want := range map[string]string{
		"Message-Id":     "<lead-42.autoreply.0011223344556677@krokosha.xyz>",
		"In-Reply-To":    "<lead-42.form@krokosha.xyz>",
		"Reply-To":       "<leads@krokosha.xyz>",
		"Auto-Submitted": "auto-replied",
		"Date":           "Sat, 19 Sep 2026 12:00:00 +0300",
		"Mime-Version":   "1.0",
	} {
		if got := parsed.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("content type: %s %v", mediaType, err)
	}
	parts := multipart.NewReader(parsed.Body, params["boundary"])
	var kinds, bodies []string
	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		// multipart.Reader decodes quoted-printable by itself and drops the header.
		body, _ := io.ReadAll(part)
		if part.Header.Get("Content-Transfer-Encoding") != "" {
			body, _ = io.ReadAll(quotedprintable.NewReader(strings.NewReader(string(body))))
		}
		kinds = append(kinds, part.Header.Get("Content-Type"))
		bodies = append(bodies, string(body))
	}
	if len(kinds) != 2 || !strings.HasPrefix(kinds[0], "text/plain") || !strings.HasPrefix(kinds[1], "text/html") {
		t.Fatalf("parts: %v (plain text goes first, mail programs show the last they can)", kinds)
	}
	if !strings.Contains(bodies[0], "Здравствуйте, Иван!") || !strings.Contains(bodies[1], "<b>#K-0042</b>") {
		t.Errorf("bodies: %q", bodies)
	}
}

func TestHeadersCannotBeInjected(t *testing.T) {
	message := sample()
	message.Subject = "Привет\r\nBcc: everyone@example.com"
	message.Headers["X-Krokosha-Lead"] = "K-0042\r\nX-Evil: 1"
	raw, err := message.Bytes(sentAt)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := netmail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Get("Bcc") != "" || parsed.Header.Get("X-Evil") != "" {
		t.Errorf("injected headers got through: %v", parsed.Header)
	}

	if _, err := (Message{Subject: "no addresses"}).Bytes(sentAt); err == nil {
		t.Error("a message without a sender was rendered")
	}
}

func TestPlainTextOnly(t *testing.T) {
	message := sample()
	message.HTML = ""
	raw, _ := message.Bytes(sentAt)
	parsed, err := netmail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if mediaType, _, _ := mime.ParseMediaType(parsed.Header.Get("Content-Type")); mediaType != "text/plain" {
		t.Errorf("content type = %s", mediaType)
	}
}

func TestSendingAndFailures(t *testing.T) {
	server := mailtest.Start(t)
	sender := &Sender{Addr: server.Addr, Hello: "krokosha.xyz", Now: func() time.Time { return sentAt }}
	ctx := context.Background()

	if err := sender.Send(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	got := server.Messages()
	if len(got) != 1 || got[0].From != "denis@krokosha.xyz" || len(got[0].To) != 1 || got[0].To[0] != "ivan@company.com" {
		t.Fatalf("received: %+v", got)
	}
	// A line with a single dot would end the message if it were sent as it is.
	if !strings.Contains(got[0].Raw, "=D0=BD=D0=B5 =D0=B4=D0=BE=D0=BB=D0=B6=D0=BD=D0=B0") && !strings.Contains(got[0].Raw, "multipart/alternative") {
		t.Error("the message arrived cut short")
	}
	if parsed, err := got[0].Message(); err != nil || parsed.Header.Get("X-Krokosha-Lead") != "K-0042" {
		t.Errorf("the received message: %v", err)
	}

	// «No such user» will not change tomorrow; «try later» and a dead server may.
	server.RejectWith("550 5.1.1 no such user")
	var permanent PermanentError
	if err := sender.Send(ctx, sample()); !errors.As(err, &permanent) {
		t.Errorf("550: %v, want a permanent error", err)
	}
	server.RejectWith("451 4.3.0 try again later")
	if err := sender.Send(ctx, sample()); err == nil || errors.As(err, &permanent) {
		t.Errorf("451: %v, want a temporary error", err)
	}
	server.Stop()
	if err := sender.Send(ctx, sample()); err == nil || errors.As(err, &permanent) {
		t.Errorf("a dead server: %v, want a temporary error", err)
	}
	if len(server.Messages()) != 1 {
		t.Errorf("messages accepted: %d, want 1", len(server.Messages()))
	}
}
