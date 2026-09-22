package leads

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	netmail "net/mail"
	"strings"
	texttemplate "text/template"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/mail"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// Mailer turns outbox tasks about requests into email: the notification for the owner and the
// automatic confirmation for the client (brief B10.1, B10.2). It reads the request when the task
// is delivered, not when it was queued: a message that waited out a mail server outage still
// tells the truth.
type Mailer struct {
	Store    *Store
	Deliver  func(ctx context.Context, message mail.Message) error // mail.Sender.Send
	From     netmail.Address
	NotifyTo netmail.Address
	SiteHost string
	AdminURL string // https://example.com/_secret — links of the «open in the admin area» buttons
	Form     func() config.Form
	// TelegramURL returns the «continue in Telegram» link, or "" without a bot.
	TelegramURL func(lead *Lead) string
	Location    *time.Location
	// Inbox is the service mailbox client answers come back to («leads@domain»), Secret signs the
	// request's number into the address (ReplyAddress). Without an inbox letters carry no
	// Reply-To, and an answer goes to From — the owner's own mailbox.
	Inbox  string
	Secret []byte
}

// replyTo is where the client's mail program sends an answer to a letter about a request.
func (m *Mailer) replyTo(lead *Lead) *netmail.Address {
	address := ReplyAddress(m.Secret, m.Inbox, lead.ID)
	if address == "" || len(m.Secret) == 0 {
		return nil
	}
	return &netmail.Address{Name: m.From.Name, Address: address}
}

// Send implements outbox.Sender for the email channel.
func (m *Mailer) Send(ctx context.Context, task outbox.Task) error {
	if task.Kind == outbox.KindAlert {
		return m.sendAlert(ctx, task)
	}
	var payload TaskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.LeadID <= 0 {
		return outbox.Permanent(fmt.Errorf("unreadable task payload: %s", task.Payload))
	}
	lead, err := m.Store.Get(ctx, payload.LeadID)
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Permanent(errors.New("the request is gone (deleted on the client's demand?)"))
	}
	if err != nil {
		return err
	}

	// Letters name the files that came with the request; the files themselves stay on the
	// server and are downloaded from the admin area.
	files, err := m.Store.Attachments(ctx, lead.ID)
	if err != nil {
		return err
	}

	var message mail.Message
	switch task.Kind {
	case TaskNotify:
		message, err = m.notification(lead, files)
	case TaskAutoReply:
		if lead.ContactMethod != MethodEmail {
			return outbox.Permanent(errors.New("the client left no email address"))
		}
		message, err = m.autoReply(lead, files)
	case TaskReply:
		return m.sendReply(ctx, lead, payload.MessageID)
	case TaskClientMessage:
		message, err = m.clientWrote(ctx, lead, payload.MessageID)
		if errors.Is(err, sql.ErrNoRows) {
			return outbox.Permanent(errors.New("the client's message is gone"))
		}
	case TaskUndelivered:
		message, err = m.undelivered(lead, task.ID, payload.Note)
	default:
		return outbox.Permanent(fmt.Errorf("the mailer does not know the task %q", task.Kind))
	}
	if err != nil {
		return outbox.Permanent(err)
	}
	err = m.Deliver(ctx, message)
	var permanent mail.PermanentError
	if errors.As(err, &permanent) {
		return outbox.Permanent(err)
	}
	return err
}

// sendAlert tells the owner that the server needs a look (a certificate, a backup): the letter
// goes to the owner's mailbox on this very server, so whatever keeps mail from leaving does not
// keep this one from arriving.
func (m *Mailer) sendAlert(ctx context.Context, task outbox.Task) error {
	alert, err := outbox.ReadAlert(task)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("[%s] %s", m.SiteHost, alert.Subject)
	v := view{Host: m.SiteHost, Body: alert.Text, Author: alert.Subject, AdminURL: strings.TrimRight(m.AdminURL, "/") + "/status", Lang: "ru", Title: subject}
	text, html, err := render(alertText, alertHTML, v)
	if err != nil {
		return outbox.Permanent(err)
	}
	err = m.Deliver(ctx, mail.Message{
		From: m.From, To: m.NotifyTo,
		Subject:   subject,
		Text:      text,
		HTML:      html,
		MessageID: m.stableID(fmt.Sprintf("alert-%d", task.ID)),
		Headers:   map[string]string{"Auto-Submitted": "auto-generated", "X-Auto-Response-Suppress": "All"},
	})
	var permanent mail.PermanentError
	if errors.As(err, &permanent) {
		return outbox.Permanent(err)
	}
	return err
}

