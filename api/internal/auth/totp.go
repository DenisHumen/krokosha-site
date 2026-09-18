package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 authenticator apps use HMAC-SHA1; it is not used as a collision-resistant hash here
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Time-based one-time passwords (RFC 6238) with the parameters every authenticator app expects:
// HMAC-SHA1, 6 digits, 30-second steps.

const (
	totpDigits = 6
	totpPeriod = 30
	totpSkew   = 1 // accept the previous and the next step: clocks of phones drift
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns 20 random bytes — the size RFC 4226 recommends for HMAC-SHA1.
func NewTOTPSecret() ([]byte, error) {
	secret := make([]byte, 20)
	_, err := rand.Read(secret)
	return secret, err
}

// TOTPSecretText is the secret as an authenticator app wants it typed: base32 in groups of four.
func TOTPSecretText(secret []byte) string {
	encoded := base32NoPad.EncodeToString(secret)
	var groups []string
	for len(encoded) > 4 {
		groups, encoded = append(groups, encoded[:4]), encoded[4:]
	}
	return strings.Join(append(groups, encoded), " ")
}

// TOTPURI is the otpauth:// link encoded in the enrolment QR code.
func TOTPURI(secret []byte, issuer, account string) string {
	query := url.Values{
		"secret":    {base32NoPad.EncodeToString(secret)},
		"issuer":    {issuer},
		"algorithm": {"SHA1"},
		"digits":    {fmt.Sprint(totpDigits)},
		"period":    {fmt.Sprint(totpPeriod)},
	}
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + query.Encode()
}

func totpAt(secret []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step)) //nolint:gosec // a time step is never negative
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, value%1_000_000)
}

// VerifyTOTP checks a code against the steps around now. It returns the step that matched, so the
// caller can refuse the same code a second time (a code watched over a shoulder is useless), or
// 0 when nothing matched. Steps not newer than lastStep are never accepted.
func VerifyTOTP(secret []byte, code string, now time.Time, lastStep int64) int64 {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0
	}
	current := now.Unix() / totpPeriod
	var matched int64
	for step := current - totpSkew; step <= current+totpSkew; step++ {
		// Compare every candidate: no early exit, no timing hint about which step was close.
		if subtle.ConstantTimeCompare([]byte(totpAt(secret, step)), []byte(code)) == 1 && step > lastStep {
			matched = step
		}
	}
	return matched
}

// TOTPCode returns the code an authenticator app shows at the given moment.
func TOTPCode(secret []byte, at time.Time) string {
	return totpAt(secret, at.Unix()/totpPeriod)
}
