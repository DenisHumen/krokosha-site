// Package leads is the contact form's back end and the small CRM behind it (brief B10): it
// validates what a visitor sent, keeps spam out without third-party services, stores the request
// and queues the notifications — all in one transaction.
package leads

import (
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

// Contact methods.
const (
	MethodEmail    = "email"
	MethodTelegram = "telegram"
	MethodPhone    = "phone"
)

// Limits of the form (brief B10.1).
const (
	MaxName        = 100
	MinDescription = 20
	MaxDescription = 4000
)

// Error codes of fields. The site shows them with its own localized texts
// (content/site.yaml → contacts.form.errors).
const (
	ErrRequired          = "required"
	ErrInvalidEmail      = "invalid_email"
	ErrInvalidTelegram   = "invalid_telegram"
	ErrInvalidPhone      = "invalid_phone"
	ErrDescriptionLength = "description_length"
	ErrConsentRequired   = "consent_required"
	ErrInvalidChoice     = "invalid_choice"
)

// Submission is a validated request, ready to be stored.
type Submission struct {
	Name          string
	ContactMethod string
	ContactValue  string // normalized: lower-case email, @username, +380…
	Direction     string
	Description   string
	Budget        string // the English label of the chosen option: the admin area reads one language
	Timeline      string
	Lang          string

	// What the spam checks look at, besides the texts above.
	Honeypot string // a field people never see; bots fill it in
	Proof    string // the proof-of-work payload («altcha»)
}

// FieldErrors maps a field of the form to an error code.
type FieldErrors map[string]string

const (
	zeroWidthJoiner = rune(0x200D)
	// What people put into phone numbers for readability, the no-break space included.
	phoneSpacers = " -()./" + string(rune(0xA0))
)

var languages = map[string]bool{"en": true, "uk": true, "ru": true}

var (
	reTelegram = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`) // 5 to 32 characters
	rePhone    = regexp.MustCompile(`^\+?[0-9]{7,15}$`)
)

// Parse validates the values of the form. get returns a field by its name.
func Parse(get func(string) string, form config.Form) (Submission, FieldErrors) {
	errs := FieldErrors{}
	sub := Submission{
		Name:        clean(get("name"), false),
		Description: clean(get("description"), true),
		Honeypot:    get("website"),
		Proof:       get("altcha"),
		Lang:        get("lang"),
	}
	if !languages[sub.Lang] {
		sub.Lang = "en"
	}

	switch {
	case sub.Name == "":
		errs["name"] = ErrRequired
	case utf8.RuneCountInString(sub.Name) > MaxName:
		sub.Name = string([]rune(sub.Name)[:MaxName])
	}

	sub.ContactMethod = get("contact_method")
	value := strings.TrimSpace(get("contact_value"))
	switch {
	case value == "":
		errs["contact_value"] = ErrRequired
	case sub.ContactMethod == MethodEmail:
		if sub.ContactValue = normalizeEmail(value); sub.ContactValue == "" {
			errs["contact_value"] = ErrInvalidEmail
		}
	case sub.ContactMethod == MethodTelegram:
		if sub.ContactValue = normalizeTelegram(value); sub.ContactValue == "" {
			errs["contact_value"] = ErrInvalidTelegram
		}
	case sub.ContactMethod == MethodPhone:
		if sub.ContactValue = normalizePhone(value); sub.ContactValue == "" {
			errs["contact_value"] = ErrInvalidPhone
		}
	default:
		errs["contact_method"] = ErrInvalidChoice
	}

	sub.Direction = get("direction")
	if _, ok := form.Direction(sub.Direction); !ok {
		if sub.Direction == "" {
			errs["direction"] = ErrRequired
		} else {
			errs["direction"] = ErrInvalidChoice
		}
	}

	if length := utf8.RuneCountInString(sub.Description); length < MinDescription || length > MaxDescription {
		errs["description"] = ErrDescriptionLength
		if length == 0 {
			errs["description"] = ErrRequired
		}
	}

	if budget, err := config.Choice(form.Budgets, get("budget")); err != nil {
		errs["budget"] = ErrInvalidChoice
	} else {
		sub.Budget = budget.In("en")
	}
	if timeline, err := config.Choice(form.Timelines, get("timeline")); err != nil {
		errs["timeline"] = ErrInvalidChoice
	} else {
		sub.Timeline = timeline.In("en")
	}

	switch get("consent") {
	case "on", "1", "true", "yes":
	default:
		errs["consent"] = ErrConsentRequired
	}

	if len(errs) == 0 {
		return sub, nil
	}
	return sub, errs
}

// clean trims the text and removes what has no business in it: control characters, and line
// breaks where a single line is expected.
func clean(text string, multiline bool) string {
	text = strings.ToValidUTF8(text, "")
	text = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' && multiline:
			return r
		case r == '\r':
			return -1
		case r == '\t' || r == '\n':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r) && r != zeroWidthJoiner: // invisible format characters go, except the one that keeps emoji whole
			return -1
		}
		return r
	}, text)
	return strings.TrimSpace(text)
}

func normalizeEmail(value string) string {
	if len(value) > 200 || strings.ContainsAny(value, " \t\r\n<>()[],;:\\\"") {
		return ""
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return ""
	}
	local, domain, ok := strings.Cut(value, "@")
	if !ok || local == "" || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return ""
	}
	return local + "@" + strings.ToLower(domain)
}

// normalizeTelegram accepts «@name», «name» and links to t.me, and returns «@name».
func normalizeTelegram(value string) string {
	if parsed, err := url.Parse(value); err == nil && (parsed.Host == "t.me" || parsed.Host == "telegram.me") {
		value = strings.Trim(parsed.Path, "/")
	}
	value = strings.TrimPrefix(strings.TrimPrefix(value, "t.me/"), "@")
	if !reTelegram.MatchString(value) {
		return ""
	}
	return "@" + value
}

// normalizePhone drops what people put into numbers for readability.
func normalizePhone(value string) string {
	digits := strings.Map(func(r rune) rune {
		if strings.ContainsRune(phoneSpacers, r) {
			return -1
		}
		return r
	}, value)
	if !rePhone.MatchString(digits) {
		return ""
	}
	return digits
}
