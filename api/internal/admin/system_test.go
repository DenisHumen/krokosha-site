package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/geo"
	"github.com/DenisHumen/krokosha-site/api/internal/nginxlog"
)

// seedTraffic feeds a small access log through the real reader: a person, Googlebot and a
// scanner, all at nine in the morning of the report day.
func (s *site) seedTraffic() {
	s.t.Helper()
	const browser = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	at := reportDay.Add(-time.Hour)
	line := func(address, uri string, status, size int, seconds float64, agent string) string {
		return fmt.Sprintf(`{"time":"%s","remote_addr":"%s","host":"krokosha.xyz","method":"GET","uri":"%s","protocol":"HTTP/2.0","status":%d,"bytes_sent":%d,"request_time":%.3f,"referer":"","user_agent":"%s"}`+"\n",
			at.Format(time.RFC3339), address, uri, status, size, seconds, agent)
	}
	log := line("203.0.113.7", "/uk/", 200, 2_500_000, 0.004, browser) +
		line("66.249.66.1", "/", 200, 9000, 0.030, "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)") +
		line("198.51.100.9", "/.env", 404, 300, 0.002, "curl/8.5.0") +
		line("198.51.100.9", "/<script>alert(1)</script>", 404, 300, 0.002, "curl/8.5.0")
	path := filepath.Join(s.t.TempDir(), "access.log")
	if err := os.WriteFile(path, []byte(log), 0o640); err != nil {
		s.t.Fatal(err)
	}
	store, err := cache.New(context.Background(), "", quiet)
	if err != nil {
		s.t.Fatal(err)
	}
	reader := nginxlog.New(nginxlog.Options{Path: path, DB: s.db, Cache: store, Location: time.UTC, Log: quiet})
	if lines, err := reader.Poll(context.Background()); err != nil || lines != 4 {
		s.t.Fatalf("reading the log: %d lines, %v", lines, err)
	}
}

func TestTrafficScreen(t *testing.T) {
	s := newSite(t)
	s.seedTraffic()
	s.signIn()

	page := s.do(http.MethodGet, prefix+"/traffic", nil, nil)
	if page.status != http.StatusOK {
		t.Fatalf("traffic: %d", page.status)
	}
	for _, want := range []string{
		"Трафик · лог сервера", `title="Суббота, 19 сентября 2026"`,
		`title="09:00 — 4 запроса, из них боты и программы: 3; не найдено: 2; ошибок сервера: 0"`,
		`title="09:00 — 2,4 МБ"`,
		"Googlebot", "curl", "Chrome", ".env", "198.51.100.0/24",
		"2xx — отдано", "≤ 5 мс",
		"прочитан только что",
		`href="` + prefix + `/traffic?d=2026-09-18&amp;p=day"`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the traffic screen lacks %q", want)
		}
	}
	if strings.Contains(page.body, "<script>alert") || strings.Contains(page.body, "203.0.113.7") || strings.Contains(page.body, "66.249.66.1") {
		t.Error("a requested path reached the page unescaped, or a full address is shown")
	}

	// The overview's visits know about the bots from the same data: in the tooltips and below the chart.
	overview := s.do(http.MethodGet, prefix+"/", nil, nil)
	if !strings.Contains(overview.body, `ботов: 2"`) || !strings.Contains(overview.body, "Боты и программы (по логу сервера): 2") {
		t.Error("the overview does not show the bots that the server log knows about")
	}
	if empty := s.do(http.MethodGet, prefix+"/?p=day&d=2026-09-10", nil, nil); strings.Contains(empty.body, "Боты и программы (по логу сервера)") {
		t.Error("bots are counted for a day the log says nothing about")
	}

	s.cookie = ""
	if got := s.do(http.MethodGet, prefix+"/traffic", nil, nil); got.status != http.StatusSeeOther {
		t.Errorf("anonymous traffic screen: %d", got.status)
	}
}

