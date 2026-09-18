package config

import (
	"bufio"
	"os"
	"strings"
)

// DefaultEnvFile is where install.sh keeps the settings of an installed site.
const DefaultEnvFile = "/etc/krokosha/env"

// ReadEnvFile parses KEY=VALUE lines (the format systemd's EnvironmentFile reads).
func ReadEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		values[strings.TrimSpace(key)] = value
	}
	return values, scanner.Err()
}

// LookupWithFile prefers the process environment and falls back to the env file. The service gets
// its settings from systemd; the CLI, started by hand with sudo, finds them in the file.
func LookupWithFile(path string) func(string) (string, bool) {
	fromFile, _ := ReadEnvFile(path) // a missing or unreadable file simply contributes nothing
	return func(key string) (string, bool) {
		if value, ok := os.LookupEnv(key); ok {
			return value, true
		}
		value, ok := fromFile[key]
		return value, ok
	}
}