// view is what the templates see.
type view struct {
	Lead      *Lead
	Number    string
	Direction string
	// Contact is a link made from a validated value (an address, a user name, digits), which is
	// why it may bypass the template's URL filter — «tel:» is not on its list of schemes.
	Contact     htmltemplate.URL // mailto:, https://t.me/…, tel:
	Created     string
	Source      string
	Sections    string
	TimeOnSite  string
	AdminURL    string
	TelegramURL string
	ReplyHours  int
	Host        string
	Files       string // «spec.pdf (1,2 МБ), plan.png (310 КБ)»
	FileCount   int
	Body        string // an answer to the client
	Author      string
	T           map[string]string // the texts of the client's language

	// What letters to the client show — they go to an address anybody could have typed into
	// the form, so they repeat nothing the visitor wrote freely (see greetingName).
	Name      string // the visitor's name if it reads as a name, else ""
	Budget    string // the chosen options, in the letter's language
	Timeline  string
	Signature string // the sender's name
	Lang      string // of the letter: <html lang>
	Title     string // the subject, as the HTML's <title>
}

func (m *Mailer) view(lead *Lead, lang string) view {
	form := m.Form()
	direction := lead.Direction
	if option, ok := form.Direction(lead.Direction); ok {
		direction = option.Label.In(lang)
	}
	location := m.Location
	if location == nil {
		location = time.UTC
	}
	v := view{
		Lead: lead, Number: lead.Number(), Direction: direction, Host: m.SiteHost, ReplyHours: form.ReplyWithinHours(),
		Created:  lead.CreatedAt.In(location).Format("02.01.2006 15:04"),
		AdminURL: strings.TrimRight(m.AdminURL, "/") + fmt.Sprintf("/leads/%d", lead.ID),
		T:        texts[lang],
		Name:     greetingName(lead.Name),
		Budget:   localizedChoice(form.Budgets, lead.Budget, lang),
		Timeline: localizedChoice(form.Timelines, lead.Timeline, lang),
		Lang:     lang, Signature: m.From.Name,
	}
	switch lead.ContactMethod {
	case MethodEmail:
		v.Contact = htmltemplate.URL("mailto:" + lead.ContactValue) //nolint:gosec // validated by Parse
	case MethodTelegram:
		v.Contact = htmltemplate.URL("https://t.me/" + strings.TrimPrefix(lead.ContactValue, "@")) //nolint:gosec // validated by Parse
	case MethodPhone:
		v.Contact = htmltemplate.URL("tel:" + lead.ContactValue) //nolint:gosec // validated by Parse
	}
	if m.TelegramURL != nil {
		v.TelegramURL = m.TelegramURL(lead)
	}

	v.Source, v.Sections, v.TimeOnSite = VisitSummary(lead.Session)
	return v
}

// VisitSummary puts the visit a request came from into words: where from and on what, which
// sections were looked at, for how long. The letter to the owner and the card in Telegram say
// the same thing.
func VisitSummary(session analytics.SessionSummary) (source, sections, timeOnSite string) {
	var parts []string
	if name := sourceNames[session.Source]; name != "" {
		parts = append(parts, name)
	}
	if session.ReferrerHost != "" {
		parts = append(parts, session.ReferrerHost)
	}
	if session.UTMCampaign != "" {
		parts = append(parts, "utm_campaign="+session.UTMCampaign)
	} else if session.UTMSource != "" {
		parts = append(parts, "utm_source="+session.UTMSource)
	}
	if session.Country != "" {
		parts = append(parts, session.Country)
	}
	if device := strings.TrimSpace(deviceNames[session.Device] + " " + session.Browser + " · " + session.OS); device != "·" {
		parts = append(parts, device)
	}
	if session.TimeOnSiteMs >= 1000 {
		timeOnSite = (time.Duration(session.TimeOnSiteMs) * time.Millisecond).Round(time.Second).String()
	}
	return strings.Join(parts, " · "), session.SectionsPath(), timeOnSite
}

