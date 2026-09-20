package imap_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/imap"
	"github.com/DenisHumen/krokosha-site/api/internal/imap/imaptest"
)

const (
	user     = "leads@example.com"
	password = `p@ss "word" \ with spaces`
)

func letter(subject, body string) []byte {
	return []byte("From: Client <client@example.org>\r\nTo: leads@example.com\r\nSubject: " + subject +
		"\r\nMessage-ID: <" + subject + "@example.org>\r\n\r\n" + body + "\r\n")
}

func dial(t *testing.T, server *imaptest.Server) *imap.Client {
	t.Helper()
	client, err := imap.Dial(context.Background(), imap.Options{Addr: server.Addr, User: server.User, Password: server.Password, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestSignInSearchFetchFlagDelete(t *testing.T) {
	server := imaptest.New(t, user, password)
	// A letter that looks like the protocol itself: tagged lines, literals, a lonely brace.
	tricky := "K3 OK Fetch completed\r\n* 99 EXISTS\r\n{5}\r\nhello }\r\n"
	first := server.Deliver(letter("first", "plain"))
	second := server.Deliver(letter("second", tricky))

	client := dial(t, server)
	if !client.Has("idle") || !client.Has("UIDPLUS") {
		t.Fatal("the capabilities of the server were not noticed")
	}
	mailbox, err := client.Select("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.Exists != 2 || mailbox.UIDValidity == 0 || mailbox.UIDNext != 3 {
		t.Fatalf("mailbox = %+v", mailbox)
	}

	uids, err := client.Search("UNSEEN")
	if err != nil || len(uids) != 2 || uids[0] != first || uids[1] != second {
		t.Fatalf("unseen = %v, %v", uids, err)
	}
	raw, err := client.Fetch(second, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(letter("second", tricky)) {
		t.Fatalf("the letter came back changed:\n%q", raw)
	}
	// Reading it did not mark it as read.
	if uids, _ = client.Search("UNSEEN"); len(uids) != 2 {
		t.Fatalf("fetching marked the letter as seen: %v", uids)
	}

	if err := client.AddFlags(first, `\Seen`); err != nil {
		t.Fatal(err)
	}
	if uids, _ = client.Search("UNSEEN"); len(uids) != 1 || uids[0] != second {
		t.Fatalf("unseen after the flag = %v", uids)
	}
	if uids, _ = client.Search("SEEN"); len(uids) != 1 || uids[0] != first {
		t.Fatalf("seen = %v", uids)
	}

	quoted, err := imap.Quote("<second@example.org>")
	if err != nil {
		t.Fatal(err)
	}
	if uids, err = client.Search("HEADER Message-ID " + quoted); err != nil || len(uids) != 1 || uids[0] != second {
		t.Fatalf("by Message-ID = %v, %v", uids, err)
	}

	if err := client.Delete(second); err != nil {
		t.Fatal(err)
	}
	left := server.Messages()
	if len(left) != 1 || left[0].UID != first {
		t.Fatalf("after deleting: %+v", left)
	}
	if _, err := client.Fetch(second, 1<<20); !errors.Is(err, imap.ErrNotFound) {
		t.Fatalf("a deleted letter: %v", err)
	}
	if err := client.Logout(); err != nil {
		t.Fatal(err)
	}
}

func TestALetterAboveTheLimitStaysOnTheServer(t *testing.T) {
	server := imaptest.New(t, user, password)
	uid := server.Deliver(letter("big", strings.Repeat("x", 5000)))
	client := dial(t, server)
	if _, err := client.Select("INBOX"); err != nil {
		t.Fatal(err)
	}
	var tooBig *imap.TooBigError
	if _, err := client.Fetch(uid, 1000); !errors.As(err, &tooBig) || tooBig.Size < 5000 {
		t.Fatalf("err = %v", err)
	}
	for _, command := range server.Commands() {
		if strings.Contains(command, "BODY") {
			t.Fatalf("the body was asked for anyway: %s", command)
		}
	}
	// The connection is still good.
	if _, err := client.Fetch(uid, 1<<20); err != nil {
		t.Fatal(err)
	}
}

func TestWrongPassword(t *testing.T) {
	server := imaptest.New(t, user, password)
	_, err := imap.Dial(context.Background(), imap.Options{Addr: server.Addr, User: user, Password: "not it", Timeout: 5 * time.Second})
	var refused *imap.Error
	if !errors.As(err, &refused) || refused.Status != "NO" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "not it") {
		t.Fatal("the password is in the error")
	}
}

func TestLoginWhenPlainIsNotOffered(t *testing.T) {
	server := imaptest.New(t, user, password)
	server.NoPlain = true
	client := dial(t, server) // the password has quotes and a backslash: they must survive quoting
	if _, err := client.Select("INBOX"); err != nil {
		t.Fatal(err)
	}
	for _, command := range server.Commands() {
		if strings.Contains(command, "word") {
			t.Fatalf("the test server keeps passwords: %s", command)
		}
	}
}

func TestIdleWakesUpWhenALetterArrives(t *testing.T) {
	server := imaptest.New(t, user, password)
	client := dial(t, server)
	if _, err := client.Select("INBOX"); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		server.Deliver(letter("new", "hello"))
	}()
	started := time.Now()
	arrived, err := client.Idle(context.Background(), 10*time.Second)
	if err != nil || !arrived {
		t.Fatalf("arrived = %v, err = %v", arrived, err)
	}
	if waited := time.Since(started); waited > 5*time.Second {
		t.Fatalf("woke up after %s: by the clock, not by the letter", waited)
	}
	// The connection works as before.
	if uids, err := client.Search("UNSEEN"); err != nil || len(uids) != 1 {
		t.Fatalf("after idle: %v, %v", uids, err)
	}
}

func TestIdleEndsByTheClockAndByTheContext(t *testing.T) {
	server := imaptest.New(t, user, password)
	client := dial(t, server)
	if _, err := client.Select("INBOX"); err != nil {
		t.Fatal(err)
	}
	arrived, err := client.Idle(context.Background(), 200*time.Millisecond)
	if err != nil || arrived {
		t.Fatalf("a quiet wait: arrived = %v, err = %v", arrived, err)
	}
	if _, err := client.Search("ALL"); err != nil {
		t.Fatalf("the connection after a quiet wait: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	if _, err := client.Idle(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if waited := time.Since(started); waited > 5*time.Second {
		t.Fatalf("the context was noticed after %s", waited)
	}
}

func TestIdleNoticesAServerThatWentAway(t *testing.T) {
	server := imaptest.New(t, user, password)
	client := dial(t, server)
	if _, err := client.Select("INBOX"); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		server.Hangup()
	}()
	if _, err := client.Idle(context.Background(), 10*time.Second); err == nil {
		t.Fatal("a dropped connection went unnoticed")
	}
}

func TestWithoutIdleTheServerIsAskedAgain(t *testing.T) {
	server := imaptest.New(t, user, password)
	server.NoIdle = true
	client := dial(t, server)
	if _, err := client.Select("INBOX"); err != nil {
		t.Fatal(err)
	}
	look, err := client.Idle(context.Background(), 50*time.Millisecond)
	if err != nil || !look {
		t.Fatalf("look = %v, err = %v", look, err)
	}
	commands := strings.Join(server.Commands(), "\n")
	if strings.Contains(commands, "IDLE") || !strings.Contains(commands, "NOOP") {
		t.Fatalf("commands:\n%s", commands)
	}
}

func TestStringsThatCannotBeQuotedAreRefused(t *testing.T) {
	for _, text := range []string{"two\r\nlines", "кириллица", "zero\x00byte"} {
		if _, err := imap.Quote(text); err == nil {
			t.Errorf("%q was quoted", text)
		}
	}
	if quoted, err := imap.Quote(`a "b" \c`); err != nil || quoted != `"a \"b\" \\c"` {
		t.Fatalf("quoted = %s, %v", quoted, err)
	}
}
