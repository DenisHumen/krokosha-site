package nginxlog

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

var quiet = slog.New(slog.DiscardHandler)

const (
	chrome    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	googlebot = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
)

// logLine writes a request the way nginx does (deploy/nginx/krokosha-http.conf).
func logLine(at time.Time, address, method, uri string, status int, bytesSent int, seconds float64, userAgent string) string {
	return fmt.Sprintf(`{"time":"%s","remote_addr":"%s","host":"krokosha.xyz","method":"%s","uri":"%s","protocol":"HTTP/2.0","status":%d,"bytes_sent":%d,"request_time":%.3f,"referer":"","user_agent":"%s"}`+"\n",
		at.Format("2006-01-02T15:04:05-07:00"), address, method, uri, status, bytesSent, seconds, userAgent)
}

func TestParseLine(t *testing.T) {
	kyiv := time.FixedZone("EEST", 3*3600)
	line := logLine(time.Date(2026, 9, 19, 0, 30, 5, 0, kyiv), "203.0.113.7", "GET", "/uk/?utm_source=x&email=someone@example.com", 200, 5120, 0.004, chrome)
	entry, err := ParseLine([]byte(strings.TrimSpace(line)))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path != "/uk/" {
		t.Errorf("path = %q: the query string must go, it may carry personal data", entry.Path)
	}
	if !entry.Time.Equal(time.Date(2026, 9, 18, 21, 30, 5, 0, time.UTC)) || entry.Status != 200 || entry.BytesSent != 5120 || entry.RequestTime != 4*time.Millisecond {
		t.Errorf("entry: %+v", entry)
	}

	// What nginx escapes comes back as text; what cannot be displayed is dropped.
	odd, err := ParseLine([]byte(`{"time":"2026-09-19T00:30:05+03:00","remote_addr":"::1","method":"GET","uri":"/a\u0000b\"<script>\\x/` + strings.Repeat("я", 300) + `","status":404,"bytes_sent":0,"request_time":0.000,"user_agent":"-"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(odd.Path, `/ab"<script>\x/яяя`) || len(odd.Path) > maxPathLength || !strings.HasSuffix(odd.Path, "я") {
		t.Errorf("path = %q (%d bytes)", odd.Path, len(odd.Path))
	}

	for name, bad := range map[string]string{
		"not JSON":        `203.0.113.7 - - [19/Sep/2026:00:30:05 +0300] "GET / HTTP/1.1" 200 512`,
		"half a line":     `{"time":"2026-09-19T00:30:05+03:00","remote_addr":"203.0.`,
		"no time":         `{"remote_addr":"203.0.113.7","uri":"/","status":200}`,
		"status 0":        `{"time":"2026-09-19T00:30:05+03:00","uri":"/","status":0}`,
		"status as words": `{"time":"2026-09-19T00:30:05+03:00","uri":"/","status":"OK"}`,
	} {
		if _, err := ParseLine([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestClassifyAgent(t *testing.T) {
	for userAgent, want := range map[string]Agent{
		googlebot: {"Googlebot", KindBot},
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm) Chrome/116.0.1938.76 Safari/537.36": {"Bingbot", KindBot},
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)":                                           {"OpenAI", KindBot},
		"Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)":                                                                               {"AhrefsBot", KindBot},
		"TelegramBot (like TwitterBot)":                                                         {"Telegram (превью)", KindBot},
		"Mozilla/5.0 (compatible; SomeNewCrawler/3.1; +https://example.com/bot)":                {"SomeNewCrawler", KindBot},
		"Mozilla/5.0 (compatible; InternetMeasurement/1.0; +https://internet-measurement.com/)": {"InternetMeasurement", KindTool},
		"curl/8.5.0":             {"curl", KindTool},
		"python-requests/2.32.3": {"Python", KindTool},
		"Go-http-client/2.0":     {"Go", KindTool},
		"Hello World":            {"другие программы", KindTool},
		chrome:                   {"Chrome", KindBrowser},
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1": {"Safari", KindBrowser},
		"":  {"без User-Agent", KindUnknown},
		"-": {"без User-Agent", KindUnknown},
	} {
		if got := ClassifyAgent(userAgent); got != want {
			t.Errorf("ClassifyAgent(%q) = %+v, want %+v", userAgent, got, want)
		}
	}
}

func TestProbe(t *testing.T) {
	for path, want := range map[string]string{
		"/":                                 "",
		"/uk/":                              "",
		"/privacy/":                         "",
		"/_astro/index.Bx81kQ.css":          "",
		"/assets/analytics.js":              "",
		"/api/e":                            "",
		"/favicon.ico":                      "",
		"/robots.txt":                       "",
		"/sitemap.xml":                      "",
		"/.well-known/acme-challenge/token": "",
		"/wp-login.php":                     "wordpress",
		"/blog/wp-admin/setup-config.php":   "wordpress",
		"/.env":                             ".env",
		"/api/.env.production":              ".env",
		"/.git/config":                      ".git",
		"/.aws/credentials":                 "секреты и бэкапы",
		"/backup.sql.gz":                    "секреты и бэкапы",
		"/phpmyadmin/index.php":             "phpMyAdmin",
		"/index.php":                        "php",
		"/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php": "exploit",
		"/cgi-bin/luci":            "php",
		"/admin/":                  "панели и API",
		"/actuator/health":         "панели и API",
		"/static/../../etc/passwd": "exploit",
		"/?x=${jndi:ldap://evil}":  "exploit",
	} {
		if got := Probe(path); got != want {
			t.Errorf("Probe(%q) = %q, want %q", path, got, want)
		}
	}
}

// --- the reader, against a real database -----------------------------------------------------------

type fixture struct {
	t      *testing.T
	db     *sql.DB
	path   string
	reader *Reader
}

func newFixture(t *testing.T, maxPaths int) *fixture {
	t.Helper()
	cfg := testenv.MySQL(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, cfg, migrations.Files, quiet); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, cfg, 10*time.Second, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store, err := cache.New(ctx, "", quiet)
	if err != nil {
		t.Fatal(err)
	}
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "nginx-access.json.log")
	return &fixture{t: t, db: pool, path: path,
		reader: New(Options{Path: path, DB: pool, Cache: store, Location: kyiv, Log: quiet, MaxPaths: maxPaths})}
}

func (f *fixture) write(lines ...string) {
	f.t.Helper()
	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		f.t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(strings.Join(lines, "")); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) poll(want int) {
	f.t.Helper()
	got, err := f.reader.Poll(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	if got != want {
		f.t.Fatalf("poll digested %d lines, want %d", got, want)
	}
}

func (f *fixture) value(query string, args ...any) string {
	f.t.Helper()
	var out sql.NullString
	if err := f.db.QueryRow(query, args...).Scan(&out); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	return out.String
}

var noon = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC) // 12:00 in Kyiv

func TestReaderAggregates(t *testing.T) {
	f := newFixture(t, DefaultMaxPaths)
	f.poll(0) // no log yet: nothing to do, nothing to complain about twice

	f.write(
		logLine(noon, "203.0.113.7", "GET", "/", 200, 9000, 0.002, chrome),
		logLine(noon.Add(10*time.Second), "203.0.113.7", "GET", "/_astro/a.css", 200, 3000, 0.001, chrome),
		logLine(noon.Add(20*time.Second), "66.249.66.1", "GET", "/uk/?from=search", 200, 8000, 0.030, googlebot),
		logLine(noon.Add(30*time.Second), "66.249.66.1", "GET", "/old-page", 404, 500, 0.003, googlebot),
		logLine(noon.Add(70*time.Second), "198.51.100.9", "GET", "/.env", 404, 500, 0.002, "python-requests/2.32.3"),
		logLine(noon.Add(71*time.Second), "198.51.100.9", "POST", "/wp-login.php", 404, 500, 0.002, "python-requests/2.32.3"),
		logLine(noon.Add(72*time.Second), "198.51.100.77", "GET", "/.env.bak", 404, 500, 7.5, "python-requests/2.32.3"),
		logLine(noon.Add(73*time.Second), "203.0.113.7", "POST", "/api/e", 204, 120, 0.004, chrome),
		logLine(noon.Add(74*time.Second), "203.0.113.7", "GET", "/api/health", 503, 80, 0.300, chrome),
		"this line is not JSON\n",
	)
	f.poll(9)

	for query, want := range map[string]string{
		`SELECT COUNT(*) FROM traffic_minutes`: "2",
		`SELECT CONCAT_WS(' ', requests, bytes_sent, status_2xx, status_4xx, bot_requests, not_found, probes) FROM traffic_minutes WHERE minute = '2026-09-19 09:00:00'`: "4 20500 3 1 2 1 0",
		`SELECT CONCAT_WS(' ', requests, status_2xx, status_4xx, status_5xx, bot_requests, not_found, probes) FROM traffic_minutes WHERE minute = '2026-09-19 09:01:00'`: "5 1 3 1 3 3 3",
		`SELECT CONCAT_WS(' ', SUM(t_5ms), SUM(t_50ms), SUM(t_500ms), SUM(t_slow)) FROM traffic_minutes`:                                                                 "6 1 1 1",
		`SELECT CONCAT_WS(' ', bot_clients, other_clients) FROM traffic_hours WHERE hour = '2026-09-19 09:00:00'`:                                                        "3 1",
		`SELECT CONCAT_WS(' ', hits, bytes_sent) FROM traffic_paths WHERE day = '2026-09-19' AND path = '/uk/' AND status = 200`:                                         "1 8000",
		`SELECT COUNT(*) FROM traffic_paths WHERE path LIKE '%?%'`:                                                                                                       "0",
		`SELECT CONCAT_WS(' ', kind, hits) FROM traffic_agents WHERE agent = 'Googlebot'`:                                                                                "bot 2",
		`SELECT CONCAT_WS(' ', kind, hits) FROM traffic_agents WHERE agent = 'Python'`:                                                                                   "tool 3",
		`SELECT CONCAT_WS(' ', kind, hits) FROM traffic_agents WHERE agent = 'Chrome'`:                                                                                   "browser 4",
		`SELECT GROUP_CONCAT(CONCAT_WS(' ', pattern, hits, last_path) ORDER BY pattern SEPARATOR '; ') FROM traffic_probes WHERE ip_prefix = '198.51.100.0/24'`:          ".env 2 /.env.bak; wordpress 1 /wp-login.php",
	} {
		if got := f.value(query); got != want {
			t.Errorf("%s\n got %q\nwant %q", query, got, want)
		}
	}
	// Nothing that identifies anybody is kept.
	for _, table := range []string{"traffic_paths", "traffic_agents", "traffic_probes"} {
		rows := f.value(`SELECT GROUP_CONCAT(CONCAT_WS(' ', ` + map[string]string{
			"traffic_paths": "path", "traffic_agents": "agent", "traffic_probes": "CONCAT(ip_prefix, last_path)"}[table] + `)) FROM ` + table)
		for _, secret := range []string{"203.0.113.7", "198.51.100.9", "66.249.66.1", "Mozilla", "python-requests"} {
			if strings.Contains(rows, secret) {
				t.Errorf("%s keeps %q", table, secret)
			}
		}
	}

	// Nothing new: nothing happens. A line that nginx has not finished yet waits for its end.
	f.poll(0)
	half := logLine(noon.Add(2*time.Minute), "203.0.113.7", "GET", "/ru/", 200, 7000, 0.002, chrome)
	f.write(half[:40])
	f.poll(0)
	f.write(half[40:])
	f.poll(1)
	if got := f.value(`SELECT SUM(requests) FROM traffic_minutes`); got != "10" {
		t.Errorf("requests = %s, want 10: every line exactly once", got)
	}
}

func TestReaderSurvivesRestartAndRotation(t *testing.T) {
	f := newFixture(t, DefaultMaxPaths)
	f.write(logLine(noon, "203.0.113.7", "GET", "/", 200, 100, 0.001, chrome))
	f.poll(1)

	// The service restarts: a new reader finds its place in the database.
	f.reader = New(f.reader.opts)
	f.write(logLine(noon.Add(time.Second), "203.0.113.7", "GET", "/uk/", 200, 100, 0.001, chrome))
	f.poll(1)

	// logrotate: the file moves to «.1», nginx keeps writing to it until it is told to reopen,
	// then starts a new one.
	f.write(logLine(noon.Add(2*time.Second), "203.0.113.7", "GET", "/ru/", 200, 100, 0.001, chrome))
	if err := os.Rename(f.path, f.path+".1"); err != nil {
		t.Fatal(err)
	}
	f.write(logLine(noon.Add(time.Hour), "203.0.113.7", "GET", "/privacy/", 200, 100, 0.001, chrome))
	f.poll(2) // the tail of the old file and the head of the new one
	f.write(logLine(noon.Add(time.Hour+time.Second), "203.0.113.7", "GET", "/", 200, 100, 0.001, chrome))
	f.poll(1)

	// copytruncate, or an operator emptying the file: start over.
	if err := os.WriteFile(f.path, []byte(logLine(noon.Add(2*time.Hour), "203.0.113.7", "GET", "/", 200, 100, 0.001, chrome)), 0o640); err != nil {
		t.Fatal(err)
	}
	f.poll(1)

	if got := f.value(`SELECT SUM(requests) FROM traffic_minutes`); got != "6" {
		t.Errorf("requests = %s, want 6: nothing lost, nothing counted twice", got)
	}
	if got := f.value(`SELECT GROUP_CONCAT(CONCAT_WS(' ', path, hits) ORDER BY path SEPARATOR '; ') FROM traffic_paths`); got != "/ 3; /privacy/ 1; /ru/ 1; /uk/ 1" {
		t.Errorf("paths: %s", got)
	}
}

func TestAScannerCannotBloatTheTable(t *testing.T) {
	f := newFixture(t, 5)
	var lines []string
	for i := range 40 {
		lines = append(lines, logLine(noon.Add(time.Duration(i)*time.Second), "198.51.100.9", "GET", fmt.Sprintf("/guess-%d", i), 404, 100, 0.001, "curl/8.5.0"))
	}
	lines = append(lines, logLine(noon.Add(time.Minute), "203.0.113.7", "GET", "/", 200, 100, 0.001, chrome))
	f.write(lines...)
	f.poll(41)
	f.write(logLine(noon.Add(2*time.Minute), "203.0.113.7", "GET", "/", 200, 100, 0.001, chrome),
		logLine(noon.Add(2*time.Minute), "198.51.100.9", "GET", "/guess-new", 404, 100, 0.001, "curl/8.5.0"))
	f.poll(2)

	if got := f.value(`SELECT COUNT(*) FROM traffic_paths`); got != "6" { // five kept and «other»
		t.Errorf("rows = %s, want 6", got)
	}
	if got := f.value(`SELECT hits FROM traffic_paths WHERE path = '/' AND status = 200`); got != "2" {
		t.Errorf("the home page has %s hits, want 2: real pages come first and keep counting", got)
	}
	if got := f.value(`SELECT SUM(hits) FROM traffic_paths`); got != "43" {
		t.Errorf("hits in total = %s, want 43: folded, not dropped", got)
	}
	if got := f.value(`SELECT hits FROM traffic_paths WHERE path = ?`, otherPaths); got != "37" {
		t.Errorf("«other addresses» = %s, want 37", got)
	}
}

func TestTrafficReport(t *testing.T) {
	f := newFixture(t, DefaultMaxPaths)
	evening := time.Date(2026, 9, 18, 21, 30, 0, 0, time.UTC) // 00:30 on the 19th in Kyiv: the owner's day decides
	var lines []string
	for i := range 30 { // a scanner at work: a spike of 404s within one hour
		lines = append(lines, logLine(noon.Add(time.Duration(i)*time.Second), "198.51.100.9", "GET", fmt.Sprintf("/wp-content/plugins/p%d.php", i), 404, 200, 0.002, "python-requests/2.32.3"))
	}
	lines = append(lines,
		logLine(evening, "203.0.113.7", "GET", "/", 200, 9000, 0.004, chrome),
		logLine(noon, "203.0.113.7", "GET", "/uk/", 200, 8000, 0.020, chrome),
		logLine(noon.Add(time.Minute), "66.249.66.1", "GET", "/uk/", 200, 8000, 0.040, googlebot),
		logLine(noon.Add(2*time.Minute), "203.0.113.7", "GET", "/api/health", 502, 100, 1.5, chrome),
		logLine(noon.Add(26*time.Hour), "203.0.113.7", "GET", "/", 200, 9000, 0.004, chrome), // the 20th
	)
	f.write(lines...)
	f.poll(35)

	kyiv, _ := time.LoadLocation("Europe/Kyiv")
	reports := NewReports(f.db, kyiv)
	day := time.Date(2026, 9, 19, 0, 0, 0, 0, kyiv)
	report, err := reports.Traffic(context.Background(), analytics.Period{From: day, To: day, Kind: "day"})
	if err != nil {
		t.Fatal(err)
	}

	want := Totals{Requests: 34, Bots: 31, NotFound: 30, Probes: 30, Bytes: 30*200 + 9000 + 8000 + 8000 + 100, Status: [4]int{3, 0, 30, 1}}
	if report.Totals != want {
		t.Errorf("totals = %+v\n   want %+v", report.Totals, want)
	}
	if !report.Hourly || len(report.Timeline) != 24 {
		t.Fatalf("timeline: hourly=%v, %d buckets", report.Hourly, len(report.Timeline))
	}
	if got := report.Timeline[0]; got.Requests != 1 || got.Bytes != 9000 {
		t.Errorf("00:00 Kyiv (21:00 UTC the day before): %+v", got)
	}
	if got := report.Timeline[12]; got.Requests != 33 || got.Bots != 31 || got.NotFound != 30 || got.Errors != 1 || got.BotClients != 2 {
		t.Errorf("12:00 Kyiv: %+v", got)
	}
	if report.Median != "≤ 5 мс" || report.P95 != "≤ 50 мс" {
		t.Errorf("response time: median %s, p95 %s", report.Median, report.P95)
	}
	if len(report.Spikes) != 1 || report.Spikes[0].Start.Hour() != 12 || report.Spikes[0].NotFound != 30 || report.Spikes[0].Probes != 30 {
		t.Errorf("spikes: %+v", report.Spikes)
	}
	if len(report.Pages) != 2 || report.Pages[0].Path != "/uk/" || report.Pages[0].Hits != 2 || report.Pages[1].Path != "/" {
		t.Errorf("pages: %+v", report.Pages)
	}
	if len(report.Missing) != 12 || len(report.Failed) != 1 || report.Failed[0].Path != "/api/health" || report.Failed[0].Status != 502 {
		t.Errorf("missing: %d, failed: %+v", len(report.Missing), report.Failed)
	}
	if len(report.Bots) != 1 || report.Bots[0].Agent != "Googlebot" || report.Bots[0].Hits != 1 {
		t.Errorf("bots: %+v", report.Bots)
	}
	if len(report.Clients) != 2 || report.Clients[0].Agent != "Python" || report.Clients[0].Kind != KindTool || report.Clients[1].Agent != "Chrome" {
		t.Errorf("clients: %+v", report.Clients)
	}
	if len(report.Probes) != 1 || report.Probes[0].Pattern != "wordpress" || report.Probes[0].Hits != 30 || report.Probes[0].Networks != 1 {
		t.Errorf("probes: %+v", report.Probes)
	}
	if len(report.Networks) != 1 || report.Networks[0].Prefix != "198.51.100.0/24" || report.Networks[0].Patterns != "wordpress" {
		t.Errorf("networks: %+v", report.Networks)
	}
	if report.ReadAt.IsZero() || f.reader.LastPoll().IsZero() {
		t.Error("the report does not say when the log was read")
	}

	// A week: one bar per day, the 19th and the 20th have traffic.
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, kyiv)
	week, err := reports.Traffic(context.Background(), analytics.Period{From: monday, To: monday.AddDate(0, 0, 6), Kind: "week"})
	if err != nil {
		t.Fatal(err)
	}
	if week.Hourly || len(week.Timeline) != 7 || week.Timeline[5].Requests != 34 || week.Timeline[6].Requests != 1 || week.Timeline[4].Requests != 0 {
		t.Errorf("week: %+v", week.Timeline)
	}

	empty, err := reports.Traffic(context.Background(), analytics.Period{From: monday.AddDate(0, -2, 0), To: monday.AddDate(0, -2, 0), Kind: "day"})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Totals.Requests != 0 || empty.Median != "—" || len(empty.Pages) != 0 {
		t.Errorf("an empty day: %+v", empty.Totals)
	}
}

func TestPercentile(t *testing.T) {
	histogram := make([]int, len(latencyLabels))
	histogram[0], histogram[3], histogram[10] = 90, 9, 1 // 90 fast, 9 middling, one very slow
	for p, want := range map[int]string{50: "≤ 5 мс", 90: "≤ 5 мс", 91: "≤ 50 мс", 99: "≤ 50 мс", 100: "> 5 с"} {
		if got := percentile(histogram, p); got != want {
			t.Errorf("p%d = %s, want %s", p, got, want)
		}
	}
}
