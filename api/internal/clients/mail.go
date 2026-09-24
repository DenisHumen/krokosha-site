package clients

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

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/mail"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// Mailer writes the letters with codes. The letter is made when it is sent: a code that waited out
// a mail server outage past its lifetime is not sent at all.
type Mailer struct {
	Service  *Service
	Deliver  func(ctx context.Context, message mail.Message) error
	From     netmail.Address
	SiteHost string
}

// Send implements outbox.Sender for TaskLogin.
func (m *Mailer) Send(ctx context.Context, task outbox.Task) error {
	var payload loginPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.LoginID <= 0 {
		return outbox.Permanent(fmt.Errorf("unreadable task payload: %s", task.Payload))
	}
	s := m.Service
	l, err := scanLogin(s.opts.DB.QueryRowContext(ctx, `SELECT `+loginColumns+` FROM client_logins WHERE id = ?`, payload.LoginID))
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Permanent(errors.New("the login is gone"))
	}
	if err != nil {
		return err
	}
	if l.usedAt.Valid || !s.now().Before(l.expiresAt) || l.email == "" {
		return outbox.Permanent(errors.New("the code is used or expired: nothing to send"))
	}
	lang := l.lang
	if loginTexts[lang] == nil {
		lang = "en"
	}
	v := loginView{
		T: loginTexts[lang], Lang: lang, Host: m.SiteHost, Link: s.LinkURL(lang, s.linkToken(l)),
		Minutes: int(l.expiresAt.Sub(l.createdAt).Minutes()), Adding: l.clientID > 0, LinkOnly: payload.LinkOnly,
	}
	if !payload.LinkOnly {
		v.Code = s.code(l)
	}
	subject := v.T["subject"]
	switch {
	case v.LinkOnly:
		subject = v.T["subject_link"]
	case v.Adding:
		subject = v.T["subject_add"]
	}
	v.Title = strings.NewReplacer("{code}", v.Code, "{host}", m.SiteHost).Replace(subject)

	var text, html bytes.Buffer
	if err := loginText.Execute(&text, v); err != nil {
		return outbox.Permanent(err)
	}
	if err := loginHTML.Execute(&html, v); err != nil {
		return outbox.Permanent(err)
	}
	err = m.Deliver(ctx, mail.Message{
		From: m.From, To: netmail.Address{Address: l.email},
		Subject:   v.Title,
		Text:      strings.TrimSpace(text.String()) + "\n",
		HTML:      html.String(),
		MessageID: m.messageID(l.id),
		// RFC 3834: robots must not answer this one.
		Headers: map[string]string{"Auto-Submitted": "auto-generated", "X-Auto-Response-Suppress": "All"},
	})
	var permanent mail.PermanentError
	if errors.As(err, &permanent) {
		return outbox.Permanent(err)
	}
	return err
}

// messageID is the same for every attempt, and signed so that it is never the same on another
// installation (see leads.Mailer.stableID).
func (m *Mailer) messageID(loginID int64) string {
	local := fmt.Sprintf("login-%d", loginID)
	h := hmac.New(sha256.New, m.Service.opts.Secret)
	h.Write([]byte("message-id:" + local))
	return local + "." + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h.Sum(nil)[:5])) + "@" + m.SiteHost
}

type loginView struct {
	T        map[string]string
	Lang     string
	Title    string
	Host     string
	Code     string
	Link     string
	Minutes  int
	Adding   bool // the address is being added to an account
	LinkOnly bool // the owner sent a link from the admin area: no code, no browser waits
}

