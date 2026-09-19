package leads

import (
	"strings"
	"testing"
)

func TestReplyAddressesCannotBeForged(t *testing.T) {
	address := ReplyAddress(secret, "leads@krokosha.xyz", 42)
	if !strings.HasPrefix(address, "leads+k-0042.") || !strings.HasSuffix(address, "@krokosha.xyz") || len(address) != len("leads+k-0042.")+16+len("@krokosha.xyz") {
		t.Fatalf("the address: %q", address)
	}
	for name, given := range map[string]string{
		"as it was built":          address,
		"upper case, as some send": strings.ToUpper(address),
		"with spaces around":       "  " + address + " ",
	} {
		if id, ok := ParseReplyAddress(secret, given); !ok || id != 42 {
			t.Errorf("%s: %d %v", name, id, ok)
		}
	}
	signature := address[len("leads+k-0042.") : len("leads+k-0042.")+16]
	for name, given := range map[string]string{
		"another request with this signature": "leads+k-0043." + signature + "@krokosha.xyz",
		"a signature made with another key":   ReplyAddress([]byte("another-secret-another-secret-00"), "leads@krokosha.xyz", 42),
		"no signature":                        "leads+k-0042@krokosha.xyz",
		"a plain address":                     "leads@krokosha.xyz",
		"a shortened signature":               "leads+k-0042." + signature[:15] + "@krokosha.xyz",
		"number zero":                         "leads+k-0000." + signature + "@krokosha.xyz",
		"nothing":                             "",
	} {
		if id, ok := ParseReplyAddress(secret, given); ok {
			t.Errorf("%s was accepted as request %d", name, id)
		}
	}
	// Big numbers keep working: the number is not limited to four digits.
	if id, ok := ParseReplyAddress(secret, ReplyAddress(secret, "leads@krokosha.xyz", 123456)); !ok || id != 123456 {
		t.Errorf("a six-digit request: %d %v", id, ok)
	}
	if ReplyAddress(secret, "not-an-address", 1) != "" {
		t.Error("an inbox without a domain must give no address")
	}
}
