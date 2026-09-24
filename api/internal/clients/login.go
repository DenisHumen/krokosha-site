package clients

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// Signing in with a one-time code. A browser asks for a code for an address, or for a link into the
// site's Telegram bot; the code comes by email or from the bot and works in that browser only — the
// one holding the cookie it got when it asked. A letter also carries a link that works anywhere:
// opening the letter proves the address as well as the code does.
//
// Neither the code nor the link is stored. A login keeps a random nonce; the code and the link are
// HMACs of it with APP_SECRET, made again when the letter is written and when the code comes back.

// Ways to sign in.
const (
	MethodEmail    = "email"
	MethodTelegram = "telegram"
)

const (
	// LoginLifetime is how long a code lives; a link the owner sends from the admin area lives a day.
	LoginLifetime = 15 * time.Minute
	linkLifetime  = 24 * time.Hour
	loginTries    = 5

	// TelegramPrefix starts «/start l_…»: the bot hands such a login its code.
	TelegramPrefix = "l_"
	// TaskLogin is the outbox task of a letter with a code.
	TaskLogin = "client.login"
)

// Attempt is who asks: the language of the page, the network and the software, and a keyed hash
// of the address for the rate limits.
type Attempt struct {
	Lang    string
	Network string
	Device  string
	Key     string
}

// Started is what the browser gets back when it asks for a code.
type Started struct {
	Browser string // the value of the browser's cookie: the code works only with it
	SentTo  string // the address, half hidden: «d***s@gmail.com»
	BotURL  string // Telegram: the link that opens the bot with the login
}

// Result of a code or a link that worked.
type Result struct {
	ClientID int64
	Lang     string
	// Token of the new session; "" when the code added an address or Telegram to the account of
	// the browser that asked, which is signed in already.
	Token string
	// Created: the account is new; Linked: requests left before with this address joined it;
	// Merged: the address or Telegram opened another account of the person, which joined this one.
	Created bool
	Linked  int
	Merged  bool
}

// login is a row of client_logins.
type login struct {
	id           int64
	createdAt    time.Time
	expiresAt    time.Time
	channel      string
	email        string
	telegramID   int64
	telegramName string
	telegramUser string
	nonce        []byte
	clientID     int64
	lang         string
	tries        int
	usedAt       sql.NullTime
}

const loginColumns = `id, created_at, expires_at, channel, email, telegram_id, telegram_name, telegram_user, nonce, client_id, lang, tries, used_at`

func scanLogin(row interface{ Scan(dest ...any) error }) (*login, error) {
	var l login
	var email, telegramName, telegramUser sql.NullString
	var telegramID, clientID sql.NullInt64
	err := row.Scan(&l.id, &l.createdAt, &l.expiresAt, &l.channel, &email, &telegramID, &telegramName, &telegramUser, &l.nonce, &clientID,
		&l.lang, &l.tries, &l.usedAt)
	if err != nil {
		return nil, err
	}
	l.email, l.telegramID, l.telegramName, l.telegramUser, l.clientID = email.String, telegramID.Int64, telegramName.String, telegramUser.String, clientID.Int64
	return &l, nil
}

// code is the six digits of a login.
func (s *Service) code(l *login) string {
	sum := s.mac("code", []byte(strconv.FormatInt(l.id, 10)), l.nonce)
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(sum[:4])%1_000_000)
}

// linkToken is the secret of the link: «<id>.<mac>».
func (s *Service) linkToken(l *login) string {
	sum := s.mac("link", []byte(strconv.FormatInt(l.id, 10)), l.nonce)
	return strconv.FormatInt(l.id, 36) + "." + base64.RawURLEncoding.EncodeToString(sum[:18])
}

// LinkURL is the address of the link: the account's page, which sends the token on. The token is
// after «#», so it never reaches a server log, and a mail filter that opens links does not use it up.
func (s *Service) LinkURL(lang, token string) string {
	return strings.TrimRight(s.opts.SiteURL, "/") + AccountPath(lang) + "#login=" + token
}

// AccountPath is the personal account's page in a language (web/src/pages/[...lang]/account.astro).
func AccountPath(lang string) string {
	if lang == "uk" || lang == "ru" {
		return "/" + lang + "/account/"
	}
	return "/account/"
}