// The letter repeats nothing anybody typed: it goes to whatever address was entered on the site.
var loginTexts = map[string]map[string]string{
	"en": {
		"subject": "Sign-in code {code} — {host}", "subject_add": "Confirmation code {code} — {host}", "subject_link": "Sign in to your account — {host}",
		"code": "Your code to sign in to your personal account:", "code_add": "Your code to confirm this address for your personal account:",
		"link_only": "Here is a link to sign in to your personal account:",
		"valid":     "It is valid for {minutes} minutes and works in the browser where you asked for it.", "or": "Or simply open the link:",
		"button": "Sign in", "valid_link": "The link is valid for a day.",
		"ignore": "If you did not ask for this, just ignore the letter: nobody can sign in without the code.",
	},
	"uk": {
		"subject": "Код входу {code} — {host}", "subject_add": "Код підтвердження {code} — {host}", "subject_link": "Вхід до особистого кабінету — {host}",
		"code": "Ваш код для входу в особистий кабінет:", "code_add": "Ваш код, щоб підтвердити цю адресу для особистого кабінету:",
		"link_only": "Ось посилання для входу в особистий кабінет:",
		"valid":     "Він діє {minutes} хвилин і працює в тому браузері, де ви його запросили.", "or": "Або просто відкрийте посилання:",
		"button": "Увійти", "valid_link": "Посилання діє добу.",
		"ignore": "Якщо ви цього не запитували, просто проігноруйте лист: без коду ніхто не увійде.",
	},
	"ru": {
		"subject": "Код входа {code} — {host}", "subject_add": "Код подтверждения {code} — {host}", "subject_link": "Вход в личный кабинет — {host}",
		"code": "Ваш код для входа в личный кабинет:", "code_add": "Ваш код, чтобы подтвердить этот адрес для личного кабинета:",
		"link_only": "Вот ссылка для входа в личный кабинет:",
		"valid":     "Он действует {minutes} минут и работает в том браузере, где вы его запросили.", "or": "Или просто откройте ссылку:",
		"button": "Войти", "valid_link": "Ссылка действует сутки.",
		"ignore": "Если вы этого не запрашивали, просто проигнорируйте письмо: без кода никто не войдёт.",
	},
}

var loginFuncs = texttemplate.FuncMap{
	"minutes": func(text string, minutes int) string {
		return strings.ReplaceAll(text, "{minutes}", fmt.Sprint(minutes))
	},
}

var loginText = texttemplate.Must(texttemplate.New("login.txt").Funcs(loginFuncs).Parse(`{{if .LinkOnly}}{{.T.link_only}}

{{.Link}}

{{.T.valid_link}}{{else}}{{if .Adding}}{{.T.code_add}}{{else}}{{.T.code}}{{end}}

    {{.Code}}

{{minutes .T.valid .Minutes}}

{{.T.or}}
{{.Link}}{{end}}

{{.T.ignore}}
--
https://{{.Host}}
`))

var loginHTML = htmltemplate.Must(htmltemplate.New("login.html").Funcs(loginFuncs).Parse(leads.MailFrame + `
{{define "body"}}
{{if .LinkOnly}}
<p style="margin:0 0 16px;">{{.T.link_only}}</p>
<p style="margin:0 0 16px;"><a href="{{.Link}}" style="display:inline-block;padding:10px 18px;background:#8b6fe0;color:#ffffff;text-decoration:none;border-radius:999px;font-weight:600;">{{.T.button}}</a></p>
<p style="margin:0;color:#6b6f7e;font-size:13px;">{{.T.valid_link}}</p>
{{else}}
<p style="margin:0 0 12px;">{{if .Adding}}{{.T.code_add}}{{else}}{{.T.code}}{{end}}</p>
<p style="margin:0 0 12px;font-family:'Fira Code',ui-monospace,Consolas,monospace;font-size:30px;letter-spacing:8px;font-weight:600;">{{.Code}}</p>
<p style="margin:0 0 20px;color:#6b6f7e;font-size:13px;">{{minutes .T.valid .Minutes}}</p>
<p style="margin:0 0 8px;">{{.T.or}}</p>
<p style="margin:0;"><a href="{{.Link}}" style="display:inline-block;padding:10px 18px;background:#8b6fe0;color:#ffffff;text-decoration:none;border-radius:999px;font-weight:600;">{{.T.button}}</a></p>
{{end}}
{{end}}
{{define "foot"}}{{.T.ignore}}<br><a href="https://{{.Host}}" style="color:#6b6f7e;">{{.Host}}</a>{{end}}`))
