package leads

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Proof of work for the contact form, compatible with ALTCHA (https://altcha.org): no cookies,
// no third party, nothing to track. The browser has to find the number N for which
// SHA-256(salt + N) equals the challenge — a second of work for a person's device, a real cost
// for whoever wants to send ten thousand forms.

const (
	altchaAlgorithm = "SHA-256"
	// The number is drawn below this bound; on average half of it has to be tried.
	altchaMaxNumber = 30_000
	// How long a challenge stays valid: long enough to write a careful request.
	challengeTTL = 45 * time.Minute
)

// Challenge is what GET /api/leads/challenge answers.
type Challenge struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	MaxNumber int    `json:"maxnumber"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

type altchaPayload struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	Number    int    `json:"number"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

// Errors of VerifyProof.
var (
	ErrNoProof      = errors.New("no proof of work")
	ErrBadProof     = errors.New("the proof of work is wrong")
	ErrProofExpired = errors.New("the proof of work has expired")
)

// NewChallenge makes a challenge that expires challengeTTL from now.
func NewChallenge(key []byte, now time.Time) (Challenge, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return Challenge{}, err
	}
	number, err := rand.Int(rand.Reader, big.NewInt(altchaMaxNumber))
	if err != nil {
		return Challenge{}, err
	}
	// The salt ends with «&»: without a delimiter a solved challenge could be re-cut — digits
	// moved from the number into the salt — into one with a later expiry.
	salt := hex.EncodeToString(random) + "?expires=" + strconv.FormatInt(now.Add(challengeTTL).Unix(), 10) + "&"
	sum := sha256.Sum256([]byte(salt + number.String()))
	challenge := hex.EncodeToString(sum[:])
	return Challenge{
		Algorithm: altchaAlgorithm,
		Challenge: challenge,
		MaxNumber: altchaMaxNumber,
		Salt:      salt,
		Signature: sign(key, challenge),
	}, nil
}

func sign(key []byte, challenge string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(challenge))
	return hex.EncodeToString(mac.Sum(nil))
}

// Proof is a verified solution.
type Proof struct {
	Challenge string    // to refuse a second use
	IssuedAt  time.Time // when the form asked for the challenge: how long it took to fill it in
	ExpiresAt time.Time
}

// VerifyProof checks the payload the form sends in the «altcha» field.
func VerifyProof(key []byte, payload string, now time.Time) (Proof, error) {
	if strings.TrimSpace(payload) == "" {
		return Proof{}, ErrNoProof
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return Proof{}, ErrBadProof
	}
	var solution altchaPayload
	if err := json.Unmarshal(raw, &solution); err != nil || solution.Algorithm != altchaAlgorithm {
		return Proof{}, ErrBadProof
	}
	if solution.Number < 0 || solution.Number > altchaMaxNumber || !strings.HasSuffix(solution.Salt, "&") {
		return Proof{}, ErrBadProof
	}
	// The signature proves the challenge is ours; the hash proves the work was done on this salt.
	if subtle.ConstantTimeCompare([]byte(sign(key, solution.Challenge)), []byte(solution.Signature)) != 1 {
		return Proof{}, ErrBadProof
	}
	sum := sha256.Sum256([]byte(solution.Salt + strconv.Itoa(solution.Number)))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(solution.Challenge)) != 1 {
		return Proof{}, ErrBadProof
	}

	_, query, _ := strings.Cut(solution.Salt, "?")
	params, err := url.ParseQuery(query)
	if err != nil {
		return Proof{}, ErrBadProof
	}
	unix, err := strconv.ParseInt(params.Get("expires"), 10, 64)
	if err != nil {
		return Proof{}, ErrBadProof
	}
	expires := time.Unix(unix, 0)
	if now.After(expires) {
		return Proof{}, ErrProofExpired
	}
	return Proof{Challenge: solution.Challenge, IssuedAt: expires.Add(-challengeTTL), ExpiresAt: expires}, nil
}