func randomBytes(n int) ([]byte, error) {
	raw := make([]byte, n)
	_, err := rand.Read(raw)
	return raw, err
}

func browserHash(browser string) []byte {
	sum := sha256.Sum256([]byte("krokosha-login-browser:" + browser))
	return sum[:]
}

// allow is a rate limit per hour, keyed by a label and the caller's address hash (or an account).
func (s *Service) allow(ctx context.Context, label, key string, limit int) bool {
	return s.opts.Cache.Allow(ctx, "client-"+label+":"+key, limit, time.Hour)
}

// keyOf hides a value (an address) behind the site's secret before it goes to the cache.
func (s *Service) keyOf(value string) string {
	return hex.EncodeToString(s.mac("key", []byte(strings.ToLower(value)))[:8])
}

// newLogin writes a login and returns it with the browser's secret.
func (s *Service) newLogin(ctx context.Context, channel, email string, startHash []byte, clientID int64, lang string, lifetime time.Duration) (*login, string, error) {
	nonce, err := randomBytes(16)
	if err != nil {
		return nil, "", err
	}
	browserRaw, err := randomBytes(32)
	if err != nil {
		return nil, "", err
	}
	browser := base64.RawURLEncoding.EncodeToString(browserRaw)
	if !languages[lang] {
		lang = "en"
	}
	now := s.now()
	l := &login{createdAt: now, expiresAt: now.Add(lifetime), channel: channel, email: email, nonce: nonce, clientID: clientID, lang: lang}
	result, err := s.opts.DB.ExecContext(ctx, `
		INSERT INTO client_logins (created_at, expires_at, channel, email, start_hash, nonce, browser_hash, client_id, lang)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		now, l.expiresAt, channel, nullString(email), nullBytes(startHash), nonce, browserHash(browser), nullID(clientID), lang)
	if err != nil {
		return nil, "", err
	}
	if l.id, err = result.LastInsertId(); err != nil {
		return nil, "", err
	}
	// Housekeeping: codes nobody used are of no interest a day later.
	_, _ = s.opts.DB.ExecContext(ctx, `DELETE FROM client_logins WHERE expires_at < ?`, now.Add(-24*time.Hour))
	return l, browser, nil
}

// StartEmail sends a code to an address. clientID > 0: the address is being added to that account.
// An address of a blocked account gets nothing, and the browser is told the same as everybody.
func (s *Service) StartEmail(ctx context.Context, address string, a Attempt, clientID int64) (Started, error) {
	email := leads.NormalizeEmail(address)
	if email == "" {
		return Started{}, ErrBadContact
	}
	if !s.allow(ctx, "login", a.Key, 10) || !s.allow(ctx, "login-email", s.keyOf(email), 5) {
		return Started{}, ErrThrottled
	}
	var disabled bool
	err := s.opts.DB.QueryRowContext(ctx, `SELECT disabled_at IS NOT NULL FROM clients WHERE email = ?`, email).Scan(&disabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Started{}, err
	}
	l, browser, err := s.newLogin(ctx, MethodEmail, email, nil, clientID, a.Lang, LoginLifetime)
	if err != nil {
		return Started{}, err
	}
	if !disabled {
		if err := s.queueLetter(ctx, l.id, false); err != nil {
			return Started{}, err
		}
	}
	return Started{Browser: browser, SentTo: maskEmail(email)}, nil
}

func (s *Service) queueLetter(ctx context.Context, loginID int64, linkOnly bool) error {
	if err := outbox.Enqueue(ctx, s.opts.DB, s.now(), outbox.NewTask{
		Channel: outbox.ChannelEmail, Kind: TaskLogin, DedupeKey: fmt.Sprintf("client-login:%d", loginID),
		Payload: loginPayload{LoginID: loginID, LinkOnly: linkOnly},
	}); err != nil {
		return err
	}
	s.opts.Kick()
	return nil
}

// loginPayload is the outbox task of a letter with a code: the letter is written when it is sent.
type loginPayload struct {
	LoginID  int64 `json:"login_id"`
	LinkOnly bool  `json:"link_only,omitempty"` // sent by the owner: no browser waits for a code
}

// StartTelegram gives the browser a link into the bot. clientID > 0: the Telegram account is being
// added to that account.
func (s *Service) StartTelegram(ctx context.Context, a Attempt, clientID int64) (Started, error) {
	bot := s.opts.BotUsername()
	if bot == "" {
		return Started{}, ErrNoBot
	}
	if !s.allow(ctx, "login", a.Key, 10) {
		return Started{}, ErrThrottled
	}
	start, err := randomBytes(16)
	if err != nil {
		return Started{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(start)
	_, browser, err := s.newLogin(ctx, MethodTelegram, "", startHash(token), clientID, a.Lang, LoginLifetime)
	if err != nil {
		return Started{}, err
	}
	return Started{Browser: browser, BotURL: "https://t.me/" + bot + "?start=" + TelegramPrefix + token}, nil
}

func startHash(token string) []byte {
	sum := sha256.Sum256([]byte("krokosha-login-start:" + token))
	return sum[:]
}

// TelegramCode is what the bot tells the person who opened it with a login.
type TelegramCode struct {
	Code    string
	LinkURL string // none when adding: that is finished by the code only (VerifyLink)
	Lang    string
	Adding  bool // the Telegram account is being added to an account, not signed in with
	Joining bool // … and it signs in to another account now, which the code joins to that one
}

// ErrUsed: the login was opened in another Telegram account, used, or expired.
var ErrUsed = errors.New("the login is used or expired")

// TelegramStart binds a login to the Telegram account that opened the bot with it, and returns the
// code for that person — the bot shows it in the chat. The first account to open it keeps it.
func (s *Service) TelegramStart(ctx context.Context, token string, telegramID int64, firstName, username string) (TelegramCode, error) {
	token = strings.TrimPrefix(token, TelegramPrefix)
	if len(token) != 22 {
		return TelegramCode{}, ErrUsed
	}
	now := s.now()
	result, err := s.opts.DB.ExecContext(ctx, `
		UPDATE client_logins SET telegram_id = ?, telegram_name = ?, telegram_user = ?
		WHERE start_hash = ? AND channel = 'telegram' AND used_at IS NULL AND expires_at > ? AND (telegram_id IS NULL OR telegram_id = ?)`,
		telegramID, nullString(cut(firstName, 64)), nullString(cut(strings.TrimPrefix(username, "@"), 64)), startHash(token), now, telegramID)
	if err != nil {
		return TelegramCode{}, err
	}
	if bound, _ := result.RowsAffected(); bound == 0 {
		// MySQL reports no change when the same person opens the same link twice: look again.
		var id int64
		if err := s.opts.DB.QueryRowContext(ctx, `SELECT id FROM client_logins WHERE start_hash = ? AND telegram_id = ? AND used_at IS NULL AND expires_at > ?`,
			startHash(token), telegramID, now).Scan(&id); err != nil {
			return TelegramCode{}, ErrUsed
		}
	}
	l, err := scanLogin(s.opts.DB.QueryRowContext(ctx, `SELECT `+loginColumns+` FROM client_logins WHERE start_hash = ?`, startHash(token)))
	if err != nil {
		return TelegramCode{}, ErrUsed
	}
	code := TelegramCode{Code: s.code(l), Lang: l.lang, Adding: l.clientID > 0}
	if !code.Adding {
		code.LinkURL = s.LinkURL(l.lang, s.linkToken(l))
	} else if code.Joining, err = s.joins(ctx, "telegram_id", telegramID, l.clientID); err != nil {
		return TelegramCode{}, err
	}
	return code, nil
}

// joins tells whether the address or Telegram account signs in to another account than the one it
// is being added to: its code then joins that account to this one (addWay).
func (s *Service) joins(ctx context.Context, column string, value any, clientID int64) (bool, error) {
	var other bool // the column is "email" or "telegram_id"
	err := s.opts.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM clients WHERE `+column+` = ? AND id <> ?)`, value, clientID).Scan(&other)
	return other, err
}

