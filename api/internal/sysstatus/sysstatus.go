// Package sysstatus answers «is everything all right?» for the admin area (brief B6, «system
// status»): the last build of the site, the certificate, memory, disks, the database — and it
// passes the «rebuild now» button on to systemd without giving the web service any privileges.
package sysstatus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/geo"
	"github.com/DenisHumen/krokosha-site/api/internal/inbox"
	"github.com/DenisHumen/krokosha-site/api/internal/netmap"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// Options say where things live on the server (deploy/install.sh).
type Options struct {
	StateDir   string // /var/lib/krokosha: status/sync.json, requests/
	WWWDir     string // /var/www/krokosha: current → releases/<timestamp>
	DataDir    string // /srv/krokosha
	ContentDir string // …/content: generated/github.json
	SiteHost   string
	HTTPS      bool   // the site is served over TLS (SITE_URL starts with https://)
	TLSAddr    string // where nginx listens, "127.0.0.1:443" by default
	DB         *sql.DB
	Cache      *cache.Cache
	Version    string
	Started    time.Time
	Now        func() time.Time
	// LogPolled says when nginx's access log was last read (nginxlog.Reader.LastPoll).
	LogPolled func() time.Time
	// Outbox counts the notifications that wait or were given up on.
	Outbox func(ctx context.Context) (outbox.Stats, error)
	// Inbox tells how reading the service mailbox goes; nil — it is not read. Mailbox is its address.
	Inbox   func(ctx context.Context) inbox.Status
	Mailbox string
	// Geo describes the GeoIP database; nil — geolocation is switched off.
	Geo func() geo.Info
}

// Service collects the status.
type Service struct {
	opts Options

	mu       sync.Mutex
	cert     *Certificate
	certErr  string
	certRead time.Time
}

// New builds the service.
func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.TLSAddr == "" {
		opts.TLSAddr = "127.0.0.1:443"
	}
	return &Service{opts: opts}
}

// Sync is what deploy/bin/build-release.sh reports about its last run (status/sync.json).
type Sync struct {
	Known      bool      `json:"-"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	OK         bool      `json:"ok"`
	Step       string    `json:"step"`   // where it stopped: sync, dependencies, build, check, publish, done
	GitHub     string    `json:"github"` // ok | failed
	Release    string    `json:"release"`
}

// Backup is what deploy/backup.sh reports about its last run (status/backup.json).
type Backup struct {
	Known      bool      `json:"-"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	OK         bool      `json:"ok"`
	Name       string    `json:"name"`
	Bytes      int64     `json:"bytes"`
	CopiedTo   string    `json:"copied_to"` // where a copy went besides this disk; "" — nowhere
	Error      string    `json:"error"`
}

// CertWatch is what deploy/bin/krokosha-certwatch found when it last looked
// (status/certwatch.json): the certificates that are really served, the mail server's included.
type CertWatch struct {
	Known        bool                 `json:"-"`
	CheckedAt    time.Time            `json:"checked_at"`
	OK           bool                 `json:"ok"`
	Renewed      bool                 `json:"renewed"`
	Certificates []WatchedCertificate `json:"certificates"`
	Error        string               `json:"error"`
}

// WatchedCertificate is one of them. DaysLeft is nil when nothing answered with a certificate.
type WatchedCertificate struct {
	Name     string    `json:"name"` // site | mail
	Host     string    `json:"host"`
	DaysLeft *int      `json:"days_left"`
	NotAfter time.Time `json:"-"`
}

// GitHubData describes content/generated/github.json, the input of the projects section.
type GitHubData struct {
	Known    bool
	SyncedAt time.Time
	Stale    bool
	Repos    int
}

// Certificate is what visitors are served.
type Certificate struct {
	Subject   string
	Issuer    string
	NotAfter  time.Time
	DaysLeft  int
	Trusted   bool // verifies against the system's roots for the site's host name
	Names     []string
	CheckedAt time.Time
}

