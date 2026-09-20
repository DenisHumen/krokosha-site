// Package inbox reads the service mailbox (brief B10.5): a client answers a letter about their
// request, and the answer joins the conversation of that request — the number is signed into the
// address it was sent to. Letters nobody can place wait on a screen of the admin area.
//
// Everything in a letter is untrusted: the sender, the headers, the text, the names and the
// content of files. Nothing is executed, unpacked or rendered; files pass the same inspection as
// the files of the contact form.
package inbox

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// Limits of what is taken out of one letter.
const (
	maxTextBytes = 1 << 20 // of a text part; a conversation keeps far less
	maxParts     = 200
	maxDepth     = 6
	maxFiles     = 20 // named in the letter; how many are kept is the service's decision
)

// unreadable stands where bytes of a letter are no text in the encoding the letter claims.
const unreadable = "\uFFFD"

// Letter is an incoming email, taken apart.
type Letter struct {
	MessageID  string // without the angle brackets
	InReplyTo  string
	References []string
	From       mail.Address
	Recipients []string // every address the letter names as its destination, lower case
	Subject    string
	Date       time.Time
	// Text is what the sender wrote, the quoted conversation cut off; Full is the same with it.
	Text string
	Full string
	// Files are the attachments, decoded. One that is larger than a request may carry has no
	// content — it is only known to have been there.
	Files []File
	// Automatic: written by a program — an out-of-office note, a mailing, a delivery report.
	Automatic bool
	// Bounce is set when a mail server reports what became of a letter of ours.
	Bounce *Bounce
}

// File is an attachment of a letter.
type File struct {
	Name    string
	Content []byte
	TooBig  bool
	// Inline: shown inside the letter — a pasted screenshot, or the logo of a signature.
	Inline bool
}

// Bounce is a delivery report (RFC 3464), as far as it could be read.
type Bounce struct {
	Status     string // «5.1.1»: 5 — gave up, 4 — still trying, 2 — delivered
	Diagnostic string // the other server's words
	Recipient  string
	// Of the letter that did not arrive, when the report returns its headers.
	OriginalID      string
	OriginalReplyTo string
}

// Failed: the other side gave up — as opposed to «still trying» and «delivered».
func (b *Bounce) Failed() bool { return strings.HasPrefix(b.Status, "5") || b.Status == "" }

// Parse takes a letter apart.
func Parse(raw []byte) (*Letter, error) {
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	header := message.Header
	letter := &Letter{
		MessageID:  firstID(header.Get("Message-ID")),
		InReplyTo:  firstID(header.Get("In-Reply-To")),
		References: ids(header.Get("References")),
		Subject:    oneLine(decodeWords(header.Get("Subject"))),
	}
	if date, err := header.Date(); err == nil {
		letter.Date = date
	}
	letter.From = sender(header.Get("From"))
	letter.Recipients = recipients(header)
	letter.Automatic = automatic(header, letter.From.Address)

	w := &walker{}
	w.walk(textproto.MIMEHeader(header), message.Body, 0)
	letter.Files, letter.Bounce = w.files, w.bounce
	if letter.Bounce == nil && looksLikeBounce(letter) {
		letter.Bounce = &Bounce{}
	}
	if letter.Bounce != nil {
		letter.Automatic = true
	}

	letter.Full = w.text
	if strings.TrimSpace(letter.Full) == "" && w.html != "" {
		letter.Full = htmlToText(w.html)
	}
	letter.Full = tidy(letter.Full)
	letter.Text = StripQuote(letter.Full)
	return letter, nil
}

// --- headers ---

var wordDecoder = &mime.WordDecoder{CharsetReader: charsetReader}

// decodeWords reads «=?koi8-r?B?…?=». A header that cannot be decoded is shown as it is.
func decodeWords(value string) string {
	decoded, err := wordDecoder.DecodeHeader(value)
	if err != nil {
		return strings.ToValidUTF8(value, unreadable)
	}
	return decoded
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(strings.ToValidUTF8(text, unreadable)), " ")
}

var (
	reMessageID = regexp.MustCompile(`<([^<>\s]+)>`)
	reAddress   = regexp.MustCompile(`[^\s<>,;:"()\[\]]+@[^\s<>,;:"()\[\]]+`)
	reForClause = regexp.MustCompile(`(?i)\bfor\s+<?([^\s<>;]+@[^\s<>;]+)>?`)
)

func firstID(value string) string {
	if m := reMessageID.FindStringSubmatch(value); m != nil {
		return m[1]
	}
	return ""
}

func ids(value string) []string {
	var out []string
	for _, m := range reMessageID.FindAllStringSubmatch(value, 50) {
		out = append(out, m[1])
	}
	return out
}

// sender reads From. Mail programs produce addresses the strict parser refuses; the address
// itself is then picked out of the line, because without one a letter cannot be placed at all.
func sender(value string) mail.Address {
	parser := mail.AddressParser{WordDecoder: wordDecoder}
	if parsed, err := parser.Parse(value); err == nil {
		return mail.Address{Name: oneLine(parsed.Name), Address: strings.ToLower(parsed.Address)}
	}
	if address := reAddress.FindString(value); address != "" {
		return mail.Address{Address: strings.ToLower(address)}
	}
	return mail.Address{}
}

