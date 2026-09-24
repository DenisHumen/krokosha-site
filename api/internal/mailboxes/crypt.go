package mailboxes

import (
	"crypto/rand"
	"crypto/sha512"
	"errors"
	"strconv"
	"strings"
)

// SHA-512 crypt ($6$), the scheme the mail server keeps passwords in (docker-mailserver's
// postfix-accounts.cf, «{SHA512-CRYPT}»; deploy/bin/krokosha-mailbox makes the same with
// `openssl passwd -6`). Ulrich Drepper's specification, «Unix crypt using SHA-256 and SHA-512»:
// https://www.akkadia.org/drepper/SHA-crypt.txt. The password is hashed here, in the service, so
// that it never touches a disk — what goes to the root helper is the hash.

const (
	itoa64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// DefaultRounds of the specification — what `openssl passwd -6` uses.
	DefaultRounds = 5000
	// Rounds of the hashes made here: a guess at a leaked file costs 20 times more, a login of a
	// mail program still takes a small fraction of a second.
	Rounds    = 100_000
	maxSalt   = 16
	minRounds = 1000
	maxRounds = 999_999_999
)

// ErrBadHash: not a $6$ hash.
var ErrBadHash = errors.New("not a SHA-512 crypt hash")

// Crypt hashes a password with a new random salt.
func Crypt(password string) (string, error) {
	raw := make([]byte, maxSalt)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	salt := make([]byte, maxSalt)
	for i, b := range raw {
		salt[i] = itoa64[int(b)%len(itoa64)]
	}
	return cryptWith([]byte(password), salt, Rounds, true), nil
}

// CryptSetting hashes a password with the salt and rounds of a setting («$6$salt» or
// «$6$rounds=N$salt», optionally followed by «$hash»): to check a password against a hash, and for
// the test vectors of the specification.
func CryptSetting(password, setting string) (string, error) {
	rest, ok := strings.CutPrefix(setting, "$6$")
	if !ok {
		return "", ErrBadHash
	}
	rounds, explicit := DefaultRounds, false
	if value, after, found := strings.Cut(rest, "$"); found && strings.HasPrefix(value, "rounds=") {
		n, err := strconv.Atoi(strings.TrimPrefix(value, "rounds="))
		if err != nil {
			return "", ErrBadHash
		}
		rounds, explicit, rest = min(max(n, minRounds), maxRounds), true, after
	}
	salt, _, _ := strings.Cut(rest, "$")
	if len(salt) > maxSalt {
		salt = salt[:maxSalt]
	}
	return cryptWith([]byte(password), []byte(salt), rounds, explicit), nil
}

// Verify reports whether the password matches the hash.
func Verify(password, hash string) bool {
	made, err := CryptSetting(password, hash)
	return err == nil && made == hash
}

func cryptWith(password, salt []byte, rounds int, explicitRounds bool) string {
	// B: password, salt, password.
	b := sha512.New()
	b.Write(password)
	b.Write(salt)
	b.Write(password)
	digestB := b.Sum(nil)

	// A: password, salt, then B for every byte of the password, then B or the password by the bits
	// of the password's length.
	a := sha512.New()
	a.Write(password)
	a.Write(salt)
	n := len(password)
	for ; n > 64; n -= 64 {
		a.Write(digestB)
	}
	a.Write(digestB[:n])
	for n = len(password); n > 0; n >>= 1 {
		if n&1 != 0 {
			a.Write(digestB)
		} else {
			a.Write(password)
		}
	}
	digestA := a.Sum(nil)

	// P: the password as many times as it has bytes; S: the salt 16 + A[0] times.
	dp := sha512.New()
	for range len(password) {
		dp.Write(password)
	}
	pSequence := stretch(dp.Sum(nil), len(password))
	ds := sha512.New()
	for range 16 + int(digestA[0]) {
		ds.Write(salt)
	}
	sSequence := stretch(ds.Sum(nil), len(salt))

	result := digestA
	for i := range rounds {
		c := sha512.New()
		if i%2 != 0 {
			c.Write(pSequence)
		} else {
			c.Write(result)
		}
		if i%3 != 0 {
			c.Write(sSequence)
		}
		if i%7 != 0 {
			c.Write(pSequence)
		}
		if i%2 != 0 {
			c.Write(result)
		} else {
			c.Write(pSequence)
		}
		result = c.Sum(nil)
	}

	var out strings.Builder
	out.WriteString("$6$")
	if explicitRounds {
		out.WriteString("rounds=" + strconv.Itoa(rounds) + "$")
	}
	out.Write(salt)
	out.WriteByte('$')
	for _, group := range [][3]int{
		{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48},
		{28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13},
		{56, 14, 35}, {15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41},
	} {
		encode(&out, result[group[0]], result[group[1]], result[group[2]], 4)
	}
	encode(&out, 0, 0, result[63], 2)
	return out.String()
}

// stretch repeats a digest to the given length.
func stretch(digest []byte, length int) []byte {
	out := make([]byte, 0, length)
	for len(out)+len(digest) <= length {
		out = append(out, digest...)
	}
	return append(out, digest[:length-len(out)]...)
}

func encode(out *strings.Builder, b2, b1, b0 byte, n int) {
	w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
	for range n {
		out.WriteByte(itoa64[w&0x3f])
		w >>= 6
	}
}
