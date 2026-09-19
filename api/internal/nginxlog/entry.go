// Package nginxlog turns nginx's JSON access log into the «server traffic» dashboard of the admin
// area (brief B6): it reads the file incrementally, survives logrotate, and stores aggregates
// only — requests per minute, status codes, response times, top URLs, crawlers and scanners.
// Unlike the JavaScript statistics this sees everything nginx served, bots included.
package nginxlog

import (
	"encoding/json"
	"errors"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
)

// Entry is one request as nginx logged it (format krokosha_json, deploy/nginx/krokosha-http.conf).
type Entry struct {
	Time        time.Time
	RemoteAddr  string
	Method      string
	Path        string // without the query string
	Status      int
	BytesSent   int64
	RequestTime time.Duration
	UserAgent   string
}

type rawEntry struct {
	Time        string  `json:"time"`
	RemoteAddr  string  `json:"remote_addr"`
	Method      string  `json:"method"`
	URI         string  `json:"uri"`
	Status      int     `json:"status"`
	BytesSent   int64   `json:"bytes_sent"`
	RequestTime float64 `json:"request_time"`
	UserAgent   string  `json:"user_agent"`
}

const maxPathLength = 200

// ParseLine reads one line of the log. Lines that are not ours (a truncated write, another
// format) are reported as errors and skipped by the reader.
func ParseLine(line []byte) (Entry, error) {
	var raw rawEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return Entry{}, err
	}
	at, err := time.Parse(time.RFC3339, raw.Time)
	if err != nil {
		return Entry{}, err
	}
	if raw.Status < 100 || raw.Status > 599 {
		return Entry{}, errors.New("status out of range")
	}
	return Entry{
		Time:        at.UTC(),
		RemoteAddr:  raw.RemoteAddr,
		Method:      raw.Method,
		Path:        cleanPath(raw.URI),
		Status:      raw.Status,
		BytesSent:   max(raw.BytesSent, 0),
		RequestTime: time.Duration(max(raw.RequestTime, 0) * float64(time.Second)),
		UserAgent:   raw.UserAgent,
	}, nil
}

// cleanPath drops the query string (it may carry personal data and would blow up the number of
// distinct URLs), control characters, and whatever does not fit the column.
func cleanPath(uri string) string {
	path, _, _ := strings.Cut(uri, "?")
	path = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, path)
	if path == "" {
		path = "/"
	}
	if len(path) > maxPathLength {
		path = strings.ToValidUTF8(path[:maxPathLength], "")
	}
	return path
}

// --- who asks ------------------------------------------------------------------------------------

// Agent kinds.
const (
	KindBot     = "bot"     // crawlers and link previews: they announce themselves
	KindTool    = "tool"    // curl, libraries, scanners
	KindBrowser = "browser" // people, most likely
	KindUnknown = "unknown"
)

// Agent is a client family: «Googlebot», «Chrome», «curl».
type Agent struct {
	Name string
	Kind string
}

// IsBot reports whether the agent is an automated client of any sort.
func (a Agent) IsBot() bool { return a.Kind == KindBot || a.Kind == KindTool }

