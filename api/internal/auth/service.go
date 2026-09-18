package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
)

// Session limits: two hours without activity, a day at most (brief B6: «срок ограничен»).
const (
	SessionIdle     = 2 * time.Hour
	SessionLifetime = 24 * time.Hour

	// Five wrong attempts per quarter of an hour, counted per login and per network.
	loginAttempts = 5
	loginWindow   = 15 * time.Minute
)

// Errors of the login flow. The form shows the same message for the first two.
var (
	ErrBadCredentials = errors.New("wrong login or password")
	ErrBadCode        = errors.New("wrong one-time code")
	ErrCodeRequired   = errors.New("one-time code required")
	ErrThrottled      = errors.New("too many attempts, try again later")
	ErrNoSession      = errors.New("not signed in")
)

var reLogin = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

// User is an administrator account.
type User struct {
	ID          int64
	Login       string
	TOTPEnabled bool
	Disabled    bool
	CreatedAt   time.Time
	LastLoginAt sql.NullTime
}

// Session is a signed-in browser.
type Session struct {
	User      User
	CSRFToken string
	CreatedAt time.Time
	ExpiresAt time.Time
	tokenHash [32]byte
}

// Service implements accounts and sessions on top of MySQL.
type Service struct {
	db    *sql.DB
	cache *cache.Cache
	log   *slog.Logger
	now   func() time.Time
}

// New builds the service.
func New(db *sql.DB, store *cache.Cache, log *slog.Logger) *Service {
	return &Service{db: db, cache: store, log: log, now: time.Now}
}

// SetClock replaces the time source. For tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// NormalizeLogin lower-cases and validates a login name.
func NormalizeLogin(login string) (string, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	if !reLogin.MatchString(login) {
		return "", errors.New("a login is 3–64 characters: latin letters, digits, dot, dash, underscore")
	}
	return login, nil
}

// CreateUser adds an administrator. Used by the CLI and the installer.
func (s *Service) CreateUser(ctx context.Context, login, password string) error {
	login, err := NormalizeLogin(login)
	if err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO admin_users (login, password_hash, created_at) VALUES (?, ?, ?)`, login, hash, s.now().UTC())
	if err != nil && strings.Contains(err.Error(), "Duplicate entry") {
		return errors.New("this login already exists")
	}
	if err == nil {
		s.Audit(ctx, "system", "admin.create", login, "", "")
	}
	return err
}

// SetPassword replaces the password and signs the user out everywhere.
func (s *Service) SetPassword(ctx context.Context, login, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE admin_users SET password_hash = ? WHERE login = ?`, hash, login)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return errors.New("no such login")
	}
	_, err = s.db.ExecContext(ctx,
		`DELETE s FROM admin_sessions s JOIN admin_users u ON u.id = s.user_id WHERE u.login = ?`, login)
	s.Audit(ctx, login, "admin.password", login, "", "")
	return err
}

// SetDisabled blocks or unblocks an account; blocking ends its sessions at once.
func (s *Service) SetDisabled(ctx context.Context, login string, disabled bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE admin_users SET disabled = ? WHERE login = ?`, disabled, login)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return errors.New("no such login")
	}
	if disabled {
		_, err = s.db.ExecContext(ctx,
			`DELETE s FROM admin_sessions s JOIN admin_users u ON u.id = s.user_id WHERE u.login = ?`, login)
	}
	return err
}

// ResetTOTP removes two-factor authentication from an account (lost phone). CLI only.
func (s *Service) ResetTOTP(ctx context.Context, login string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE admin_users SET totp_secret = NULL, totp_enabled = 0, totp_last_step = 0 WHERE login = ?`, login)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return errors.New("no such login")
	}
	s.Audit(ctx, "system", "admin.totp-reset", login, "", "")
	return nil
}

