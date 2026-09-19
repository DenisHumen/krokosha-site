package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

const botUsage = `Usage: krokosha-cli bot <command> [flags]

  check                     ask Telegram whose token is configured (getMe)
  invite [--owner]          make a one-time invitation: a code that lives 24 hours
  users                     who has access to the bot
  disable ID                revoke access (ID from «users»); works at once
  enable  ID                give access back

Flags:
  --env-file PATH           settings of the installed site (default /etc/krokosha/env)

The first owner gets in with «krokosha-cli bot invite --owner»; everybody else can be invited
by an owner from the bot itself (/invite) or from the admin area.
`

func runBot(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("bot", flag.ContinueOnError)
	flags.Usage = func() { fmt.Fprint(os.Stderr, botUsage) }
	envFile := flags.String("env-file", config.DefaultEnvFile, "")
	owner := flags.Bool("owner", false, "")

	if len(args) == 0 {
		flags.Usage()
		return flag.ErrHelp
	}
	command := args[0]
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	// Flags may come before or after the argument: `bot disable 3 --env-file …`.
	var argument string
	if rest := flags.Args(); len(rest) > 0 {
		argument = rest[0]
		if err := flags.Parse(rest[1:]); err != nil {
			return err
		}
	}

	env, err := config.LoadEnv(config.LookupWithFile(*envFile))
	if err != nil {
		return fmt.Errorf("%w (is the site installed? try sudo, or --env-file)", err)
	}

	// me asks Telegram who the bot is; ok is false when there is no token or no answer.
	me := func() (telegram.User, error) {
		if env.Telegram.Token == "" {
			return telegram.User{}, errors.New("TELEGRAM_BOT_TOKEN is not set in " + *envFile)
		}
		asking, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return telegram.NewClient(env.Telegram.Token, env.Telegram.API).GetMe(asking)
	}
	if command == "check" {
		bot, err := me()
		if err != nil {
			return err
		}
		fmt.Printf("@%s\n", bot.Username)
		return nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pool, err := db.Open(ctx, env.MySQL, 30*time.Second, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	// The CLI may run before the service ever started (the installer invites the first owner).
	if _, err := db.Migrate(ctx, env.MySQL, migrations.Files, log); err != nil {
		return err
	}
	access := telegram.NewAccess(pool, nil)

	switch command {
	case "invite":
		role := telegram.RoleMember
		if *owner {
			role = telegram.RoleOwner
		}
		code, expires, err := access.Invite(ctx, role, "cli")
		if err != nil {
			return err
		}
		// The code goes to standard output alone, so that a script can take it; the explanation
		// goes to the person.
		fmt.Fprintf(os.Stderr, "One-time invitation for a new %s, valid until %s.\n", role, expires.Local().Format("02.01.2006 15:04"))
		if bot, err := me(); err == nil && bot.Username != "" {
			fmt.Fprintf(os.Stderr, "Open in Telegram:  https://t.me/%s?start=%s\n", bot.Username, code)
		}
		fmt.Fprintln(os.Stderr, "Or write to the bot:")
		fmt.Printf("/start %s\n", code)
		return nil
	case "users":
		members, err := access.Members(ctx)
		if err != nil {
			return err
		}
		for _, member := range members {
			state := "active"
			if member.DisabledAt.Valid {
				state = "disabled"
			}
			fmt.Printf("%d\t%s\t%s\t%s\t@%s\n", member.ID, member.Role, state, member.Name, member.Username)
		}
		return nil
	case "disable", "enable":
		id, err := strconv.ParseInt(argument, 10, 64)
		if err != nil {
			return fmt.Errorf("%s needs the ID of a member (see «krokosha-cli bot users»)", command)
		}
		member, err := access.SetDisabled(ctx, id, command == "disable")
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", member.Name, map[bool]string{true: "access revoked", false: "access given back"}[command == "disable"])
		return nil
	default:
		flags.Usage()
		return fmt.Errorf("unknown command %q", command)
	}
}
