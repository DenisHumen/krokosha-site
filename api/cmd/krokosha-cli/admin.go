package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/DenisHumen/krokosha-site/api/internal/auth"
	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

const adminUsage = `Usage: krokosha-cli admin <command> [flags]

  list                      accounts of the admin area
  create  LOGIN             add an administrator (asks for the password)
  passwd  LOGIN             set a new password, ends the account's sessions
  disable LOGIN             block an account and end its sessions
  enable  LOGIN             unblock an account
  totp-reset LOGIN          switch two-factor authentication off (lost phone)

Flags:
  --env-file PATH           settings of the installed site (default /etc/krokosha/env)
  --password-stdin          read the password from standard input instead of asking

The password must be at least 12 characters long.
`

func runAdmin(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("admin", flag.ContinueOnError)
	flags.Usage = func() { fmt.Fprint(os.Stderr, adminUsage) }
	envFile := flags.String("env-file", config.DefaultEnvFile, "")
	passwordStdin := flags.Bool("password-stdin", false, "")

	if len(args) == 0 {
		flags.Usage()
		return flag.ErrHelp
	}
	command := args[0]
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	// Flags may come before or after the login: `admin create denis --password-stdin`.
	var login string
	rest := flags.Args()
	if len(rest) > 0 {
		login = rest[0]
		if err := flags.Parse(rest[1:]); err != nil {
			return err
		}
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
	// The CLI may run before the service ever started (the installer creates the first account).
	if _, err := db.Migrate(ctx, env.MySQL, migrations.Files, log); err != nil {
		return err
	}
	store, err := cache.New(ctx, "", slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}
	accounts := auth.New(pool, store, log)

	needLogin := func() error {
		if login == "" {
			return errors.New("which login? see: krokosha-cli admin --help")
		}
		return nil
	}

	switch command {
	case "list":
		users, err := accounts.Users(ctx)
		if err != nil {
			return err
		}
		if len(users) == 0 {
			// Nothing on standard output: the installer tells «none yet» from «some» by that.
			fmt.Fprintln(os.Stderr, "no administrators yet: krokosha-cli admin create LOGIN")
			return nil
		}
		for _, user := range users {
			state := "active"
			if user.Disabled {
				state = "disabled"
			}
			twoFactor := "password only"
			if user.TOTPEnabled {
				twoFactor = "two-factor"
			}
			lastLogin := "never signed in"
			if user.LastLoginAt.Valid {
				lastLogin = "last login " + user.LastLoginAt.Time.Local().Format("2006-01-02 15:04")
			}
			fmt.Printf("%-24s %-9s %-14s %s\n", user.Login, state, twoFactor, lastLogin)
		}
		return nil
	case "create", "passwd":
		if err := needLogin(); err != nil {
			return err
		}
		password, err := readPassword(*passwordStdin)
		if err != nil {
			return err
		}
		if command == "create" {
			err = accounts.CreateUser(ctx, login, password)
		} else {
			err = accounts.SetPassword(ctx, strings.ToLower(login), password)
		}
		if err != nil {
			return err
		}
		fmt.Printf("ok: %s\nadmin area: %s%s/\n", strings.ToLower(login), env.SiteURL, env.AdminPath)
		return nil
	case "disable", "enable":
		if err := needLogin(); err != nil {
			return err
		}
		return accounts.SetDisabled(ctx, strings.ToLower(login), command == "disable")
	case "totp-reset":
		if err := needLogin(); err != nil {
			return err
		}
		return accounts.ResetTOTP(ctx, strings.ToLower(login))
	default:
		flags.Usage()
		return fmt.Errorf("unknown admin command %q", command)
	}
}

// readPassword asks twice on a terminal (no echo) or takes one line from standard input.
func readPassword(fromStdin bool) (string, error) {
	stdin := int(os.Stdin.Fd())
	if fromStdin || !term.IsTerminal(stdin) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no password on standard input")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(stdin)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Once more: ")
	second, err := term.ReadPassword(stdin)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("the passwords do not match")
	}
	return string(first), nil
}
