package analytics

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"net"
	"sync"
	"time"
)

// salts hands out the secret of the day. A visitor id is a hash of that secret, the address and
// the browser; once the secret is deleted (two days later) the ids can never be reproduced, so
// visits of different days cannot be linked — not even by someone holding the whole database.
type salts struct {
	db *sql.DB

	mu    sync.Mutex
	known map[string][32]byte
}

func newSalts(db *sql.DB) *salts {
	return &salts{db: db, known: map[string][32]byte{}}
}

func (s *salts) forDay(ctx context.Context, now time.Time) ([32]byte, error) {
	day := now.UTC().Format(time.DateOnly)

	s.mu.Lock()
	defer s.mu.Unlock()
	if salt, ok := s.known[day]; ok {
		return salt, nil
	}

	var fresh [32]byte
	if _, err := rand.Read(fresh[:]); err != nil {
		return fresh, err
	}
	// INSERT IGNORE + SELECT: whichever instance gets there first decides; everyone reads the same.
	if _, err := s.db.ExecContext(ctx, `INSERT IGNORE INTO analytics_salts (day, salt) VALUES (?, ?)`, day, fresh[:]); err != nil {
		return fresh, err
	}
	var stored []byte
	if err := s.db.QueryRowContext(ctx, `SELECT salt FROM analytics_salts WHERE day = ?`, day).Scan(&stored); err != nil {
		return fresh, err
	}
	var salt [32]byte
	copy(salt[:], stored)

	// Yesterday's salt stays for sessions that cross midnight; everything older goes.
	cutoff := now.UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM analytics_salts WHERE day < ?`, cutoff); err != nil {
		return salt, err
	}
	for known := range s.known {
		if known < cutoff {
			delete(s.known, known)
		}
	}
	s.known[day] = salt
	return salt, nil
}

// visitorID is the daily pseudonym of a visitor: 16 bytes of sha256(salt, ip, user agent).
func visitorID(salt [32]byte, ip net.IP, userAgent string) [16]byte {
	hash := sha256.New()
	hash.Write(salt[:])
	hash.Write([]byte(ip.String()))
	hash.Write([]byte{0})
	hash.Write([]byte(userAgent))
	var id [16]byte
	copy(id[:], hash.Sum(nil))
	return id
}

// addressKey is a salted hash of the address for short-lived rate-limit counters, so that not even
// Redis ever holds a visitor's IP.
func addressKey(salt [32]byte, ip net.IP) [8]byte {
	hash := sha256.New()
	hash.Write([]byte("rate"))
	hash.Write(salt[:])
	hash.Write([]byte(ip.String()))
	var key [8]byte
	copy(key[:], hash.Sum(nil))
	return key
}