// VerifyCode finishes the login the browser asked for, with the code it was given.
func (s *Service) VerifyCode(ctx context.Context, browser, code string, a Attempt) (Result, error) {
	code = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, code)
	if browser == "" || len(code) != 6 {
		return Result{}, ErrBadCode
	}
	if !s.allow(ctx, "code", a.Key, 30) {
		return Result{}, ErrThrottled
	}
	now := s.now()
	l, err := scanLogin(s.opts.DB.QueryRowContext(ctx, `SELECT `+loginColumns+` FROM client_logins
		WHERE browser_hash = ? AND used_at IS NULL AND expires_at > ? ORDER BY id DESC LIMIT 1`, browserHash(browser), now))
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrBadCode
	}
	if err != nil {
		return Result{}, err
	}
	if l.channel == MethodTelegram && l.telegramID == 0 {
		return Result{}, ErrBadCode // nobody opened the bot with it yet
	}
	// A try is counted before it is judged, so that parallel guesses cannot share one.
	result, err := s.opts.DB.ExecContext(ctx, `UPDATE client_logins SET tries = tries + 1 WHERE id = ? AND tries < ?`, l.id, loginTries)
	if err != nil {
		return Result{}, err
	}
	if counted, _ := result.RowsAffected(); counted == 0 {
		return Result{}, ErrBadCode
	}
	if !hmac.Equal([]byte(code), []byte(s.code(l))) {
		return Result{}, ErrBadCode
	}
	return s.complete(ctx, l, a)
}