var knownAgents = []struct {
	pattern *regexp.Regexp
	agent   Agent
}{
	// Search engines (brief B6 asks for Googlebot and Bingbot by name).
	{regexp.MustCompile(`(?i)googlebot|adsbot-google|google-inspectiontool|googleother|mediapartners-google|storebot-google|google-extended`), Agent{"Googlebot", KindBot}},
	{regexp.MustCompile(`(?i)bingbot|bingpreview|adidxbot|msnbot`), Agent{"Bingbot", KindBot}},
	{regexp.MustCompile(`(?i)yandex(bot|images|mobilebot|metrika|webmaster|favicons)`), Agent{"YandexBot", KindBot}},
	{regexp.MustCompile(`(?i)duckduck(bot|go-favicons)`), Agent{"DuckDuckBot", KindBot}},
	{regexp.MustCompile(`(?i)applebot`), Agent{"Applebot", KindBot}},
	{regexp.MustCompile(`(?i)baiduspider`), Agent{"Baiduspider", KindBot}},
	{regexp.MustCompile(`(?i)petalbot`), Agent{"PetalBot", KindBot}},
	{regexp.MustCompile(`(?i)seznambot`), Agent{"SeznamBot", KindBot}},
	{regexp.MustCompile(`(?i)qwantbot|qwantify`), Agent{"Qwantbot", KindBot}},
	// AI crawlers.
	{regexp.MustCompile(`(?i)gptbot|chatgpt-user|oai-searchbot`), Agent{"OpenAI", KindBot}},
	{regexp.MustCompile(`(?i)claudebot|claude-web|claude-user|claude-searchbot|anthropic-ai`), Agent{"Anthropic", KindBot}},
	{regexp.MustCompile(`(?i)perplexitybot|perplexity-user`), Agent{"PerplexityBot", KindBot}},
	{regexp.MustCompile(`(?i)ccbot`), Agent{"CCBot", KindBot}},
	{regexp.MustCompile(`(?i)bytespider`), Agent{"Bytespider", KindBot}},
	{regexp.MustCompile(`(?i)amazonbot`), Agent{"Amazonbot", KindBot}},
	{regexp.MustCompile(`(?i)meta-externalagent|meta-externalfetcher|facebookbot`), Agent{"Meta AI", KindBot}},
	// SEO tools.
	{regexp.MustCompile(`(?i)ahrefs(bot|siteaudit)`), Agent{"AhrefsBot", KindBot}},
	{regexp.MustCompile(`(?i)semrushbot|siteauditbot|splitsignalbot`), Agent{"SemrushBot", KindBot}},
	{regexp.MustCompile(`(?i)mj12bot`), Agent{"MJ12bot", KindBot}},
	{regexp.MustCompile(`(?i)dotbot`), Agent{"DotBot", KindBot}},
	{regexp.MustCompile(`(?i)dataforseobot`), Agent{"DataForSeoBot", KindBot}},
	{regexp.MustCompile(`(?i)serpstatbot`), Agent{"SerpstatBot", KindBot}},
	// Link previews of messengers and social networks.
	{regexp.MustCompile(`(?i)telegrambot`), Agent{"Telegram (превью)", KindBot}},
	{regexp.MustCompile(`(?i)whatsapp`), Agent{"WhatsApp (превью)", KindBot}},
	{regexp.MustCompile(`(?i)facebookexternalhit|facebookcatalog`), Agent{"Facebook (превью)", KindBot}},
	{regexp.MustCompile(`(?i)twitterbot`), Agent{"X / Twitter (превью)", KindBot}},
	{regexp.MustCompile(`(?i)linkedinbot`), Agent{"LinkedIn (превью)", KindBot}},
	{regexp.MustCompile(`(?i)discordbot`), Agent{"Discord (превью)", KindBot}},
	{regexp.MustCompile(`(?i)slackbot|slack-imgproxy`), Agent{"Slack (превью)", KindBot}},
	{regexp.MustCompile(`(?i)viber`), Agent{"Viber (превью)", KindBot}},
	{regexp.MustCompile(`(?i)skypeuripreview`), Agent{"Skype (превью)", KindBot}},
	// Monitoring.
	{regexp.MustCompile(`(?i)uptimerobot`), Agent{"UptimeRobot", KindBot}},
	{regexp.MustCompile(`(?i)pingdom`), Agent{"Pingdom", KindBot}},
	{regexp.MustCompile(`(?i)statuscake|site24x7|betteruptime|better uptime|hetrixtools|freshping`), Agent{"Мониторинг", KindBot}},
	{regexp.MustCompile(`(?i)chrome-lighthouse|pagespeed|gtmetrix`), Agent{"Lighthouse / PageSpeed", KindBot}},
	{regexp.MustCompile(`(?i)let's encrypt validation`), Agent{"Let's Encrypt", KindBot}},
	// Internet-wide scanners.
	{regexp.MustCompile(`(?i)censys`), Agent{"Censys", KindTool}},
	{regexp.MustCompile(`(?i)zgrab`), Agent{"zgrab", KindTool}},
	{regexp.MustCompile(`(?i)masscan`), Agent{"masscan", KindTool}},
	{regexp.MustCompile(`(?i)nmap`), Agent{"nmap", KindTool}},
	{regexp.MustCompile(`(?i)nuclei`), Agent{"Nuclei", KindTool}},
	{regexp.MustCompile(`(?i)expanse|paloalto|cortex-xpanse`), Agent{"Palo Alto Expanse", KindTool}},
	{regexp.MustCompile(`(?i)internet-?measurement|internetmeasurement`), Agent{"InternetMeasurement", KindTool}},
	{regexp.MustCompile(`(?i)shodan`), Agent{"Shodan", KindTool}},
	{regexp.MustCompile(`(?i)l9(explore|tcpid)|leakix`), Agent{"LeakIX", KindTool}},
	{regexp.MustCompile(`(?i)netcraft`), Agent{"Netcraft", KindTool}},
	{regexp.MustCompile(`(?i)wpscan|sqlmap|nikto|dirbuster|gobuster|feroxbuster|ffuf|wfuzz|acunetix|nessus|openvas`), Agent{"Сканер уязвимостей", KindTool}},
	// Libraries and command-line tools.
	{regexp.MustCompile(`(?i)^curl/`), Agent{"curl", KindTool}},
	{regexp.MustCompile(`(?i)^wget/`), Agent{"Wget", KindTool}},
	{regexp.MustCompile(`(?i)python-requests|python-urllib|python-httpx|aiohttp|^python/|scrapy`), Agent{"Python", KindTool}},
	{regexp.MustCompile(`(?i)go-http-client|^go-resty|fasthttp`), Agent{"Go", KindTool}},
	{regexp.MustCompile(`(?i)okhttp|^java/|apache-httpclient|^jakarta`), Agent{"Java", KindTool}},
	{regexp.MustCompile(`(?i)node-fetch|axios|^got |undici|^node`), Agent{"Node.js", KindTool}},
	{regexp.MustCompile(`(?i)libwww-perl|lwp::`), Agent{"Perl", KindTool}},
	{regexp.MustCompile(`(?i)^ruby|faraday|httpclient`), Agent{"Ruby", KindTool}},
	{regexp.MustCompile(`(?i)php/|guzzlehttp`), Agent{"PHP", KindTool}},
	{regexp.MustCompile(`(?i)headlesschrome|phantomjs|puppeteer|playwright|selenium`), Agent{"Headless-браузер", KindTool}},
}

