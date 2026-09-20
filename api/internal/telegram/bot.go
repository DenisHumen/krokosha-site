package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// ClientPrefix starts the «continue in Telegram» links of clients (brief B10.5).
const ClientPrefix = "c_"

// Options are what the bot works with.
type Options struct {
	API    *Client
	Access *Access
	Cache  *cache.Cache
	Log    *slog.Logger
	// DB keeps what the bot has sent about requests; Leads is where every action on a request
	// ends — the same store the admin area works with.
	DB    *sql.DB
	Leads *leads.Store
	// Form names the directions of requests (content/site.yaml); Location is the owner's time zone.
	Form     func() config.Form
	Location *time.Location
	// AdminURL is the address of the admin area, for the «open in the admin area» button.
	AdminURL string
	// Kick tells the outbox that there is something to deliver right now. Hurry does more: it
	// makes cards that were waiting for somebody to join go out at once.
	Kick  func()
	Hurry func(ctx context.Context)
	// RemindAfter: a new request nobody took for so long is pushed once more; 0 — never.
	// DigestAt: «09:00» — when the morning list of open requests goes out, by the owner's clock; "" — never.
	RemindAfter time.Duration
	DigestAt    string
	// Letters counts the letters that wait in the admin area for somebody to say whose they are;
	// nil — mail is not read. The morning digest mentions them.
	Letters func(ctx context.Context) int
	// Audit writes into the journal of the admin area: who let whom in, who switched whom off.
	Audit func(ctx context.Context, actor, action, subject, details string)
	// SiteURL is the public address of the site, for the greeting of strangers.
	SiteURL string
	Now     func() time.Time
}

// Bot decides what to do with every update.
type Bot struct {
	opts Options
	me   atomic.Pointer[User] // who the token belongs to; set by the runner once Telegram answered

	redraw chan int64    // requests whose cards have to be redrawn
	wipe   chan struct{} // «there is something to wipe»
}

// Username is the bot's own name in Telegram, "" until Telegram confirmed the token.
func (b *Bot) Username() string {
	if me := b.me.Load(); me != nil {
		return me.Username
	}
	return ""
}

// New builds the bot.
func New(opts Options) *Bot {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Audit == nil {
		opts.Audit = func(context.Context, string, string, string, string) {}
	}
	if opts.Kick == nil {
		opts.Kick = func() {}
	}
	if opts.Hurry == nil {
		opts.Hurry = func(context.Context) {}
	}
	if opts.Form == nil {
		opts.Form = func() config.Form { return config.Form{} }
	}
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	return &Bot{opts: opts, redraw: make(chan int64, 256), wipe: make(chan struct{}, 1)}
}

// Handle deals with one update. Nothing is returned: whatever goes wrong is logged, and the
// person gets an answer whenever one can be given.
func (b *Bot) Handle(ctx context.Context, update Update) {
	switch {
	case update.Message != nil:
		b.message(ctx, update.Message)
	case update.CallbackQuery != nil:
		b.callback(ctx, update.CallbackQuery)
	}
}

// say sends a text and logs a failure: there is nobody to return it to.
func (b *Bot) say(ctx context.Context, message Outgoing) {
	if _, err := b.opts.API.Send(ctx, message); err != nil && ctx.Err() == nil {
		b.opts.Log.Warn("telegram: cannot send a message", "error", err)
	}
}

