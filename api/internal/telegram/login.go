package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/clients"
)

// Signing in to the personal account with Telegram (docs/architecture.md, 2026-09-24). The site
// gives the browser a link into the bot, «/start l_<token>»; the bot binds the login to whoever
// opened it and hands them the code, which they type on the site — in the browser that asked. A
// button with a link signs in wherever it is opened, like the link of a letter; linking Telegram to
// an account has no such button — only the code, typed where it was asked for.

// Logins signs clients in (clients.Service).
type Logins interface {
	TelegramStart(ctx context.Context, token string, telegramID int64, firstName, username string) (clients.TelegramCode, error)
}

var loginTexts = map[string]map[string]string{
	"ru": {
		"code":   "Код для входа в личный кабинет на сайте:",
		"add":    "Код, чтобы привязать этот Telegram к личному кабинету:",
		"where":  "Введите его на странице, с которой вы пришли. Код действует 15 минут.",
		"button": "Войти на сайт",
		"ignore": "Если вход начинали не вы — просто ничего не делайте.",
		"used":   "Эта ссылка для входа устарела или уже открыта в другом аккаунте Telegram. Начните вход на сайте заново.",
		"slow":   "Слишком много попыток входа. Попробуйте через час.",
		"failed": "Не получилось подготовить код. Попробуйте ещё раз чуть позже.",
	},
	"uk": {
		"code":   "Код для входу в особистий кабінет на сайті:",
		"add":    "Код, щоб прив'язати цей Telegram до особистого кабінету:",
		"where":  "Введіть його на сторінці, з якої ви прийшли. Код діє 15 хвилин.",
		"button": "Увійти на сайт",
		"ignore": "Якщо вхід починали не ви — просто нічого не робіть.",
		"used":   "Це посилання для входу застаріло або вже відкрите в іншому акаунті Telegram. Почніть вхід на сайті знову.",
		"slow":   "Забагато спроб входу. Спробуйте за годину.",
		"failed": "Не вдалося підготувати код. Спробуйте ще раз трохи згодом.",
	},
	"en": {
		"code":   "Your code to sign in to your personal account on the site:",
		"add":    "Your code to link this Telegram account to your personal account:",
		"where":  "Type it on the page you came from. The code is valid for 15 minutes.",
		"button": "Sign in to the site",
		"ignore": "If it was not you who started signing in, just do nothing.",
		"used":   "This sign-in link has expired or was opened in another Telegram account. Please start signing in on the site again.",
		"slow":   "Too many sign-in attempts. Please try again in an hour.",
		"failed": "The code could not be prepared. Please try again a little later.",
	},
}

func loginText(lang, key string) string {
	texts, ok := loginTexts[lang]
	if !ok {
		texts = loginTexts["en"]
	}
	return texts[key]
}

// signIn answers «/start l_<token>», for anybody: a client, a stranger, the owner trying it out.
func (b *Bot) signIn(ctx context.Context, message *Message, token string) {
	chatID, lang := message.Chat.ID, message.From.LanguageCode
	if !b.opts.Cache.Allow(ctx, "tg-login:"+strconv.FormatInt(message.From.ID, 10), 10, time.Hour) {
		b.say(ctx, Outgoing{ChatID: chatID, Text: Escape(loginText(lang, "slow"))})
		return
	}
	username := message.From.Username
	code, err := b.opts.Logins.TelegramStart(ctx, token, message.From.ID, strings.TrimSpace(message.From.FirstName+" "+message.From.LastName), username)
	switch {
	case errors.Is(err, clients.ErrUsed):
		b.say(ctx, Outgoing{ChatID: chatID, Text: Escape(loginText(lang, "used"))})
		return
	case err != nil:
		b.opts.Log.Error("telegram: cannot prepare a sign-in code", "error", err)
		b.say(ctx, Outgoing{ChatID: chatID, Text: Escape(loginText(lang, "failed"))})
		return
	}
	lang = code.Lang
	intro := loginText(lang, "code")
	if code.Adding {
		intro = loginText(lang, "add")
	}
	// <code> makes the digits copy with a tap.
	text := Escape(intro) + "\n\n<code>" + Escape(code.Code) + "</code>\n\n" + Escape(loginText(lang, "where")) + "\n\n" + Escape(loginText(lang, "ignore"))
	out := Outgoing{ChatID: chatID, Text: text}
	if code.LinkURL != "" {
		out.Buttons = Keyboard{{{Text: loginText(lang, "button"), URL: code.LinkURL}}}
	}
	b.say(ctx, out)
}
