package telegram

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Who may use the bot (brief B10.3). The bot is public — anybody can find it and write to it —
// so access is by invitation only: a one-time code that lives for a day. A person is bound by
// the numeric Telegram id; a user name can be changed and given away, an id cannot.

// Roles.
const (
	RoleOwner  = "owner"  // manages access, sees everything
	RoleMember = "member" // gets requests and works with them
	// RoleNotify gets the cards of requests and the reminders, and can read them, but nothing can
	// be done from the bot: no answers, no taking, no statuses (docs/telegram.md).
	RoleNotify = "notify"
)

// ValidRole tells a role from a typo.
func ValidRole(role string) bool {
	return role == RoleOwner || role == RoleMember || role == RoleNotify
}

// InviteLifetime is how long an invitation can be used.
const InviteLifetime = 24 * time.Hour

// InvitePrefix starts every invitation code, so that «/start <something>» can be told from a
// client's link (ClientPrefix) at a glance.
const InvitePrefix = "i_"

// Errors of the access rules.
var (
	ErrNoAccess  = errors.New("this Telegram account has no access to the bot")
	ErrBadInvite = errors.New("the invitation is wrong, used or expired")
	ErrBadRole   = errors.New("the role must be owner, member or notify")
	// ErrBadID: a Telegram id is a positive number (a person's, not a group's).
	ErrBadID = errors.New("a Telegram id is a positive number")
)

// Member is a person with access.
type Member struct {
	ID         int64
	TelegramID int64
	Role       string
	Name       string
	Username   string
	CreatedAt  time.Time
	InvitedBy  string
	DisabledAt sql.NullTime
	MutedUntil sql.NullTime
	// LastSeenAt is when the person last wrote to the bot or pressed one of its buttons.
	LastSeenAt sql.NullTime
}

// Owner reports whether the person manages access.
func (m Member) Owner() bool { return m.Role == RoleOwner }

// CanAct reports whether the person may do something with a request, not only read about it.
func (m Member) CanAct() bool { return m.Role == RoleOwner || m.Role == RoleMember }

// Actor is how the person is written into the history of requests and the audit log.
func (m Member) Actor() string { return m.Name }

// Invite is an invitation that was made.
type Invite struct {
	ID        int64
	Role      string
	CreatedAt time.Time
	CreatedBy string
	ExpiresAt time.Time
	UsedAt    sql.NullTime
}

// Access keeps members and invitations.
type Access struct {
	db  *sql.DB
	now func() time.Time
}

// NewAccess builds the store.
func NewAccess(db *sql.DB, now func() time.Time) *Access {
	if now == nil {
		now = time.Now
	}
	return &Access{db: db, now: now}
}

func hashCode(code string) []byte {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(code))))
	return sum[:]
}

// Invite makes a one-time invitation and returns its code — the only time the code exists in
// readable form: the database keeps a hash.
func (a *Access) Invite(ctx context.Context, role, createdBy string) (code string, expires time.Time, err error) {
	if !ValidRole(role) {
		return "", time.Time{}, ErrBadRole
	}
	random := make([]byte, 12) // 96 bits: nothing to guess
	if _, err := rand.Read(random); err != nil {
		return "", time.Time{}, err
	}
	code = InvitePrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random))
	now := a.now().UTC()
	expires = now.Add(InviteLifetime)
	_, err = a.db.ExecContext(ctx, `INSERT INTO bot_invites (code_hash, role, created_at, created_by, expires_at) VALUES (?, ?, ?, ?, ?)`,
		hashCode(code), role, now, createdBy, expires)
	return code, expires, err
}

