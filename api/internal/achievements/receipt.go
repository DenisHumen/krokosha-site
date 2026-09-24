// Package achievements keeps the global statistics of the easter eggs' achievements — how many
// players found each, and so how rare it is, the way Steam shows it — and signs receipts of what a
// browser found. The eggs themselves live in the page (design/components/eggs/eggs.js): the page
// reports a find, gets a receipt back and keeps it in localStorage. The receipt of «all», issued
// only for the receipts of every egg, is what the contact form claims the eggs' discount with.
package achievements

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Eggs are the achievements of design/components/eggs/eggs.js (ALL), in its order. A test reads
// the file and compares.
var Eggs = []string{"konami", "sudo", "croc", "cat", "reboot", "console", "croc5", "lost_packet"}

// All is the achievement of finding every egg: gold, like a rare one in Steam.
const All = "all"

// players is the pseudo-achievement every browser counts itself into once.
const players = "players"

// IsEgg reports whether id is one of the eggs.
func IsEgg(id string) bool {
	for _, egg := range Eggs {
		if egg == id {
			return true
		}
	}
	return false
}

// Known reports whether id is an achievement: an egg or «all».
func Known(id string) bool { return id == All || IsEgg(id) }

// Receipt is what a signed receipt says.
type Receipt struct {
	ID string
	At time.Time
	// Span is how long it took from the first egg to the last; only receipts of «all» carry it.
	// A player who found everything in a few seconds read the source rather than the page.
	Span time.Duration
}

// ErrBadReceipt: the receipt was not made here, or not in this form.
var ErrBadReceipt = errors.New("not a receipt of this site")

// Sign makes the receipt of a found egg: «konami.<time>.<mac>».
func Sign(secret []byte, id string, at time.Time) string {
	payload := id + "." + strconv.FormatInt(at.Unix(), 36)
	return payload + "." + mac(secret, payload)
}

// SignAll makes the receipt of «all»: «all.<time>.<span>.<mac>».
func SignAll(secret []byte, at time.Time, span time.Duration) string {
	payload := All + "." + strconv.FormatInt(at.Unix(), 36) + "." + strconv.FormatInt(int64(span/time.Second), 36)
	return payload + "." + mac(secret, payload)
}

// Verify checks a receipt and reads it.
func Verify(secret []byte, receipt string) (Receipt, error) {
	if len(receipt) > 80 || len(secret) == 0 {
		return Receipt{}, ErrBadReceipt
	}
	cut := strings.LastIndexByte(receipt, '.')
	if cut < 0 {
		return Receipt{}, ErrBadReceipt
	}
	payload, signature := receipt[:cut], receipt[cut+1:]
	if !hmac.Equal([]byte(signature), []byte(mac(secret, payload))) {
		return Receipt{}, ErrBadReceipt
	}
	parts := strings.Split(payload, ".")
	var out Receipt
	switch {
	case len(parts) == 2 && IsEgg(parts[0]):
	case len(parts) == 3 && parts[0] == All:
		span, err := strconv.ParseInt(parts[2], 36, 64)
		if err != nil || span < 0 {
			return Receipt{}, ErrBadReceipt
		}
		out.Span = time.Duration(span) * time.Second
	default:
		return Receipt{}, ErrBadReceipt
	}
	unix, err := strconv.ParseInt(parts[1], 36, 64)
	if err != nil || unix <= 0 {
		return Receipt{}, ErrBadReceipt
	}
	out.ID, out.At = parts[0], time.Unix(unix, 0).UTC()
	return out, nil
}

// Complete checks the receipts of every egg and says when the first and the last were found.
// The same egg twice, a stranger's receipt, one egg missing — and there is no «all».
func Complete(secret []byte, receipts []string) (first, last time.Time, err error) {
	seen := map[string]bool{}
	for _, text := range receipts {
		receipt, err := Verify(secret, text)
		if err != nil || receipt.ID == All || seen[receipt.ID] {
			return time.Time{}, time.Time{}, ErrBadReceipt
		}
		seen[receipt.ID] = true
		if first.IsZero() || receipt.At.Before(first) {
			first = receipt.At
		}
		if receipt.At.After(last) {
			last = receipt.At
		}
	}
	if len(seen) != len(Eggs) {
		return time.Time{}, time.Time{}, ErrBadReceipt
	}
	return first, last, nil
}

// Fingerprint is how a used receipt is remembered: one receipt of «all», one discount.
func Fingerprint(receipt string) []byte {
	sum := sha256.Sum256([]byte("krokosha-egg-receipt:" + receipt))
	return sum[:]
}

// mac signs with the site's secret; 120 bits, base64url.
func mac(secret []byte, payload string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("krokosha-egg:" + payload))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:15])
}
