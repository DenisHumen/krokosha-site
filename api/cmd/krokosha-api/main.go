// Command krokosha-api is the backend of the site: one binary under systemd, listening on
// loopback behind nginx (brief B2). Configuration comes from the environment, see
// deploy/env/.env.example.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // the owner's time zone must resolve even on a system without tzdata

	"github.com/DenisHumen/krokosha-site/api/internal/admin"
	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/auth"
	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/geo"
	"github.com/DenisHumen/krokosha-site/api/internal/imap"
	"github.com/DenisHumen/krokosha-site/api/internal/inbox"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	krokoshamail "github.com/DenisHumen/krokosha-site/api/internal/mail"
	"github.com/DenisHumen/krokosha-site/api/internal/nginxlog"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
	"github.com/DenisHumen/krokosha-site/api/internal/sysstatus"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "krokosha-api:", err)
		os.Exit(1)
	}
}

func run() error {
	env, err := config.LoadEnvFromOS()
	if err != nil {
		return err
	}
	log := newLogger(env.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// After a reboot the database container may need a moment; systemd restarts us if it takes longer.
	pool, err := db.Open(ctx, env.MySQL, 90*time.Second, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	applied, err := db.Migrate(ctx, env.MySQL, migrations.Files, log)
	if err != nil {
		return err
	}
	if applied > 0 {
		log.Info("database schema updated", "migrations", applied)
	}

	store, err := cache.New(ctx, env.RedisURL, log)
	if err != nil {
		return err
	}
	defer store.Close()

	started := time.Now()
	srv := server.New(server.Deps{
		Env:     env,
		DB:      pool,
		Cache:   store,
		Log:     log,
		Version: version(),
		Started: started,
	})

	siteURL, _ := url.Parse(env.SiteURL) // validated by LoadEnv
	location := ownerLocation(env.ContentDir, log)
	// Where a visitor is from, by a local database (brief B5): nil when switched off, and a
	// locator that waits for the file when krokosha-dbip or geoipupdate has not brought it yet.
	var locator *geo.Locator
	var geoInfo func() geo.Info
	if env.GeoIPDB != "" {
		locator = geo.Open(env.GeoIPDB, log)
		defer locator.Close()
		geoInfo = locator.Info
	} else {
		log.Info("GEOIP_DB=off: countries and cities of visitors are not looked up")
	}
	stats := analytics.New(analytics.Options{
		DB:       pool,
		Cache:    store,
		Log:      log,
		SiteHost: siteURL.Hostname(),
		Location: location,
		// The owner browsing their own site while signed in to the admin area is not a visitor.
		IgnoreCookie: admin.CookieName,
		Geo:          locator,
	})
	stats.Register(srv.Mux())

	// Requests from the contact form: stored together with the notifications to send, which a
	// worker then delivers with retries (brief B10.1, B10.2).
	form := config.WatchForm(env.ContentDir, log)
	leadStore := leads.NewStore(pool, nil)
	// Files that come with requests live in the data root, outside anything nginx serves.
	attachments := leads.NewFiles(filepath.Join(env.DataDir, "attachments"))
	leadStore.UseFiles(attachments)
	deliveries := outbox.NewWorker(pool, log)
	// «Continue in Telegram» on the «thank you» page and in the confirmation letter: the link of
	// the request's own token (brief B10.5). There is one only while the bot is connected.
	var bot *telegram.Bot
	telegramURL := func(lead *leads.Lead) string {
		if bot == nil || bot.Username() == "" {
			return ""
		}
		return "https://t.me/" + bot.Username() + "?start=" + telegram.ClientPrefix + lead.PublicToken
	}
	// Letters link to the bot through the site's own address (leads.TelegramPath), which sends on
	// to the same place: mail filters judge a letter by where its links lead.
	letterTelegramURL := func(lead *leads.Lead) string {
		if telegramURL(lead) == "" {
			return ""
		}
		return strings.TrimRight(env.SiteURL, "/") + leads.TelegramPath + lead.PublicToken
	}
	if env.Mail.SMTPAddr != "" {
		from, _ := mail.ParseAddress(env.Mail.From) // both validated by LoadEnv
		notifyTo, _ := mail.ParseAddress(env.Mail.NotifyTo)
		if from.Name == "" {
			from.Name = senderName(env.ContentDir) // a letter from a bare address looks like a robot's
		}
		smtp := &krokoshamail.Sender{Addr: env.Mail.SMTPAddr, User: env.Mail.User, Password: env.Mail.Password, Hello: siteURL.Hostname(), Envelope: env.Mail.Inbox}
		deliveries.Register(outbox.ChannelEmail, &leads.Mailer{
			Store: leadStore, Deliver: smtp.Send, From: *from, NotifyTo: *notifyTo, SiteHost: siteURL.Hostname(),
			AdminURL: env.SiteURL + env.AdminPath, Form: form.Current, Location: location, TelegramURL: letterTelegramURL,
			Inbox: env.Mail.Inbox, Secret: []byte(env.Secret),
		})
	} else {
		log.Warn("SMTP_ADDR is not set: notifications about requests wait in the outbox until mail is configured")
	}
	// An answer that could not be delivered after all the retries is marked so in the conversation.
	deliveries.OnGiveUp(func(ctx context.Context, task outbox.Task, _ string) {
		var payload leads.TaskPayload
		if task.Kind == leads.TaskReply && json.Unmarshal(task.Payload, &payload) == nil && payload.MessageID > 0 {
			if err := leadStore.MarkDelivery(ctx, payload.MessageID, "failed", ""); err != nil {
				log.Error("cannot mark an answer as failed", "error", err)
			}
		}
	})
	leads.NewHandler(leads.Options{
		Store: leadStore, Cache: store, Sessions: stats, Log: log, Secret: []byte(env.Secret), Files: attachments,
		Form: form.Current, WWWDir: env.WWWDir, TelegramURL: telegramURL,
		OnCreated: func(*leads.Lead) { deliveries.Kick() },
	}).Register(srv.Mux())

	// Answers by mail (brief B10.5): the service mailbox is read over IMAP, and a letter joins the
	// conversation of the request whose number is signed into the address it was sent to.
	var letters *inbox.Service
	var lettersPanel admin.Inbox // stays a nil interface without a mailbox: a nil pointer inside one is not nil
	var lettersStatus func(ctx context.Context) inbox.Status
	if env.Mail.IMAPAddr != "" {
		letters = inbox.New(inbox.Options{
			Dial: func(ctx context.Context) (*imap.Client, error) {
				return imap.Dial(ctx, imap.Options{Addr: env.Mail.IMAPAddr, User: env.Mail.IMAPUser, Password: env.Mail.IMAPPassword})
			},
			DB: pool, Leads: leadStore, Files: attachments, Secret: []byte(env.Secret), Inbox: env.Mail.Inbox, Log: log,
			KeepDays: env.Retention.SpamDays, Kick: deliveries.Kick,
		})
		lettersPanel, lettersStatus = letters, letters.Status
	} else {
		log.Warn("IMAP_ADDR is not set: answers of clients by mail are not read")
	}

	// Everything nginx served, bots included: read from its access log (brief B6).
	accessLog := nginxlog.New(nginxlog.Options{Path: env.AccessLog, DB: pool, Cache: store, Location: location, Log: log})
	system := sysstatus.New(sysstatus.Options{
		StateDir:   env.StateDir,
		WWWDir:     env.WWWDir,
		DataDir:    env.DataDir,
		ContentDir: env.ContentDir,
		SiteHost:   siteURL.Hostname(),
		HTTPS:      siteURL.Scheme == "https",
		DB:         pool,
		Cache:      store,
		Version:    version(),
		Started:    started,
		LogPolled:  accessLog.LastPoll,
		Outbox:     func(ctx context.Context) (outbox.Stats, error) { return outbox.ReadStats(ctx, pool, time.Now()) },
		Inbox:      lettersStatus,
		Mailbox:    env.Mail.Inbox,
		Geo:        geoInfo,
	})

	accounts := auth.New(pool, store, log)

	// The Telegram bot (brief B10.3). Who has access is kept whether or not there is a token:
	// invitations can be prepared first. Without a token nothing talks to Telegram.
	botAccess := telegram.NewAccess(pool, nil)
	var botRunner *telegram.Runner
	if env.Telegram.Token != "" {
		bot = telegram.New(telegram.Options{
			API: telegram.NewClient(env.Telegram.Token, env.Telegram.API), Access: botAccess, Cache: store, Log: log, SiteURL: env.SiteURL,
			DB: pool, Leads: leadStore, Form: form.Current, Location: location, AdminURL: env.SiteURL + env.AdminPath, Kick: deliveries.Kick,
			Hurry:       func(ctx context.Context) { deliveries.Hurry(ctx, outbox.ChannelTelegram) },
			RemindAfter: time.Duration(env.Telegram.RemindMinutes) * time.Minute, DigestAt: env.Telegram.DigestAt,
			Letters: func(ctx context.Context) int {
				if letters == nil {
					return 0
				}
				return letters.Status(ctx).Unmatched
			},
			Audit: func(ctx context.Context, actor, action, subject, details string) {
				accounts.Audit(ctx, actor, action, subject, details, "telegram")
			},
		})
		// New requests reach the bot through the outbox; whatever then happens to a request — in
		// the bot or in the admin area — redraws its cards, and erasing one wipes them.
		deliveries.Register(outbox.ChannelTelegram, bot)
		leadStore.OnChange(bot.Changed)
		leadStore.OnErase(bot.Erasing)
		botRunner = telegram.NewRunner(bot, telegram.RunnerOptions{Mode: env.Telegram.Mode, SiteURL: env.SiteURL, Secret: []byte(env.Secret), Log: log})
		botRunner.Register(srv.Mux())
	} else {
		log.Warn("TELEGRAM_BOT_TOKEN is not set: there is no bot; notifications for Telegram wait in the outbox")
	}

	reports := analytics.NewReports(pool, location, nil)
	reports.KeepRaw(env.AnalyticsKeepMonths)
	panel, err := admin.New(admin.Options{
		Prefix:   env.AdminPath,
		SiteHost: siteURL.Hostname(),
		Auth:     accounts,
		Log:      log,
		Version:  version(),
		Reports:  reports,
		Location: location,
		Feed:     stats.Subscribe,
		Active: func(ctx context.Context, window time.Duration) int {
			return store.CountActive(ctx, analytics.ActiveSet, window)
		},
		Traffic:   nginxlog.NewReports(pool, location),
		System:    system,
		LogPolled: accessLog.LastPoll,
		Leads:     leadStore,
		Form:      form.Current,
		Kick:      deliveries.Kick,
		BotAccess: botAccess,
		BotStatus: func() (telegram.Status, bool) {
			if botRunner == nil {
				return telegram.Status{}, false
			}
			return botRunner.Status(), true
		},
		Inbox:           lettersPanel,
		Mailbox:         env.Mail.Inbox,
		KeepLettersDays: env.Retention.SpamDays,
		Geo:             geoInfo,
	})
	if err != nil {
		return err
	}
	panel.Register(srv.Mux())

	// Background workers outlive the HTTP server by a moment: they flush what is still queued.
	var workers sync.WaitGroup
	workers.Add(4)
	go func() {
		defer workers.Done()
		leads.Retention{
			Store: leadStore, Files: attachments, Log: log,
			KeepMonths: env.Retention.KeepMonths, Delete: env.Retention.Delete, SpamDays: env.Retention.SpamDays,
		}.Run(ctx)
	}()
	if botRunner != nil {
		workers.Add(2)
		go func() {
			defer workers.Done()
			botRunner.Run(ctx)
		}()
		go func() {
			defer workers.Done()
			bot.RunCards(ctx)
		}()
		workers.Add(1)
		go func() {
			defer workers.Done()
			bot.RunReminders(ctx)
		}()
	}
	if letters != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			letters.Run(ctx)
		}()
	}
	// Finished days are summed up for good; raw page views older than the storage period go (brief B5).
	workers.Add(1)
	go func() {
		defer workers.Done()
		analytics.Rollup{DB: pool, Location: location, Log: log, KeepMonths: env.AnalyticsKeepMonths}.Run(ctx)
	}()
	go func() {
		defer workers.Done()
		stats.Run(ctx)
	}()
	go func() {
		defer workers.Done()
		deliveries.Run(ctx)
	}()
	go func() {
		defer workers.Done()
		accessLog.Run(ctx)
	}()

	err = srv.Run(ctx)
	stop()
	workers.Wait()
	return err
}

