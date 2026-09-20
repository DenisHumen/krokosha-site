package inbox

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/quotedprintable"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

func crlf(text string) []byte {
	return []byte(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n"))
}

func qp(text string) string {
	var out bytes.Buffer
	w := quotedprintable.NewWriter(&out)
	_, _ = w.Write([]byte(text))
	_ = w.Close()
	return out.String()
}

func b64(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var out strings.Builder
	for len(encoded) > 76 {
		out.WriteString(encoded[:76] + "\n")
		encoded = encoded[76:]
	}
	out.WriteString(encoded + "\n")
	return out.String()
}

var pdf = []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")

// The brief's own test (B10.7): a letter with a quote and an attachment, the way Gmail sends it.
func TestAnAnswerWithAQuoteAndAnAttachment(t *testing.T) {
	own := "Добрый день!\n\nДа, VLAN два: офис и гости. Схему сети прикладываю.\nКогда сможете начать?\n\n-- \nИван Петров\nООО «Компания»"
	quoted := "пн, 1 сент. 2026 г. в 10:04, Denis Humen <leads+k-0042.abcdefghijklmnop@krokosha.xyz>:\n\n" +
		"> Спасибо, изучу и отвечу до конца дня.\n>\n> --\n> Denis Humen\n> krokosha.xyz · #K-0042\n"
	raw := crlf(`Return-Path: <ivan@company.test>
Delivered-To: leads+k-0042.abcdefghijklmnop@krokosha.xyz
Received: from mail.company.test (mail.company.test [203.0.113.7])
	by mail.krokosha.xyz (Postfix) with ESMTPS id 4XyZ
	for <leads+k-0042.abcdefghijklmnop@krokosha.xyz>; Tue, 1 Sep 2026 10:20:01 +0000 (UTC)
From: =?UTF-8?B?0JjQstCw0L0g0J/QtdGC0YDQvtCy?= <Ivan@Company.test>
To: Denis Humen <leads+K-0042.ABCDEFGHIJKLMNOP@krokosha.xyz>
Subject: =?UTF-8?Q?Re:_=D0=97=D0=B0=D1=8F=D0=B2=D0=BA=D0=B0_#K-0042_=D0=BF=D1=80=D0=B8=D0=BD=D1=8F=D1=82=D0=B0?=
Date: Tue, 1 Sep 2026 13:20:00 +0300
Message-ID: <CAF=abc123@mail.company.test>
In-Reply-To: <lead-42.reply-7@krokosha.xyz>
References: <lead-42.autoreply@krokosha.xyz> <lead-42.reply-7@krokosha.xyz>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="outer"

--outer
Content-Type: multipart/alternative; boundary="inner"

--inner
Content-Type: text/plain; charset="UTF-8"
Content-Transfer-Encoding: quoted-printable

` + qp(own+"\n\n"+quoted) + `
--inner
Content-Type: text/html; charset="UTF-8"
Content-Transfer-Encoding: quoted-printable

` + qp(`<div dir="ltr">Добрый день!<br><script>alert(1)</script></div><div class="gmail_quote">…</div>`) + `
--inner--
--outer
Content-Type: application/pdf; name="=?UTF-8?B?0YHRhdC10LzQsC5wZGY=?="
Content-Disposition: attachment; filename="=?UTF-8?B?0YHRhdC10LzQsC5wZGY=?="
Content-Transfer-Encoding: base64

` + b64(pdf) + `
--outer--
`)
	letter, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if letter.From.Address != "ivan@company.test" || letter.From.Name != "Иван Петров" {
		t.Errorf("from = %+v", letter.From)
	}
	if letter.Subject != "Re: Заявка #K-0042 принята" {
		t.Errorf("subject = %q", letter.Subject)
	}
	if letter.MessageID != "CAF=abc123@mail.company.test" || letter.InReplyTo != "lead-42.reply-7@krokosha.xyz" || len(letter.References) != 2 {
		t.Errorf("ids = %q, %q, %q", letter.MessageID, letter.InReplyTo, letter.References)
	}
	own = strings.ReplaceAll(own, "-- ", "--") // spaces at the end of a line are not kept
	if letter.Text != own {
		t.Errorf("the client's own words:\n%q\nwant\n%q", letter.Text, own)
	}
	if !strings.Contains(letter.Full, "изучу и отвечу") {
		t.Errorf("the full text lost the quote: %q", letter.Full)
	}
	wantRecipient := "leads+k-0042.abcdefghijklmnop@krokosha.xyz"
	if len(letter.Recipients) == 0 || letter.Recipients[0] != wantRecipient {
		t.Errorf("recipients = %q", letter.Recipients)
	}
	if len(letter.Files) != 1 || letter.Files[0].Name != "схема.pdf" || !bytes.Equal(letter.Files[0].Content, pdf) || letter.Files[0].Inline {
		t.Fatalf("files = %+v", letter.Files)
	}
	if kind, err := leads.Inspect(letter.Files[0].Name, int64(len(letter.Files[0].Content)), bytes.NewReader(letter.Files[0].Content)); err != nil || kind != leads.KindPDF {
		t.Errorf("the attachment does not pass the inspection of files: %q, %v", kind, err)
	}
	if letter.Automatic || letter.Bounce != nil {
		t.Errorf("a person's letter was taken for a program's: %+v", letter)
	}
}

func TestOldEncodingsAreRead(t *testing.T) {
	body, _ := charmap.Windows1251.NewEncoder().String("Здравствуйте! Подскажите по срокам.")
	subject, _ := charmap.KOI8R.NewEncoder().String("Вопрос")
	raw := append(crlf(`From: client@example.org
To: leads@krokosha.xyz
Subject: =?koi8-r?B?`+base64.StdEncoding.EncodeToString([]byte(subject))+`?=
Content-Type: text/plain; charset=windows-1251
Content-Transfer-Encoding: 8bit

`), []byte(body)...)
	letter, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if letter.Subject != "Вопрос" || letter.Text != "Здравствуйте! Подскажите по срокам." {
		t.Fatalf("subject = %q, text = %q", letter.Subject, letter.Text)
	}
}

func TestALetterInHTMLOnly(t *testing.T) {
	raw := crlf(`From: Client <client@example.org>
To: leads@krokosha.xyz
Subject: Re: request
Content-Type: text/html; charset=utf-8

<html><head><style>p { color: red }</style><title>x</title></head><body>
<p>Hello &amp; thanks!</p><div>Two&nbsp;VLANs: office &lt;-&gt; guests.<br>Regards</div>
<!-- a comment -->
<div class="gmail_quote gmail_quote_container"><div class="gmail_attr">On Mon, Denis wrote:</div><blockquote>the old text</blockquote></div>
</body></html>
`)
	letter, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := "Hello & thanks!\nTwo VLANs: office <-> guests.\nRegards"
	if letter.Text != want {
		t.Fatalf("text = %q, want %q", letter.Text, want)
	}
}

func TestQuotes(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"gmail, english": {
			"Sounds good, let's start on Monday.\n\nOn Mon, Sep 1, 2026 at 10:04 AM Denis Humen <denis@krokosha.xyz>\nwrote:\n\n> Thanks, I'll reply today.\n",
			"Sounds good, let's start on Monday.",
		},
		"apple mail": {
			"Да, подходит.\n\n> 1 сент. 2026 г., в 10:04, Denis Humen <denis@krokosha.xyz> написал(а):\n> \n> Спасибо, изучу.\n",
			"Да, подходит.",
		},
		"ukrainian gmail": {
			"Так, підходить.\n\nпн, 1 вер. 2026 р. о 10:04 Denis Humen <denis@krokosha.xyz> пише:\n\n> Дякую, відповім сьогодні.\n",
			"Так, підходить.",
		},
		"yandex": {
			"Хорошо, жду.\n\n01.09.2026, 10:04, \"Denis Humen\" <denis@krokosha.xyz>:\n> Спасибо, изучу.\n",
			"Хорошо, жду.",
		},
		"outlook": {
			"Коллеги, договор во вложении.\n\n________________________________\nОт: Denis Humen <denis@krokosha.xyz>\nОтправлено: 1 сентября 2026 г. 10:04\nКому: Иван\nТема: Re: Заявка\n\nСпасибо, изучу.\n",
			"Коллеги, договор во вложении.",
		},
		"original message": {
			"OK.\n\n-----Original Message-----\nFrom: Denis\nSent: Monday\n\nThanks.\n",
			"OK.",
		},
		"an answer below the quote": {
			"On Mon, Sep 1, 2026 at 10:04 AM Denis <denis@krokosha.xyz> wrote:\n> When can you start?\n\nNext week.\n",
			"Next week.",
		},
		"an answer between the lines": {
			"> How many VLANs?\nTwo.\n> Which router?\nMikroTik hEX.\n",
			"> How many VLANs?\nTwo.\n> Which router?\nMikroTik hEX.",
		},
		"words that only sound like a quote": {
			"Вчера мой коллега написал:\nроутер перезагружается сам.\nЧто посоветуете?",
			"Вчера мой коллега написал:\nроутер перезагружается сам.\nЧто посоветуете?",
		},
		"a time in the text": {
			"Созвон в 15:30 подойдёт?\nИли завтра:\nв любое время.",
			"Созвон в 15:30 подойдёт?\nИли завтра:\nв любое время.",
		},
		"nothing but a quote": {
			"> only the old text\n",
			"> only the old text",
		},
	}
	for name, c := range cases {
		if got := StripQuote(tidy(c.in)); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, c.want)
		}
	}
}

