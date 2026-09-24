// Package clients is the personal account of the site's clients (docs/architecture.md, 2026-09-24):
// their requests and inquiries with statuses and the conversation, discounts and the levels of
// regular clients, the ways to reach them. There are no passwords to leak or to forget: a client
// signs in with a one-time code that comes by email or from the site's Telegram bot, and whoever
// signs in with an address or a Telegram account nobody has yet gets a new account.
//
// The owner sees every account in the admin area: who they are, how to write to them, what they
// ordered — and can give a personal discount, end sessions, block or delete an account.
package clients

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// Cookies. The __Host- prefix makes browsers insist on Secure, Path=/ and no Domain.
const (
	SessionCookie = "__Host-kc" // a signed-in client
	loginCookie   = "__Host-kl" // the browser that asked for a code: only it can use the code
)

// Session limits: a client comes back now and then, not every day.
const (
	SessionIdle     = 30 * 24 * time.Hour
	SessionLifetime = 90 * 24 * time.Hour
)

// Errors.
var (
	ErrNotFound    = errors.New("no such client")
	ErrNoSession   = errors.New("not signed in")
	ErrBadCode     = errors.New("wrong or expired code")
	ErrThrottled   = errors.New("too many attempts")
	ErrTaken       = errors.New("this address or Telegram account belongs to another client")
	ErrLastWay     = errors.New("the only way to sign in cannot be removed")
	ErrNoBot       = errors.New("the site has no Telegram bot")
	ErrTooMany     = errors.New("too many contacts")
	ErrDisabled    = errors.New("the account is blocked")
	ErrBadDiscount = errors.New("a discount is a percent from 0 to 100")
)

// Client is an account.
type Client struct {
	ID               int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LastSeenAt       sql.NullTime
	Name             string
	Company          string
	Lang             string
	Email            string // "" — none proved
	TelegramID       int64  // 0 — none linked
	TelegramUsername string
	Preferred        string
	Personal         Personal
	OrdersCarried    int
	SpentCarried     float64
	Note             string
	DisabledAt       sql.NullTime
}

// Personal is the discount the owner gave the client.
type Personal struct {
	Percent int
	Note    string // the client sees it: «партнёрская», «за отзыв»
	Until   sql.NullTime
	Once    bool
}

// Contact is a way to reach the client that nothing proves.
type Contact struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"-"`
}

// Link is where the contact leads the owner.
func (c Contact) Link() string { return ContactLink(c.Kind, c.Value) }

// SessionInfo is a signed-in browser, as the account and the admin area list them.
type SessionInfo struct {
	ID        string    `json:"id"` // a short handle of the token's hash: enough to end it, useless to sign in
	CreatedAt time.Time `json:"created"`
	LastSeen  time.Time `json:"seen"`
	Device    string    `json:"device"`
	Network   string    `json:"-"` // the truncated address: the owner sees it, the page does not need it
	Current   bool      `json:"current"`
}

// Options configure the service.
type Options struct {
	DB     *sql.DB
	Cache  *cache.Cache
	Leads  *leads.Store
	Log    *slog.Logger
	Secret []byte // APP_SECRET: codes, links and rate limits are keyed with it
	// SiteURL is the public address: links in letters and in the bot lead there.
	SiteURL string
	// BotUsername is the bot's name in Telegram, "" while there is no bot: signing in with Telegram waits for it.
	BotUsername func() string
	// Kick tells the outbox a letter with a code waits: it should go at once.
	Kick     func()
	Location *time.Location
	Now      func() time.Time
}

// Service keeps the accounts.
type Service struct {
	opts Options
}

// New builds the service.
func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.BotUsername == nil {
		opts.BotUsername = func() string { return "" }
	}
	if opts.Kick == nil {
		opts.Kick = func() {}
	}
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	return &Service{opts: opts}
}

func (s *Service) now() time.Time { return s.opts.Now().UTC() }

// mac keys a value with the site's secret under a label of its own.
func (s *Service) mac(label string, parts ...[]byte) []byte {
	h := hmac.New(sha256.New, s.opts.Secret)
	h.Write([]byte("krokosha-client:" + label))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write(part)
	}
	return h.Sum(nil)
}

const clientColumns = `id, created_at, updated_at, last_seen_at, name, company, lang, email, telegram_id, telegram_username, preferred,
	personal_discount, personal_note, personal_until, personal_once, orders_carried, spent_carried, note, disabled_at`

func scanClient(row interface{ Scan(dest ...any) error }) (*Client, error) {
	var c Client
	var email, telegramName, preferred, personalNote, note sql.NullString
	var telegram, personal sql.NullInt64
	err := row.Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt, &c.LastSeenAt, &c.Name, &c.Company, &c.Lang, &email, &telegram, &telegramName, &preferred,
		&personal, &personalNote, &c.Personal.Until, &c.Personal.Once, &c.OrdersCarried, &c.SpentCarried, &note, &c.DisabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Email, c.TelegramID, c.TelegramUsername, c.Preferred = email.String, telegram.Int64, telegramName.String, preferred.String
	c.Personal.Percent, c.Personal.Note, c.Note = int(personal.Int64), personalNote.String, note.String
	return &c, nil
}

// Get reads an account.
func (s *Service) Get(ctx context.Context, id int64) (*Client, error) {
	return scanClient(s.opts.DB.QueryRowContext(ctx, `SELECT `+clientColumns+` FROM clients WHERE id = ?`, id))
}

// Contacts lists the ways to reach a client beyond the address and Telegram.
func (s *Service) Contacts(ctx context.Context, id int64) ([]Contact, error) {
	rows, err := s.opts.DB.QueryContext(ctx, `SELECT id, kind, value, created_at FROM client_contacts WHERE client_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.Kind, &c.Value, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddContact keeps a way to reach the client. The same one twice is kept once.