// A bot we have no name for still says «bot» somewhere: «Mozilla/5.0 (compatible; FooBot/1.2; +http://…)».
var reBotToken = regexp.MustCompile(`(?i)([a-z0-9][a-z0-9_.-]{1,38}(?:bot|crawler|spider|crawl))\b`)

// ClassifyAgent names the client family behind a User-Agent string. Only the family is ever
// stored: the full string is as good as a fingerprint.
func ClassifyAgent(userAgent string) Agent {
	ua := strings.TrimSpace(userAgent)
	if ua == "" || ua == "-" {
		return Agent{"без User-Agent", KindUnknown}
	}
	for _, known := range knownAgents {
		if known.pattern.MatchString(ua) {
			return known.agent
		}
	}
	if token := reBotToken.FindStringSubmatch(ua); token != nil {
		return Agent{token[1], KindBot}
	}
	if !strings.HasPrefix(ua, "Mozilla/") && !strings.HasPrefix(ua, "Opera/") {
		return Agent{"другие программы", KindTool} // every browser since the nineties starts with «Mozilla/»
	}
	if analytics.IsBot(ua) {
		return Agent{"другие боты", KindBot}
	}
	client := analytics.ParseUserAgent(ua)
	if client.Browser == "" || client.Browser == "Other" {
		return Agent{"другие браузеры", KindBrowser}
	}
	return Agent{client.Browser, KindBrowser}
}

// --- what they look for --------------------------------------------------------------------------

// This site is static files plus /api/: nothing below ever existed here, so whoever asks for it
// is feeling for holes (brief B6: «подозрительные сканеры (/wp-admin, .env и т.п.)»).
var probePatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"exploit", regexp.MustCompile(`(?i)\.\./|%2e%2e|%00|/etc/passwd|\$\{jndi:|/bin/(ba)?sh|eval-stdin|<script|union(\+|%20)select|/proc/self`)},
	{"wordpress", regexp.MustCompile(`(?i)/wp-(admin|login|content|includes|json|config)|/xmlrpc\.php|/wordpress|/wp/`)},
	{".env", regexp.MustCompile(`(?i)\.env(\.|/|$)`)},
	{".git", regexp.MustCompile(`(?i)/\.(git|svn|hg)(/|$)`)},
	{"секреты и бэкапы", regexp.MustCompile(`(?i)/\.(aws|ssh|docker|kube|npmrc|htpasswd|htaccess|ds_store|vscode|idea)|id_rsa|/credentials|/secrets?\b|/config\.(json|ya?ml|php|js)$|/(backup|dump|database|db)\b.*\.(sql|zip|gz|tar|bak)|\.(sql|bak|old|swp)(\.gz)?$`)},
	{"phpMyAdmin", regexp.MustCompile(`(?i)phpmyadmin|/pma/|/myadmin|/adminer|/mysql/`)},
	{"php", regexp.MustCompile(`(?i)\.php\d?(/|$)|/vendor/|/cgi-bin/`)},
	{"панели и API", regexp.MustCompile(`(?i)^/(admin|administrator|manager|login|signin|user/login|console|dashboard|actuator|solr|jenkins|owa|ecp|autodiscover|boaform|hnap1|gponform|remote|portal|webui|api/v\d|v\d/|graphql|\.well-known/security)`)},
}

// Probe names what a request was looking for, or returns "" for an ordinary one.
func Probe(path string) string {
	for _, probe := range probePatterns {
		if probe.pattern.MatchString(path) {
			return probe.name
		}
	}
	return ""
}

// clientPrefix truncates an address the way the rest of the project does: /24 and /48.
func clientPrefix(address string) string {
	ip := net.ParseIP(address)
	if ip == nil {
		return "unknown"
	}
	return analytics.TruncateIP(ip)
}
