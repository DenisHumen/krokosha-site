package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// Env is the configuration of the API service. On the server it comes from /etc/krokosha/env
// through systemd's EnvironmentFile (deploy/env/.env.example describes every variable).
type Env struct {
	// Listen is the address of the HTTP server. Always loopback: nginx is the only client.
	Listen string
	// SiteURL is the public origin of the site, e.g. https://krokosha.xyz.
	SiteURL string
	// AdminPath is the hidden prefix of the admin area, e.g. /_k7f3a9 (brief B6).
	AdminPath string
	// ContentDir points to content/ of the installed repository.
	ContentDir string
	// DataDir is the root of everything that must survive a migration (docs/architecture.md §6).
	DataDir string
	// StateDir holds what can be rebuilt: caches, the report of the last site build, requests
	// to the build (the «rebuild now» button).
	StateDir string
	// WWWDir is where the releases of the site live; «current» points to the live one.
	WWWDir string
	// AccessLog is nginx's JSON access log, the source of the «server traffic» screen.
	AccessLog string

	MySQL MySQL
	// RedisURL may be empty: the service then keeps rate limits and live data in memory.
	RedisURL string

	LogLevel string
}

// MySQL holds the connection parameters of the main database.
type MySQL struct {
	Addr     string // host:port
	Database string
	User     string
	Password string
}

var reAdminPath = regexp.MustCompile(`^/[A-Za-z0-9_-]{4,64}$`)

// LoadEnv reads the configuration from environment variables and validates it.
// lookup is os.LookupEnv in production; tests pass their own.
func LoadEnv(lookup func(string) (string, bool)) (*Env, error) {
	get := func(key, fallback string) string {
		if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
		return fallback
	}

	env := &Env{
		Listen:     get("KROKOSHA_LISTEN", "127.0.0.1:8080"),
		SiteURL:    strings.TrimRight(get("SITE_URL", ""), "/"),
		AdminPath:  strings.TrimRight(get("ADMIN_PATH", ""), "/"),
		ContentDir: get("KROKOSHA_CONTENT_DIR", "/opt/krokosha/repo/content"),
		DataDir:    get("KROKOSHA_DATA", "/srv/krokosha"),
		StateDir:   get("KROKOSHA_STATE", "/var/lib/krokosha"),
		WWWDir:     get("KROKOSHA_WWW", "/var/www/krokosha"),
		AccessLog:  get("KROKOSHA_ACCESS_LOG", "/var/log/krokosha/nginx-access.json.log"),
		RedisURL:   get("REDIS_URL", ""),
		LogLevel:   strings.ToLower(get("KROKOSHA_LOG_LEVEL", "info")),
		MySQL: MySQL{
			Addr:     get("MYSQL_ADDR", "127.0.0.1:3306"),
			Database: get("MYSQL_DATABASE", "krokosha"),
			User:     get("MYSQL_USER", "krokosha"),
			Password: get("MYSQL_PASSWORD", ""),
		},
	}

	var problems []string
	if host, _, err := net.SplitHostPort(env.Listen); err != nil {
		problems = append(problems, fmt.Sprintf("KROKOSHA_LISTEN: %v", err))
	} else if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		// The API trusts X-Real-IP from its only client, nginx. Exposed directly, anyone could forge it.
		problems = append(problems, "KROKOSHA_LISTEN must be a loopback address: the service is reached only through nginx")
	}
	if parsed, err := url.Parse(env.SiteURL); err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		problems = append(problems, "SITE_URL must be the public origin of the site, e.g. https://example.com")
	}
	if !reAdminPath.MatchString(env.AdminPath) {
		problems = append(problems, "ADMIN_PATH must look like /_k7f3a9 (letters, digits, - and _, at least 4 characters)")
	}
	if env.MySQL.Password == "" {
		problems = append(problems, "MYSQL_PASSWORD is required")
	}
	if env.RedisURL != "" {
		if parsed, err := url.Parse(env.RedisURL); err != nil || (parsed.Scheme != "redis" && parsed.Scheme != "rediss" && parsed.Scheme != "unix") {
			problems = append(problems, "REDIS_URL must look like redis://:password@127.0.0.1:6379/0")
		}
	}
	switch env.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, "KROKOSHA_LOG_LEVEL must be debug, info, warn or error")
	}
	if len(problems) > 0 {
		return nil, errors.New("configuration: " + strings.Join(problems, "; "))
	}
	return env, nil
}

// LoadEnvFromOS is LoadEnv over the process environment.
func LoadEnvFromOS() (*Env, error) {
	return LoadEnv(os.LookupEnv)
}