// ownerLocation is the time zone «today» is counted in (content/site.yaml → timezone).
func ownerLocation(contentDir string, log *slog.Logger) *time.Location {
	content, err := config.LoadContent(contentDir)
	if err != nil {
		log.Warn("cannot read the content directory, reports will use UTC", "error", err)
		return time.UTC
	}
	location, err := time.LoadLocation(content.Site.Timezone)
	if err != nil || content.Site.Timezone == "" {
		log.Warn("content/site.yaml: unknown timezone, reports will use UTC", "timezone", content.Site.Timezone)
		return time.UTC
	}
	return location
}

// senderName is what letters are signed with when MAIL_FROM carries no name: the owner as the
// site presents them (content/site.yaml → profile), or nothing if the content cannot be read.
func senderName(contentDir string) string {
	content, err := config.LoadContent(contentDir)
	if err != nil {
		return ""
	}
	return content.Site.SenderName()
}

func newLogger(level string) *slog.Logger {
	levels := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}
	// journald adds the timestamp itself.
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: levels[level],
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		},
	}))
}

// version is the commit the binary was built from: the server builds inside the git checkout,
// so the toolchain stamps it without any build flags.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	revision, dirty := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision == "" {
		return "unknown"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if dirty {
		revision += "-dirty"
	}
	return revision
}
