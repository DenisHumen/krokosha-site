package admin

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/telegram"
)

var reInviteCode = regexp.MustCompile(`/start (i_[a-z2-7]{20})`)

func TestBotPage(t *testing.T) {
	s := newSite(t)
	access := telegram.NewAccess(s.db, nil)
	ctx := context.Background()

	if got := s.do(http.MethodGet, prefix+"/bot", nil, nil); got.status != http.StatusSeeOther || got.location != prefix+"/login" {
		t.Fatalf("the bot page without signing in: %d → %q", got.status, got.location)
	}
	s.signIn()

	// No token yet: the page says what to do, and invitations can be prepared all the same.
	page := s.do(http.MethodGet, prefix+"/bot", nil, nil)
	if page.status != http.StatusOK || !strings.Contains(page.body, "Бот не настроен") || !strings.Contains(page.body, "--telegram-token-file") {
		t.Fatalf("the page without a bot: %d\n%s", page.status, page.body)
	}
	made := s.post("/bot/invite", url.Values{"role": {"owner"}})
	code := reInviteCode.FindStringSubmatch(made.body)
	if made.status != http.StatusOK || code == nil || !strings.Contains(made.body, "Приглашение для владельца") || strings.Contains(made.body, "https://t.me/") {
		t.Fatalf("a new invitation: %d\n%s", made.status, made.body)
	}
	// The code is shown once: the database has only its hash, and so has every later page.
	page = s.do(http.MethodGet, prefix+"/bot", nil, nil)
	if strings.Contains(page.body, code[1]) || !strings.Contains(page.body, "создал(а) denis") {
		t.Errorf("the page after the invitation was made:\n%s", page.body)
	}
	if got := s.post("/bot/invite", url.Values{"role": {"root"}}); got.status != http.StatusBadRequest {
		t.Errorf("a made-up role: %d", got.status)
	}

	// The owner opens the link in Telegram; with a bot running the page offers a link as well.
	owner, err := access.Redeem(ctx, code[1], telegram.User{ID: 1001, FirstName: "Денис", Username: "DenisHumen"})
	if err != nil {
		t.Fatal(err)
	}
	s.bot = telegram.Status{Mode: telegram.ModeWebhook, Username: "krokosha_bot", ConnectedAt: time.Now(), LastError: "cannot get updates: 502 Bad Gateway", LastErrorAt: time.Now()}
	made = s.post("/bot/invite", url.Values{"role": {"member"}})
	code = reInviteCode.FindStringSubmatch(made.body)
	if code == nil || !strings.Contains(made.body, "https://t.me/krokosha_bot?start="+code[1]) {
		t.Fatalf("an invitation with a link:\n%s", made.body)
	}
	member, err := access.Redeem(ctx, code[1], telegram.User{ID: 1002, FirstName: "<i>Олена</i>", Username: "olena_k"})
	if err != nil {
		t.Fatal(err)
	}

	page = s.do(http.MethodGet, prefix+"/bot", nil, nil)
	for _, want := range []string{"@krokosha_bot", "Telegram вызывает сайт (webhook)", "502 Bad Gateway", "Денис", "@DenisHumen", "владелец", "&lt;i&gt;Олена&lt;/i&gt;", "участник"} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}

	// Access is revoked and given back here too; the only owner stays.
	id := strconv.FormatInt(member.ID, 10)
	if got := s.post("/bot/member", url.Values{"id": {id}, "action": {"disable"}}); got.status != http.StatusSeeOther {
		t.Fatalf("revoking access: %d", got.status)
	}
	if _, err := access.Member(ctx, 1002); err == nil {
		t.Error("the member still has access")
	}
	if page = s.do(http.MethodGet, prefix+"/bot", nil, nil); !strings.Contains(page.body, "доступ отключён") || !strings.Contains(page.body, "Вернуть доступ") {
		t.Error("the page does not show the revoked access")
	}
	if got := s.post("/bot/member", url.Values{"id": {id}, "action": {"enable"}}); got.status != http.StatusSeeOther {
		t.Errorf("giving access back: %d", got.status)
	}
	if got := s.post("/bot/member", url.Values{"id": {strconv.FormatInt(owner.ID, 10)}, "action": {"disable"}}); got.status != http.StatusConflict {
		t.Errorf("switching the only owner off: %d", got.status)
	}
	if got := s.post("/bot/member", url.Values{"id": {"999"}, "action": {"disable"}}); got.status != http.StatusNotFound {
		t.Errorf("a member that is not: %d", got.status)
	}
	if got := s.post("/bot/member", url.Values{"id": {id}, "action": {"delete"}}); got.status != http.StatusBadRequest {
		t.Errorf("an unknown action: %d", got.status)
	}

	// An invitation that was not used can be taken back.
	made = s.post("/bot/invite", url.Values{"role": {"member"}})
	code = reInviteCode.FindStringSubmatch(made.body)
	pending, _ := access.Invites(ctx)
	if code == nil || len(pending) != 1 {
		t.Fatalf("pending invitations: %d", len(pending))
	}
	if got := s.post("/bot/invite/revoke", url.Values{"id": {strconv.FormatInt(pending[0].ID, 10)}}); got.status != http.StatusSeeOther {
		t.Fatalf("revoking an invitation: %d", got.status)
	}
	if _, err := access.Redeem(ctx, code[1], telegram.User{ID: 1003, FirstName: "Late"}); err == nil {
		t.Error("a revoked invitation let somebody in")
	}

	if got := s.do(http.MethodPost, prefix+"/bot/invite", url.Values{"role": {"owner"}, "csrf": {"nope"}}, nil); got.status != http.StatusForbidden {
		t.Errorf("a forged form: %d", got.status)
	}
	var actions string
	if err := s.db.QueryRow(`SELECT GROUP_CONCAT(action ORDER BY id SEPARATOR ' ') FROM audit_log WHERE action LIKE 'bot.%'`).Scan(&actions); err != nil ||
		actions != "bot.invite bot.invite bot.disable bot.enable bot.invite bot.invite-revoke" {
		t.Errorf("the journal: %q %v", actions, err)
	}
}
