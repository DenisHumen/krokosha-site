package leads

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	netmail "net/mail"
	"strings"
	texttemplate "text/template"
	"time"

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
}

// Send implements outbox.Sender for the email channel.
func (m *Mailer) Send(ctx context.Context, task outbox.Task) error {
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

	var message mail.Message
	switch task.Kind {
	case TaskNotify:
		message, err = m.notification(lead)
	case TaskAutoReply:
		if lead.ContactMethod != MethodEmail {
			return outbox.Permanent(errors.New("the client left no email address"))
		}
		message, err = m.autoReply(lead)
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
	T           map[string]string // the texts of the client's language
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

	session := lead.Session
	var source []string
	if name := sourceNames[session.Source]; name != "" {
		source = append(source, name)
	}
	if session.ReferrerHost != "" {
		source = append(source, session.ReferrerHost)
	}
	if session.UTMCampaign != "" {
		source = append(source, "utm_campaign="+session.UTMCampaign)
	} else if session.UTMSource != "" {
		source = append(source, "utm_source="+session.UTMSource)
	}
	if session.Country != "" {
		source = append(source, session.Country)
	}
	if device := strings.TrimSpace(deviceNames[session.Device] + " " + session.Browser + " · " + session.OS); device != "·" {
		source = append(source, device)
	}
	v.Source = strings.Join(source, " · ")
	v.Sections = session.SectionsPath()
	if session.TimeOnSiteMs >= 1000 {
		v.TimeOnSite = (time.Duration(session.TimeOnSiteMs) * time.Millisecond).Round(time.Second).String()
	}
	return v
}

var (
	sourceNames = map[string]string{"direct": "прямой заход", "search": "поиск", "social": "соцсети", "other": "другой сайт", "ads": "реклама"}
	deviceNames = map[string]string{"desktop": "компьютер", "mobile": "телефон", "tablet": "планшет"}
)

// notification is the owner's copy: everything known about the request, in the owner's language.
func (m *Mailer) notification(lead *Lead) (mail.Message, error) {
	v := m.view(lead, "ru")
	text, html, err := render(notifyText, notifyHTML, v)
	if err != nil {
		return mail.Message{}, err
	}
	message := mail.Message{
		From: m.From, To: m.NotifyTo,
		Subject:   fmt.Sprintf("Заявка #%s · %s · %s", v.Number, v.Direction, lead.Name),
		Text:      text,
		HTML:      html,
		MessageID: mail.NewMessageID(fmt.Sprintf("lead-%d.notify", lead.ID), m.SiteHost),
		Headers:   map[string]string{"X-Krokosha-Lead": v.Number},
	}
	// «Reply» in the owner's mail program goes straight to the client (brief B10.2).
	if lead.ContactMethod == MethodEmail {
		message.ReplyTo = &netmail.Address{Name: lead.Name, Address: lead.ContactValue}
	}
	return message, nil
}

// autoReply confirms the request to the client, in the language of the page they wrote from.
func (m *Mailer) autoReply(lead *Lead) (mail.Message, error) {
	lang := lead.Lang
	if texts[lang] == nil {
		lang = "en"
	}
	v := m.view(lead, lang)
	text, html, err := render(autoReplyText, autoReplyHTML, v)
	if err != nil {
		return mail.Message{}, err
	}
	return mail.Message{
		From: m.From, To: netmail.Address{Name: lead.Name, Address: lead.ContactValue},
		Subject:   strings.NewReplacer("{id}", "#"+v.Number, "{host}", m.SiteHost).Replace(v.T["subject"]),
		Text:      text,
		HTML:      html,
		MessageID: mail.NewMessageID(fmt.Sprintf("lead-%d.autoreply", lead.ID), m.SiteHost),
		// RFC 3834: tells other robots not to answer this one — no loops of automatic replies.
		Headers: map[string]string{"Auto-Submitted": "auto-replied", "X-Auto-Response-Suppress": "All", "X-Krokosha-Lead": v.Number},
	}, nil
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
		"reply": "I'll reply within", "hours": "h.", "copy": "A copy of what you sent", "name": "Name", "contact": "Contact",
		"area": "Area", "budget": "Budget", "timeline": "Timeline", "task": "Task", "telegram": "Continue in Telegram",
		"auto": "This is an automatic confirmation. If you did not send this request, simply ignore this email.",
	},
	"uk": {
		"subject": "Заявку {id} прийнято — {host}", "hello": "Вітаю", "thanks": "Дякую за заявку. Її номер —",
		"reply": "Відповім протягом", "hours": "год.", "copy": "Копія того, що ви надіслали", "name": "Ім'я", "contact": "Контакт",
		"area": "Напрям", "budget": "Бюджет", "timeline": "Терміни", "task": "Задача", "telegram": "Продовжити в Telegram",
		"auto": "Це автоматичне підтвердження. Якщо ви не надсилали заявку, просто проігноруйте цей лист.",
	},
	"ru": {
		"subject": "Заявка {id} принята — {host}", "hello": "Здравствуйте", "thanks": "Спасибо за заявку. Её номер —",
		"reply": "Отвечу в течение", "hours": "ч.", "copy": "Копия того, что вы отправили", "name": "Имя", "contact": "Контакт",
		"area": "Направление", "budget": "Бюджет", "timeline": "Сроки", "task": "Задача", "telegram": "Продолжить в Telegram",
		"auto": "Это автоматическое подтверждение. Если вы не отправляли заявку, просто проигнорируйте это письмо.",
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

{{if .Source}}Откуда: {{.Source}}
{{end}}{{if .Sections}}Смотрел: {{.Sections}}{{if .TimeOnSite}} ({{.TimeOnSite}}){{end}}
{{end}}{{if .Lead.Verdict.Score}}Подозрение на спам: {{.Lead.Verdict.Score}} из 100
{{end}}
Открыть в админке: {{.AdminURL}}
`))

var autoReplyText = texttemplate.Must(texttemplate.New("autoreply.txt").Parse(`{{.T.hello}}, {{.Lead.Name}}!

{{.T.thanks}} #{{.Number}}.{{if .ReplyHours}} {{.T.reply}} {{.ReplyHours}} {{.T.hours}}{{end}}

{{.T.copy}}:

{{.T.name}}: {{.Lead.Name}}
{{.T.contact}}: {{.Lead.ContactValue}}
{{.T.area}}: {{.Direction}}
{{- if .Lead.Budget}}
{{.T.budget}}: {{.Lead.Budget}}{{end}}
{{- if .Lead.Timeline}}
{{.T.timeline}}: {{.Lead.Timeline}}{{end}}

{{.Lead.Description}}
{{if .TelegramURL}}
{{.T.telegram}}: {{.TelegramURL}}
{{end}}
--
{{.Host}}
{{.T.auto}}
`))

// The HTML versions: tables and inline styles, the only layout every mail program understands.
// html/template escapes whatever the visitor typed.
const mailFrame = `<!doctype html>
<html><body style="margin:0;padding:24px 12px;background:#f3f3f6;font-family:-apple-system,'Segoe UI',Roboto,Arial,sans-serif;color:#1a1b20;">
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
{{if .Source}}<p style="margin:0 0 4px;font-size:13px;color:#6b6f7e;">Откуда: {{.Source}}</p>{{end}}
{{if .Sections}}<p style="margin:0 0 4px;font-size:13px;color:#6b6f7e;">Смотрел: {{.Sections}}{{if .TimeOnSite}} ({{.TimeOnSite}}){{end}}</p>{{end}}
{{if .Lead.Verdict.Score}}<p style="margin:0 0 4px;font-size:13px;color:#b26a00;">Подозрение на спам: {{.Lead.Verdict.Score}} из 100</p>{{end}}
<p style="margin:20px 0 0;"><a href="{{.AdminURL}}" style="display:inline-block;padding:10px 18px;background:#8b6fe0;color:#ffffff;text-decoration:none;border-radius:999px;font-weight:600;">Открыть в админке</a></p>
{{end}}
{{define "foot"}}Ответ на это письмо уйдёт клиенту напрямую, если он оставил почту. История переписки ведётся в админке.{{end}}`))

var autoReplyHTML = htmltemplate.Must(htmltemplate.New("autoreply.html").Parse(mailFrame + `
{{define "body"}}
<p style="margin:0 0 12px;">{{.T.hello}}, {{.Lead.Name}}!</p>
<p style="margin:0 0 16px;">{{.T.thanks}} <b>#{{.Number}}</b>.{{if .ReplyHours}} {{.T.reply}} {{.ReplyHours}} {{.T.hours}}{{end}}</p>
<p style="margin:0 0 6px;font-size:13px;color:#6b6f7e;">{{.T.copy}}</p>
<table role="presentation" cellpadding="0" cellspacing="0" style="font-size:14px;line-height:1.55;">
<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">{{.T.name}}</td><td>{{.Lead.Name}}</td></tr>
<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">{{.T.contact}}</td><td>{{.Lead.ContactValue}}</td></tr>
<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">{{.T.area}}</td><td>{{.Direction}}</td></tr>
{{if .Lead.Budget}}<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">{{.T.budget}}</td><td>{{.Lead.Budget}}</td></tr>{{end}}
{{if .Lead.Timeline}}<tr><td style="padding:2px 16px 2px 0;color:#6b6f7e;">{{.T.timeline}}</td><td>{{.Lead.Timeline}}</td></tr>{{end}}
</table>
<p style="margin:12px 0 0;padding:12px 14px;background:#f6f5fb;border-left:3px solid #8b6fe0;border-radius:4px;white-space:pre-wrap;">{{.Lead.Description}}</p>
{{if .TelegramURL}}<p style="margin:20px 0 0;"><a href="{{.TelegramURL}}" style="display:inline-block;padding:10px 18px;background:#8b6fe0;color:#ffffff;text-decoration:none;border-radius:999px;font-weight:600;">{{.T.telegram}}</a></p>{{end}}
{{end}}
{{define "foot"}}<a href="https://{{.Host}}" style="color:#6b6f7e;">{{.Host}}</a><br>{{.T.auto}}{{end}}`))