func (s *Service) AddContact(ctx context.Context, id int64, kind, value string) (Contact, error) {
	value, err := NormalizeContact(kind, value)
	if err != nil {
		return Contact{}, err
	}
	var count int
	if err := s.opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM client_contacts WHERE client_id = ?`, id).Scan(&count); err != nil {
		return Contact{}, err
	}
	if count >= MaxContacts {
		return Contact{}, ErrTooMany
	}
	now := s.now()
	if _, err := s.opts.DB.ExecContext(ctx, `INSERT IGNORE INTO client_contacts (client_id, kind, value, created_at) VALUES (?, ?, ?, ?)`,
		id, kind, value, now); err != nil {
		return Contact{}, err
	}
	contact := Contact{Kind: kind, Value: value}
	err = s.opts.DB.QueryRowContext(ctx, `SELECT id, created_at FROM client_contacts WHERE client_id = ? AND kind = ? AND value = ?`, id, kind, value).
		Scan(&contact.ID, &contact.CreatedAt)
	s.touch(ctx, id)
	return contact, err
}

// RemoveContact forgets a way to reach the client.
func (s *Service) RemoveContact(ctx context.Context, id, contactID int64) error {
	result, err := s.opts.DB.ExecContext(ctx, `DELETE FROM client_contacts WHERE id = ? AND client_id = ?`, contactID, id)
	if err != nil {
		return err
	}
	if removed, _ := result.RowsAffected(); removed == 0 {
		return ErrNotFound
	}
	s.touch(ctx, id)
	return nil
}

func (s *Service) touch(ctx context.Context, id int64) {
	_, _ = s.opts.DB.ExecContext(ctx, `UPDATE clients SET updated_at = ? WHERE id = ?`, s.now(), id)
}

// Profile is what the client edits about themselves.
type Profile struct {
	Name      string
	Company   string
	Lang      string
	Preferred string
}

var languages = map[string]bool{"en": true, "uk": true, "ru": true}

// preferable are the ways a client may prefer to be reached.
func preferable(kind string) bool {
	if kind == "email" {
		return true
	}
	for _, known := range Kinds {
		if known == kind {
			return true
		}
	}
	return false
}

// SetProfile changes the name, the company, the language of letters and the preferred way.
func (s *Service) SetProfile(ctx context.Context, id int64, profile Profile) error {
	profile.Name = cut(leads.Clean(profile.Name, false), 100)
	profile.Company = cut(leads.Clean(profile.Company, false), 120)
	if !languages[profile.Lang] {
		profile.Lang = "en"
	}
	preferred := sql.NullString{String: profile.Preferred, Valid: preferable(profile.Preferred)}
	_, err := s.opts.DB.ExecContext(ctx, `UPDATE clients SET name = ?, company = ?, lang = ?, preferred = ?, updated_at = ? WHERE id = ?`,
		profile.Name, profile.Company, profile.Lang, preferred, s.now(), id)
	return err
}

// cut shortens a text to at most limit runes.
func cut(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