// Disk is a filesystem.
type Disk struct {
	Path        string
	TotalBytes  uint64
	FreeBytes   uint64
	UsedPercent float64
}

// Host is the machine.
type Host struct {
	Supported   bool // false where /proc is not available (development on another OS)
	CPUs        int
	Load        [3]float64
	MemoryTotal uint64
	MemoryFree  uint64 // «available»: what programs can still get without swapping
	SwapTotal   uint64
	SwapFree    uint64
	Uptime      time.Duration
	Disks       []Disk
}

// Database is MySQL as the service sees it.
type Database struct {
	OK        bool
	Version   string
	SizeBytes int64
	Tables    int
}

// MapStatus is the map of the internet (/map): its newest sync and the newest one that worked.
type MapStatus struct {
	Known        bool // the table could be read
	Last, LastOK netmap.SyncRun
}

// Problem is something the owner should look at.
type Problem struct {
	Level string // warn | error
	Text  string
}

// Status is the whole screen.
type Status struct {
	Problems         []Problem
	Sync             Sync
	GitHub           GitHubData
	RebuildRequested bool
	Release          string
	ReleaseAt        time.Time
	HTTPS            bool
	Certificate      *Certificate
	CertificateError string
	Host             Host
	Database         Database
	Redis            string // ok | degraded | off
	LogReadAt        time.Time
	Outbox           outbox.Stats
	Inbox            *inbox.Status // nil — answers by mail are not read
	Geo              *geo.Info     // nil — geolocation is switched off
	Map              MapStatus
	Backup           Backup
	CertWatch        CertWatch
	Mailbox          string
	Version          string
	Uptime           time.Duration
}

// Collect gathers everything. Parts that cannot be read are reported as such, never as an error
// of the whole page: a status screen that fails when something is wrong would be of little use.
func (s *Service) Collect(ctx context.Context) *Status {
	now := s.opts.Now()
	out := &Status{HTTPS: s.opts.HTTPS, Version: s.opts.Version, Uptime: now.Sub(s.opts.Started).Round(time.Second)}

	out.Sync = s.readSync()
	out.GitHub = s.readGitHub()
	out.Backup.Known = s.readReport("backup.json", &out.Backup)
	out.CertWatch.Known = s.readReport("certwatch.json", &out.CertWatch)
	out.RebuildRequested = s.RebuildRequested()
	if target, err := os.Readlink(filepath.Join(s.opts.WWWDir, "current")); err == nil {
		out.Release = filepath.Base(target)
		if at, err := time.ParseInLocation("20060102-150405", out.Release, time.UTC); err == nil {
			out.ReleaseAt = at
		}
	}
	if s.opts.HTTPS {
		out.Certificate, out.CertificateError = s.certificate(ctx)
	}
	out.Host = readHost(s.opts.DataDir)
	out.Database = s.database(ctx)
	out.Redis = s.redis(ctx)
	if s.opts.LogPolled != nil {
		out.LogReadAt = s.opts.LogPolled()
	}
	if s.opts.Outbox != nil {
		if stats, err := s.opts.Outbox(ctx); err == nil {
			out.Outbox = stats
		}
	}
	if s.opts.Inbox != nil {
		status := s.opts.Inbox(ctx)
		out.Inbox, out.Mailbox = &status, s.opts.Mailbox
	}
	if s.opts.Geo != nil {
		info := s.opts.Geo()
		out.Geo = &info
	}
	if s.opts.DB != nil {
		var err error
		out.Map.Last, out.Map.LastOK, err = netmap.Runs(ctx, s.opts.DB)
		out.Map.Known = err == nil
	}
	out.Problems = problems(out, now)
	return out
}