// recipients collects every address the letter was meant for. The signed address of a request
// is looked for among them: usually it is in To, but a copy, a hidden copy or a forwarding rule
// leaves it only in what the mail servers wrote down on the way.
func recipients(header mail.Header) []string {
	seen := map[string]bool{}
	var out []string
	add := func(address string) {
		address = strings.ToLower(strings.Trim(address, "<>"))
		if address != "" && !seen[address] && len(out) < 50 {
			seen[address] = true
			out = append(out, address)
		}
	}
	for _, name := range []string{"Delivered-To", "X-Original-To", "Envelope-To", "To", "Cc"} {
		for _, value := range header[textproto.CanonicalMIMEHeaderKey(name)] {
			for _, address := range reAddress.FindAllString(value, 50) {
				add(address)
			}
		}
	}
	for _, value := range header["Received"] {
		if m := reForClause.FindStringSubmatch(value); m != nil {
			add(m[1])
		}
	}
	return out
}

// automatic tells letters of programs from letters of people (RFC 3834 and common practice).
// An automatic letter is kept, but it moves nothing and nobody is woken up for it.
func automatic(header mail.Header, from string) bool {
	if value := strings.ToLower(strings.TrimSpace(header.Get("Auto-Submitted"))); value != "" && value != "no" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(header.Get("Precedence"))) {
	case "bulk", "junk", "list", "auto_reply":
		return true
	}
	for _, name := range []string{"X-Autoreply", "X-Autorespond", "List-Id", "List-Unsubscribe"} {
		if header.Get(name) != "" {
			return true
		}
	}
	if strings.TrimSpace(header.Get("Return-Path")) == "<>" {
		return true
	}
	local, _, _ := strings.Cut(from, "@")
	switch local {
	case "mailer-daemon", "postmaster", "noreply", "no-reply", "no_reply", "donotreply", "do-not-reply":
		return true
	}
	return false
}

var reBounceSubject = regexp.MustCompile(`(?i)undeliver|delivery status|delivery failure|failure notice|returned mail|не доставлено|недоставленное|mail delivery`)

// looksLikeBounce catches delivery reports that are not written by the book.
func looksLikeBounce(letter *Letter) bool {
	local, _, _ := strings.Cut(letter.From.Address, "@")
	return (local == "mailer-daemon" || local == "postmaster") && reBounceSubject.MatchString(letter.Subject)
}

// --- the body ---

type walker struct {
	text, html string
	files      []File
	bounce     *Bounce
	parts      int
}

func (w *walker) walk(header textproto.MIMEHeader, body io.Reader, depth int) {
	w.parts++
	if depth > maxDepth || w.parts > maxParts {
		return
	}
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType, params = "text/plain", map[string]string{"charset": params["charset"]}
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		if params["boundary"] == "" {
			return
		}
		if mediaType == "multipart/report" && strings.EqualFold(params["report-type"], "delivery-status") && w.bounce == nil {
			w.bounce = &Bounce{}
		}
		reader := multipart.NewReader(body, params["boundary"])
		for {
			part, err := reader.NextRawPart() // raw: the transfer encoding is undone here, the same way for every part
			if err != nil {
				return
			}
			w.walk(part.Header, part, depth+1)
		}
	}

	content := decodeTransfer(header.Get("Content-Transfer-Encoding"), body)
	disposition, dispositionParams, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	name := dispositionParams["filename"]
	if name == "" {
		name = params["name"]
	}
	name = oneLine(decodeWords(name))

	switch {
	case w.bounce != nil && mediaType == "message/delivery-status":
		w.bounce.readStatus(readText(content, ""))
	case w.bounce != nil && (mediaType == "message/rfc822" || mediaType == "text/rfc822-headers"):
		w.bounce.readOriginal(content)
	case disposition != "attachment" && name == "" && mediaType == "text/plain":
		if w.text == "" {
			w.text = readText(content, params["charset"])
		}
	case disposition != "attachment" && name == "" && mediaType == "text/html":
		if w.html == "" {
			w.html = readText(content, params["charset"])
		}
	default:
		if len(w.files) >= maxFiles {
			return
		}
		if name == "" {
			name = map[string]string{"message/rfc822": "письмо.eml", "text/calendar": "приглашение.ics"}[mediaType]
		}
		if name == "" {
			name = "файл"
		}
		file := File{Name: name, Inline: disposition == "inline" || header.Get("Content-Id") != ""}
		data, err := io.ReadAll(io.LimitReader(content, leads.MaxAttachmentBytes+1))
		if err != nil && len(data) == 0 {
			return
		}
		if len(data) > leads.MaxAttachmentBytes {
			file.TooBig = true
		} else {
			file.Content = data
		}
		w.files = append(w.files, file)
	}
}