func (b *Bot) message(ctx context.Context, message *Message) {
	// Private chats only. In a group the bot stays silent: a card of a request shown there would
	// be shown to everybody in the group.
	if message.From == nil || message.From.IsBot || message.Chat.Type != "private" {
		return
	}
	command, argument := splitCommand(message.Text)
	member, err := b.opts.Access.Member(ctx, message.From.ID)
	switch {
	case errors.Is(err, ErrNoAccess):
		b.stranger(ctx, message, command, argument)
		return
	case err != nil:
		b.opts.Log.Error("telegram: cannot check access", "error", err)
		return
	}

	switch command {
	case "/start":
		if strings.HasPrefix(argument, InvitePrefix) {
			b.redeem(ctx, message, argument) // an invitation with another role
			return
		}
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: b.help(member)})
	case "/help":
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: b.help(member)})
	case "/invite":
		b.invite(ctx, message, member, argument)
	case "/users":
		b.users(ctx, message.Chat.ID, member)
	case "/leads":
		b.leadsCommand(ctx, message.Chat.ID, member, "")
	case "/lead":
		b.leadCommand(ctx, message.Chat.ID, argument)
	case "/search":
		b.searchCommand(ctx, message.Chat.ID, argument)
	case "/stats":
		b.statsCommand(ctx, message.Chat.ID)
	case "/mute":
		b.muteCommand(ctx, message.Chat.ID, member, argument)
	case "/cancel":
		_ = b.opts.Access.SetDialog(ctx, member.TelegramID, nil)
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: "Отменено."})
	case "":
		// A plain text: the answer, the note or the reason the bot asked for.
		if !b.dialogText(ctx, message, member) {
			b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: "Не понял. Чтобы ответить клиенту или оставить заметку, нажмите кнопку на карточке заявки. Команды — /help"})
		}
	default:
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: "Не понял. Список команд — /help"})
	}
}

// splitCommand tells «/lead K-0042» apart: the command without the bot's name, and the rest.
func splitCommand(text string) (command, argument string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	command, argument, _ = strings.Cut(text, " ")
	command, _, _ = strings.Cut(command, "@") // «/help@krokosha_bot», as clients write it in groups
	return strings.ToLower(command), strings.TrimSpace(argument)
}

// --- people without access ------------------------------------------------------------------------

var greetings = map[string]string{
	"ru": "Здравствуйте! Это бот сайта %s.\n\nЧтобы продолжить разговор по своей заявке, откройте кнопку «Продолжить в Telegram» на странице, которая появилась после отправки формы, или в письме с подтверждением.\n\nОставить заявку: %s",
	"uk": "Вітаю! Це бот сайту %s.\n\nЩоб продовжити розмову щодо своєї заявки, відкрийте кнопку «Продовжити в Telegram» на сторінці, яка з'явилася після надсилання форми, або в листі з підтвердженням.\n\nЗалишити заявку: %s",
	"en": "Hello! This is the bot of %s.\n\nTo continue the conversation about your request, use the “Continue in Telegram” button on the page shown after you sent the form, or in the confirmation email.\n\nSend a request: %s",
}

// stranger is anybody without access: a client, a passer-by, somebody trying codes. They learn
// nothing about requests or people here, and the bot does not let itself be used as an echo.
func (b *Bot) stranger(ctx context.Context, message *Message, command, argument string) {
	who := strconv.FormatInt(message.From.ID, 10)
	if command == "/start" && strings.HasPrefix(argument, InvitePrefix) {
		// A code cannot be guessed (96 bits), but nobody needs to be allowed to try all day.
		if !b.opts.Cache.Allow(ctx, "tg-invite:"+who, 5, time.Hour) {
			return
		}
		b.redeem(ctx, message, argument)
		return
	}
	// A client: somebody who came by the link of their own request, or comes by it right now.
	if b.opts.Leads != nil {
		if command == "/start" && strings.HasPrefix(argument, ClientPrefix) && b.opts.Cache.Allow(ctx, "tg-link:"+who, 10, time.Hour) &&
			b.linkClient(ctx, message, argument) {
			return
		}
		if b.clientWrites(ctx, message, command) {
			return
		}
	}
	// One greeting in ten minutes; whatever else they write meanwhile stays unanswered.
	if !b.opts.Cache.Allow(ctx, "tg-greeting:"+who, 1, 10*time.Minute) {
		return
	}
	text, ok := greetings[message.From.LanguageCode]
	if !ok {
		text = greetings["en"]
	}
	host := strings.TrimPrefix(strings.TrimPrefix(b.opts.SiteURL, "https://"), "http://")
	b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: fmt.Sprintf(text, Escape(host), Escape(b.opts.SiteURL+"/#contacts"))})
}