func problems(status *Status, now time.Time) []Problem {
	var out []Problem
	add := func(level, format string, args ...any) {
		out = append(out, Problem{level, fmt.Sprintf(format, args...)})
	}

	if !status.Database.OK {
		add("error", "База данных не отвечает.")
	}
	if status.Redis == "degraded" {
		add("warn", "Redis недоступен: лимиты считаются в памяти сервиса, «сейчас на сайте» может врать.")
	}
	switch {
	case !status.Sync.Known:
		add("warn", "Сайт ещё ни разу не собирался этим сервером (нет отчёта о сборке).")
	case !status.Sync.OK:
		add("error", "Последняя сборка сайта не удалась на шаге «%s». Посетители видят предыдущий релиз. Журнал: journalctl -u krokosha-sync", status.Sync.Step)
	case now.Sub(status.Sync.FinishedAt) > 13*time.Hour:
		add("warn", "Сайт не пересобирался больше 12 часов: таймер krokosha-sync.timer остановлен? (после отката он на паузе)")
	}
	if status.Sync.Known && status.Sync.OK && status.Sync.GitHub == "failed" {
		add("warn", "GitHub не ответил при последней сборке: проекты показаны из кэша.")
	}
	if status.GitHub.Known && status.GitHub.Stale {
		add("warn", "Данные GitHub не обновлялись больше суток: на сайте показано предупреждение об этом.")
	}
	if status.HTTPS {
		switch cert := status.Certificate; {
		case cert == nil:
			add("error", "Сертификат сайта прочитать не удалось: %s", status.CertificateError)
		case cert.DaysLeft < 0:
			add("error", "Сертификат сайта истёк %s.", cert.NotAfter.Format("02.01.2006"))
		case cert.DaysLeft < 14:
			add("error", "Сертификат сайта истекает через %d дн. — автоматическое продление не сработало: certbot renew", cert.DaysLeft)
		case !cert.Trusted:
			add("warn", "Сертификат сайта не доверенный (самоподписанный или тестовый).")
		}
	}
	for _, disk := range status.Host.Disks {
		if disk.UsedPercent >= 90 {
			add("error", "Диск %s заполнен на %.0f%%.", disk.Path, disk.UsedPercent)
		} else if disk.UsedPercent >= 80 {
			add("warn", "Диск %s заполнен на %.0f%%.", disk.Path, disk.UsedPercent)
		}
	}
	if host := status.Host; host.Supported && host.MemoryTotal > 0 {
		if free := float64(host.MemoryFree) / float64(host.MemoryTotal); free < 0.08 {
			add("warn", "Свободной памяти осталось %.0f%%.", free*100)
		}
		if host.CPUs > 0 && host.Load[1] > float64(host.CPUs)*1.5 {
			add("warn", "Высокая нагрузка: %.2f при %d ядрах (за 5 минут).", host.Load[1], host.CPUs)
		}
	}
	switch backup := status.Backup; {
	case backup.Known && !backup.OK:
		add("error", "Последняя резервная копия не сделана: %s", backup.Error)
	case backup.Known && now.Sub(backup.FinishedAt) > 50*time.Hour:
		add("warn", "Резервная копия не делалась больше двух суток: таймер krokosha-backup.timer остановлен?")
	case !backup.Known && status.Uptime > 36*time.Hour:
		add("warn", "Резервные копии ещё ни разу не делались: sudo systemctl status krokosha-backup.timer")
	}
	switch watch := status.CertWatch; {
	case watch.Known && !watch.OK:
		add("error", "%s. Подробности: journalctl -u krokosha-certwatch", strings.TrimSuffix(watch.Error, "."))
	case watch.Known && status.HTTPS && now.Sub(watch.CheckedAt) > 72*time.Hour:
		add("warn", "Сертификаты не проверялись больше трёх суток: таймер krokosha-certwatch.timer остановлен?")
	}
	if status.Outbox.Failed > 0 {
		add("error", "Не доставлено уведомлений за 30 дней: %d. Последняя ошибка: %s", status.Outbox.Failed, status.Outbox.LastError)
	}
	if !status.Outbox.OldestPending.IsZero() && now.Sub(status.Outbox.OldestPending) > 15*time.Minute {
		add("warn", "Уведомления ждут отправки дольше 15 минут (%d в очереди): %s", status.Outbox.Pending, status.Outbox.LastError)
	}
	if mail := status.Inbox; mail != nil && !mail.Connected && mail.LastError != "" && now.Sub(mail.LastErrorAt) < 24*time.Hour {
		add("error", "Почтовый ящик %s не читается: ответы клиентов письмом не попадают в заявки. Сервис пробует снова сам. Ошибка: %s", status.Mailbox, mail.LastError)
	}
	if mail := status.Inbox; mail != nil && mail.Unmatched > 0 {
		add("warn", "Писем без заявки: %d — посмотрите раздел «Входящие».", mail.Unmatched)
	}
	if !status.LogReadAt.IsZero() && now.Sub(status.LogReadAt) > 10*time.Minute {
		add("warn", "Лог nginx не читается больше 10 минут: экран «Трафик сервера» отстаёт. Подробности — journalctl -u krokosha-api")
	}
	switch run := status.Map; {
	case !run.Known:
	case run.Last.ID > run.LastOK.ID && !run.Last.Finished.IsZero():
		shown := "Карты ещё нет"
		if run.LastOK.ID != 0 {
			shown = "На сайте карта от " + run.LastOK.Finished.Format("02.01.2006")
		}
		add("warn", "Карта интернета не обновилась: %s. %s. Журнал: sudo journalctl -u krokosha-netmap", run.Last.Error, shown)
	case run.Last.ID > run.LastOK.ID && now.Sub(run.Last.Started) > 2*time.Hour:
		add("warn", "Обновление карты интернета идёт больше двух часов — прервалось? sudo journalctl -u krokosha-netmap")
	case run.LastOK.ID != 0 && now.Sub(run.LastOK.Finished) > 50*time.Hour:
		add("warn", "Карта интернета не обновлялась больше двух суток: sudo systemctl status krokosha-netmap.timer")
	case run.LastOK.ID == 0 && run.Last.ID == 0 && status.Uptime > 6*time.Hour:
		add("warn", "Карта интернета ещё не построена: sudo systemctl start krokosha-netmap.service (несколько минут)")
	}
	// GeoLite2 comes out twice a week and DB-IP once a month: a database older than six weeks means
	// that what fetches it has stopped.
	if info := status.Geo; info != nil && info.Loaded && now.Sub(info.Built) > 42*24*time.Hour {
		built := info.Built.Format("02.01.2006")
		switch info.Source() {
		case "DB-IP":
			add("warn", "База GeoIP (DB-IP) собрана %s и не обновлялась: sudo systemctl status krokosha-dbip.timer, sudo journalctl -u krokosha-dbip, скачать сейчас — sudo systemctl start krokosha-dbip.service", built)
		case "MaxMind":
			add("warn", "База GeoIP собрана %s и не обновлялась: sudo systemctl status krokosha-geoipupdate.timer, sudo geoipupdate -v", built)
		default:
			add("warn", "База GeoIP %s собрана %s и не обновлялась", info.Path, built)
		}
	}
	return out
}

