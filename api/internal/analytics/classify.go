package analytics

import (
	"net"
	"regexp"
	"strings"
)

// Everything here turns raw request data into coarse, non-identifying categories.

// TruncateIP keeps the network part only: /24 for IPv4, /48 for IPv6 (brief B5).
func TruncateIP(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	if v6 := ip.To16(); v6 != nil {
		return v6.Mask(net.CIDRMask(48, 128)).String() + "/48"
	}
	return "unknown"
}

// Bots that run JavaScript. Crawlers that do not never reach this endpoint at all —
// they show up in the «server traffic» dashboard, which reads nginx's log.
var reBot = regexp.MustCompile(`(?i)bot\b|bot/|crawl|spider|slurp|headless|lighthouse|pagespeed|pingdom|uptime|monitor|preview|scrap|python-requests|curl/|wget/|httpclient|go-http-client|okhttp|java/|libwww|phantomjs|selenium|puppeteer|playwright|facebookexternalhit|whatsapp|telegrambot|discordbot|slackbot|embedly|quora link|bingpreview|yandex(?:bot|images)|ahrefs|semrush|mj12|dotbot|petalbot|bytespider|gptbot|claudebot|ccbot|amazonbot|applebot`)

// IsBot reports whether the User-Agent belongs to an automated client.
func IsBot(userAgent string) bool {
	ua := strings.TrimSpace(userAgent)
	return ua == "" || len(ua) < 20 || reBot.MatchString(ua)
}

// Client is the coarse description of a visitor's software (brief B5: «грубо, из UA»).
type Client struct {
	Device  string // desktop | mobile | tablet
	Browser string
	OS      string
}

// ParseUserAgent extracts device class, browser family and OS family. Versions are dropped on
// purpose: they add nothing to the statistics and make visitors more distinguishable.
func ParseUserAgent(userAgent string) Client {
	ua := strings.ToLower(userAgent)
	has := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(ua, part) {
				return true
			}
		}
		return false
	}

	client := Client{Device: "desktop", Browser: "other", OS: "other"}

	switch {
	case has("iphone", "ipod"):
		client.OS, client.Device = "iOS", "mobile"
	case has("ipad"):
		client.OS, client.Device = "iOS", "tablet"
	case has("android"):
		client.OS = "Android"
		if has("mobile") {
			client.Device = "mobile"
		} else {
			client.Device = "tablet"
		}
	case has("windows"):
		client.OS = "Windows"
	case has("cros"):
		client.OS = "ChromeOS"
	case has("mac os x", "macintosh"):
		client.OS = "macOS"
	case has("linux", "x11"):
		client.OS = "Linux"
	}
	if client.Device == "desktop" && has("mobile") {
		client.Device = "mobile"
	}

	// Order matters: almost every browser claims to be Chrome and Safari.
	switch {
	case has("edg/", "edga/", "edgios/"):
		client.Browser = "Edge"
	case has("opr/", "opera"):
		client.Browser = "Opera"
	case has("yabrowser"):
		client.Browser = "Yandex"
	case has("samsungbrowser"):
		client.Browser = "Samsung"
	case has("firefox/", "fxios/"):
		client.Browser = "Firefox"
	case has("chrome/", "crios/", "chromium/"):
		client.Browser = "Chrome"
	case has("safari/"):
		client.Browser = "Safari"
	}
	return client
}

// Referrer kinds.
const (
	ReferrerDirect = "direct"
	ReferrerSearch = "search"
	ReferrerSocial = "social"
	ReferrerOther  = "other"
)

var (
	searchHosts = []string{"google.", "bing.com", "duckduckgo.com", "yahoo.", "yandex.", "ya.ru", "baidu.com",
		"ecosia.org", "brave.com", "startpage.com", "qwant.com", "kagi.com", "meta.ua", "ukr.net"}
	socialHosts = []string{"t.me", "telegram.org", "telegram.me", "facebook.com", "fb.com", "instagram.com",
		"x.com", "twitter.com", "t.co", "linkedin.com", "lnkd.in", "youtube.com", "youtu.be", "reddit.com",
		"tiktok.com", "threads.net", "viber.com", "whatsapp.com", "dou.ua", "habr.com"}
)

// ClassifyReferrer sorts the referring host into a kind. The site's own host counts as direct:
// moving between pages says nothing about where the visitor came from.
func ClassifyReferrer(host, siteHost string) (kind, cleanHost string) {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	site := strings.TrimPrefix(strings.ToLower(siteHost), "www.")
	if host == "" || host == site {
		return ReferrerDirect, ""
	}
	matches := func(list []string) bool {
		for _, known := range list {
			if strings.HasSuffix(known, ".") { // "google." — any national domain
				if strings.HasPrefix(host, known) || strings.Contains(host, "."+known) {
					return true
				}
				continue
			}
			if host == known || strings.HasSuffix(host, "."+known) {
				return true
			}
		}
		return false
	}
	switch {
	case matches(searchHosts):
		return ReferrerSearch, host
	case matches(socialHosts):
		return ReferrerSocial, host
	}
	return ReferrerOther, host
}

// IsPaid reports advertising traffic: a paid medium in the UTM tags or an ad click id on the URL.
func IsPaid(utm UTM, adClick string) bool {
	if adClick != "" {
		return true
	}
	switch strings.ToLower(utm.Medium) {
	case "cpc", "ppc", "cpm", "cpa", "paid", "ads", "ad", "paidsocial", "paid-social", "paid_social", "display", "banner", "retargeting":
		return true
	}
	return false
}