func (b *Bot) redeem(ctx context.Context, message *Message, code string) {
	member, err := b.opts.Access.Redeem(ctx, code, *message.From)
	switch {
	case errors.Is(err, ErrBadInvite):
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: "Приглашение не подходит: оно уже использовано или его срок (24 часа) вышел. Попросите новое."})
	case err != nil:
		b.opts.Log.Error("telegram: cannot redeem an invitation", "error", err)
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: "Не получилось проверить приглашение. Попробуйте ещё раз чуть позже."})
	default:
		b.opts.Audit(ctx, "bot:"+member.Actor(), "bot.join", strconv.FormatInt(member.TelegramID, 10), member.Role+", пригласил(а) "+member.InvitedBy)
		b.opts.Log.Info("telegram: a person joined by invitation", "role", member.Role, "telegram_id", member.TelegramID)
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: "Доступ открыт, " + Escape(member.Name) + ".\n\n" + b.help(member)})
		// Requests that came while nobody could receive them have been waiting for this moment.
		b.opts.Hurry(ctx)
	}
}

// --- members ----------------------------------------------------------------------------------------

func (b *Bot) help(member *Member) string {
	lines := []string{
		"Новые заявки с сайта приходят сюда карточками с кнопками: взять в работу, ответить клиенту, оставить заметку, отклонить. Что бы ни сделали вы или коллеги — здесь или в админке, — карточка меняется у всех сразу.",
		"",
		"/leads — открытые заявки: все, новые, мои, ждут клиента",
		"/lead K-0042 — карточка заявки по номеру",
		"/search текст — поиск по имени, контакту, тексту, номеру",
		"/stats — неделя в цифрах",
		"/mute 2h — заявки приходят без звука; /mute off — вернуть звук",
		"/cancel — отменить то, что бот сейчас ждёт (текст ответа, заметки, причины)",
		"/help — эта справка",
	}
	if member.Owner() {
		lines = append(lines,
			"/invite — приглашение для нового участника (действует 24 часа, на одного человека); /invite owner — для ещё одного владельца",
			"/users — кто имеет доступ; отключить или вернуть доступ")
	}
	return strings.Join(lines, "\n")
}

func (b *Bot) invite(ctx context.Context, message *Message, member *Member, argument string) {
	if !member.Owner() {
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: "Приглашать может только владелец."})
		return
	}
	role := RoleMember
	if strings.EqualFold(argument, RoleOwner) {
		role = RoleOwner
	}
	code, _, err := b.opts.Access.Invite(ctx, role, "bot:"+member.Actor())
	if err != nil {
		b.opts.Log.Error("telegram: cannot make an invitation", "error", err)
		b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: "Не получилось создать приглашение."})
		return
	}
	b.opts.Audit(ctx, "bot:"+member.Actor(), "bot.invite", role, "")
	b.say(ctx, Outgoing{ChatID: message.Chat.ID, Text: b.InviteText(role, code)})
}

// InviteText is what the maker of an invitation gets to pass on: the link, and the code for
// those who would rather type.
func (b *Bot) InviteText(role, code string) string {
	roleName := "участника"
	if role == RoleOwner {
		roleName = "владельца"
	}
	text := "Приглашение для " + roleName + " — на одного человека, действует 24 часа.\n\n"
	if username := b.Username(); username != "" {
		text += "Ссылка: https://t.me/" + Escape(username) + "?start=" + code + "\n"
	}
	return text + "Или написать боту: <code>/start " + code + "</code>"
}

