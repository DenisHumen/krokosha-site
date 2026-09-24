// Package mail builds and sends the site's outgoing email: notifications about requests, automatic
// confirmations, replies to clients (brief B10). Standard library only.
package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"sort"
	"strings"
	"time"
)

// Message is one email with a plain-text and, optionally, an HTML body.
type Message struct {
	From    mail.Address
	To      mail.Address
	ReplyTo *mail.Address
	Subject string
	Text    string
	HTML    string

	// MessageID without the angle brackets; NewMessageID makes one. InReplyTo and References
	// keep a conversation in one thread of the recipient's mail program.
	MessageID  string
	InReplyTo  string
	References []string
	// Headers are added as they are: «Auto-Submitted», «X-Krokosha-Lead»…
	Headers map[string]string
	// Attachments go after the text, as files of the letter.
	Attachments []Attachment
}

// Attachment is a file of a letter.
type Attachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// NewMessageID returns «<prefix>.<random>@<domain>».
func NewMessageID(prefix, domain string) string {
	random := make([]byte, 8)
	_, _ = rand.Read(random)
	return prefix + "." + hex.EncodeToString(random) + "@" + domain
}

// Bytes renders the message as it goes on the wire (RFC 5322, CRLF line ends).
func (m Message) Bytes(now time.Time) ([]byte, error) {
	if m.From.Address == "" || m.To.Address == "" {
		return nil, errors.New("mail: a message needs a sender and a recipient")
	}
	var out bytes.Buffer
	header := func(name, value string) {
		// A line break in a header would let data become headers. Nothing here should ever
		// contain one; if it does, it goes no further.
		value = strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
		fmt.Fprintf(&out, "%s: %s\r\n", name, value)
	}

	header("Date", now.Format(time.RFC1123Z))
	header("From", m.From.String())
	header("To", m.To.String())
	if m.ReplyTo != nil {
		header("Reply-To", m.ReplyTo.String())
	}
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	if m.MessageID != "" {
		header("Message-ID", "<"+m.MessageID+">")
	}
	if m.InReplyTo != "" {
		header("In-Reply-To", "<"+m.InReplyTo+">")
	}
	if len(m.References) > 0 {
		header("References", "<"+strings.Join(m.References, "> <")+">")
	}
	names := make([]string, 0, len(m.Headers))
	for name := range m.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		header(name, m.Headers[name])
	}
	header("MIME-Version", "1.0")
	if len(m.Attachments) == 0 {
		if err := m.writeBody(&out); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	// The text first, then the files: a mail program shows the first part and lists the rest.
	mark, err := boundary()
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&out, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mark)
	fmt.Fprintf(&out, "--%s\r\n", mark)
	if err := m.writeBody(&out); err != nil {
		return nil, err
	}
	for _, file := range m.Attachments {
		fmt.Fprintf(&out, "--%s\r\n", mark)
		writeAttachment(&out, file)
	}
	fmt.Fprintf(&out, "--%s--\r\n", mark)
	return out.Bytes(), nil
}

// writeBody writes the text of a letter as one MIME entity: plain text, or plain text and HTML
// as alternatives.
func (m Message) writeBody(out *bytes.Buffer) error {
	part := func(contentType, body string) error {
		fmt.Fprintf(out, "Content-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", contentType)
		encoder := quotedprintable.NewWriter(out)
		if _, err := encoder.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
			return err
		}
		if err := encoder.Close(); err != nil {
			return err
		}
		out.WriteString("\r\n")
		return nil
	}

	if m.HTML == "" {
		return part("text/plain", m.Text)
	}
	mark, err := boundary()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", mark)
	// The plain text first: a mail program shows the last alternative it understands.
	for _, alternative := range []struct{ contentType, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		fmt.Fprintf(out, "--%s\r\n", mark)
		if err := part(alternative.contentType, alternative.body); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "--%s--\r\n", mark)
	return nil
}

// writeAttachment writes a file in base64, in lines of 76 characters. The name, whatever the
// language, is encoded the way RFC 2231 says — a line break in it goes nowhere.
func writeAttachment(out *bytes.Buffer, file Attachment) {
	name := strings.NewReplacer("\r", " ", "\n", " ", "\"", "'").Replace(file.Filename)
	contentType := file.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	typed, disposition := mime.FormatMediaType(contentType, map[string]string{"name": name}), mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if typed == "" || disposition == "" { // a name no standard can carry: the file goes without it
		typed, disposition = "application/octet-stream", "attachment"
	}
	fmt.Fprintf(out, "Content-Type: %s\r\n", typed)
	fmt.Fprintf(out, "Content-Disposition: %s\r\n", disposition)
	out.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	encoded := base64.StdEncoding.EncodeToString(file.Content)
	for len(encoded) > 76 {
		out.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	out.WriteString(encoded + "\r\n")
}

func boundary() (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "krokosha-" + hex.EncodeToString(random), nil
}
