package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/db"
	"github.com/DenisHumen/krokosha-site/api/internal/testenv"
	"github.com/DenisHumen/krokosha-site/api/migrations"
)

var quiet = slog.New(slog.DiscardHandler)

const goodPassword = "correct horse battery staple"

func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("hash format: %s", hash)
	}
	if !VerifyPassword(hash, goodPassword) {
		t.Error("the right password was refused")
	}
	if VerifyPassword(hash, goodPassword+" ") || VerifyPassword(hash, "") {
		t.Error("a wrong password was accepted")
	}
	other, _ := HashPassword(goodPassword)
	if other == hash {
		t.Error("two hashes of one password are equal: the salt is not random")
	}

	if _, err := HashPassword("short"); err == nil {
		t.Error("a 5-character password was accepted")
	}
	if _, err := HashPassword("коротко1234"); err == nil { // 11 characters, more than 12 bytes
		t.Error("length must be counted in characters, not bytes")
	}
}

func TestVerifyPasswordRejectsTamperedHashes(t *testing.T) {
	for _, encoded := range []string{
		"",
		"plain-text-password",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$aGFzaGhhc2g",
		"$argon2id$v=18$m=65536,t=3,p=2$c2FsdHNhbHQ$aGFzaGhhc2g",
		// Parameters that would make the server allocate 16 GB.
		"$argon2id$v=19$m=16777216,t=3,p=2$c2FsdHNhbHQ$aGFzaGhhc2g",
		"$argon2id$v=19$m=65536,t=3,p=0$c2FsdHNhbHQ$aGFzaGhhc2g",
		"$argon2id$v=19$m=65536,t=3,p=2$not base64!$aGFzaGhhc2g",
	} {
		if VerifyPassword(encoded, goodPassword) {
			t.Errorf("accepted %q", encoded)
		}
	}
}

