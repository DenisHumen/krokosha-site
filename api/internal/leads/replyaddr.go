package leads

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"regexp"
	"strconv"
	"strings"
)

// The address a client's answer comes back to (brief B10.5): «leads+k-0042.<signature>@domain».
// Everything after «+» is ignored by the mail server when it picks the mailbox, and tells the
// service which request a letter belongs to. The signature — an HMAC with the installation's
// secret — is what makes the number trustworthy: nobody can write into somebody else's request
// by guessing its number.

var replyEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func replySignature(secret []byte, leadID int64) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("reply:" + Number(leadID)))
	return strings.ToLower(replyEncoding.EncodeToString(mac.Sum(nil)[:10])) // 80 bits, 16 characters
}

// ReplyAddress builds the address for a request. inbox is the service mailbox, «leads@domain».
func ReplyAddress(secret []byte, inbox string, leadID int64) string {
	local, domain, ok := strings.Cut(inbox, "@")
	if !ok {
		return ""
	}
	return local + "+" + strings.ToLower(Number(leadID)) + "." + replySignature(secret, leadID) + "@" + domain
}

var reReplyAddress = regexp.MustCompile(`^[^+@\s]+\+k-0*([1-9][0-9]{0,17})\.([a-z2-7]{16})@`)

// ParseReplyAddress returns the request an address belongs to — if the signature says it is ours.
// Mail programs and servers change the case of addresses as they please; so does this.
func ParseReplyAddress(secret []byte, address string) (leadID int64, ok bool) {
	match := reReplyAddress.FindStringSubmatch(strings.ToLower(strings.TrimSpace(address)))
	if match == nil {
		return 0, false
	}
	id, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || subtle.ConstantTimeCompare([]byte(match[2]), []byte(replySignature(secret, id))) != 1 {
		return 0, false
	}
	return id, true
}