// VerifyLink finishes a login by the link of a letter or of the bot.
//
// It signs in only. Adding a way to an account is finished by the code, typed in the browser that
// asked for it: a link works in any browser, and whoever clicked one — the owner of the address or
// of the Telegram account, talked into it — would hand that address or Telegram account, their
// requests and their own account over to the account that asked. Such logins get no link at all.
func (s *Service) VerifyLink(ctx context.Context, token string, a Attempt) (Result, error) {
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || len(token) > 60 {
		return Result{}, ErrBadCode
	}
	id, err := strconv.ParseInt(token[:dot], 36, 64)
	if err != nil {
		return Result{}, ErrBadCode
	}
	if !s.allow(ctx, "code", a.Key, 30) {
		return Result{}, ErrThrottled
	}
	l, err := scanLogin(s.opts.DB.QueryRowContext(ctx, `SELECT `+loginColumns+` FROM client_logins WHERE id = ? AND used_at IS NULL AND expires_at > ?`, id, s.now()))
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrBadCode
	}
	if err != nil {
		return Result{}, err
	}
	if !hmac.Equal([]byte(token), []byte(s.linkToken(l))) || (l.channel == MethodTelegram && l.telegramID == 0) || l.clientID > 0 {
		return Result{}, ErrBadCode
	}
	return s.complete(ctx, l, a)
}

// complete spends a login: it signs the client in (making the account when there is none), or adds
// the proven address or Telegram account to the account that asked.
func (s *Service) complete(ctx context.Context, l *login, a Attempt) (Result, error) {
	result, err := s.opts.DB.ExecContext(ctx, `UPDATE client_logins SET used_at = ? WHERE id = ? AND used_at IS NULL`, s.now(), l.id)
	if err != nil {
		return Result{}, err
	}
	if spent, _ := result.RowsAffected(); spent == 0 {
		return Result{}, ErrBadCode // the same code, a moment earlier, in another tab
	}
	out := Result{Lang: l.lang}
	if l.clientID > 0 {
		if out.Merged, err = s.addWay(ctx, l); err != nil {
			return Result{}, err
		}
		out.ClientID = l.clientID
	} else {
		var client *Client
		if client, out.Created, err = s.findOrCreate(ctx, l); err != nil {
			return Result{}, err
		}
		if client.DisabledAt.Valid {
			return Result{}, ErrDisabled
		}
		out.ClientID, out.Lang = client.ID, client.Lang
		if out.Token, err = s.openSession(ctx, client.ID, a.Network, a.Device); err != nil {
			return Result{}, err
		}
	}
	// Whatever this address or Telegram account left before joins the account now.
	if l.channel == MethodEmail {
		out.Linked, err = s.opts.Leads.LinkByEmail(ctx, out.ClientID, l.email)
	} else {
		out.Linked, err = s.opts.Leads.LinkByTelegram(ctx, out.ClientID, l.telegramID)
	}
	return out, err
}