// Test vectors of RFC 6238, appendix B (SHA-1), cut to six digits.
func TestTOTPMatchesRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	} {
		if got := totpAt(secret, unix/totpPeriod); got != want {
			t.Errorf("T=%d: got %s, want %s", unix, got, want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	step := now.Unix() / totpPeriod

	if got := VerifyTOTP(secret, "050471", now, 0); got != step {
		t.Errorf("current code: step %d, want %d", got, step)
	}
	if got := VerifyTOTP(secret, " 050 471 ", now, 0); got != step {
		t.Error("spaces typed by a person must not matter")
	}
	if got := VerifyTOTP(secret, "081804", now, 0); got != step-1 {
		t.Errorf("code of the previous step: got %d, want %d (phones' clocks drift)", got, step-1)
	}
	if VerifyTOTP(secret, "050471", now.Add(2*time.Minute), 0) != 0 {
		t.Error("a code two minutes old was accepted")
	}
	if VerifyTOTP(secret, "050471", now, step) != 0 {
		t.Error("the same code was accepted twice")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "000000"} {
		if VerifyTOTP(secret, bad, now, 0) != 0 {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTOTPEnrolmentTexts(t *testing.T) {
	secret := []byte("12345678901234567890")
	if got := TOTPSecretText(secret); got != "GEZD GNBV GY3T QOJQ GEZD GNBV GY3T QOJQ" {
		t.Errorf("secret text: %q", got)
	}
	uri := TOTPURI(secret, "krokosha.xyz", "denis")
	for _, want := range []string{"otpauth://totp/krokosha.xyz:denis?", "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "issuer=krokosha.xyz", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI %q lacks %q", uri, want)
		}
	}
}

type fixture struct {
	*Service
	mu  sync.Mutex
	now time.Time
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := testenv.MySQL(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, cfg, migrations.Files, quiet); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, cfg, 10*time.Second, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store, err := cache.New(ctx, "", quiet)
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	clock := func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
	store.SetClock(clock)
	f.Service = New(pool, store, quiet)
	f.SetClock(clock)
	if err := f.CreateUser(ctx, " Denis ", goodPassword); err != nil {
		t.Fatal(err)
	}
	return f
}

func attempt(password, code string) Attempt {
	return Attempt{Login: "denis", Password: password, Code: code, IPPrefix: "203.0.113.0/24", Client: "Chrome · Windows"}
}

func TestLoginAndSessionLifetime(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.Login(ctx, attempt("wrong password!", "")); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	unknown := attempt(goodPassword, "")
	unknown.Login = "nobody"
	if _, err := f.Login(ctx, unknown); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("unknown login must look exactly like a wrong password: %v", err)
	}

	result, err := f.Login(ctx, attempt(goodPassword, ""))
	if err != nil {
		t.Fatal(err)
	}
	token, session := result.Token, result.Session
	if len(token) != 43 || len(session.CSRFToken) != 43 || session.User.Login != "denis" {
		t.Errorf("token %q, session %+v", token, session)
	}
	if got, err := f.Authenticate(ctx, token); err != nil || got.CSRFToken != session.CSRFToken {
		t.Fatalf("Authenticate: %v", err)
	}
	// One character is changed — really changed: the last one of such a token is «A» once in
	// sixteen times, and replacing it with «A» would test nothing.
	other := "A"
	if token[42] == 'A' {
		other = "B"
	}
	if _, err := f.Authenticate(ctx, token[:42]+other); !errors.Is(err, ErrNoSession) {
		t.Error("a token that differs in one character was accepted")
	}

	// The token itself is not in the database — only its hash.
	var stored int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM admin_sessions WHERE HEX(token_hash) LIKE ? OR csrf_token = ?`,
		"%"+token+"%", token).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("the raw token is stored (%d rows, err %v)", stored, err)
	}

	// Activity keeps the session alive…
	for range 5 {
		f.advance(90 * time.Minute)
		if _, err := f.Authenticate(ctx, token); err != nil {
			t.Fatalf("an active session ended early: %v", err)
		}
	}
	// …two hours of silence end it…
	f.advance(SessionIdle + time.Minute)
	if _, err := f.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("an idle session is still alive: %v", err)
	}

	// …and so does a day, however busy.
	result, _ = f.Login(ctx, attempt(goodPassword, ""))
	token = result.Token
	for range 23 {
		f.advance(time.Hour)
		if _, err := f.Authenticate(ctx, token); err != nil {
			t.Fatalf("hour by hour: %v", err)
		}
	}
	f.advance(time.Hour + time.Minute)
	if _, err := f.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("a session older than a day is still alive: %v", err)
	}
}

func TestLoginIsThrottled(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var last error
	for range loginAttempts*2 + 1 {
		_, last = f.Login(ctx, attempt("wrong password!", ""))
	}
	if !errors.Is(last, ErrThrottled) {
		t.Fatalf("after %d wrong passwords: %v, want ErrThrottled", loginAttempts*2+1, last)
	}
	if _, err := f.Login(ctx, attempt(goodPassword, "")); !errors.Is(err, ErrThrottled) {
		t.Errorf("the right password got through a throttled login: %v", err)
	}
	f.advance(loginWindow + time.Second)
	if _, err := f.Login(ctx, attempt(goodPassword, "")); err != nil {
		t.Errorf("after the window: %v", err)
	}
}

// TestLoginNamesAreNotPatterns: MySQL compares text by collation, so «ádmin» or «ＡＤＭＩＮ» would
// find the account «admin» — each with a counter of tries of its own. What cannot be a login is
// refused before the database, even with the right password, and still leaves a line in the log.
func TestLoginNamesAreNotPatterns(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, name := range []string{"dénis", "ＤＥＮＩＳ", "den\u200bis", "denis\u200b", "denis" + strings.Repeat("\u200b", 60), strings.Repeat("d", 65)} {
		lookalike := attempt(goodPassword, "")
		lookalike.Login = name
		if _, err := f.Login(ctx, lookalike); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("%q: %v", name, err)
		}
	}
	var logged int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'admin.login-failed' AND details LIKE 'not a login name:%'`).Scan(&logged); err != nil || logged != 6 {
		t.Errorf("failed logins in the audit log: %d (%v)", logged, err)
	}
	// Upper case and spaces around are still the login itself.
	upper := attempt(goodPassword, "")
	upper.Login = "  DENIS "
	if _, err := f.Login(ctx, upper); err != nil {
		t.Errorf("the login in upper case: %v", err)
	}
}

func TestTwoFactorAuthentication(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first, err := f.Login(ctx, attempt(goodPassword, ""))
	if err != nil {
		t.Fatal(err)
	}
	session := first.Session

	secret, err := f.BeginTOTP(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	// Started but not confirmed: nobody is locked out by a QR code that was never scanned.
	if _, err := f.Login(ctx, attempt(goodPassword, "")); err != nil {
		t.Fatalf("login during enrolment: %v", err)
	}
	if err := f.ConfirmTOTP(ctx, session, "000000"); !errors.Is(err, ErrBadCode) {
		t.Errorf("confirming with a wrong code: %v", err)
	}
	code := func() string { return totpAt(secret, f.now.Unix()/totpPeriod) }
	if err := f.ConfirmTOTP(ctx, session, code()); err != nil {
		t.Fatal(err)
	}

	// Step one: the password alone yields a ticket, not a session.
	step1, err := f.Login(ctx, attempt(goodPassword, ""))
	if !errors.Is(err, ErrCodeRequired) || step1.Token != "" || len(step1.Pending) != 43 {
		t.Fatalf("password only: %+v, %v — want ErrCodeRequired and a ticket", step1, err)
	}
	if _, err := f.Login(ctx, attempt("wrong password!", code())); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("a valid code must not help a wrong password: %v", err)
	}
	meta := Attempt{IPPrefix: "203.0.113.0/24", Client: "Chrome · Windows"}
	if _, err := f.CompleteLogin(ctx, "made-up-ticket", code(), meta); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("a made-up ticket: %v", err)
	}
	if _, err := f.CompleteLogin(ctx, step1.Pending, "123456", meta); !errors.Is(err, ErrBadCode) {
		t.Errorf("wrong code: %v", err)
	}

	// The code that confirmed the enrolment was used: it cannot sign in as well.
	if _, err := f.CompleteLogin(ctx, step1.Pending, code(), meta); !errors.Is(err, ErrBadCode) {
		t.Errorf("a code was accepted twice: %v", err)
	}
	f.advance(totpPeriod * time.Second)
	done, err := f.CompleteLogin(ctx, step1.Pending, code(), meta)
	if err != nil || done.Token == "" {
		t.Fatalf("next code: %+v, %v", done, err)
	}
	if _, err := f.CompleteLogin(ctx, step1.Pending, code(), meta); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("a ticket was used twice: %v", err)
	}

	// A ticket survives only a few wrong codes…
	f.advance(totpPeriod * time.Second)
	step1, _ = f.Login(ctx, attempt(goodPassword, ""))
	var last error
	for range pendingTries + 1 {
		_, last = f.CompleteLogin(ctx, step1.Pending, "000000", meta)
	}
	if !errors.Is(last, ErrThrottled) {
		t.Errorf("after %d wrong codes: %v, want ErrThrottled", pendingTries+1, last)
	}
	// …and five minutes.
	step1, _ = f.Login(ctx, attempt(goodPassword, ""))
	f.advance(pendingLifetime + time.Second)
	if _, err := f.CompleteLogin(ctx, step1.Pending, code(), meta); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("an expired ticket: %v", err)
	}

	// Lost phone: reset from the server's console.
	if err := f.ResetTOTP(ctx, "denis"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Login(ctx, attempt(goodPassword, "")); err != nil {
		t.Errorf("after the reset: %v", err)
	}
}

