package config

import (
	"strings"
	"testing"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"SITE_URL":       "https://krokosha.xyz/",
		"ADMIN_PATH":     "/_k7f3a9/",
		"MYSQL_PASSWORD": "secret",
		"APP_SECRET":     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func TestLoadEnvDefaults(t *testing.T) {
	env, err := LoadEnv(lookupFrom(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if env.Listen != "127.0.0.1:8080" || env.MySQL.Addr != "127.0.0.1:3306" || env.MySQL.Database != "krokosha" {
		t.Errorf("defaults: %+v", env)
	}
	if env.SiteURL != "https://krokosha.xyz" || env.AdminPath != "/_k7f3a9" {
		t.Errorf("trailing slashes must be trimmed: %q %q", env.SiteURL, env.AdminPath)
	}
	if env.RedisURL != "" {
		t.Error("Redis must be optional")
	}
}

func TestLoadEnvRejects(t *testing.T) {
	cases := map[string]struct {
		key, value, want string
	}{
		"listening on every interface":   {"KROKOSHA_LISTEN", "0.0.0.0:8080", "loopback"},
		"listening on a public address":  {"KROKOSHA_LISTEN", "203.0.113.7:8080", "loopback"},
		"listen without a port":          {"KROKOSHA_LISTEN", "127.0.0.1", "KROKOSHA_LISTEN"},
		"site url without a scheme":      {"SITE_URL", "krokosha.xyz", "SITE_URL"},
		"guessable admin path":           {"ADMIN_PATH", "/a", "ADMIN_PATH"},
		"admin path with a slash inside": {"ADMIN_PATH", "/_a/b", "ADMIN_PATH"},
		"no database password":           {"MYSQL_PASSWORD", " ", "MYSQL_PASSWORD"},
		"redis url of another scheme":    {"REDIS_URL", "http://127.0.0.1:6379", "REDIS_URL"},
		"unknown log level":              {"KROKOSHA_LOG_LEVEL", "verbose", "KROKOSHA_LOG_LEVEL"},
		"a secret one could guess":       {"APP_SECRET", "changeme", "APP_SECRET"},
		"smtp without a port":            {"SMTP_ADDR", "127.0.0.1", "SMTP_ADDR"},
		"smtp without a sender":          {"SMTP_ADDR", "127.0.0.1:587", "MAIL_FROM"},
		"months that are not a number":   {"LEADS_KEEP_MONTHS", "two years", "LEADS_KEEP_MONTHS"},
		"a bot token that is not one":    {"TELEGRAM_BOT_TOKEN", "my-bot", "TELEGRAM_BOT_TOKEN"},
		"a digest at no time":            {"TELEGRAM_DIGEST_AT", "9 утра", "TELEGRAM_DIGEST_AT"},
		"a reminder in minus minutes":    {"TELEGRAM_REMIND_MINUTES", "-5", "TELEGRAM_REMIND_MINUTES"},
		"a negative number of days":      {"LEADS_SPAM_DAYS", "-1", "LEADS_SPAM_DAYS"},
		"an unknown rule for old data":   {"LEADS_EXPIRED", "archive", "LEADS_EXPIRED"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			values := validEnv()
			values[tc.key] = tc.value
			_, err := LoadEnv(lookupFrom(values))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadEnvReportsEveryProblemAtOnce(t *testing.T) {
	_, err := LoadEnv(lookupFrom(map[string]string{}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"SITE_URL", "ADMIN_PATH", "MYSQL_PASSWORD", "APP_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestLoadEnvAcceptsLocalhostAndIPv6Loopback(t *testing.T) {
	for _, listen := range []string{"localhost:8080", "[::1]:8080", "127.0.0.2:9000"} {
		values := validEnv()
		values["KROKOSHA_LISTEN"] = listen
		if _, err := LoadEnv(lookupFrom(values)); err != nil {
			t.Errorf("%s: %v", listen, err)
		}
	}
}

func TestLoadEnvMail(t *testing.T) {
	values := validEnv()
	values["SMTP_ADDR"], values["SMTP_USER"], values["SMTP_PASSWORD"] = "127.0.0.1:587", "leads@krokosha.xyz", "pw"
	values["MAIL_FROM"], values["MAIL_NOTIFY_TO"] = "Denis Humen <denis@krokosha.xyz>", "denis@krokosha.xyz"
	env, err := LoadEnv(lookupFrom(values))
	if err != nil {
		t.Fatal(err)
	}
	if env.Mail.SMTPAddr != "127.0.0.1:587" || env.Mail.From != "Denis Humen <denis@krokosha.xyz>" || env.Mail.NotifyTo != "denis@krokosha.xyz" {
		t.Errorf("mail: %+v", env.Mail)
	}
	// Mail is optional: without it notifications wait in the outbox.
	if env, err := LoadEnv(lookupFrom(validEnv())); err != nil || env.Mail.SMTPAddr != "" {
		t.Errorf("without mail: %+v, %v", env, err)
	}
}

// The privacy page promises 24 months; the service keeps that promise unless told otherwise.
func TestLoadEnvRetention(t *testing.T) {
	env, err := LoadEnv(lookupFrom(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if env.Retention != (Retention{KeepMonths: 24, SpamDays: 30}) {
		t.Errorf("defaults: %+v", env.Retention)
	}
	values := validEnv()
	values["LEADS_KEEP_MONTHS"], values["LEADS_SPAM_DAYS"], values["LEADS_EXPIRED"] = "12", "0", "Delete"
	if env, err = LoadEnv(lookupFrom(values)); err != nil || env.Retention != (Retention{KeepMonths: 12, Delete: true}) {
		t.Errorf("set by hand: %+v %v", env.Retention, err)
	}
}

func TestLoadEnvTelegram(t *testing.T) {
	values := validEnv()
	env, err := LoadEnv(lookupFrom(values))
	if err != nil || env.Telegram.Token != "" {
		t.Fatalf("without a token there is no bot: %+v %v", env.Telegram, err)
	}
	values["TELEGRAM_BOT_TOKEN"] = "123456789:AAH-abcdefghijklmnopqrstuvwxyz_0123456"
	if env, err = LoadEnv(lookupFrom(values)); err != nil || env.Telegram.Mode != "webhook" || env.Telegram.API != "" ||
		env.Telegram.RemindMinutes != 30 || env.Telegram.DigestAt != "09:00" {
		t.Fatalf("defaults: %+v %v", env.Telegram, err)
	}
	values["TELEGRAM_REMIND_MINUTES"], values["TELEGRAM_DIGEST_AT"] = "0", "OFF"
	if env, err = LoadEnv(lookupFrom(values)); err != nil || env.Telegram.RemindMinutes != 0 || env.Telegram.DigestAt != "" {
		t.Fatalf("reminders switched off: %+v %v", env.Telegram, err)
	}
	for name, change := range map[string][2]string{
		"an unknown mode":            {"TELEGRAM_MODE", "push"},
		"an API address that is not": {"TELEGRAM_API_URL", "telegram"},
		"a webhook without https":    {"SITE_URL", "http://localhost:8099"},
	} {
		broken := validEnv()
		broken["TELEGRAM_BOT_TOKEN"] = values["TELEGRAM_BOT_TOKEN"]
		broken[change[0]] = change[1]
		if _, err := LoadEnv(lookupFrom(broken)); err == nil || !strings.Contains(err.Error(), "TELEGRAM_") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A developer's machine: no HTTPS, so the site asks Telegram instead of being called.
	values["SITE_URL"], values["TELEGRAM_MODE"], values["TELEGRAM_API_URL"] = "http://localhost:8099", "Polling", "http://127.0.0.1:8081/"
	if env, err = LoadEnv(lookupFrom(values)); err != nil || env.Telegram.Mode != "polling" || env.Telegram.API != "http://127.0.0.1:8081" {
		t.Errorf("polling on a developer's machine: %+v %v", env.Telegram, err)
	}
}
