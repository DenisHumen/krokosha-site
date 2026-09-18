package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"time"
)

// What a signed-in administrator can do with their own account.

// ChangePassword verifies the current password, sets the new one and ends every other session.
func (s *Service) ChangePassword(ctx context.Context, session *Session, current, next string) error {
	var hash string
	if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM admin_users WHERE id = ?`, session.User.ID).Scan(&hash); err != nil {
		return err
	}
	if !VerifyPassword(hash, current) {
		return ErrBadCredentials
	}
	nextHash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE admin_users SET password_hash = ? WHERE id = ?`, nextHash, session.User.ID); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE user_id = ? AND token_hash <> ?`, session.User.ID, session.tokenHash[:])
	s.Audit(ctx, session.User.Login, "admin.password", session.User.Login, "", "")
	return err
}

// BeginTOTP stores a fresh secret for enrolment and returns it. Two-factor authentication is
// not active until ConfirmTOTP has seen a valid code — a secret that was never scanned locks nobody out.
func (s *Service) BeginTOTP(ctx context.Context, session *Session) ([]byte, error) {
	if session.User.TOTPEnabled {
		return nil, errors.New("two-factor authentication is already on")
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE admin_users SET totp_secret = ?, totp_last_step = 0 WHERE id = ? AND totp_enabled = 0`, secret, session.User.ID)
	return secret, err
}

// PendingTOTP returns the secret of an enrolment that was started but not confirmed yet.
func (s *Service) PendingTOTP(ctx context.Context, session *Session) ([]byte, error) {
	var secret []byte
	err := s.db.QueryRowContext(ctx, `SELECT totp_secret FROM admin_users WHERE id = ? AND totp_enabled = 0`, session.User.ID).Scan(&secret)
	return secret, err
}

// ConfirmTOTP turns two-factor authentication on once the app shows the right code.
func (s *Service) ConfirmTOTP(ctx context.Context, session *Session, code string) error {
	secret, err := s.PendingTOTP(ctx, session)
	if err != nil || len(secret) == 0 {
		return errors.New("start the enrolment first")
	}
	step := VerifyTOTP(secret, code, s.now(), 0)
	if step == 0 {
		return ErrBadCode
	}
	_, err = s.db.ExecContext(ctx, `UPDATE admin_users SET totp_enabled = 1, totp_last_step = ? WHERE id = ?`, step, session.User.ID)
	s.Audit(ctx, session.User.Login, "admin.totp-on", session.User.Login, "", "")
	return err
}

// DisableTOTP turns two-factor authentication off; the password is asked again for that.
func (s *Service) DisableTOTP(ctx context.Context, session *Session, password string) error {
	var hash string
	if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM admin_users WHERE id = ?`, session.User.ID).Scan(&hash); err != nil {
		return err
	}
	if !VerifyPassword(hash, password) {
		return ErrBadCredentials
	}
	_, err := s.db.ExecContext(ctx, `UPDATE admin_users SET totp_secret = NULL, totp_enabled = 0, totp_last_step = 0 WHERE id = ?`, session.User.ID)
	s.Audit(ctx, session.User.Login, "admin.totp-off", session.User.Login, "", "")
	return err
}

// SessionInfo is a row of «where am I signed in».
type SessionInfo struct {
	ID        string // first bytes of the token hash: enough to address a session, useless to hijack it
	Current   bool
	CreatedAt time.Time
	LastSeen  time.Time
	IPPrefix  string
	Client    string
}

// Sessions lists the live sessions of the signed-in user.
func (s *Service) Sessions(ctx context.Context, session *Session) ([]SessionInfo, error) {
	now := s.now().UTC()
	rows, err := s.db.QueryContext(ctx,
		`SELECT token_hash, created_at, last_seen_at, ip_prefix, client FROM admin_sessions
		  WHERE user_id = ? AND expires_at > ? AND last_seen_at > ? ORDER BY last_seen_at DESC`,
		session.User.ID, now, now.Add(-SessionIdle))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []SessionInfo
	for rows.Next() {
		var hash []byte
		var info SessionInfo
		if err := rows.Scan(&hash, &info.CreatedAt, &info.LastSeen, &info.IPPrefix, &info.Client); err != nil {
			return nil, err
		}
		info.ID = hex.EncodeToString(hash[:8])
		info.Current = string(hash) == string(session.tokenHash[:])
		list = append(list, info)
	}
	return list, rows.Err()
}

// Revoke ends another session of the same user.
func (s *Service) Revoke(ctx context.Context, session *Session, id string) error {
	prefix, err := hex.DecodeString(id)
	if err != nil || len(prefix) != 8 {
		return errors.New("unknown session")
	}
	_, err = s.db.ExecContext(ctx,
		`DELETE FROM admin_sessions WHERE user_id = ? AND LEFT(token_hash, 8) = ?`, session.User.ID, prefix)
	s.Audit(ctx, session.User.Login, "admin.session-revoke", id, "", "")
	return err
}