func TestAccountManagement(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	loginA, _ := f.Login(ctx, attempt(goodPassword, ""))
	loginB, _ := f.Login(ctx, attempt(goodPassword, ""))
	tokenA, sessionA, tokenB := loginA.Token, loginA.Session, loginB.Token

	sessions, err := f.Sessions(ctx, sessionA)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions: %d, err %v", len(sessions), err)
	}
	for _, info := range sessions {
		if !info.Current {
			if err := f.Revoke(ctx, sessionA, info.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := f.Authenticate(ctx, tokenB); !errors.Is(err, ErrNoSession) {
		t.Error("the revoked session is still alive")
	}
	if _, err := f.Authenticate(ctx, tokenA); err != nil {
		t.Errorf("revoking another session ended this one: %v", err)
	}

	const next = "a brand new long password"
	if err := f.ChangePassword(ctx, sessionA, "not the current one", next); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("password change without the current password: %v", err)
	}
	if err := f.ChangePassword(ctx, sessionA, goodPassword, "short"); err == nil {
		t.Error("a short new password was accepted")
	}
	loginC, _ := f.Login(ctx, attempt(goodPassword, ""))
	tokenC := loginC.Token
	if err := f.ChangePassword(ctx, sessionA, goodPassword, next); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Authenticate(ctx, tokenC); !errors.Is(err, ErrNoSession) {
		t.Error("other sessions survived a password change")
	}
	if _, err := f.Login(ctx, attempt(goodPassword, "")); !errors.Is(err, ErrBadCredentials) {
		t.Error("the old password still works")
	}

	if err := f.SetDisabled(ctx, "denis", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Authenticate(ctx, tokenA); !errors.Is(err, ErrNoSession) {
		t.Error("a disabled account keeps its session")
	}
	if _, err := f.Login(ctx, attempt(next, "")); !errors.Is(err, ErrBadCredentials) {
		t.Error("a disabled account can sign in")
	}

	if err := f.CreateUser(ctx, "denis", goodPassword); err == nil {
		t.Error("a duplicate login was created")
	}
	if err := f.CreateUser(ctx, "bad login!", goodPassword); err == nil {
		t.Error("a login with spaces was created")
	}

	var failures int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'admin.login-failed'`).Scan(&failures); err != nil || failures == 0 {
		t.Errorf("failed logins are not in the audit log (%d, %v)", failures, err)
	}
}