// findOrCreate returns the account of the proven address or Telegram account, making one if needed.
func (s *Service) findOrCreate(ctx context.Context, l *login) (*Client, bool, error) {
	column, value := "email", any(l.email)
	if l.channel == MethodTelegram {
		column, value = "telegram_id", l.telegramID
	}
	lookup := func() (*Client, error) { // the column is one of the two constants above
		return scanClient(s.opts.DB.QueryRowContext(ctx, `SELECT `+clientColumns+` FROM clients WHERE `+column+` = ?`, value))
	}
	client, err := lookup()
	if err == nil {
		if l.channel == MethodTelegram && l.telegramUser != "" && l.telegramUser != client.TelegramUsername {
			_, _ = s.opts.DB.ExecContext(ctx, `UPDATE clients SET telegram_username = ? WHERE id = ?`, l.telegramUser, client.ID)
		}
		return client, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	// A new client: the name they gave in their last request with this address, or in Telegram.
	name := l.telegramName
	if l.channel == MethodEmail {
		_ = s.opts.DB.QueryRowContext(ctx, `SELECT name FROM leads WHERE contact_method = 'email' AND contact_value = ? AND anonymized_at IS NULL
			AND status <> 'spam' ORDER BY id DESC LIMIT 1`, l.email).Scan(&name)
	}
	now := s.now()
	var email, username sql.NullString
	var telegram sql.NullInt64
	if l.channel == MethodEmail {
		email = sql.NullString{String: l.email, Valid: true}
	} else {
		telegram = sql.NullInt64{Int64: l.telegramID, Valid: true}
		username = nullString(l.telegramUser)
	}
	_, inserted := s.opts.DB.ExecContext(ctx, `
		INSERT INTO clients (created_at, updated_at, name, lang, email, telegram_id, telegram_username, preferred) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		now, now, cut(name, 100), l.lang, email, telegram, username, l.channel)
	if inserted != nil && !isDuplicate(inserted) {
		return nil, false, inserted
	}
	// A duplicate: another tab made the account a moment ago. Either way it is there now.
	client, err = lookup()
	return client, inserted == nil, err
}

// addWay puts a proven address or Telegram account on the account that asked for the code.
//
// When the address or the Telegram account signs in to another account already, the person has
// just proved both are theirs: they typed, in the browser signed in to this account, the code that
// went to that address or to that Telegram account — whoever holds the code could sign in there
// anyway (VerifyLink: no link finishes this). The other account joins this one (Merge: requests,
// contacts, achievements), so that whichever way they sign in afterwards, it is the same account.
// A blocked account joins nothing: that is the owner's call.
func (s *Service) addWay(ctx context.Context, l *login) (merged bool, err error) {
	column, value := "email", any(l.email)
	if l.channel == MethodTelegram {
		column, value = "telegram_id", l.telegramID
	}
	var owner int64 // the column is one of the two constants above
	var blocked bool
	err = s.opts.DB.QueryRowContext(ctx, `SELECT id, disabled_at IS NOT NULL FROM clients WHERE `+column+` = ?`, value).Scan(&owner, &blocked)
	switch {
	case err == nil && owner != l.clientID && blocked:
		return false, ErrTaken
	case err == nil && owner != l.clientID:
		if err := s.Merge(ctx, l.clientID, owner); err != nil {
			return false, err
		}
		merged = true
		s.opts.Log.Info("account: two accounts of one person became one", "kept", l.clientID, "joined", owner, "by", l.channel)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	if l.channel == MethodEmail {
		_, err = s.opts.DB.ExecContext(ctx, `UPDATE clients SET email = ?, updated_at = ? WHERE id = ?`, l.email, s.now(), l.clientID)
	} else {
		_, err = s.opts.DB.ExecContext(ctx, `UPDATE clients SET telegram_id = ?, telegram_username = ?, updated_at = ? WHERE id = ?`,
			l.telegramID, nullString(l.telegramUser), s.now(), l.clientID)
	}
	if isDuplicate(err) {
		return merged, ErrTaken
	}
	return merged, err
}

// maskEmail hides most of an address: enough for its owner to recognise it, not for anybody else to learn it.
func maskEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" {
		return email
	}
	runes := []rune(local)
	if len(runes) <= 2 {
		return string(runes[0]) + "***@" + domain
	}
	return string(runes[0]) + "***" + string(runes[len(runes)-1]) + "@" + domain
}

func isDuplicate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Duplicate entry")
}

func nullString(text string) sql.NullString { return sql.NullString{String: text, Valid: text != ""} }

func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id > 0} }

func nullBytes(data []byte) any {
	if len(data) == 0 {
		return nil
	}
	return data
}
