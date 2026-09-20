package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

// runAlert queues a message about the server for its owner — by mail and in Telegram. The
// scripts that watch over the server use it (krokosha-certwatch, backup.sh): they cannot send
// anything themselves, and should not have to know how.
//
//	echo "certbot renew failed: …" | krokosha-cli alert --key cert:2026-09-20 --subject "Сертификат не продлевается"
func runAlert(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("alert", flag.ContinueOnError)
	envFile := flags.String("env-file", config.DefaultEnvFile, "settings of the installed site")
	key := flags.String("key", "", "names the occasion: an alert with the same key is sent once (default: the subject and today's date)")
	subject := flags.String("subject", "", "one line: what is wrong")
	flags.Usage = func() {
		_, _ = fmt.Fprint(flags.Output(), "Usage: krokosha-cli alert --subject TEXT [--key KEY] < details\n\nQueues a message for the owner of the site: mail to MAIL_NOTIFY_TO, Telegram to the owners of the bot.\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*subject) == "" {
		flags.Usage()
		return errors.New("--subject is required")
	}
	// The details come on standard input: they may be long, and may quote what a program printed.
	var text []byte
	if stat, err := os.Stdin.Stat(); err == nil && stat.Mode()&os.ModeCharDevice == 0 {
		if text, err = io.ReadAll(io.LimitReader(os.Stdin, 16<<10)); err != nil {
			return err
		}
	}
	if *key == "" {
		*key = *subject + ":" + time.Now().UTC().Format(time.DateOnly)
	}

	env, err := config.LoadEnv(config.LookupWithFile(*envFile))
	if err != nil {
		return fmt.Errorf("%w (is the site installed? try sudo, or --env-file)", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pool, err := db.Open(ctx, env.MySQL, 30*time.Second, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, env.MySQL, migrations.Files, log); err != nil {
		return err
	}
	return outbox.EnqueueAlert(ctx, pool, time.Now(), *key, outbox.Alert{Subject: *subject, Text: string(text)})
}