// Redeem lets a person in by an invitation. One code — one person: of two people racing with
// the same code only one finds it unused. Somebody who already has access gets the role of the
// new invitation; somebody whose access was revoked gets it back — the owner sent them a code.
func (a *Access) Redeem(ctx context.Context, code string, who User) (*Member, error) {
	if who.ID == 0 || who.IsBot || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(code)), InvitePrefix) {
		return nil, ErrBadInvite
	}
	now := a.now().UTC()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	hash := hashCode(code)
	// The WHERE clause is the lock, as with taking a request.
	result, err := tx.ExecContext(ctx, `UPDATE bot_invites SET used_at = ?, used_by = ? WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?`,
		now, who.ID, hash, now)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return nil, ErrBadInvite
	}
	var role, createdBy string
	if err := tx.QueryRowContext(ctx, `SELECT role, created_by FROM bot_invites WHERE code_hash = ?`, hash).Scan(&role, &createdBy); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO bot_users (telegram_id, role, name, username, created_at, invited_by) VALUES (?, ?, ?, NULLIF(?, ''), ?, ?)
		ON DUPLICATE KEY UPDATE role = VALUES(role), name = VALUES(name), username = VALUES(username), invited_by = VALUES(invited_by),
		                        disabled_at = NULL, state = NULL`,
		who.ID, role, who.DisplayName(), who.Username, now, createdBy); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return a.Member(ctx, who.ID)
}

const memberColumns = `id, telegram_id, role, name, COALESCE(username, ''), created_at, invited_by, disabled_at, muted_until, last_seen_at`

func scanMember(row interface{ Scan(...any) error }) (Member, error) {
	var m Member
	err := row.Scan(&m.ID, &m.TelegramID, &m.Role, &m.Name, &m.Username, &m.CreatedAt, &m.InvitedBy, &m.DisabledAt, &m.MutedUntil, &m.LastSeenAt)
	return m, err
}

// Member returns the person behind a Telegram id — if they have access right now. This is asked
// for every single update: revoking access works at once.
func (a *Access) Member(ctx context.Context, telegramID int64) (*Member, error) {
	member, err := scanMember(a.db.QueryRowContext(ctx, `SELECT `+memberColumns+` FROM bot_users WHERE telegram_id = ? AND disabled_at IS NULL`, telegramID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoAccess
	}
	if err != nil {
		return nil, err
	}
	return &member, nil
}

// Members lists everybody who ever got in, those whose access was revoked included.
func (a *Access) Members(ctx context.Context) ([]Member, error) {
	return a.members(ctx, `SELECT `+memberColumns+` FROM bot_users ORDER BY disabled_at IS NOT NULL, role DESC, id`)
}

// Recipients lists the people requests are sent to: everybody with access.
func (a *Access) Recipients(ctx context.Context) ([]Member, error) {
	return a.members(ctx, `SELECT `+memberColumns+` FROM bot_users WHERE disabled_at IS NULL ORDER BY id`)
}

func (a *Access) members(ctx context.Context, query string) ([]Member, error) {
	rows, err := a.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		member, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	return out, rows.Err()
}

// SetDisabled revokes access or gives it back. The last owner cannot be switched off: somebody
// has to be able to let people in.
func (a *Access) SetDisabled(ctx context.Context, id int64, disabled bool) (*Member, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	member, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberColumns+` FROM bot_users WHERE id = ? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoAccess
	}
	if err != nil {
		return nil, err
	}
	if disabled && member.Owner() && !member.DisabledAt.Valid {
		var owners int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bot_users WHERE role = 'owner' AND disabled_at IS NULL`).Scan(&owners); err != nil {
			return nil, err
		}
		if owners <= 1 {
			return nil, ErrLastOwner
		}
	}
	when := sql.NullTime{Time: a.now().UTC(), Valid: disabled}
	if _, err := tx.ExecContext(ctx, `UPDATE bot_users SET disabled_at = ?, state = NULL WHERE id = ?`, when, id); err != nil {
		return nil, err
	}
	member.DisabledAt = when
	return &member, tx.Commit()
}

// ErrLastOwner: the only owner cannot be switched off.
var ErrLastOwner = errors.New("the last owner cannot be switched off")

// Add lets a person in by their numeric Telegram id, without an invitation — for the owner who
// knows the id of a colleague (the colleague sees it in @userinfobot and the like). The name is how
// cards call them until they write to the bot. Somebody who is there already gets the role;
// somebody whose access was revoked gets it back.
func (a *Access) Add(ctx context.Context, telegramID int64, role, name, addedBy string) (*Member, error) {
	if !ValidRole(role) {
		return nil, ErrBadRole
	}
	if telegramID <= 0 {
		return nil, ErrBadID
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "id " + strconv.FormatInt(telegramID, 10)
	}
	if runes := []rune(name); len(runes) > 64 {
		name = string(runes[:64])
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if role != RoleOwner {
		// Making the last owner a member would leave nobody to let people in.
		if err := keepAnOwner(ctx, tx, telegramID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO bot_users (telegram_id, role, name, created_at, invited_by) VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE role = VALUES(role), disabled_at = NULL, state = NULL`,
		telegramID, role, name, a.now().UTC(), addedBy); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return a.Member(ctx, telegramID)
}