var (
	sourceNames = map[string]string{"direct": "прямой заход", "search": "поиск", "social": "соцсети", "other": "другой сайт", "ads": "реклама"}
	deviceNames = map[string]string{"desktop": "компьютер", "mobile": "телефон", "tablet": "планшет"}
)

// greetingName is the visitor's name as a letter to them may use it: a few words of letters,
// hyphens and apostrophes. Anything else — a link, digits, a sentence — gives "", and the letter
// greets without a name. The confirmation goes to whatever address was typed into the form; if
// it repeated free text, anybody could send their own words from this domain to anyone, and mail
// services would soon count the domain among the spammers.
func greetingName(name string) string {
	words := strings.Fields(name)
	if len(words) == 0 || len(words) > 4 || utf8.RuneCountInString(name) > 40 {
		return ""
	}
	for _, word := range words {
		for _, r := range word {
			if !unicode.IsLetter(r) && !unicode.IsMark(r) && r != '-' && r != '\'' && r != '’' {
				return ""
			}
		}
	}
	return strings.Join(words, " ")
}

// localizedChoice finds the option a request stores by its English label and returns it in the
// letter's language.
func localizedChoice(options []config.Localized, english, lang string) string {
	if english == "" {
		return ""
	}
	for _, option := range options {
		if option.In("en") == english {
			return option.In(lang)
		}
	}
	return english
}

// notification is the owner's copy: everything known about the request, in the owner's language.
func (m *Mailer) notification(lead *Lead, files []Attachment) (mail.Message, error) {
	v := m.view(lead, "ru")
	v.Files = fileList(files)
	v.Title = fmt.Sprintf("Заявка #%s · %s · %s", v.Number, v.Direction, lead.Name)
	text, html, err := render(notifyText, notifyHTML, v)
	if err != nil {
		return mail.Message{}, err
	}
	message := mail.Message{
		From: m.From, To: m.NotifyTo,
		Subject:   v.Title,
		Text:      text,
		HTML:      html,
		MessageID: m.messageID(lead.ID, "notify"),
		Headers:   map[string]string{"X-Krokosha-Lead": v.Number},
	}
	// «Reply» in the owner's mail program goes straight to the client (brief B10.2).
	if lead.ContactMethod == MethodEmail {
		message.ReplyTo = &netmail.Address{Name: lead.Name, Address: lead.ContactValue}
	}
	return message, nil
}

// autoReply confirms the request to the client, in the language of the page they wrote from. It
// names what was chosen in the form and repeats nothing typed freely — not the description, not
// file names, a name only if it reads as one (see greetingName): the letter goes to whatever
// address was entered, and must be of no use to somebody who enters a stranger's.
func (m *Mailer) autoReply(lead *Lead, files []Attachment) (mail.Message, error) {
	lang := lead.Lang
	if texts[lang] == nil {
		lang = "en"
	}
	v := m.view(lead, lang)
	v.FileCount = len(files)
	v.Title = strings.NewReplacer("{id}", "#"+v.Number, "{host}", m.SiteHost).Replace(v.T["subject"])
	text, html, err := render(autoReplyText, autoReplyHTML, v)
	if err != nil {
		return mail.Message{}, err
	}
	return mail.Message{
		From: m.From, To: netmail.Address{Name: v.Name, Address: lead.ContactValue},
		Subject:   v.Title,
		Text:      text,
		HTML:      html,
		MessageID: m.messageID(lead.ID, "autoreply"),
		ReplyTo:   m.replyTo(lead),
		// RFC 3834: tells other robots not to answer this one — no loops of automatic replies.
		Headers: map[string]string{"Auto-Submitted": "auto-replied", "X-Auto-Response-Suppress": "All", "X-Krokosha-Lead": v.Number},
	}, nil
}

