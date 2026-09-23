package sysstatus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/geo"
	"github.com/DenisHumen/krokosha-site/api/internal/inbox"
)

var now = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// server lays out the directories the way install.sh does, in a temporary place.
type server struct {
	t                   *testing.T
	state, www, content string
}

func newServer(t *testing.T) *server {
	t.Helper()
	root := t.TempDir()
	s := &server{t: t, state: filepath.Join(root, "state"), www: filepath.Join(root, "www"), content: filepath.Join(root, "content")}
	for _, dir := range []string{filepath.Join(s.state, "status"), filepath.Join(s.state, "requests"), filepath.Join(s.www, "releases", "20260919-060012"), filepath.Join(s.content, "generated")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (s *server) file(path, content string) {
	s.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *server) service(opts Options) *Service {
	opts.StateDir, opts.WWWDir, opts.ContentDir = s.state, s.www, s.content
	opts.Now = func() time.Time { return now }
	opts.Started = now.Add(-90 * time.Minute)
	return New(opts)
}

func TestCollectReadsWhatTheBuildLeft(t *testing.T) {
	s := newServer(t)
	s.file(filepath.Join(s.state, "status", "sync.json"),
		`{"started_at":"2026-09-19T06:00:05Z","finished_at":"2026-09-19T06:01:10Z","ok":true,"step":"done","github":"ok","release":"20260919-060012"}`)
	s.file(filepath.Join(s.content, "generated", "github.json"), `{"public":[{"name":"a"},{"name":"b"}],"syncedAt":"2026-09-19T06:00:07Z","stale":false}`)
	if err := os.Symlink(filepath.Join(s.www, "releases", "20260919-060012"), filepath.Join(s.www, "current")); err != nil {
		t.Skipf("symbolic links are not available here: %v", err)
	}

	status := s.service(Options{Version: "abc123", LogPolled: func() time.Time { return now.Add(-5 * time.Second) }}).Collect(context.Background())
	if !status.Sync.Known || !status.Sync.OK || status.Sync.Step != "done" || status.Sync.FinishedAt.Sub(status.Sync.StartedAt) != 65*time.Second {
		t.Errorf("sync: %+v", status.Sync)
	}
	if !status.GitHub.Known || status.GitHub.Repos != 2 || status.GitHub.Stale {
		t.Errorf("github: %+v", status.GitHub)
	}
	if status.Release != "20260919-060012" || !status.ReleaseAt.Equal(time.Date(2026, 9, 19, 6, 0, 12, 0, time.UTC)) {
		t.Errorf("release: %q %v", status.Release, status.ReleaseAt)
	}
	if status.Version != "abc123" || status.Uptime != 90*time.Minute || status.Redis != "off" || status.RebuildRequested {
		t.Errorf("status: %+v", status)
	}
	// No database was given to this test: that, and only that, is wrong.
	if len(status.Problems) != 1 || status.Problems[0].Level != "error" || !strings.Contains(status.Problems[0].Text, "База данных") {
		t.Errorf("problems: %+v", status.Problems)
	}
}

// The nightly backup and the daily look at the certificates leave their reports next to the
// build's; the status screen reads them.
func TestCollectReadsWhatTheWatchdogsLeft(t *testing.T) {
	s := newServer(t)
	s.file(filepath.Join(s.state, "status", "backup.json"),
		`{"started_at":"2026-09-19T03:31:00Z","finished_at":"2026-09-19T03:31:40Z","ok":true,"name":"20260919-033100","bytes":52428800,"copied_to":"backup@nas:/krokosha","error":""}`)
	s.file(filepath.Join(s.state, "status", "certwatch.json"),
		`{"checked_at":"2026-09-19T04:50:00Z","ok":false,"renewed":false,"certificates":[{"name":"site","host":"krokosha.xyz","days_left":9,"not_after":"2026-09-28T00:00:00Z"},{"name":"mail","host":"mail.krokosha.xyz","days_left":null,"not_after":""}],"error":"Сертификат krokosha.xyz истекает через 9 дн. и не продлевается"}`)
	status := s.service(Options{}).Collect(context.Background())
	if backup := status.Backup; !backup.Known || !backup.OK || backup.Name != "20260919-033100" || backup.Bytes != 50<<20 || backup.CopiedTo != "backup@nas:/krokosha" {
		t.Errorf("backup: %+v", backup)
	}
	watch := status.CertWatch
	if !watch.Known || watch.OK || len(watch.Certificates) != 2 || watch.Certificates[0].DaysLeft == nil || *watch.Certificates[0].DaysLeft != 9 || watch.Certificates[1].DaysLeft != nil {
		t.Errorf("certwatch: %+v", watch)
	}
	found := false
	for _, problem := range status.Problems {
		found = found || (problem.Level == "error" && strings.Contains(problem.Text, "истекает через 9 дн. и не продлевается. Подробности: journalctl -u krokosha-certwatch"))
	}
	if !found {
		t.Errorf("problems: %+v", status.Problems)
	}
}

func TestANewServerIsDescribedNotFailed(t *testing.T) {
	s := newServer(t)
	s.file(filepath.Join(s.state, "status", "sync.json"), `{"started_at": broken`)
	status := s.service(Options{}).Collect(context.Background())
	if status.Sync.Known || status.GitHub.Known || status.Release != "" || status.Certificate != nil {
		t.Errorf("status of an empty server: %+v", status)
	}
}

func TestRebuildRequest(t *testing.T) {
	s := newServer(t)
	service := s.service(Options{})
	if service.RebuildRequested() {
		t.Fatal("a rebuild is requested before anybody asked")
	}
	if err := service.RequestRebuild(); err != nil {
		t.Fatal(err)
	}
	if err := service.RequestRebuild(); err != nil { // pressing the button twice is fine
		t.Fatal(err)
	}
	if !service.RebuildRequested() {
		t.Error("the request is not visible")
	}
	if _, err := os.Stat(filepath.Join(s.state, "requests", "rebuild")); err != nil {
		t.Errorf("systemd watches for this very file: %v", err)
	}
	// The build removes the file when it starts.
	_ = os.Remove(filepath.Join(s.state, "requests", "rebuild"))
	if service.RebuildRequested() {
		t.Error("the request outlived its file")
	}

	// Without the directory (the unit was not installed) the button says so instead of pretending.
	if err := New(Options{StateDir: filepath.Join(s.state, "missing")}).RequestRebuild(); err == nil {
		t.Error("a request into nowhere succeeded")
	}
}

func TestCertificateIsReadFromTheServer(t *testing.T) {
	web := httptest.NewTLSServer(http.NotFoundHandler())
	defer web.Close()
	s := newServer(t)
	service := s.service(Options{HTTPS: true, SiteHost: "example.com", TLSAddr: web.Listener.Addr().String()})

	status := service.Collect(context.Background())
	cert := status.Certificate
	if cert == nil {
		t.Fatalf("no certificate: %s", status.CertificateError)
	}
	// The test server's certificate is self-signed: described, flagged, not an error.
	if cert.Trusted || cert.DaysLeft < 1 || len(cert.Names) == 0 || cert.NotAfter.Before(now) {
		t.Errorf("certificate: %+v", cert)
	}
	var texts []string
	for _, problem := range status.Problems {
		texts = append(texts, problem.Level+": "+problem.Text)
	}
	if joined := strings.Join(texts, "\n"); !strings.Contains(joined, "warn: Сертификат сайта не доверенный") {
		t.Errorf("problems:\n%s", joined)
	}

	// The answer is cached: a status page reloaded every few seconds must not open a TLS
	// connection each time.
	web.Close()
	if again := service.Collect(context.Background()); again.Certificate == nil {
		t.Errorf("the certificate was asked for again: %s", again.CertificateError)
	}

	down := s.service(Options{HTTPS: true, SiteHost: "example.com", TLSAddr: web.Listener.Addr().String()}).Collect(context.Background())
	if down.Certificate != nil || down.CertificateError == "" {
		t.Errorf("nginx is down: %+v", down.Certificate)
	}
}

func TestProblems(t *testing.T) {
	healthy := func() *Status {
		return &Status{
			Database: Database{OK: true}, Redis: "ok", HTTPS: true,
			Sync:        Sync{Known: true, OK: true, GitHub: "ok", FinishedAt: now.Add(-2 * time.Hour)},
			GitHub:      GitHubData{Known: true},
			Certificate: &Certificate{DaysLeft: 60, Trusted: true, NotAfter: now.AddDate(0, 0, 60)},
			Host: Host{Supported: true, CPUs: 2, Load: [3]float64{0.2, 0.3, 0.1}, MemoryTotal: 2 << 30, MemoryFree: 1 << 30,
				Disks: []Disk{{Path: "/", UsedPercent: 41}}},
			LogReadAt: now.Add(-8 * time.Second),
			Backup:    Backup{Known: true, OK: true, FinishedAt: now.Add(-9 * time.Hour)},
			CertWatch: CertWatch{Known: true, OK: true, CheckedAt: now.Add(-8 * time.Hour)},
			Uptime:    72 * time.Hour,
		}
	}
	if got := problems(healthy(), now); len(got) != 0 {
		t.Fatalf("a healthy server has problems: %+v", got)
	}

	for name, tc := range map[string]struct {
		breakIt func(*Status)
		level   string
		text    string
	}{
		"database down":         {func(s *Status) { s.Database.OK = false }, "error", "База данных"},
		"redis down":            {func(s *Status) { s.Redis = "degraded" }, "warn", "Redis"},
		"never built":           {func(s *Status) { s.Sync = Sync{} }, "warn", "ни разу не собирался"},
		"build failed":          {func(s *Status) { s.Sync.OK, s.Sync.Step = false, "build" }, "error", "на шаге «build»"},
		"timer stopped":         {func(s *Status) { s.Sync.FinishedAt = now.Add(-20 * time.Hour) }, "warn", "больше 12 часов"},
		"github unreachable":    {func(s *Status) { s.Sync.GitHub = "failed" }, "warn", "GitHub не ответил"},
		"github stale":          {func(s *Status) { s.GitHub.Stale = true }, "warn", "больше суток"},
		"certificate unread":    {func(s *Status) { s.Certificate, s.CertificateError = nil, "connection refused" }, "error", "connection refused"},
		"certificate expiring":  {func(s *Status) { s.Certificate.DaysLeft = 9 }, "error", "через 9 дн."},
		"certificate expired":   {func(s *Status) { s.Certificate.DaysLeft = -1 }, "error", "истёк"},
		"certificate untrusted": {func(s *Status) { s.Certificate.Trusted = false }, "warn", "не доверенный"},
		"disk filling up":       {func(s *Status) { s.Host.Disks[0].UsedPercent = 84 }, "warn", "Диск / заполнен на 84%"},
		"disk full":             {func(s *Status) { s.Host.Disks[0].UsedPercent = 93 }, "error", "Диск / заполнен на 93%"},
		"memory":                {func(s *Status) { s.Host.MemoryFree = 100 << 20 }, "warn", "памяти осталось 5%"},
		"load":                  {func(s *Status) { s.Host.Load[1] = 4.2 }, "warn", "4.20 при 2 ядрах"},
		"log reader stuck":      {func(s *Status) { s.LogReadAt = now.Add(-time.Hour) }, "warn", "Лог nginx не читается"},
		"mailbox unreachable": {func(s *Status) {
			s.Mailbox, s.Inbox = "leads@krokosha.xyz", &inbox.Status{LastError: "connection refused", LastErrorAt: now.Add(-time.Minute)}
		}, "error", "leads@krokosha.xyz не читается"},
		"letters wait": {func(s *Status) { s.Inbox = &inbox.Status{Connected: true, Unmatched: 3} }, "warn", "Писем без заявки: 3"},
		"backup failed": {func(s *Status) {
			s.Backup = Backup{Known: true, OK: false, Error: "backup.sh stopped at line 102"}
		}, "error", "резервная копия не сделана: backup.sh stopped at line 102"},
		"backup is old": {func(s *Status) {
			s.Backup = Backup{Known: true, OK: true, FinishedAt: now.Add(-60 * time.Hour)}
		}, "warn", "не делалась больше двух суток"},
		"never backed up": {func(s *Status) { s.Backup, s.Uptime = Backup{}, 48*time.Hour }, "warn", "ни разу не делались"},
		"certificates unwatched": {func(s *Status) {
			s.CertWatch = CertWatch{Known: true, OK: true, CheckedAt: now.Add(-100 * time.Hour)}
		}, "warn", "не проверялись больше трёх суток"},
		"db-ip is old": {func(s *Status) {
			s.Geo = &geo.Info{Loaded: true, Type: "DBIP-City-Lite", Built: now.AddDate(0, 0, -50)}
		}, "warn", "systemctl status krokosha-dbip.timer"},
		"geolite2 is old": {func(s *Status) {
			s.Geo = &geo.Info{Loaded: true, Type: "GeoLite2-City", Built: now.AddDate(0, 0, -50)}
		}, "warn", "systemctl status krokosha-geoipupdate.timer"},
		"an own database is old": {func(s *Status) {
			s.Geo = &geo.Info{Path: "/srv/geo/own.mmdb", Loaded: true, Type: "Own-City", Built: now.AddDate(0, 0, -50)}
		}, "warn", "База GeoIP /srv/geo/own.mmdb собрана"},
	} {
		status := healthy()
		tc.breakIt(status)
		got := problems(status, now)
		if len(got) != 1 || got[0].Level != tc.level || !strings.Contains(got[0].Text, tc.text) {
			t.Errorf("%s: %+v, want one %q about %q", name, got, tc.level, tc.text)
		}
	}

	// A mailbox that is read and has nothing to decide about is no news; nor is one that is only connecting.
	for _, mail := range []*inbox.Status{{Connected: true}, {}, {Connected: true, LastError: "an old story", LastErrorAt: now.Add(-time.Hour)}} {
		quiet := healthy()
		quiet.Inbox = mail
		if got := problems(quiet, now); len(got) != 0 {
			t.Errorf("incoming mail %+v: %+v", mail, got)
		}
	}

	// DB-IP of last month is as fresh as DB-IP gets; a database that is not there yet is told about
	// on the status screen, not as a problem.
	for _, info := range []geo.Info{
		{Loaded: true, Type: "DBIP-City-Lite", Built: now.AddDate(0, 0, -33)},
		{Path: "/var/lib/GeoIP/dbip-city-lite.mmdb"},
	} {
		quiet := healthy()
		quiet.Geo = &info
		if got := problems(quiet, now); len(got) != 0 {
			t.Errorf("GeoIP %+v: %+v", info, got)
		}
	}

	// Plain HTTP (tests, --tls none): nobody misses a certificate.
	plain := healthy()
	plain.HTTPS, plain.Certificate = false, nil
	if got := problems(plain, now); len(got) != 0 {
		t.Errorf("a site without HTTPS: %+v", got)
	}
}