func TestADeliveryReportIsReadApart(t *testing.T) {
	raw := crlf(`Return-Path: <>
From: Mail Delivery System <MAILER-DAEMON@mail.krokosha.xyz>
To: leads@krokosha.xyz
Subject: Undelivered Mail Returned to Sender
Auto-Submitted: auto-replied
Message-ID: <20260901102001.4XyZ@mail.krokosha.xyz>
Content-Type: multipart/report; report-type=delivery-status; boundary="b1"

--b1
Content-Description: Notification
Content-Type: text/plain; charset=us-ascii

This is the mail system at host mail.krokosha.xyz.
<ivan@compny.test>: Host or domain name not found.

--b1
Content-Description: Delivery report
Content-Type: message/delivery-status

Reporting-MTA: dns; mail.krokosha.xyz
Arrival-Date: Tue,  1 Sep 2026 10:20:00 +0000 (UTC)

Final-Recipient: rfc822; ivan@compny.test
Original-Recipient: rfc822;ivan@compny.test
Action: failed
Status: 5.4.4
Diagnostic-Code: X-Postfix; Host or domain name not found. Name service error
    for name=compny.test type=AAAA: Host not found

--b1
Content-Description: Undelivered Message Headers
Content-Type: text/rfc822-headers

Return-Path: <leads@krokosha.xyz>
From: Denis Humen <denis@krokosha.xyz>
To: Ivan <ivan@compny.test>
Reply-To: Denis Humen <leads+k-0042.abcdefghijklmnop@krokosha.xyz>
Subject: Re: Request #K-0042 received
Message-ID: <lead-42.reply-7@krokosha.xyz>
X-Krokosha-Lead: K-0042

--b1--
`)
	letter, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	bounce := letter.Bounce
	if bounce == nil || !letter.Automatic {
		t.Fatalf("not recognised as a delivery report: %+v", letter)
	}
	if bounce.Status != "5.4.4" || !bounce.Failed() || bounce.Recipient != "ivan@compny.test" {
		t.Errorf("bounce = %+v", bounce)
	}
	if !strings.HasPrefix(bounce.Diagnostic, "Host or domain name not found") || strings.Contains(bounce.Diagnostic, "\n") {
		t.Errorf("diagnostic = %q", bounce.Diagnostic)
	}
	if bounce.OriginalID != "lead-42.reply-7@krokosha.xyz" || bounce.OriginalReplyTo != "leads+k-0042.abcdefghijklmnop@krokosha.xyz" {
		t.Errorf("the returned headers: %+v", bounce)
	}
	if len(letter.Files) != 0 {
		t.Errorf("parts of a report became files: %+v", letter.Files)
	}
	delayed := &Bounce{Status: "4.4.1"}
	if delayed.Failed() {
		t.Error("«still trying» was read as «gave up»")
	}
}