func decodeTransfer(encoding string, body io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &base64Cleaner{source: body})
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	default:
		return body
	}
}

// base64Cleaner drops what is not base64 — spaces, stray characters at the end of lines — the
// way mail programs do, so that a slightly broken attachment is still an attachment.
type base64Cleaner struct{ source io.Reader }

func (c *base64Cleaner) Read(into []byte) (int, error) {
	for {
		n, err := c.source.Read(into)
		kept := 0
		for _, b := range into[:n] {
			if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '+' || b == '/' || b == '=' {
				into[kept] = b
				kept++
			}
		}
		if kept > 0 || err != nil {
			return kept, err
		}
	}
}

// readText reads a text part as UTF-8, whatever it was written in.
func readText(content io.Reader, charset string) string {
	if decoded, err := charsetReader(charset, content); err == nil {
		content = decoded
	}
	data, _ := io.ReadAll(io.LimitReader(content, maxTextBytes))
	return strings.ToValidUTF8(string(data), unreadable)
}

// charsetReader converts from the encodings mail is still written in: windows-1251, koi8-r,
// iso-8859-x, and the rest of the list browsers know.
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	charset = strings.ToLower(strings.TrimSpace(charset))
	switch charset {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return input, nil
	}
	encoding, err := htmlindex.Get(charset)
	if err != nil {
		return input, nil // an unknown name: read as it is, what is not UTF-8 becomes U+FFFD
	}
	return encoding.NewDecoder().Reader(input), nil
}

var (
	reStatus     = regexp.MustCompile(`(?im)^Status:\s*(\d\.\d{1,3}\.\d{1,3})`)
	reDiagnostic = regexp.MustCompile(`(?im)^Diagnostic-Code:\s*(?:[a-z0-9-]+;\s*)?(.+(?:\r?\n[ \t]+.+)*)`)
	reFinal      = regexp.MustCompile(`(?im)^Final-Recipient:\s*(?:[a-z0-9-]+;\s*)?(\S+)`)
)

func (b *Bounce) readStatus(report string) {
	if m := reStatus.FindStringSubmatch(report); m != nil {
		b.Status = m[1]
	}
	if m := reDiagnostic.FindStringSubmatch(report); m != nil {
		b.Diagnostic = oneLine(m[1])
		if len(b.Diagnostic) > 300 {
			b.Diagnostic = strings.ToValidUTF8(b.Diagnostic[:300], "") + "…"
		}
	}
	if m := reFinal.FindStringSubmatch(report); m != nil {
		b.Recipient = strings.ToLower(strings.Trim(m[1], "<>"))
	}
}

func (b *Bounce) readOriginal(content io.Reader) {
	original, err := textproto.NewReader(bufio.NewReader(io.LimitReader(content, 256<<10))).ReadMIMEHeader()
	if err != nil && len(original) == 0 {
		return
	}
	b.OriginalID = firstID(original.Get("Message-Id"))
	b.OriginalReplyTo = strings.ToLower(reAddress.FindString(original.Get("Reply-To")))
}

// --- HTML ---

var (
	reHTMLDrop   = regexp.MustCompile(`(?is)<!--.*?-->|<(head|style|script|title)\b.*?</(head|style|script|title)\s*>`)
	reHTMLQuote  = regexp.MustCompile(`(?is)<blockquote\b|<[a-z0-9]+\b[^>]*\b(?:class="[^"]*\b(?:gmail_quote|gmail_attr|moz-cite-prefix|yahoo_quoted)\b[^"]*"|id="(?:divRplyFwdMsg|appendonsend|OLK_SRC_BODY_SECTION|mail-editor-reference-message-container)")`)
	reHTMLBreak  = regexp.MustCompile(`(?i)<br\s*/?>|</(?:p|div|tr|li|h[1-6]|table|ul|ol|pre)\s*>`)
	reHTMLTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlankLines = regexp.MustCompile(`\n{3,}`)
)

// htmlToText is for letters that have no plain-text version: the words without the markup, and
// without the quoted conversation where the mail program marked it. What comes out is plain
// text and is treated — escaped — as such everywhere.
func htmlToText(source string) string {
	source = reHTMLDrop.ReplaceAllString(source, "")
	if at := reHTMLQuote.FindStringIndex(source); at != nil {
		source = source[:at[0]]
	}
	source = strings.NewReplacer("\r", "", "\n", " ").Replace(source)
	source = reHTMLBreak.ReplaceAllString(source, "\n")
	source = reHTMLTag.ReplaceAllString(source, "")
	return html.UnescapeString(source)
}

// tidy makes text fit for a conversation: Unix line ends, no trailing spaces, no runs of empty lines.
func tidy(text string) string {
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n", " ", " ").Replace(text)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	text = reBlankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(text)
}

// excerpt cuts text for a list, on a character boundary.
func excerpt(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// ErrNoSender: a letter that does not say who wrote it cannot be answered or placed.
var ErrNoSender = errors.New("the letter names no sender")
