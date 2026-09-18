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
		"Трафик сервера · Суббота, 19 сентября 2026",
		"<title>09:00 — 4 запроса, из них боты и программы: 3; не найдено: 2; ошибок сервера: 0</title>",
		"<title>09:00 — 2,4 МБ</title>",
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

	// The overview's timeline gets its third colour from the same data.
	overview := s.do(http.MethodGet, prefix+"/", nil, nil)
	if !strings.Contains(overview.body, "chart-bar-bots") || !strings.Contains(overview.body, "ботов: 2</title>") || !strings.Contains(overview.body, "Боты и программы (по логу сервера)") {
		t.Error("the overview does not show the bots that the server log knows about")
	}
	if empty := s.do(http.MethodGet, prefix+"/?p=day&d=2026-09-10", nil, nil); strings.Contains(empty.body, "chart-bar-bots") {
		t.Error("bots are drawn for a day the log says nothing about")
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
	for value, want := range map[int64]int64{0: 4, 900: 1000, 1500: 4 << 10, 3 << 20: 4 << 20, 7 << 20: 10 << 20, 900 << 20: 1000 << 20} {
		if got := niceCeilBytes(value); got != want {
			t.Errorf("niceCeilBytes(%d) = %d, want %d", value, got, want)
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