func TestLettersOfPrograms(t *testing.T) {
	for name, headers := range map[string]string{
		"out of office": "Auto-Submitted: auto-replied\n",
		"a mailing":     "List-Unsubscribe: <mailto:x@example.org>\n",
		"precedence":    "Precedence: bulk\n",
		"no-reply":      "",
	} {
		from := "client@example.org"
		if name == "no-reply" {
			from = "No-Reply@shop.example"
		}
		letter, err := Parse(crlf("From: " + from + "\nTo: leads@krokosha.xyz\nSubject: x\n" + headers + "\nI am away until Monday.\n"))
		if err != nil {
			t.Fatal(err)
		}
		if !letter.Automatic {
			t.Errorf("%s: taken for a person's letter", name)
		}
	}
	letter, _ := Parse(crlf("From: client@example.org\nTo: leads@krokosha.xyz\nSubject: x\nAuto-Submitted: no\nX-Auto-Response-Suppress: All\n\nHello.\n"))
	if letter.Automatic {
		t.Error("a person's letter was taken for a program's")
	}
}

func TestFilesOfALetter(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{1}, 64)...)
	big := bytes.Repeat([]byte("0123456789abcdef"), (leads.MaxAttachmentBytes/16)+1)
	raw := crlf(`From: client@example.org
To: leads@krokosha.xyz
Subject: files
Content-Type: multipart/mixed; boundary=b

--b
Content-Type: text/plain

See attached.
--b
Content-Type: image/png
Content-Disposition: inline; filename="logo.png"
Content-ID: <logo@sig>
Content-Transfer-Encoding: base64

` + b64(png) + `
--b
Content-Type: application/octet-stream
Content-Disposition: attachment; filename*=UTF-8''%D0%BF%D0%BB%D0%B0%D0%BD%20%D1%81%D0%B5%D1%82%D0%B8.txt
Content-Transfer-Encoding: base64

` + b64([]byte("план сети\n")) + `
--b
Content-Type: video/mp4
Content-Disposition: attachment; filename="../../etc/video.mp4"
Content-Transfer-Encoding: base64

` + b64(big) + `
--b
Content-Type: message/rfc822

From: someone@example.org
Subject: forwarded

inner
--b--
`)
	letter, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if letter.Text != "See attached." {
		t.Errorf("text = %q", letter.Text)
	}
	if len(letter.Files) != 4 {
		t.Fatalf("files = %d: %+v", len(letter.Files), names(letter.Files))
	}
	if f := letter.Files[0]; f.Name != "logo.png" || !f.Inline || !bytes.Equal(f.Content, png) {
		t.Errorf("the inline picture: %q inline=%v", f.Name, f.Inline)
	}
	if f := letter.Files[1]; f.Name != "план сети.txt" || string(f.Content) != "план сети\n" {
		t.Errorf("RFC 2231 name: %q %q", f.Name, f.Content)
	}
	if f := letter.Files[2]; !f.TooBig || f.Content != nil {
		t.Errorf("a file above the limit was kept: %q, %d bytes", f.Name, len(f.Content))
	}
	if f := letter.Files[3]; f.Name != "письмо.eml" {
		t.Errorf("a forwarded letter: %q", f.Name)
	}
}