// clientWrote tells the owner that the client wrote again — in Telegram or by mail. The letter
// joins the thread of the request's notification in the owner's mailbox.
func (m *Mailer) clientWrote(ctx context.Context, lead *Lead, messageID int64) (mail.Message, error) {
	body, channel, err := m.Store.IncomingMessage(ctx, lead.ID, messageID)
	if err != nil {
		return mail.Message{}, err
	}
	files, err := m.Store.MessageFiles(ctx, lead.ID, messageID)
	if err != nil {
		return mail.Message{}, err
	}
	v := m.view(lead, "ru")
	v.Body, v.Author = body, map[string]string{ChannelTelegram: "в Telegram", ChannelEmail: "письмом"}[channel]
	v.Files = fileList(files)
	v.Title = fmt.Sprintf("Re: Заявка #%s · %s · %s", v.Number, v.Direction, lead.Name)
	text, html, err := render(clientWroteText, clientWroteHTML, v)
	if err != nil {
		return mail.Message{}, err
	}
	notification := m.messageID(lead.ID, "notify")
	message := mail.Message{
		From: m.From, To: m.NotifyTo,
		Subject:   v.Title,
		Text:      text,
		HTML:      html,
		MessageID: m.messageID(lead.ID, fmt.Sprintf("client-%d", messageID)), InReplyTo: notification, References: []string{notification},
		Headers: map[string]string{"X-Krokosha-Lead": v.Number},
	}
	if lead.ContactMethod == MethodEmail {
		message.ReplyTo = &netmail.Address{Name: lead.Name, Address: lead.ContactValue}
	}
	return message, nil
}

// undelivered tells the owner that a letter to the client came back (brief B10.5). It goes to the
// owner's own mailbox on the same server: whatever stops mail from leaving does not stop this.
func (m *Mailer) undelivered(lead *Lead, taskID int64, reason string) (mail.Message, error) {
	v := m.view(lead, "ru")
	v.Body = reason
	v.Title = fmt.Sprintf("Не доставлено: заявка #%s · %s", v.Number, lead.Name)
	text, html, err := render(undeliveredText, undeliveredHTML, v)
	if err != nil {
		return mail.Message{}, err
	}
	notification := m.messageID(lead.ID, "notify")
	return mail.Message{
		From: m.From, To: m.NotifyTo,
		Subject:   v.Title,
		Text:      text,
		HTML:      html,
		MessageID: m.messageID(lead.ID, fmt.Sprintf("undelivered-%d", taskID)), InReplyTo: notification, References: []string{notification},
		Headers: map[string]string{"X-Krokosha-Lead": v.Number},
	}, nil
}

// messageID is the same for every attempt to deliver the same letter: a mail server that got it
// twice — the first attempt timed out after all — can tell. It also lets an answer point at the
// confirmation the client already has, so that the conversation stays one thread.
func (m *Mailer) messageID(leadID int64, part string) string {
	return m.stableID(fmt.Sprintf("lead-%d.%s", leadID, part))
}

// stableID adds to a letter's ID a mark signed with the site's secret, so that the ID is still the
// same for every attempt, but never the same on another installation: numbers of requests start
// from one again after a reinstall, and a mailbox that already holds a letter with an ID drops a
// new letter with that ID without a word.
func (m *Mailer) stableID(local string) string {
	if len(m.Secret) > 0 {
		mac := hmac.New(sha256.New, m.Secret)
		mac.Write([]byte("message-id:" + local))
		local += "." + idMark.EncodeToString(mac.Sum(nil)[:5])
	}
	return local + "@" + m.SiteHost
}

