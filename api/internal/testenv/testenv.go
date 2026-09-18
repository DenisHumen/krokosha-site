// Package testenv gives integration tests a real MySQL and Redis (api/compose.test.yaml, or the
// service containers in CI). Without KROKOSHA_TEST_MYSQL / KROKOSHA_TEST_REDIS such tests skip.
package testenv

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

const rootPassword = "krokosha-test"

// MySQL creates an empty database that lives for the duration of the test.
func MySQL(t *testing.T) config.MySQL {
	t.Helper()
	addr := os.Getenv("KROKOSHA_TEST_MYSQL")
	if addr == "" {
		t.Skip("KROKOSHA_TEST_MYSQL is not set (see api/compose.test.yaml)")
	}

	var raw [6]byte
	_, _ = rand.Read(raw[:])
	name := "t_" + hex.EncodeToString(raw[:])

	admin := mysql.NewConfig()
	admin.Net, admin.Addr, admin.User, admin.Passwd = "tcp", addr, "root", rootPassword
	admin.Timeout = 5 * time.Second
	connector, err := mysql.NewConnector(admin)
	if err != nil {
		t.Fatal(err)
	}
	root := sql.OpenDB(connector)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := root.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		_ = root.Close()
		t.Fatalf("cannot create a test database at %s: %v", addr, err)
	}
	t.Cleanup(func() {
		_, _ = root.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+name+"`")
		_ = root.Close()
	})
	return config.MySQL{Addr: addr, Database: name, User: "root", Password: rootPassword}
}

// RedisURL returns the URL of the test Redis, on a database number of its own… there is only one
// test Redis, so tests must use unique keys.
func RedisURL(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("KROKOSHA_TEST_REDIS")
	if addr == "" {
		t.Skip("KROKOSHA_TEST_REDIS is not set (see api/compose.test.yaml)")
	}
	return "redis://" + addr + "/0"
}
