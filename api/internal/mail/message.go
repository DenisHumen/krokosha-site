// Package mail builds and sends the site's outgoing email: notifications about requests, automatic
// confirmations, replies to clients (brief B10). Standard library only.
package mail

import (
	"bytes"
	"crypto/rand"
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

	part := func(contentType, body string) error {
		fmt.Fprintf(&out, "Content-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", contentType)
		encoder := quotedprintable.NewWriter(&out)
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
		if err := part("text/plain", m.Text); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	boundary := make([]byte, 12)
	if _, err := rand.Read(boundary); err != nil {
		return nil, err
	}
	mark := "krokosha-" + hex.EncodeToString(boundary)
	fmt.Fprintf(&out, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", mark)
	// The plain text first: a mail program shows the last alternative it understands.
	for _, alternative := range []struct{ contentType, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		fmt.Fprintf(&out, "--%s\r\n", mark)
		if err := part(alternative.contentType, alternative.body); err != nil {
			return nil, err
		}
	}
	fmt.Fprintf(&out, "--%s--\r\n", mark)
	return out.Bytes(), nil
}