var idMark = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// sendReply delivers an answer written in the admin area or in the bot (brief B10.5) and
// records what became of it.
func (m *Mailer) sendReply(ctx context.Context, lead *Lead, messageID int64) error {
	if lead.ContactMethod != MethodEmail {
		return outbox.Permanent(errors.New("the client left no email address"))
	}
	body, author, err := m.Store.Message(ctx, lead.ID, messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Permanent(errors.New("the answer is gone"))
	}
	if err != nil {
		return err
	}
	lang := lead.Lang
	if texts[lang] == nil {
		lang = "en"
	}
	v := m.view(lead, lang)
	v.Body, v.Author = body, m.From.Name
	if v.Author == "" {
		v.Author = author
	}
	v.Title = "Re: " + strings.NewReplacer("{id}", "#"+v.Number, "{host}", m.SiteHost).Replace(v.T["subject"])
	text, html, err := render(replyText, replyHTML, v)
	if err != nil {
		return outbox.Permanent(err)
	}
	// The thread so far: the confirmation, then every answer already sent.
	references := []string{m.messageID(lead.ID, "autoreply")}
	if sent, err := m.Store.ThreadIDs(ctx, lead.ID); err == nil {
		references = append(references, sent...)
	}
	id := m.messageID(lead.ID, fmt.Sprintf("reply-%d", messageID))
	message := mail.Message{
		From: m.From, To: netmail.Address{Name: v.Name, Address: lead.ContactValue},
		Subject:   v.Title,
		Text:      text,
		HTML:      html,
		MessageID: id, InReplyTo: references[len(references)-1], References: references,
		ReplyTo: m.replyTo(lead),
		Headers: map[string]string{"X-Krokosha-Lead": v.Number},
	}
	err = m.Deliver(ctx, message)
	var permanent mail.PermanentError
	switch {
	case err == nil:
		return m.Store.MarkDelivery(ctx, messageID, "sent", id)
	case errors.As(err, &permanent):
		_ = m.Store.MarkDelivery(ctx, messageID, "failed", "")
		return outbox.Permanent(err)
	default:
		return err
	}
}

// fileList names the files of a request in one line.
func fileList(files []Attachment) string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, fmt.Sprintf("%s (%s)", file.Filename, FormatSize(file.Size)))
	}
	return strings.Join(names, ", ")
}

// FormatSize writes a size the way people say it: 310 КБ, 1,2 МБ.
func FormatSize(size int64) string {
	switch {
	case size >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.1f МБ", float64(size)/(1<<20)), ".", ",", 1)
	case size >= 1<<10:
		return fmt.Sprintf("%d КБ", size>>10)
	default:
		return fmt.Sprintf("%d Б", size)
	}
}

func render(text *texttemplate.Template, html *htmltemplate.Template, v view) (string, string, error) {
	var plain, rich bytes.Buffer
	if err := text.Execute(&plain, v); err != nil {
		return "", "", err
	}
	if err := html.Execute(&rich, v); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(plain.String()) + "\n", rich.String(), nil
}

// texts of the automatic reply. The client reads them, so they follow the language of the page.
var texts = map[string]map[string]string{
	"en": {
		"subject": "Request {id} received — {host}", "hello": "Hello", "thanks": "Thank you for your request. Its number is",
		"reply": "I'll reply within", "hours": "h.", "summary": "Your request", "contact": "Contact",
		"area": "Area", "budget": "Budget", "timeline": "Timeline", "files": "Files", "telegram": "Continue in Telegram",
		"saved": "Your message is saved with the request, there is no need to send it again.",
		"auto":  "This is an automatic confirmation. If you did not send this request, simply ignore this email.",
	},
	"uk": {
		"subject": "Заявку {id} прийнято — {host}", "hello": "Вітаю", "thanks": "Дякую за заявку. Її номер —",
		"reply": "Відповім протягом", "hours": "год.", "summary": "Ваша заявка", "contact": "Контакт",
		"area": "Напрям", "budget": "Бюджет", "timeline": "Терміни", "files": "Файли", "telegram": "Продовжити в Telegram",
		"saved": "Ваше повідомлення збережено разом із заявкою, надсилати його ще раз не потрібно.",
		"auto":  "Це автоматичне підтвердження. Якщо ви не надсилали заявку, просто проігноруйте цей лист.",
	},
	"ru": {
		"subject": "Заявка {id} принята — {host}", "hello": "Здравствуйте", "thanks": "Спасибо за заявку. Её номер —",
		"reply": "Отвечу в течение", "hours": "ч.", "summary": "Ваша заявка", "contact": "Контакт",
		"area": "Направление", "budget": "Бюджет", "timeline": "Сроки", "files": "Файлы", "telegram": "Продолжить в Telegram",
		"saved": "Ваше сообщение сохранено вместе с заявкой, отправлять его ещё раз не нужно.",
		"auto":  "Это автоматическое подтверждение. Если вы не отправляли заявку, просто проигнорируйте это письмо.",
	},
}

