package admin

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DenisHumen/krokosha-site/api/internal/mailboxes"
)

// mailServer plays the root helper once: it lists the mailboxes of the mail server.
func (s *site) mailServer(addresses ...string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.state, "mail", "mailboxes"), []byte(strings.Join(addresses, "\n")+"\n"), 0o640); err != nil {
		s.t.Fatal(err)
	}
}

// requests are the requests waiting for the helper, as its files say.
func (s *site) requests() []string {
	s.t.Helper()
	names, err := filepath.Glob(filepath.Join(s.state, "requests", "mail", "*.req"))
	if err != nil {
		s.t.Fatal(err)
	}
	var out []string
	for _, name := range names {
		content, err := os.ReadFile(name)
		if err != nil {
			s.t.Fatal(err)
		}
		out = append(out, string(content))
		if err := os.Remove(name); err != nil { // taken by the helper
			s.t.Fatal(err)
		}
	}
	return out
}

func TestMailWithoutAMailServer(t *testing.T) {
	s := newSite(t)
	s.signIn()
	page := s.do(http.MethodGet, prefix+"/mail", nil, nil)
	if page.status != http.StatusOK || !strings.Contains(page.body, "Почтового сервера здесь нет") || strings.Contains(page.body, "Новый ящик") {
		t.Fatalf("page: %d\n%s", page.status, page.body)
	}
	if got := s.post("/mail", url.Values{"address": {"ivan"}}); got.status != http.StatusBadRequest || !strings.Contains(got.body, "--no-mail") {
		t.Errorf("add: %d", got.status)
	}
	if len(s.requests()) != 0 {
		t.Error("a request without a mail server")
	}
}

var reIssued = regexp.MustCompile(`<p class="secret mono copyable">([^<]+)</p>`)

