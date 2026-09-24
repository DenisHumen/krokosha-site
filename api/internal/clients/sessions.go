package clients

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// Session is a signed-in browser of a client.
type Session struct {
	Client    *Client
	CSRFToken string
	tokenHash [32]byte
}

// Handle is the short name of the session, as the account's list shows it.
func (s *Session) Handle() string { return hex.EncodeToString(s.tokenHash[:6]) }

// openSession signs a client in: the token goes to the cookie once, only its hash stays here.
func (s *Service) openSession(ctx context.Context, clientID int64, network, device string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	csrf := make([]byte, 32)
	if _, err := rand.Read(csrf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	now := s.now()
	if _, err := s.opts.DB.ExecContext(ctx, `
		INSERT INTO client_sessions (token_hash, client_id, csrf_token, created_at, last_seen_at, expires_at, ip_prefix, device)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		hash[:], clientID, base64.RawURLEncoding.EncodeToString(csrf), now, now, now.Add(SessionLifetime), network, cut(device, 60)); err != nil {
		return "", err
	}
	_, _ = s.opts.DB.ExecContext(ctx, `UPDATE clients SET last_seen_at = ? WHERE id = ?`, now, clientID)
	// Housekeeping rides along with sign-ins: they are rare, and so are expired sessions.
	_, _ = s.opts.DB.ExecContext(ctx, `DELETE FROM client_sessions WHERE expires_at < ? OR last_seen_at < ?`, now, now.Add(-SessionIdle))
	return token, nil
}

// Authenticate resolves the cookie of a signed-in client.
func (s *Service) Authenticate(ctx context.Context, token string) (*Session, error) {
	if len(token) != 43 { // 32 bytes, base64url without padding
		return nil, ErrNoSession
	}
	hash := sha256.Sum256([]byte(token))
	now := s.now()
	session := &Session{tokenHash: hash}
	var clientID int64
	var lastSeen, expires time.Time
	err := s.opts.DB.QueryRowContext(ctx, `SELECT client_id, csrf_token, last_seen_at, expires_at FROM client_sessions WHERE token_hash = ?`, hash[:]).
		Scan(&clientID, &session.CSRFToken, &lastSeen, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	if now.After(expires) || now.Sub(lastSeen) > SessionIdle {
		_, _ = s.opts.DB.ExecContext(ctx, `DELETE FROM client_sessions WHERE token_hash = ?`, hash[:])
		return nil, ErrNoSession
	}
	client, err := s.Get(ctx, clientID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	if client.DisabledAt.Valid {
		_, _ = s.opts.DB.ExecContext(ctx, `DELETE FROM client_sessions WHERE client_id = ?`, clientID)
		return nil, ErrNoSession
	}
	session.Client = client
	// One write an hour keeps the idle clock honest.
	if now.Sub(lastSeen) > time.Hour {
		_, _ = s.opts.DB.ExecContext(ctx, `UPDATE client_sessions SET last_seen_at = ? WHERE token_hash = ?`, now, hash[:])
		_, _ = s.opts.DB.ExecContext(ctx, `UPDATE clients SET last_seen_at = ? WHERE id = ?`, now, clientID)
	}
	return session, nil
}

// Logout ends one session.
func (s *Service) Logout(ctx context.Context, session *Session) error {
	_, err := s.opts.DB.ExecContext(ctx, `DELETE FROM client_sessions WHERE token_hash = ?`, session.tokenHash[:])
	return err
}

// EndSessions signs a client out everywhere, but the session given (nil — everywhere).
func (s *Service) EndSessions(ctx context.Context, clientID int64, keep *Session) (int, error) {
	query, args := `DELETE FROM client_sessions WHERE client_id = ?`, []any{clientID}
	if keep != nil {
		query += ` AND token_hash <> ?`
		args = append(args, keep.tokenHash[:])
	}
	result, err := s.opts.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	ended, _ := result.RowsAffected()
	return int(ended), nil
}

// EndSession ends the session with the given handle (SessionInfo.ID) of a client.
func (s *Service) EndSession(ctx context.Context, clientID int64, handle string) error {
	raw, err := hex.DecodeString(handle)
	if err != nil || len(raw) != 6 {
		return ErrNotFound
	}
	result, err := s.opts.DB.ExecContext(ctx, `DELETE FROM client_sessions WHERE client_id = ? AND LEFT(token_hash, 6) = ?`, clientID, raw)
	if err != nil {
		return err
	}
	if ended, _ := result.RowsAffected(); ended == 0 {
		return ErrNotFound
	}
	return nil
}

// Sessions lists the signed-in browsers of a client, the current one marked.
func (s *Service) Sessions(ctx context.Context, clientID int64, current *Session) ([]SessionInfo, error) {
	rows, err := s.opts.DB.QueryContext(ctx, `
		SELECT token_hash, created_at, last_seen_at, device, ip_prefix FROM client_sessions
		WHERE client_id = ? AND expires_at > ? ORDER BY last_seen_at DESC LIMIT 50`, clientID, s.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionInfo
	for rows.Next() {
		var hash []byte
		var info SessionInfo
		if err := rows.Scan(&hash, &info.CreatedAt, &info.LastSeen, &info.Device, &info.Network); err != nil {
			return nil, err
		}
		info.ID = hex.EncodeToString(hash[:6])
		info.Current = current != nil && string(hash) == string(current.tokenHash[:])
		out = append(out, info)
	}
	return out, rows.Err()
}