// Users lists the accounts.
func (s *Service) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, login, totp_enabled, disabled, created_at, last_login_at FROM admin_users ORDER BY login`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Login, &u.TOTPEnabled, &u.Disabled, &u.CreatedAt, &u.LastLoginAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// Attempt describes who is trying to sign in.
type Attempt struct {
	Login    string
	Password string
	Code     string // one-time code, empty on the first step
	IPPrefix string // truncated address: for throttling and the session list
	Client   string // «Chrome · Windows»
}

// LoginResult is the outcome of a login step.
type LoginResult struct {
	// Token is the cookie value of the new session: shown to the browser once, never stored.
	Token   string
	Session *Session
	// Pending is set together with ErrCodeRequired: a ticket that lets the second step finish
	// the login with the one-time code alone, so the password never travels back to the browser.
	Pending string
}

const (
	pendingLifetime = 5 * time.Minute
	pendingTries    = 5
)

// Login checks login and password. With two-factor authentication on and no code given it
// returns ErrCodeRequired and a ticket for CompleteLogin.
func (s *Service) Login(ctx context.Context, a Attempt) (LoginResult, error) {
	login := strings.ToLower(strings.TrimSpace(a.Login))

	// Throttle before any work: per account, and per network so that one host cannot lock
	// everybody out by cycling logins.
	if !s.cache.Allow(ctx, "login:u:"+login, loginAttempts*2, loginWindow) ||
		!s.cache.Allow(ctx, "login:ip:"+a.IPPrefix, loginAttempts*4, loginWindow) {
		return LoginResult{}, ErrThrottled
	}

	user, hash, err := s.userByLogin(ctx, login)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		VerifyPassword(dummy(), a.Password) // same cost as a real check
		return LoginResult{}, s.failed(ctx, login, a, "unknown login")
	case err != nil:
		return LoginResult{}, err
	}
	if !VerifyPassword(hash, a.Password) || user.Disabled {
		return LoginResult{}, s.failed(ctx, login, a, "wrong password")
	}

	if user.TOTPEnabled {
		if strings.TrimSpace(a.Code) == "" {
			ticket := make([]byte, 32)
			if _, err := rand.Read(ticket); err != nil {
				return LoginResult{}, err
			}
			pending := base64.RawURLEncoding.EncodeToString(ticket)
			s.cache.Set(ctx, "2fa:"+pending, user.Login, pendingLifetime)
			return LoginResult{Pending: pending}, ErrCodeRequired
		}
		if err := s.checkCode(ctx, user, a); err != nil {
			return LoginResult{}, err
		}
	}
	return s.signIn(ctx, user, a)
}

// CompleteLogin finishes a login that stopped at ErrCodeRequired.
func (s *Service) CompleteLogin(ctx context.Context, pending, code string, a Attempt) (LoginResult, error) {
	login, ok := s.cache.Get(ctx, "2fa:"+pending)
	if !ok || len(pending) != 43 {
		return LoginResult{}, ErrBadCredentials // expired or made up: start over
	}
	if !s.cache.Allow(ctx, "2fa-try:"+pending, pendingTries, pendingLifetime) {
		s.cache.Del(ctx, "2fa:"+pending)
		return LoginResult{}, ErrThrottled
	}
	user, _, err := s.userByLogin(ctx, login)
	if err != nil || user.Disabled || !user.TOTPEnabled {
		return LoginResult{}, ErrBadCredentials
	}
	a.Login, a.Code = login, code
	if err := s.checkCode(ctx, user, a); err != nil {
		return LoginResult{Pending: pending}, err
	}
	s.cache.Del(ctx, "2fa:"+pending)
	return s.signIn(ctx, user, a)
}

type userRecord struct {
	User
	totpSecret   []byte
	totpLastStep int64
}

func (s *Service) userByLogin(ctx context.Context, login string) (userRecord, string, error) {
	var user userRecord
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, login, password_hash, totp_secret, totp_enabled, totp_last_step, disabled FROM admin_users WHERE login = ?`, login).
		Scan(&user.ID, &user.Login, &hash, &user.totpSecret, &user.TOTPEnabled, &user.totpLastStep, &user.Disabled)
	return user, hash, err
}