var notifyText = texttemplate.Must(texttemplate.New("notify.txt").Parse(`Заявка #{{.Number}} · {{.Direction}}
{{.Created}}

Имя: {{.Lead.Name}}
Контакт: {{.Lead.ContactValue}}
{{- if .Lead.Budget}}
Бюджет: {{.Lead.Budget}}{{end}}
{{- if .Lead.Timeline}}
Сроки: {{.Lead.Timeline}}{{end}}

{{.Lead.Description}}

{{if .Files}}Вложения (в админке): {{.Files}}
{{end}}{{if .Source}}Откуда: {{.Source}}
{{end}}{{if .Sections}}Смотрел: {{.Sections}}{{if .TimeOnSite}} ({{.TimeOnSite}}){{end}}
{{end}}{{if .Lead.Verdict.Score}}Подозрение на спам: {{.Lead.Verdict.Score}} из 100
{{end}}
Открыть в админке: {{.AdminURL}}
`))

var autoReplyText = texttemplate.Must(texttemplate.New("autoreply.txt").Parse(`{{.T.hello}}{{if .Name}}, {{.Name}}{{end}}!

{{.T.thanks}} #{{.Number}}.{{if .ReplyHours}} {{.T.reply}} {{.ReplyHours}} {{.T.hours}}{{end}}

{{.T.summary}}:

{{.T.area}}: {{.Direction}}
{{- if .Budget}}
{{.T.budget}}: {{.Budget}}{{end}}
{{- if .Timeline}}
{{.T.timeline}}: {{.Timeline}}{{end}}
{{.T.contact}}: {{.Lead.ContactValue}}
{{- if .FileCount}}
{{.T.files}}: {{.FileCount}}{{end}}

{{.T.saved}}
{{if .TelegramURL}}
{{.T.telegram}}: {{.TelegramURL}}
{{end}}
--
{{if .Signature}}{{.Signature}}
{{end}}https://{{.Host}}
{{.T.auto}}
`))

// The HTML versions: tables and inline styles, the only layout every mail program understands.
// html/template escapes whatever the visitor typed.
const mailFrame = `<!doctype html>
<html lang="{{.Lang}}"><head><meta http-equiv="Content-Type" content="text/html; charset=utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}}</title></head>
<body style="margin:0;padding:24px 12px;background:#f3f3f6;font-family:-apple-system,'Segoe UI',Roboto,Arial,sans-serif;color:#1a1b20;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px;margin:0 auto;background:#ffffff;border-radius:10px;border:1px solid #e3e3ea;">
<tr><td style="padding:20px 24px;border-bottom:3px solid #8b6fe0;font-size:12px;letter-spacing:4px;color:#6b6f7e;">KROKOSHA</td></tr>
<tr><td style="padding:24px;font-size:15px;line-height:1.55;">{{template "body" .}}</td></tr>
<tr><td style="padding:16px 24px;border-top:1px solid #e3e3ea;font-size:12px;line-height:1.5;color:#6b6f7e;">{{template "foot" .}}</td></tr>
</table></body></html>`