func (s *Service) readSync() Sync {
	var out Sync
	raw, err := os.ReadFile(filepath.Join(s.opts.StateDir, "status", "sync.json"))
	if err != nil || json.Unmarshal(raw, &out) != nil {
		return Sync{}
	}
	out.Known = true
	return out
}

// readReport reads what a maintenance script left in the status directory.
func (s *Service) readReport(name string, into any) bool {
	raw, err := os.ReadFile(filepath.Join(s.opts.StateDir, "status", name))
	return err == nil && json.Unmarshal(raw, into) == nil
}

func (s *Service) readGitHub() GitHubData {
	raw, err := os.ReadFile(filepath.Join(s.opts.ContentDir, "generated", "github.json"))
	if err != nil {
		return GitHubData{}
	}
	var data struct {
		Public   []json.RawMessage `json:"public"`
		SyncedAt time.Time         `json:"syncedAt"`
		Stale    bool              `json:"stale"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return GitHubData{}
	}
	return GitHubData{Known: true, SyncedAt: data.SyncedAt, Stale: data.Stale, Repos: len(data.Public)}
}

// certificate reads what nginx serves for the site's name. Asking the server rather than
// reading files: the service has no access to /etc/letsencrypt, and this is what visitors get.
func (s *Service) certificate(ctx context.Context) (*Certificate, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	if now.Sub(s.certRead) < 5*time.Minute && (s.cert != nil || s.certErr != "") {
		return s.cert, s.certErr
	}
	s.certRead = now

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 3 * time.Second},
		// Verification is done below, by hand: an expired or self-signed certificate must be
		// described on the status page, not turned into «connection failed».
		Config: &tls.Config{ServerName: s.opts.SiteHost, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // see above
	}
	conn, err := dialer.DialContext(ctx, "tcp", s.opts.TLSAddr)
	if err != nil {
		s.cert, s.certErr = nil, err.Error()
		return s.cert, s.certErr
	}
	defer func() { _ = conn.Close() }()
	chain := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(chain) == 0 {
		s.cert, s.certErr = nil, "the server presented no certificate"
		return s.cert, s.certErr
	}

	leaf := chain[0]
	intermediates := x509.NewCertPool()
	for _, cert := range chain[1:] {
		intermediates.AddCert(cert)
	}
	_, verifyErr := leaf.Verify(x509.VerifyOptions{DNSName: s.opts.SiteHost, Intermediates: intermediates, CurrentTime: now})
	issuer := leaf.Issuer.CommonName
	if len(leaf.Issuer.Organization) > 0 {
		issuer = leaf.Issuer.Organization[0] + " · " + issuer
	}
	s.cert = &Certificate{
		Subject: leaf.Subject.CommonName, Issuer: issuer, NotAfter: leaf.NotAfter, Names: leaf.DNSNames,
		DaysLeft: int(leaf.NotAfter.Sub(now).Hours() / 24), Trusted: verifyErr == nil, CheckedAt: now,
	}
	if leaf.NotAfter.Before(now) {
		s.cert.DaysLeft = -1
	}
	s.certErr = ""
	return s.cert, s.certErr
}

func (s *Service) database(ctx context.Context) Database {
	var out Database
	if s.opts.DB == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.opts.DB.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&out.Version); err != nil {
		return out
	}
	out.OK = true
	_ = s.opts.DB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(data_length + index_length), 0), COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()`).
		Scan(&out.SizeBytes, &out.Tables)
	return out
}

func (s *Service) redis(ctx context.Context) string {
	if s.opts.Cache == nil {
		return "off"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	switch err := s.opts.Cache.Ping(ctx); {
	case err == nil:
		return "ok"
	case errors.Is(err, cache.ErrDisabled):
		return "off"
	default:
		return "degraded"
	}
}

// --- «rebuild now» -------------------------------------------------------------------------------

func (s *Service) requestFile() string { return filepath.Join(s.opts.StateDir, "requests", "rebuild") }

// RequestRebuild asks for a rebuild of the site. The web service cannot start systemd units and
// should not be able to: it drops a file, krokosha-rebuild.path notices it and starts the same
// krokosha-sync.service the timer runs.
func (s *Service) RequestRebuild() error {
	file, err := os.OpenFile(s.requestFile(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(file, "%s\n", s.opts.Now().UTC().Format(time.RFC3339))
	return file.Close()
}

// RebuildRequested reports a request that the build has not picked up yet.
func (s *Service) RebuildRequested() bool {
	_, err := os.Stat(s.requestFile())
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}
