package clients

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// Ways to reach a client beyond the two they sign in with (the address and Telegram): what the
// client writes into the account, so that the owner can write to them. Nothing proves them — the
// owner sees them marked so — but every one is checked for its form and turned into a link.

// Kinds of contacts, in the order of the account's form.
var Kinds = []string{"phone", "whatsapp", "viber", "signal", "telegram", "linkedin", "facebook", "instagram", "x", "discord", "skype", "github", "website"}

// KindNames are the names the admin area shows.
var KindNames = map[string]string{
	"phone": "Телефон", "whatsapp": "WhatsApp", "viber": "Viber", "signal": "Signal", "telegram": "Telegram",
	"linkedin": "LinkedIn", "facebook": "Facebook", "instagram": "Instagram", "x": "X", "discord": "Discord",
	"skype": "Skype", "github": "GitHub", "website": "Сайт",
}

// ErrBadContact: the value is not a contact of this kind.
var ErrBadContact = errors.New("not a contact of this kind")

// MaxContacts an account may keep: a person does not have more ways to be reached than this.
const MaxContacts = 20

var (
	reHandle    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	reDiscord   = regexp.MustCompile(`^[a-z0-9_.]{2,32}$`)
	reSkype     = regexp.MustCompile(`^(live:)?[A-Za-z][A-Za-z0-9._,-]{2,63}$`)
	reGitHub    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	reProfileID = regexp.MustCompile(`^[A-Za-z0-9._%-]{1,100}$`)
)

// NormalizeContact checks a value of a kind and returns it the way it is kept: a phone number as
// digits, a handle without «@» or the site's address, a profile as its path.
func NormalizeContact(kind, value string) (string, error) {
	value = leads.Clean(value, false)
	if value == "" || len(value) > 200 {
		return "", ErrBadContact
	}
	switch kind {
	case "phone", "whatsapp", "viber", "signal":
		if phone := leads.NormalizePhone(value); phone != "" {
			return phone, nil
		}
	case "telegram":
		if name := leads.NormalizeTelegram(value); name != "" {
			return name, nil
		}
	case "linkedin":
		return profilePath(value, []string{"linkedin.com"}, []string{"in/", "company/"})
	case "facebook":
		return profilePath(value, []string{"facebook.com", "fb.com"}, nil)
	case "instagram":
		return handle(value, []string{"instagram.com"}, reHandle)
	case "x":
		return handle(value, []string{"x.com", "twitter.com"}, reHandle)
	case "github":
		return handle(value, []string{"github.com"}, reGitHub)
	case "discord":
		if name := strings.ToLower(strings.TrimPrefix(value, "@")); reDiscord.MatchString(name) {
			return name, nil
		}
	case "skype":
		if reSkype.MatchString(value) {
			return value, nil
		}
	case "website":
		parsed, err := url.Parse(value)
		if err == nil && parsed.Scheme == "" {
			parsed, err = url.Parse("https://" + value)
		}
		if err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && strings.Contains(parsed.Host, ".") && parsed.User == nil {
			return parsed.String(), nil
		}
	}
	return "", ErrBadContact
}

// handle accepts «name», «@name» and a link to the profile.
func handle(value string, hosts []string, pattern *regexp.Regexp) (string, error) {
	if path, ok := linkPath(value, hosts); ok {
		value = strings.SplitN(path, "/", 2)[0]
	}
	value = strings.TrimPrefix(value, "@")
	if !pattern.MatchString(value) {
		return "", ErrBadContact
	}
	return value, nil
}

// profilePath keeps the path of a profile's address: «in/ivan-petrov», «profile.php?id=…» is refused
// as it is not a name. A bare name is taken as the first of the prefixes.
func profilePath(value string, hosts, prefixes []string) (string, error) {
	path, ok := linkPath(value, hosts)
	if !ok {
		path = strings.TrimPrefix(value, "@")
		if len(prefixes) > 0 && !strings.Contains(path, "/") {
			path = prefixes[0] + path
		}
	}
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	switch {
	case len(prefixes) == 0 && len(parts) == 1 && reProfileID.MatchString(parts[0]):
		return parts[0], nil
	case len(prefixes) > 0 && len(parts) == 2 && reProfileID.MatchString(parts[1]):
		for _, prefix := range prefixes {
			if parts[0]+"/" == prefix {
				return path, nil
			}
		}
	}
	return "", ErrBadContact
}

// linkPath returns the path of a link to one of the hosts (with or without www.).
func linkPath(value string, hosts []string) (string, bool) {
	candidate := value
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	parsed, err := url.Parse(candidate)
	if err != nil {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	host = strings.TrimPrefix(host, "m.")
	for _, known := range hosts {
		if host == known {
			return strings.Trim(parsed.Path, "/"), true
		}
	}
	return "", false
}

// ContactLink is where a contact leads the owner: a chat, a profile, a call. The values were checked
// by NormalizeContact, so nothing in them can make the link go elsewhere.
func ContactLink(kind, value string) string {
	digits := strings.TrimPrefix(value, "+")
	switch kind {
	case "phone":
		return "tel:" + value
	case "whatsapp":
		return "https://wa.me/" + digits
	case "viber":
		return "viber://chat?number=%2B" + digits
	case "signal":
		return "https://signal.me/#p/+" + digits
	case "telegram":
		return "https://t.me/" + strings.TrimPrefix(value, "@")
	case "linkedin":
		return "https://www.linkedin.com/" + value
	case "facebook":
		return "https://www.facebook.com/" + value
	case "instagram":
		return "https://www.instagram.com/" + value
	case "x":
		return "https://x.com/" + value
	case "github":
		return "https://github.com/" + value
	case "skype":
		return "skype:" + value + "?chat"
	case "website":
		return value
	}
	return "" // Discord has no link to a person by name
}