func (b *Bot) users(ctx context.Context, chatID int64, member *Member) {
	if !member.Owner() {
		b.say(ctx, Outgoing{ChatID: chatID, Text: "Список участников виден только владельцу."})
		return
	}
	text, buttons, err := b.usersView(ctx)
	if err != nil {
		b.opts.Log.Error("telegram: cannot list the members", "error", err)
		return
	}
	b.say(ctx, Outgoing{ChatID: chatID, Text: text, Buttons: buttons})
}

func (b *Bot) usersView(ctx context.Context) (string, Keyboard, error) {
	members, err := b.opts.Access.Members(ctx)
	if err != nil {
		return "", nil, err
	}
	lines := []string{"<b>Доступ к боту</b>", ""}
	var buttons Keyboard
	for _, member := range members {
		line := "• " + Escape(member.Name)
		if member.Username != "" {
			line += " (@" + Escape(member.Username) + ")"
		}
		if member.Owner() {
			line += " — владелец"
		}
		id := strconv.FormatInt(member.ID, 10)
		if member.DisabledAt.Valid {
			line += " — <i>доступ отключён</i>"
			buttons = append(buttons, []Button{{Text: "Вернуть доступ: " + member.Name, Data: "u:on:" + id}})
		} else {
			buttons = append(buttons, []Button{{Text: "Отключить: " + member.Name, Data: "u:off:" + id}})
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), buttons, nil
}

// --- buttons ----------------------------------------------------------------------------------------

func (b *Bot) callback(ctx context.Context, query *CallbackQuery) {
	answer := func(text string, alert bool) {
		if err := b.opts.API.AnswerCallback(ctx, query.ID, text, alert); err != nil && ctx.Err() == nil {
			b.opts.Log.Warn("telegram: cannot answer a button", "error", err)
		}
	}
	member, err := b.opts.Access.Member(ctx, query.From.ID)
	if err != nil {
		// Access was revoked while a card with buttons was still on the screen.
		answer("Нет доступа.", true)
		return
	}
	parts := strings.Split(query.Data, ":")
	switch {
	case len(parts) == 3 && parts[0] == "u":
		b.switchMember(ctx, query, member, parts[1] == "off", parts[2], answer)
	case len(parts) >= 3 && parts[0] == "l":
		b.leadButton(ctx, query, member, parts, answer)
	case len(parts) == 2 && parts[0] == "ls":
		b.listButton(ctx, query, member, parts[1], answer)
	default:
		answer("Эта кнопка больше не работает.", false)
	}
}

func (b *Bot) switchMember(ctx context.Context, query *CallbackQuery, member *Member, off bool, rawID string, answer func(string, bool)) {
	if !member.Owner() {
		answer("Доступом управляет только владелец.", true)
		return
	}
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		answer("Эта кнопка больше не работает.", false)
		return
	}
	changed, err := b.opts.Access.SetDisabled(ctx, id, off)
	switch {
	case errors.Is(err, ErrLastOwner):
		answer("Единственного владельца отключить нельзя: некому будет приглашать.", true)
		return
	case errors.Is(err, ErrNoAccess):
		answer("Такого участника уже нет.", true)
		return
	case err != nil:
		b.opts.Log.Error("telegram: cannot change access", "error", err)
		answer("Не получилось.", true)
		return
	}
	action, done := "bot.enable", "Доступ возвращён: "
	if off {
		action, done = "bot.disable", "Доступ отключён: "
	}
	b.opts.Audit(ctx, "bot:"+member.Actor(), action, strconv.FormatInt(changed.TelegramID, 10), changed.Name)
	answer(done+changed.Name, false)
	if query.Message != nil {
		if text, buttons, err := b.usersView(ctx); err == nil {
			if err := b.opts.API.Edit(ctx, query.Message.MessageID, Outgoing{ChatID: query.Message.Chat.ID, Text: text, Buttons: buttons}); err != nil {
				b.opts.Log.Warn("telegram: cannot refresh the list of members", "error", err)
			}
		}
	}
}