func (s *Service) checkCode(ctx context.Context, user userRecord, a Attempt) error {
	step := VerifyTOTP(user.totpSecret, a.Code, s.now(), user.totpLastStep)
	if step == 0 {
		_ = s.failed(ctx, user.Login, a, "wrong one-time code")
		return ErrBadCode
	}
	// The WHERE clause makes the step single-use even for two requests racing with the same code.
	result, err := s.db.ExecContext(ctx, `UPDATE admin_users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, user.ID, step)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrBadCode
	}
	return nil
}

func (s *Service) signIn(ctx context.Context, user userRecord, a Attempt) (LoginResult, error) {
	token, session, err := s.openSession(ctx, user.User, a)
	if err != nil {
		return LoginResult{}, err
	}
	s.Audit(ctx, user.Login, "admin.login", "", a.Client, a.IPPrefix)
	return LoginResult{Token: token, Session: session}, nil
}

// failed records a rejected attempt: in the audit log for the admin area, in the journal for
// whoever reads it on the server. fail2ban counts the 401 answers in nginx's log instead —
// that log has the full address, which the API deliberately never stores.
func (s *Service) failed(ctx context.Context, login string, a Attempt, reason string) error {
	s.log.Warn("admin login failed", "reason", reason, "network", a.IPPrefix)
	s.Audit(ctx, login, "admin.login-failed", "", reason, a.IPPrefix)
	return ErrBadCredentials
}

func (s *Service) openSession(ctx context.Context, user User, a Attempt) (string, *Session, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	csrf := make([]byte, 32)
	if _, err := rand.Read(csrf); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now().UTC()
	session := &Session{
		User:      user,
		CSRFToken: base64.RawURLEncoding.EncodeToString(csrf),
		CreatedAt: now,
		ExpiresAt: now.Add(SessionLifetime),
		tokenHash: sha256.Sum256([]byte(token)),
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO admin_sessions (token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, ip_prefix, client)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		session.tokenHash[:], user.ID, session.CSRFToken, now, now, session.ExpiresAt, a.IPPrefix, a.Client); err != nil {
		return "", nil, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE admin_users SET last_login_at = ? WHERE id = ?`, now, user.ID); err != nil {
		return "", nil, err
	}
	// Housekeeping rides along with logins: they are rare, and so are expired sessions.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at < ? OR last_seen_at < ?`, now, now.Add(-SessionIdle))
	return token, session, nil
}

// Authenticate resolves a cookie token to a live session and marks it as seen.
func (s *Service) Authenticate(ctx context.Context, token string) (*Session, error) {
	if len(token) != 43 { // 32 bytes, base64url without padding
		return nil, ErrNoSession
	}
	hash := sha256.Sum256([]byte(token))
	now := s.now().UTC()

	session := &Session{tokenHash: hash}
	var lastSeen time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT u.id, u.login, u.totp_enabled, u.disabled, s.csrf_token, s.created_at, s.expires_at, s.last_seen_at
		   FROM admin_sessions s JOIN admin_users u ON u.id = s.user_id WHERE s.token_hash = ?`, hash[:]).
		Scan(&session.User.ID, &session.User.Login, &session.User.TOTPEnabled, &session.User.Disabled,
			&session.CSRFToken, &session.CreatedAt, &session.ExpiresAt, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	if session.User.Disabled || now.After(session.ExpiresAt) || now.Sub(lastSeen) > SessionIdle {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash = ?`, hash[:])
		return nil, ErrNoSession
	}
	// One write a minute is enough to keep the idle timer honest.
	if now.Sub(lastSeen) > time.Minute {
		_, _ = s.db.ExecContext(ctx, `UPDATE admin_sessions SET last_seen_at = ? WHERE token_hash = ?`, now, hash[:])
	}
	return session, nil
}

// Logout ends the session.
func (s *Service) Logout(ctx context.Context, session *Session) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash = ?`, session.tokenHash[:])
	s.Audit(ctx, session.User.Login, "admin.logout", "", "", "")
	return err
}

// Audit appends to the audit trail. It never fails the action it describes.
func (s *Service) Audit(ctx context.Context, actor, action, subject, details, ipPrefix string) {
	_, err := s.db.ExecContext(context.WithoutCancel(ctx),
		`INSERT INTO audit_log (occurred_at, actor, action, subject, details, ip_prefix) VALUES (?, ?, ?, ?, ?, ?)`,
		s.now().UTC(), actor, action, nullIfEmpty(subject), nullIfEmpty(details), nullIfEmpty(ipPrefix))
	if err != nil {
		s.log.Error("cannot write the audit log", "action", action, "error", err)
	}
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