func TestStatusScreenAndRebuildButton(t *testing.T) {
	s := newSite(t)
	s.signIn()

	page := s.do(http.MethodGet, prefix+"/status", nil, nil)
	if page.status != http.StatusOK {
		t.Fatalf("status: %d", page.status)
	}
	for _, want := range []string{"Сайт ещё ни разу не собирался", "Пересобрать сейчас", "MySQL", `версия <span class="mono">test</span>`, "не настроен"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the status screen lacks %q", want)
		}
	}
	if strings.Contains(page.body, "Сертификат") {
		t.Error("a certificate card on a site without HTTPS")
	}
	// The reports of the night: a backup, and the certificate watch with one host that answered
	// and one that did not (days_left null). The page must draw both.
	for name, report := range map[string]string{
		"backup.json":    `{"started_at":"2026-09-22T03:30:00Z","finished_at":"2026-09-22T03:31:10Z","ok":true,"name":"20260922-033000","bytes":3600000,"copied_to":"","error":""}`,
		"certwatch.json": `{"checked_at":"2026-09-22T04:40:00Z","ok":true,"renewed":false,"certificates":[{"name":"site","host":"krokosha.xyz","days_left":61,"not_after":"2026-11-22T00:00:00Z"},{"name":"mail","host":"mail.krokosha.xyz","days_left":null,"not_after":""}],"error":""}`,
	} {
		if err := os.MkdirAll(filepath.Join(s.state, "status"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.state, "status", name), []byte(report), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	page = s.do(http.MethodGet, prefix+"/status", nil, nil)
	if page.status != http.StatusOK {
		t.Fatalf("status with the reports of the night: %d", page.status)
	}
	if err := os.WriteFile(filepath.Join(s.state, "status", "fail2ban.json"),
		[]byte(`{"checked_at":"2026-09-22T04:40:00Z","ok":true,"jails":[{"name":"sshd","banned":2,"total":40},{"name":"krokosha-admin","banned":1,"total":3}],"error":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	page = s.do(http.MethodGet, prefix+"/status", nil, nil)
	for _, want := range []string{"сайт: 61 день", "почта: не отвечает", "3,4 МБ", "только на этом диске", "3 адреса в бане · sshd: 2 сейчас, 40 всего · krokosha-admin: 1 сейчас, 3 всего"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the status screen lacks %q", want)
		}
	}

	// «Backup now» leaves a request for krokosha-backup-now.path, and the button waits for it.
	if got := s.do(http.MethodPost, prefix+"/status/backup", url.Values{"csrf": {s.csrf()}}, nil); got.status != http.StatusSeeOther || got.location != prefix+"/status?ok=backup" {
		t.Fatalf("backup now: %d %s", got.status, got.location)
	}
	if _, err := os.Stat(filepath.Join(s.state, "requests", "backup")); err != nil {
		t.Errorf("no request for a backup: %v", err)
	}
	if after := s.do(http.MethodGet, prefix+"/status?ok=backup", nil, nil); !strings.Contains(after.body, "Резервная копия запрошена") || !strings.Contains(after.body, "Запрошена…") {
		t.Error("the page does not say the backup was asked for")
	}
	if got := s.do(http.MethodPost, prefix+"/status/backup", url.Values{"csrf": {"forged"}}, nil); got.status != http.StatusForbidden {
		t.Errorf("backup without the CSRF token: %d", got.status)
	}

	request := filepath.Join(s.state, "requests", "rebuild")
	if got := s.do(http.MethodPost, prefix+"/status/rebuild", url.Values{"csrf": {"forged"}}, nil); got.status != http.StatusForbidden {
		t.Errorf("rebuild without the CSRF token: %d", got.status)
	}
	if _, err := os.Stat(request); err == nil {
		t.Fatal("a forged request started a rebuild")
	}
	got := s.do(http.MethodPost, prefix+"/status/rebuild", url.Values{"csrf": {s.csrf()}}, nil)
	if got.status != http.StatusSeeOther || got.location != prefix+"/status?ok=rebuild" {
		t.Fatalf("rebuild: %d → %q", got.status, got.location)
	}
	if _, err := os.Stat(request); err != nil {
		t.Errorf("the request file systemd watches for: %v", err)
	}
	after := s.do(http.MethodGet, got.location, nil, nil)
	if !strings.Contains(after.body, "Пересборка запрошена: она начнётся") || !strings.Contains(after.body, "disabled") {
		t.Error("the page does not say that the rebuild was requested")
	}
	var logged int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'admin.rebuild' AND actor = 'denis'`).Scan(&logged); err != nil || logged != 1 {
		t.Errorf("rebuilds in the audit log: %d, %v", logged, err)
	}

	// A server where the unit is not installed: the button explains what to do instead.
	if err := os.RemoveAll(filepath.Join(s.state, "requests")); err != nil {
		t.Fatal(err)
	}
	if got := s.do(http.MethodPost, prefix+"/status/rebuild", url.Values{"csrf": {s.csrf()}}, nil); got.status != http.StatusInternalServerError || !strings.Contains(got.body, "systemctl start krokosha-sync.service") {
		t.Errorf("rebuild without the requests directory: %d", got.status)
	}
}

// DB-IP gives its database away for a link wherever the results are shown (CC BY 4.0), and
// GeoLite2 asks for a line of its own: the footer of every page of the admin area carries it.
func TestTheMakerOfTheGeoDatabaseIsCredited(t *testing.T) {
	s := newSite(t)
	s.signIn()
	footer := func(path string) string {
		t.Helper()
		page := s.do(http.MethodGet, prefix+path, nil, nil)
		if page.status != http.StatusOK {
			t.Fatalf("%s: %d", path, page.status)
		}
		at := strings.LastIndex(page.body, "<footer")
		if at < 0 {
			t.Fatalf("%s has no footer", path)
		}
		return page.body[at:]
	}

	if got := footer("/"); strings.Contains(got, "DB-IP") || strings.Contains(got, "MaxMind") {
		t.Errorf("a credit without a database: %s", got)
	}
	s.geo = geo.Info{Path: "/var/lib/GeoIP/dbip-city-lite.mmdb", Loaded: true, Type: "DBIP-City-Lite", Built: reportDay}
	for _, path := range []string{"/", "/status", "/leads"} {
		if got := footer(path); !strings.Contains(got, `<a href="https://db-ip.com" rel="noopener noreferrer" target="_blank">IP Geolocation by DB-IP</a>`) {
			t.Errorf("%s does not credit DB-IP: %s", path, got)
		}
	}
	s.geo = geo.Info{Path: "/var/lib/GeoIP/GeoLite2-City.mmdb", Loaded: true, Type: "GeoLite2-City", Built: reportDay}
	if got := footer("/"); !strings.Contains(got, "GeoLite2 data created by MaxMind") || strings.Contains(got, "DB-IP") {
		t.Errorf("GeoLite2 is not credited: %s", got)
	}
	s.geo = geo.Info{Path: "/var/lib/GeoIP/dbip-city-lite.mmdb"} // the file has not come yet
	if got := footer("/"); strings.Contains(got, "DB-IP") {
		t.Errorf("a database that is not there is credited: %s", got)
	}

	// The status screen says which database it is, or how to get one.
	status := func() string {
		t.Helper()
		body := s.do(http.MethodGet, prefix+"/status", nil, nil).body
		at := strings.Index(body, "База GeoIP")
		if at < 0 {
			t.Fatal("the status screen has no line about GeoIP")
		}
		end := strings.Index(body[at:], "</dd>")
		if end < 0 {
			t.Fatal("the line about GeoIP does not end")
		}
		return body[at : at+end]
	}
	if got := status(); !strings.Contains(got, "не установлена") || !strings.Contains(got, "/var/lib/GeoIP/dbip-city-lite.mmdb") || !strings.Contains(got, "journalctl -u krokosha-dbip") {
		t.Errorf("without the file: %s", got)
	}
	s.geo = geo.Info{Path: "/var/lib/GeoIP/dbip-city-lite.mmdb", Loaded: true, Type: "DBIP-City-Lite", Built: reportDay}
	if got := status(); !strings.Contains(got, "DBIP-City-Lite от 19.09.2026") {
		t.Errorf("with DB-IP: %s", got)
	}
}

func TestFormatting(t *testing.T) {
	for value, want := range map[int64]string{0: "0 Б", 900: "900 Б", 1536: "1,5 КБ", 2_500_000: "2,4 МБ", 150 << 20: "150 МБ", 3 << 30: "3 ГБ"} {
		if got := formatBytes(value); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", value, got, want)
		}
	}
	thin := string(rune(0x202F))
	for value, want := range map[int64]string{7: "7", 1234: "1" + thin + "234", 1234567: "1" + thin + "234" + thin + "567"} {
		if got := formatCount(value); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", value, got, want)
		}
	}
	for value, want := range map[int64]string{950: "950", 12_500: "12,5 тыс", 40_000: "40 тыс", 2_300_000: "2,3 млн"} {
		if got := compactCount(value); got != want {
			t.Errorf("compactCount(%d) = %q, want %q", value, got, want)
		}
	}
	for elapsed, want := range map[time.Duration]string{5 * time.Second: "только что", 3 * time.Minute: "3 мин назад", 5 * time.Hour: "5 ч назад", 72 * time.Hour: "3 дн. назад", -time.Hour: "—"} {
		if got := ago(elapsed); got != want {
			t.Errorf("ago(%v) = %q, want %q", elapsed, got, want)
		}
	}
	if row := usage("Диск /", 100, 25); row.Percent != 75 || row.Used != 75 {
		t.Errorf("usage: %+v", row)
	}
	if !strings.Contains(string(meter(95)), "bar-error") || !strings.Contains(string(meter(85)), "bar-warn") || !strings.Contains(string(meter(40)), "bar-accent") {
		t.Error("the meter does not change colour as it fills up")
	}
}
