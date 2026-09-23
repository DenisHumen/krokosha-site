package netmap

import (
	"fmt"
	"strconv"
	"strings"
)

// PublicASN reports whether an AS number can belong to a network of the internet: not 0, not
// AS_TRANS, not one of the numbers kept for documentation or private use, not reserved
// (RFC 7607, 6793, 5398, 6996, 7300 and the IANA registry).
func PublicASN(asn uint32) bool {
	switch {
	case asn == 0, asn == 23456, asn == 65535, asn == 4294967295:
		return false
	case asn >= 64496 && asn <= 64511, asn >= 65536 && asn <= 65551: // documentation
		return false
	case asn >= 64512 && asn <= 65534, asn >= 4200000000: // private use (and the last one, reserved)
		return false
	case asn >= 65552 && asn <= 131071: // reserved by IANA
		return false
	}
	return true
}

// ParseASN reads «13335», «AS13335» or «as13335».
func ParseASN(text string) (uint32, error) {
	text = strings.TrimSpace(text)
	if len(text) > 2 && strings.EqualFold(text[:2], "as") {
		text = text[2:]
	}
	value, err := strconv.ParseUint(text, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("not an AS number: %q", text)
	}
	return uint32(value), nil
}