// SetRole changes what a person may do; the last owner stays one.
func (a *Access) SetRole(ctx context.Context, id int64, role string) (*Member, error) {
	if !ValidRole(role) {
		return nil, ErrBadRole
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	member, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberColumns+` FROM bot_users WHERE id = ? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoAccess
	}
	if err != nil {
		return nil, err
	}
	if role != RoleOwner {
		if err := keepAnOwner(ctx, tx, member.TelegramID); err != nil {
			return nil, err
		}
	}
	// A dialog the person had begun (an answer, a note) is dropped: they may not finish it now.
	if _, err := tx.ExecContext(ctx, `UPDATE bot_users SET role = ?, state = NULL WHERE id = ?`, role, id); err != nil {
		return nil, err
	}
	member.Role = role
	return &member, tx.Commit()
}

// keepAnOwner refuses to take the role of the owner from the last one with access.
func keepAnOwner(ctx context.Context, tx *sql.Tx, telegramID int64) error {
	var isOwner bool
	err := tx.QueryRowContext(ctx, `SELECT role = 'owner' AND disabled_at IS NULL FROM bot_users WHERE telegram_id = ? FOR UPDATE`, telegramID).Scan(&isOwner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !isOwner) {
		return nil
	}
	if err != nil {
		return err
	}
	var others int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bot_users WHERE role = 'owner' AND disabled_at IS NULL AND telegram_id <> ?`,
		telegramID).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return ErrLastOwner
	}
	return nil
}

// Seen notes that a person used the bot just now — at most once a minute —, and keeps their name
// in Telegram: somebody added by id has none until they write.
func (a *Access) Seen(ctx context.Context, telegramID int64, username string) error {
	now := a.now().UTC()
	_, err := a.db.ExecContext(ctx, `
		UPDATE bot_users SET last_seen_at = ?, username = COALESCE(NULLIF(?, ''), username)
		WHERE telegram_id = ? AND (last_seen_at IS NULL OR last_seen_at < ? OR (? <> '' AND NOT (username <=> ?)))`,
		now, username, telegramID, now.Add(-time.Minute), username, username)
	return err
}

// Invites lists invitations that can still be used.
func (a *Access) Invites(ctx context.Context) ([]Invite, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, role, created_at, created_by, expires_at, used_at FROM bot_invites
		WHERE used_at IS NULL AND expires_at > ? ORDER BY id DESC`, a.now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var invite Invite
		if err := rows.Scan(&invite.ID, &invite.Role, &invite.CreatedAt, &invite.CreatedBy, &invite.ExpiresAt, &invite.UsedAt); err != nil {
			return nil, err
		}
		out = append(out, invite)
	}
	return out, rows.Err()
}

// RevokeInvite makes an unused invitation worthless.
func (a *Access) RevokeInvite(ctx context.Context, id int64) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM bot_invites WHERE id = ? AND used_at IS NULL`, id)
	return err
}

// Forget removes invitations nobody can use any more: used or expired more than a month ago.
func (a *Access) Forget(ctx context.Context) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM bot_invites WHERE expires_at < ?`, a.now().UTC().AddDate(0, -1, 0))
	return err
}

// Mute stops sounds and pushes for a person until the given moment (the zero time — unmute).
func (a *Access) Mute(ctx context.Context, telegramID int64, until time.Time) error {
	value := sql.NullTime{Time: until.UTC(), Valid: !until.IsZero()}
	_, err := a.db.ExecContext(ctx, `UPDATE bot_users SET muted_until = ? WHERE telegram_id = ?`, value, telegramID)
	return err
}