func TestMailboxesOfColleagues(t *testing.T) {
	s := newSite(t)
	s.mailServer("denis@krokosha.xyz", testMailbox)
	s.signIn()

	page := s.do(http.MethodGet, prefix+"/mail", nil, nil)
	for _, want := range []string{"Ящики @krokosha.xyz", "denis@krokosha.xyz", "владелец", "служебный", "mail.krokosha.xyz:993", `href="/_test1234/mail"`} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the page has no %q", want)
		}
	}
	if strings.Contains(page.body, `http-equiv="refresh"`) {
		t.Error("nothing is waiting, and yet the page refreshes itself")
	}

	// A password made here is shown once, on the answer itself; the request carries only its hash.
	made := s.post("/mail", url.Values{"address": {"Ivan"}, "mode": {"generate"}})
	match := reIssued.FindStringSubmatch(made.body)
	if made.status != http.StatusOK || match == nil || !strings.Contains(made.body, "Пароль ящика ivan@krokosha.xyz") {
		t.Fatalf("add: %d\n%s", made.status, made.body)
	}
	issued := match[1]
	if strings.Contains(made.body, `http-equiv="refresh"`) {
		t.Error("the page with the password refreshes itself: the password would be gone before it is copied")
	}
	if !strings.Contains(made.body, "создаётся…") {
		t.Error("the mailbox asked for is not shown as coming")
	}
	requests := s.requests()
	if len(requests) != 1 || !strings.HasPrefix(requests[0], "add\nivan@krokosha.xyz\n{SHA512-CRYPT}$6$") || strings.Contains(requests[0], issued) {
		t.Fatalf("requests: %q", requests)
	}
	hash := strings.TrimPrefix(strings.Split(requests[0], "\n")[2], "{SHA512-CRYPT}")
	if !mailboxes.Verify(issued, hash) {
		t.Error("the request is not for the password shown")
	}

	// A password typed by hand: twice, the same, long enough; it is not shown back.
	s.mailServer("denis@krokosha.xyz", "ivan@krokosha.xyz", testMailbox)
	for name, tc := range map[string]struct {
		form url.Values
		want string
	}{
		"differ": {url.Values{"address": {"ivan@krokosha.xyz"}, "mode": {"custom"}, "password": {"a long enough password"}, "again": {"a long enough passw0rd"}}, "Пароли не совпадают."},
		"short":  {url.Values{"address": {"ivan@krokosha.xyz"}, "mode": {"custom"}, "password": {"short"}, "again": {"short"}}, "не короче 12 символов"},
		"leads":  {url.Values{"address": {testMailbox}, "mode": {"generate"}}, "служебный ящик сайта"},
		"nobody": {url.Values{"address": {"nobody@krokosha.xyz"}, "mode": {"generate"}}, "Такого ящика нет."},
	} {
		if got := s.post("/mail/password", tc.form); got.status != http.StatusBadRequest || !strings.Contains(got.body, tc.want) {
			t.Errorf("%s: %d, want «%s»", name, got.status, tc.want)
		}
	}
	if len(s.requests()) != 0 {
		t.Fatal("a refused change left a request")
	}
	typed := "мой пароль на двенадцать"
	changed := s.post("/mail/password", url.Values{"address": {"ivan@krokosha.xyz"}, "mode": {"custom"}, "password": {typed}, "again": {typed}})
	if changed.status != http.StatusSeeOther || changed.location != prefix+"/mail?ok=mail-request" {
		t.Fatalf("password: %d %s", changed.status, changed.location)
	}
	after := s.do(http.MethodGet, changed.location, nil, nil)
	if !strings.Contains(after.body, "меняется пароль…") || !strings.Contains(after.body, `http-equiv="refresh"`) || strings.Contains(after.body, typed) {
		t.Error("the page after a change: no «waiting», no refresh, or the password itself")
	}
	requests = s.requests()
	if len(requests) != 1 || !strings.HasPrefix(requests[0], "passwd\nivan@krokosha.xyz\n") ||
		!mailboxes.Verify(typed, strings.TrimPrefix(strings.Split(requests[0], "\n")[2], "{SHA512-CRYPT}")) {
		t.Fatalf("requests: %q", requests)
	}

	// Removing a mailbox takes its address typed again.
	if got := s.post("/mail/delete", url.Values{"address": {"ivan@krokosha.xyz"}, "confirm": {"ivan"}}); got.status != http.StatusBadRequest {
		t.Errorf("delete without the address: %d", got.status)
	}
	if got := s.post("/mail/delete", url.Values{"address": {testMailbox}, "confirm": {testMailbox}}); got.status != http.StatusBadRequest {
		t.Errorf("delete the service mailbox: %d", got.status)
	}
	if got := s.post("/mail/delete", url.Values{"address": {"ivan@krokosha.xyz"}, "confirm": {"Ivan@Krokosha.xyz "}}); got.status != http.StatusSeeOther {
		t.Errorf("delete: %d %s", got.status, got.body)
	}
	if requests = s.requests(); len(requests) != 1 || requests[0] != "del\nivan@krokosha.xyz\n\n" {
		t.Errorf("requests: %q", requests)
	}

	// What the helper did shows up on the page, the latest first.
	log := "2026-09-24T12:00:00Z\t0000000000000000001-00000001\tadd\tivan@krokosha.xyz\tok\tящик создан\n" +
		"2026-09-24T12:01:00Z\t0000000000000000002-00000002\tdel\tivan@krokosha.xyz\terror\t<b>такого ящика нет</b>\n"
	if err := os.WriteFile(filepath.Join(s.state, "mail", "log"), []byte(log), 0o640); err != nil {
		t.Fatal(err)
	}
	page = s.do(http.MethodGet, prefix+"/mail", nil, nil)
	if first, second := strings.Index(page.body, "удаление"), strings.Index(page.body, "создание"); first < 0 || second < 0 || first > second {
		t.Error("the log is not on the page, or not the latest first")
	}
	if strings.Contains(page.body, "<b>такого") {
		t.Error("the log is not escaped")
	}
}

func TestMailNeedsASignedInAdmin(t *testing.T) {
	s := newSite(t)
	s.mailServer("denis@krokosha.xyz", testMailbox)
	if got := s.do(http.MethodGet, prefix+"/mail", nil, nil); got.status == http.StatusOK && strings.Contains(got.body, "Новый ящик") {
		t.Fatal("the mail page without signing in")
	}
	got := s.do(http.MethodPost, prefix+"/mail", url.Values{"address": {"ivan"}, "mode": {"generate"}}, nil)
	if got.status == http.StatusOK || len(s.requests()) != 0 {
		t.Fatalf("a mailbox without signing in: %d", got.status)
	}
	s.signIn()
	// Signed in, but without the token of the session: refused as well.
	if got := s.do(http.MethodPost, prefix+"/mail", url.Values{"address": {"ivan"}, "mode": {"generate"}}, nil); got.status != http.StatusForbidden || len(s.requests()) != 0 {
		t.Errorf("a forged request: %d", got.status)
	}
}