var notifyHTML = htmltemplate.Must(htmltemplate.New("notify.html").Parse(mailFrame + `
{{define "body"}}
<p style="margin:0 0 4px;font-size:12px;color:#6b6f7e;">{{.Created}}</p>
<h1 style="margin:0 0 16px;font-size:20px;">Заявка #{{.Number}} · {{.Direction}}</h1>
<table role="presentation" cellpadding="0" cellspacing="0" style="font-size:15px;line-height:1.55;">
<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">Имя</td><td>{{.Lead.Name}}</td></tr>
<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">Контакт</td><td><a href="{{.Contact}}" style="color:#5b3fc4;">{{.Lead.ContactValue}}</a></td></tr>
{{if .Lead.Budget}}<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">Бюджет</td><td>{{.Lead.Budget}}</td></tr>{{end}}
{{if .Lead.Timeline}}<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">Сроки</td><td>{{.Lead.Timeline}}</td></tr>{{end}}
</table>
<p style="margin:16px 0;padding:12px 14px;background:#f6f5fb;border-left:3px solid #8b6fe0;border-radius:4px;white-space:pre-wrap;">{{.Lead.Description}}</p>
{{if .Files}}<p style="margin:0 0 12px;font-size:14px;">Вложения <span style="color:#6b6f7e;">(скачать — в админке)</span>: {{.Files}}</p>{{end}}
{{if .Source}}<p style="margin:0 0 4px;font-size:13px;color:#6b6f7e;">Откуда: {{.Source}}</p>{{end}}
{{if .Sections}}<p style="margin:0 0 4px;font-size:13px;color:#6b6f7e;">Смотрел: {{.Sections}}{{if .TimeOnSite}} ({{.TimeOnSite}}){{end}}</p>{{end}}
{{if .Lead.Verdict.Score}}<p style="margin:0 0 4px;font-size:13px;color:#b26a00;">Подозрение на спам: {{.Lead.Verdict.Score}} из 100</p>{{end}}
<p style="margin:20px 0 0;"><a href="{{.AdminURL}}" style="display:inline-block;padding:10px 18px;background:#8b6fe0;color:#ffffff;text-decoration:none;border-radius:999px;font-weight:600;">Открыть в админке</a></p>
{{end}}
{{define "foot"}}Ответ на это письмо уйдёт клиенту напрямую, если он оставил почту. История переписки ведётся в админке.{{end}}`))

var autoReplyHTML = htmltemplate.Must(htmltemplate.New("autoreply.html").Parse(mailFrame + `
{{define "body"}}
<p style="margin:0 0 12px;">{{.T.hello}}{{if .Name}}, {{.Name}}{{end}}!</p>
<p style="margin:0 0 16px;">{{.T.thanks}} <b>#{{.Number}}</b>.{{if .ReplyHours}} {{.T.reply}} {{.ReplyHours}} {{.T.hours}}{{end}}</p>
<p style="margin:0 0 6px;font-size:13px;color:#6b6f7e;">{{.T.summary}}</p>
<table role="presentation" cellpadding="0" cellspacing="0" style="font-size:14px;line-height:1.55;">
<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">{{.T.area}}</td><td>{{.Direction}}</td></tr>
{{if .Budget}}<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">{{.T.budget}}</td><td>{{.Budget}}</td></tr>{{end}}
{{if .Timeline}}<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">{{.T.timeline}}</td><td>{{.Timeline}}</td></tr>{{end}}
<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">{{.T.contact}}</td><td>{{.Lead.ContactValue}}</td></tr>
{{if .FileCount}}<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">{{.T.files}}</td><td>{{.FileCount}}</td></tr>{{end}}
</table>
<p style="margin:16px 0 0;">{{.T.saved}}</p>
{{if .TelegramURL}}<p style="margin:20px 0 0;"><a href="{{.TelegramURL}}" style="display:inline-block;padding:10px 18px;background:#8b6fe0;color:#ffffff;text-decoration:none;border-radius:999px;font-weight:600;">{{.T.telegram}}</a></p>{{end}}
{{end}}
{{define "foot"}}{{if .Signature}}{{.Signature}} · {{end}}<a href="https://{{.Host}}" style="color:#6b6f7e;">{{.Host}}</a><br>{{.T.auto}}{{end}}`))

var clientWroteText = texttemplate.Must(texttemplate.New("client.txt").Parse(`{{.Lead.Name}} пишет по заявке #{{.Number}} ({{.Author}}):

{{.Body}}
{{if .Files}}
Файлы (в админке): {{.Files}}
{{end}}
Открыть в админке: {{.AdminURL}}
`))