func names(files []File) []string {
	var out []string
	for _, file := range files {
		out = append(out, file.Name)
	}
	return out
}

func TestLettersBuiltToHurt(t *testing.T) {
	// Parts inside parts inside parts.
	var nested strings.Builder
	nested.WriteString("From: x@example.org\nTo: leads@krokosha.xyz\nSubject: deep\nContent-Type: multipart/mixed; boundary=b0\n\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&nested, "--b%d\nContent-Type: multipart/mixed; boundary=b%d\n\n", i, i+1)
	}
	nested.WriteString("--b50\nContent-Type: text/plain\n\nbottom\n--b50--\n")
	letter, err := Parse(crlf(nested.String()))
	if err != nil {
		t.Fatal(err)
	}
	if letter.Text != "" {
		t.Errorf("fifty levels deep was read: %q", letter.Text)
	}

	// A thousand attachments.
	var many strings.Builder
	many.WriteString("From: x@example.org\nTo: leads@krokosha.xyz\nSubject: many\nContent-Type: multipart/mixed; boundary=b\n\n")
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&many, "--b\nContent-Type: text/plain\nContent-Disposition: attachment; filename=f%d.txt\n\nx\n", i)
	}
	many.WriteString("--b--\n")
	if letter, err = Parse(crlf(many.String())); err != nil || len(letter.Files) > maxFiles {
		t.Fatalf("files = %d, err = %v", len(letter.Files), err)
	}

	// No boundary, a broken content type, bytes that are no text, a sender that is no address.
	for _, raw := range []string{
		"From: x@example.org\nContent-Type: multipart/mixed\n\nbody\n",
		"From: x@example.org\nContent-Type: ;;;\n\nbody\n",
		"From: x@example.org\nContent-Type: text/plain; charset=utf-8\n\n\xff\xfe\x00broken\n",
		"From: <<<>>>\nSubject: =?utf-8?B?!!!?=\n\nbody\n",
	} {
		if _, err := Parse(crlf(raw)); err != nil {
			t.Errorf("%q: %v", raw, err)
		}
	}
	if _, err := Parse([]byte("not a letter at all")); err == nil {
		t.Error("garbage was parsed")
	}
}
