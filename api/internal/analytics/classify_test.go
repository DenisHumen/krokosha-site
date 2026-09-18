package analytics

import (
	"net"
	"testing"
)

func TestTruncateIP(t *testing.T) {
	cases := map[string]string{
		"203.0.113.77":           "203.0.113.0/24",
		"10.7.0.4":               "10.7.0.0/24",
		"2001:db8:abcd:1234::1":  "2001:db8:abcd::/48",
		"::ffff:198.51.100.200":  "198.51.100.0/24", // IPv4 mapped into IPv6
		"2a02:6b8:0:1:ffff::dea": "2a02:6b8::/48",
	}
	for input, want := range cases {
		if got := TruncateIP(net.ParseIP(input)); got != want {
			t.Errorf("TruncateIP(%s) = %s, want %s", input, got, want)
		}
	}
}

const (
	uaChromeWindows  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	uaSafariIPhone   = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1"
	uaFirefoxLinux   = "Mozilla/5.0 (X11; Linux x86_64; rv:142.0) Gecko/20100101 Firefox/142.0"
	uaEdgeWindows    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0"
	uaSamsungPhone   = "Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/27.0 Chrome/125.0.0.0 Mobile Safari/537.36"
	uaChromeTablet   = "Mozilla/5.0 (Linux; Android 13; SM-X710) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36"
	uaSafariIPad     = "Mozilla/5.0 (iPad; CPU OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Mobile/15E148 Safari/604.1"
	uaSafariMac      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Safari/605.1.15"
	uaYandexWindows  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 YaBrowser/25.8.0 Safari/537.36"
	uaOperaWindows   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36 OPR/123.0.0.0"
	uaChromeOnIPhone = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/140.0.7339.95 Mobile/15E148 Safari/604.1"
)

func TestParseUserAgent(t *testing.T) {
	cases := map[string]Client{
		uaChromeWindows:                 {"desktop", "Chrome", "Windows"},
		uaSafariIPhone:                  {"mobile", "Safari", "iOS"},
		uaFirefoxLinux:                  {"desktop", "Firefox", "Linux"},
		uaEdgeWindows:                   {"desktop", "Edge", "Windows"},
		uaSamsungPhone:                  {"mobile", "Samsung", "Android"},
		uaChromeTablet:                  {"tablet", "Chrome", "Android"},
		uaSafariIPad:                    {"tablet", "Safari", "iOS"},
		uaSafariMac:                     {"desktop", "Safari", "macOS"},
		uaYandexWindows:                 {"desktop", "Yandex", "Windows"},
		uaOperaWindows:                  {"desktop", "Opera", "Windows"},
		uaChromeOnIPhone:                {"mobile", "Chrome", "iOS"},
		"SomethingNew/1.0 (Quantum OS)": {"desktop", "other", "other"},
	}
	for ua, want := range cases {
		if got := ParseUserAgent(ua); got != want {
			t.Errorf("%s\n got %+v\nwant %+v", ua, got, want)
		}
	}
}

func TestIsBot(t *testing.T) {
	people := []string{uaChromeWindows, uaSafariIPhone, uaFirefoxLinux, uaEdgeWindows, uaSamsungPhone, uaSafariMac, uaYandexWindows}
	for _, ua := range people {
		if IsBot(ua) {
			t.Errorf("a person was taken for a bot: %s", ua)
		}
	}
	bots := []string{
		"",
		"curl/8.5.0",
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/140.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Linux; Android 11; moto g power) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36 Chrome-Lighthouse",
		"python-requests/2.32.3",
		"Go-http-client/2.0",
		"facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)",
		"TelegramBot (like TwitterBot)",
		"Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)",
		"Mozilla/5.0 (compatible; YandexBot/3.0; +http://yandex.com/bots)",
	}
	for _, ua := range bots {
		if !IsBot(ua) {
			t.Errorf("a bot was taken for a person: %q", ua)
		}
	}
}

func TestClassifyReferrer(t *testing.T) {
	cases := []struct {
		host, wantKind, wantHost string
	}{
		{"", ReferrerDirect, ""},
		{"krokosha.xyz", ReferrerDirect, ""},
		{"www.krokosha.xyz", ReferrerDirect, ""},
		{"www.google.com", ReferrerSearch, "google.com"},
		{"google.com.ua", ReferrerSearch, "google.com.ua"},
		{"www.bing.com", ReferrerSearch, "bing.com"},
		{"duckduckgo.com", ReferrerSearch, "duckduckgo.com"},
		{"yandex.ru", ReferrerSearch, "yandex.ru"},
		{"t.me", ReferrerSocial, "t.me"},
		{"web.telegram.org", ReferrerSocial, "web.telegram.org"},
		{"l.facebook.com", ReferrerSocial, "l.facebook.com"},
		{"www.linkedin.com", ReferrerSocial, "linkedin.com"},
		{"github.com", ReferrerOther, "github.com"},
		{"notgoogle.example", ReferrerOther, "notgoogle.example"},
		{"evil-t.me.example.org", ReferrerOther, "evil-t.me.example.org"},
	}
	for _, tc := range cases {
		kind, host := ClassifyReferrer(tc.host, "krokosha.xyz")
		if kind != tc.wantKind || host != tc.wantHost {
			t.Errorf("ClassifyReferrer(%q) = %s %q, want %s %q", tc.host, kind, host, tc.wantKind, tc.wantHost)
		}
	}
}

func TestIsPaid(t *testing.T) {
	if !IsPaid(UTM{Medium: "CPC"}, "") || !IsPaid(UTM{}, "g") || !IsPaid(UTM{Medium: "paid_social"}, "") {
		t.Error("paid traffic was not recognised")
	}
	if IsPaid(UTM{Source: "newsletter", Medium: "email"}, "") || IsPaid(UTM{}, "") {
		t.Error("free traffic was taken for paid")
	}
}
