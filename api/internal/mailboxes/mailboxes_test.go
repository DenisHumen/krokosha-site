package mailboxes

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func newTestService(t *testing.T, mailboxes ...string) *Service {
	t.Helper()
	root := t.TempDir()
	requests, status := filepath.Join(root, "requests"), filepath.Join(root, "mail")
	for _, dir := range []string{requests, status} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if mailboxes != nil {
		if err := os.WriteFile(filepath.Join(status, "mailboxes"), []byte(strings.Join(mailboxes, "\n")+"\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	clock := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	return New(Options{Requests: requests, Status: status, Domain: "Krokosha.com", Service: "leads@krokosha.com", Owner: "denis@krokosha.com",
		Now: func() time.Time { clock = clock.Add(time.Millisecond); return clock }})
}

func TestAddress(t *testing.T) {
	s := newTestService(t)
	for _, tc := range []struct{ typed, want string }{
		{"Ivan", "ivan@krokosha.com"}, {" ivan.petrov@KROKOSHA.com ", "ivan.petrov@krokosha.com"}, {"sales-ua", "sales-ua@krokosha.com"},
	} {
		if got, err := s.Address(tc.typed); err != nil || got != tc.want {
			t.Errorf("%q → %q (%v), want %q", tc.typed, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "ivan@gmail.com", ".ivan", "iv an", "ivan..petrov", "ivan@krokosha.com@x", "іван", strings.Repeat("a", 65), "ivan\nx"} {
		if got, err := s.Address(bad); !errors.Is(err, ErrBadAddress) {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
}

func TestRequests(t *testing.T) {
	s := newTestService(t, "leads@krokosha.com", "denis@krokosha.com")
	list, err := s.List()
	if err != nil || len(list) != 2 || !list[0].Owner || !list[1].Service {
		t.Fatalf("list: %+v %v", list, err)
	}

	address, err := s.Add("ivan", "correct horse battery staple")
	if err != nil || address != "ivan@krokosha.com" {
		t.Fatalf("add: %q %v", address, err)
	}
	names, _ := filepath.Glob(filepath.Join(s.opts.Requests, "*.req"))
	if len(names) != 1 || !ValidRequestName(filepath.Base(names[0])) {
		t.Fatalf("requests: %v", names)
	}
	info, _ := os.Stat(names[0])
	content, _ := os.ReadFile(names[0])
	lines := strings.Split(string(content), "\n")
	hashPattern := regexp.MustCompile(`^\{SHA512-CRYPT\}\$6\$(rounds=[0-9]{4,9}\$)?[./0-9A-Za-z]{1,16}\$[./0-9A-Za-z]{86}$`) // the helper's
	if info.Mode().Perm() != 0o600 || lines[0] != ActionAdd || lines[1] != address || !hashPattern.MatchString(lines[2]) {
		t.Errorf("request %v: %q", info.Mode(), content)
	}
	if strings.Contains(string(content), "correct horse") {
		t.Fatal("the password itself is in the request")
	}
	if !Verify("correct horse battery staple", strings.TrimPrefix(lines[2], "{SHA512-CRYPT}")) {
		t.Error("the hash is not of the password")
	}

	// The mailbox asked for shows as coming; a second request about it waits for the first.
	list, _ = s.List()
	if len(list) != 3 || list[1].Address != "ivan@krokosha.com" || list[1].Pending != ActionAdd { // alphabetical: denis, ivan, leads
		t.Errorf("list with a request: %+v", list)
	}
	if _, err := s.Add("ivan", "another long password"); !errors.Is(err, ErrPending) {
		t.Errorf("a second request: %v", err)
	}

	for name, tc := range map[string]struct {
		err error
		run func() error
	}{
		"exists":       {ErrExists, func() error { _, err := s.Add("denis", "a long enough password"); return err }},
		"short":        {ErrShort, func() error { _, err := s.Add("olga", "short"); return err }},
		"line break":   {ErrShort, func() error { _, err := s.Add("olga", "a long enough\npassword"); return err }},
		"no such":      {ErrNoMailbox, func() error { return s.SetPassword("nobody@krokosha.com", "a long enough password") }},
		"service pass": {ErrService, func() error { return s.SetPassword("leads@krokosha.com", "a long enough password") }},
		"service del":  {ErrService, func() error { return s.Delete("leads@krokosha.com") }},
	} {
		if err := tc.run(); !errors.Is(err, tc.err) {
			t.Errorf("%s: %v, want %v", name, err, tc.err)
		}
	}
	if err := s.Delete("denis@krokosha.com"); err != nil {
		t.Errorf("delete: %v", err)
	}
	pending, _ := s.Pending()
	if len(pending) != 2 || pending[1].Action != ActionDelete {
		t.Errorf("pending: %+v", pending)
	}
}

func TestNoMailServer(t *testing.T) {
	s := newTestService(t)
	if s.Installed() {
		t.Fatal("installed without a list")
	}
	if _, err := s.List(); !errors.Is(err, ErrNoMail) {
		t.Errorf("list: %v", err)
	}
	if _, err := s.Add("ivan", "a long enough password"); !errors.Is(err, ErrNoMail) {
		t.Errorf("add: %v", err)
	}
}

func TestLog(t *testing.T) {
	s := newTestService(t, "leads@krokosha.com")
	log := "2026-09-24T12:00:00Z\t0000000000000000001-00000001\tadd\tivan@krokosha.com\tok\tящик создан\n" +
		"2026-09-24T12:01:00Z\t0000000000000000002-00000002\tdel\tleads@krokosha.com\terror\tслужебный ящик сайта нельзя удалить\n"
	if err := os.WriteFile(filepath.Join(s.opts.Status, "log"), []byte(log), 0o640); err != nil {
		t.Fatal(err)
	}
	outcomes, err := s.Log()
	if err != nil || len(outcomes) != 2 || outcomes[0].OK || outcomes[0].Address != "leads@krokosha.com" || !outcomes[1].OK || outcomes[1].Message != "ящик создан" {
		t.Errorf("log: %+v %v", outcomes, err)
	}
}

func TestGenerate(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		password, err := Generate()
		if err != nil || !regexp.MustCompile(`^[a-km-zA-HJ-NP-Z2-9]{5}(-[a-km-zA-HJ-NP-Z2-9]{5}){3}$`).MatchString(password) || seen[password] {
			t.Fatalf("Generate: %q %v", password, err)
		}
		seen[password] = true
	}
}