var clientWroteHTML = htmltemplate.Must(htmltemplate.New("client.html").Parse(mailFrame + `
{{define "body"}}
<p style="margin:0 0 12px;"><b>{{.Lead.Name}}</b> пишет по заявке <b>#{{.Number}}</b> <span style="color:#6b6f7e;">({{.Author}})</span>:</p>
<p style="margin:0;padding:12px 14px;background:#f6f5fb;border-left:3px solid #8b6fe0;border-radius:4px;white-space:pre-wrap;">{{.Body}}</p>
{{if .Files}}<p style="margin:12px 0 0;font-size:14px;">Файлы <span style="color:#6b6f7e;">(скачать — в админке)</span>: {{.Files}}</p>{{end}}
<p style="margin:20px 0 0;"><a href="{{.AdminURL}}" style="display:inline-block;padding:10px 18px;background:#8b6fe0;color:#ffffff;text-decoration:none;border-radius:999px;font-weight:600;">Открыть в админке</a></p>
{{end}}
{{define "foot"}}Заявка вернулась в работу, если ждала ответа клиента. История переписки — в админке.{{end}}`))

var undeliveredText = texttemplate.Must(texttemplate.New("undelivered.txt").Parse(`Письмо клиенту по заявке #{{.Number}} не доставлено.

{{.Body}}

{{.Lead.Name}} ({{.Lead.ContactValue}}) ответа не получил(а). Проверьте адрес или свяжитесь другим способом.

Открыть в админке: {{.AdminURL}}
`))

var undeliveredHTML = htmltemplate.Must(htmltemplate.New("undelivered.html").Parse(mailFrame + `
{{define "body"}}
<p style="margin:0 0 12px;">Письмо клиенту по заявке <b>#{{.Number}}</b> <b style="color:#b3261e;">не доставлено</b>.</p>
<p style="margin:0;padding:12px 14px;background:#fbf3f2;border-left:3px solid #b3261e;border-radius:4px;white-space:pre-wrap;">{{.Body}}</p>
<p style="margin:12px 0 0;">{{.Lead.Name}} ({{.Lead.ContactValue}}) ответа не получил(а). Проверьте адрес или свяжитесь другим способом.</p>
<p style="margin:20px 0 0;"><a href="{{.AdminURL}}" style="display:inline-block;padding:10px 18px;background:#8b6fe0;color:#ffffff;text-decoration:none;border-radius:999px;font-weight:600;">Открыть в админке</a></p>
{{end}}
{{define "foot"}}Сообщил почтовый сервер. История переписки — в админке.{{end}}`))

var alertText = texttemplate.Must(texttemplate.New("alert.txt").Parse(`{{.Author}}

{{.Body}}

Статус системы: {{.AdminURL}}
`))

var alertHTML = htmltemplate.Must(htmltemplate.New("alert.html").Parse(mailFrame + `
{{define "body"}}
<h1 style="margin:0 0 12px;font-size:18px;color:#b3261e;">{{.Author}}</h1>
<p style="margin:0;padding:12px 14px;background:#fbf3f2;border-left:3px solid #b3261e;border-radius:4px;white-space:pre-wrap;">{{.Body}}</p>
<p style="margin:20px 0 0;"><a href="{{.AdminURL}}" style="display:inline-block;padding:10px 18px;background:#8b6fe0;color:#ffffff;text-decoration:none;border-radius:999px;font-weight:600;">Статус системы</a></p>
{{end}}
{{define "foot"}}Сообщение сервера {{.Host}}. Повторяется не чаще раза в сутки, пока причина не устранена.{{end}}`))

// The plain text of letters to clients carries the same links as their HTML: a letter whose two
// versions link to different things looks put together by a spammer's tool.
var replyText = texttemplate.Must(texttemplate.New("reply.txt").Parse(`{{.Body}}

--
{{.Author}}
https://{{.Host}} · #{{.Number}}
`))

var replyHTML = htmltemplate.Must(htmltemplate.New("reply.html").Parse(mailFrame + `
{{define "body"}}<p style="margin:0;white-space:pre-wrap;">{{.Body}}</p>
<p style="margin:20px 0 0;color:#6b6f7e;">— {{.Author}}</p>{{end}}
{{define "foot"}}<a href="https://{{.Host}}" style="color:#6b6f7e;">{{.Host}}</a> · #{{.Number}}{{end}}`))
