// Command krokosha-cli is the maintenance tool of the site. It runs on the server (systemd timer,
// install and update scripts) and on a developer machine.
//
//	krokosha-cli sync      pull public repositories and the avatar from GitHub
//	krokosha-cli admin     manage the accounts of the admin area
//	krokosha-cli bot       the Telegram bot: check the token, invitations, access
//	krokosha-cli alert     tell the owner that the server needs a look (used by the watchdog scripts)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/githubsync"
	"github.com/DenisHumen/krokosha-site/api/internal/hardening"
)

const usage = `krokosha-cli — maintenance tool of krokosha-site

Usage:
  krokosha-cli sync [flags]    pull public repositories and the avatar from GitHub
                               into content/generated/ (input of the site build)
  krokosha-cli admin <cmd>     accounts of the admin area: list, create, passwd, disable,
                               enable, totp-reset
  krokosha-cli bot <cmd>       the Telegram bot: check, invite, users, disable, enable
  krokosha-cli alert [flags]   queue a message about the server for its owner (mail and
                               Telegram); the details are read from standard input
  krokosha-cli indexnow [flags]
                               tell the search engines of IndexNow which pages of a new
                               release changed (build-release.sh does it after every release)
  krokosha-cli netmap <cmd>    the map of the internet of /map: fetch, build, route

Run "krokosha-cli <command> -h" for the flags of a command.

Environment:
  GITHUB_TOKEN          optional fine-grained token, read-only access to public repositories;
                        raises the API limit from 60 to 5000 requests per hour
  KROKOSHA_CACHE_DIR    where the GitHub cache lives (default: <out>/.cache)
`

func main() {
	// The CLI runs with the secrets of /etc/krokosha/env too (the map's timer, the bot's commands).
	if err := hardening.PrivateEnvironment(); err != nil {
		fmt.Fprintln(os.Stderr, "krokosha-cli: cannot keep the environment private:", err)
		os.Exit(1)
	}
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch command, args := os.Args[1], os.Args[2:]; command {
	case "sync":
		err = runSync(ctx, args)
	case "admin":
		err = runAdmin(ctx, args)
	case "bot":
		err = runBot(ctx, args)
	case "alert":
		err = runAlert(ctx, args)
	case "indexnow":
		err = runIndexNow(ctx, args)
	case "netmap":
		err = runNetmap(ctx, args)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", command, usage)
		return 2
	}
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return 1
	}
	return 0
}

func runSync(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("sync", flag.ContinueOnError)
	contentDir := flags.String("content", "", "content directory (default: the closest content/ at or above the working directory)")
	outDir := flags.String("out", "", "output directory (default: <content>/generated)")
	cacheDir := flags.String("cache", os.Getenv("KROKOSHA_CACHE_DIR"), "cache directory (default: <out>/.cache)")
	staleAfter := flags.Duration("stale-after", 24*time.Hour, "mark the data as stale when GitHub has been unreachable for this long")
	timeout := flags.Duration("timeout", 5*time.Minute, "give up after this long")
	verbose := flags.Bool("v", false, "verbose log")
	if err := flags.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if *contentDir == "" {
		found, err := config.FindContentDir(".")
		if err != nil {
			return err
		}
		*contentDir = found
	}
	content, err := config.LoadContent(*contentDir)
	if err != nil {
		return err
	}
	if *outDir == "" {
		*outDir = filepath.Join(*contentDir, "generated")
	}
	if *cacheDir == "" {
		*cacheDir = filepath.Join(*outDir, ".cache")
	}

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	result, err := githubsync.Run(ctx, githubsync.Options{
		Content:    content,
		OutDir:     *outDir,
		CacheDir:   *cacheDir,
		Token:      os.Getenv("GITHUB_TOKEN"),
		StaleAfter: *staleAfter,
		Logger:     logger,
	})
	if err != nil {
		return err
	}

	logger.Info("GitHub sync finished",
		"user", content.Site.GitHub.User,
		"repositories", result.Repos,
		"shown", result.Public,
		"fresh", result.Fresh,
		"stale", result.Stale,
		"syncedAt", result.SyncedAt.Format(time.RFC3339),
		"metricsFetched", result.MetricsFetched,
		"avatarUpdated", result.AvatarUpdated,
		"out", *outDir,
	)
	if len(result.MetricsMissing) > 0 {
		logger.Warn("some repositories are ranked without metrics until the next run", "repos", result.MetricsMissing)
	}
	return nil
}
