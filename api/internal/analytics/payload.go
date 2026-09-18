// Package analytics is the first-party, cookie-less visit statistics of the site (brief B5):
// it accepts event batches from /assets/analytics.js, strips everything personal, and stores
// page views and events in MySQL.
//
// What is deliberately absent: cookies, fingerprints, full IP addresses, keystrokes, form input,
// session recordings. Visitors with Do Not Track or Global Privacy Control are not recorded at all.
package analytics

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	maxBodyBytes  = 16 << 10
	maxEvents     = 40
	maxOffsetMs   = 24 * 60 * 60 * 1000
	payloadFormat = 1
)

// Event types the page may send. Anything else fails validation.
const (
	TypePageview    = "pageview"
	TypeScroll      = "scroll"       // value: 25, 50, 75 or 100
	TypeTime        = "time"         // value: milliseconds the page has been visible so far
	TypeSection     = "section"      // target: data-section name
	TypeSectionTime = "section_time" // target: data-section name, value: milliseconds
	TypeClick       = "click"        // target: data-track id
	TypeOutbound    = "outbound"     // target: host of the link, or "mailto" / "tel"
	TypeEgg         = "egg"          // target: easter egg id
)

// Batch is the JSON body of POST /api/e. Field names are short: the body travels on every page view.
type Batch struct {
	Version  int     `json:"v"`
	Pageview string  `json:"id"` // 16 hex characters, made by the page, kept in memory only
	Path     string  `json:"p"`
	Lang     string  `json:"l"`
	Referrer string  `json:"r"` // host of the referring site, never the full URL
	UTM      UTM     `json:"u"`
	AdClick  string  `json:"ad"` // "g", "f" or "m": an ad click id was present; its value is never sent
	Events   []Event `json:"e"`
}

// UTM are the campaign tags of the landing URL.
type UTM struct {
	Source   string `json:"s"`
	Medium   string `json:"m"`
	Campaign string `json:"c"`
	Term     string `json:"t"`
	Content  string `json:"n"`
}

// Event is one thing that happened on the page.
type Event struct {
	Type   string `json:"t"`
	Target string `json:"x"`
	Value  int64  `json:"v"`
	Offset int64  `json:"o"` // milliseconds since the page view started
}

var (
	rePath   = regexp.MustCompile(`^/[A-Za-z0-9/_.~%-]{0,199}$`)
	reHost   = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,98}[a-z0-9])?$`)
	reTarget = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,99}$`)
	reLang   = regexp.MustCompile(`^[a-z]{2}$`)
)

// ParseBatch decodes and validates a request body. It is strict: the endpoint is public,
// and everything that gets through ends up in the database and on the admin's screen.
func ParseBatch(body []byte) (*Batch, error) {
	if len(body) > maxBodyBytes {
		return nil, errors.New("body too large")
	}
	var batch Batch
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&batch); err != nil {
		return nil, fmt.Errorf("malformed JSON: %w", err)
	}
	if decoder.More() {
		return nil, errors.New("trailing data after the JSON object")
	}

	if batch.Version != payloadFormat {
		return nil, fmt.Errorf("unsupported payload version %d", batch.Version)
	}
	if raw, err := hex.DecodeString(batch.Pageview); err != nil || len(raw) != 8 {
		return nil, errors.New("id: expected 16 hex characters")
	}
	if !rePath.MatchString(batch.Path) {
		return nil, errors.New("p: not a path")
	}
	if batch.Lang != "" && !reLang.MatchString(batch.Lang) {
		return nil, errors.New("l: not a language code")
	}
	batch.Referrer = strings.ToLower(strings.TrimSpace(batch.Referrer))
	if batch.Referrer != "" && !reHost.MatchString(batch.Referrer) {
		return nil, errors.New("r: not a host name")
	}
	switch batch.AdClick {
	case "", "g", "f", "m":
	default:
		return nil, errors.New("ad: unknown value")
	}
	for name, value := range map[string]*string{
		"u.s": &batch.UTM.Source, "u.m": &batch.UTM.Medium, "u.c": &batch.UTM.Campaign,
		"u.t": &batch.UTM.Term, "u.n": &batch.UTM.Content,
	} {
		cleaned, err := cleanTag(*value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		*value = cleaned
	}

	if len(batch.Events) == 0 || len(batch.Events) > maxEvents {
		return nil, fmt.Errorf("e: expected 1–%d events", maxEvents)
	}
	for i := range batch.Events {
		if err := validateEvent(&batch.Events[i]); err != nil {
			return nil, fmt.Errorf("e[%d]: %w", i, err)
		}
	}
	return &batch, nil
}

// cleanTag accepts free text of a UTM tag: printable, at most 100 characters.
func cleanTag(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 100 {
		return "", errors.New("longer than 100 characters")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("control character")
		}
	}
	return value, nil
}

func validateEvent(event *Event) error {
	if event.Offset < 0 || event.Offset > maxOffsetMs {
		return errors.New("o: out of range")
	}
	needsTarget, needsValue := false, false
	switch event.Type {
	case TypePageview:
	case TypeScroll:
		switch event.Value {
		case 25, 50, 75, 100:
		default:
			return errors.New("scroll: v must be 25, 50, 75 or 100")
		}
	case TypeTime:
		needsValue = true
	case TypeSection, TypeClick, TypeOutbound, TypeEgg:
		needsTarget = true
	case TypeSectionTime:
		needsTarget, needsValue = true, true
	default:
		return fmt.Errorf("unknown type %q", event.Type)
	}
	if needsTarget && !reTarget.MatchString(event.Target) {
		return errors.New("x: expected an identifier of up to 100 characters")
	}
	if !needsTarget && event.Target != "" {
		return errors.New("x: not expected for this type")
	}
	if needsValue && (event.Value < 0 || event.Value > maxOffsetMs) {
		return errors.New("v: out of range")
	}
	return nil
}
