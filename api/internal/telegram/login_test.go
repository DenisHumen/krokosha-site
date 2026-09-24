package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/DenisHumen/krokosha-site/api/internal/clients"
)

// fakeLogins answers every «/start l_…» with the same code.
type fakeLogins struct{ code clients.TelegramCode }

func (l fakeLogins) TelegramStart(context.Context, string, int64, string, string) (clients.TelegramCode, error) {
	return l.code, nil
}

// TestTheCodeOfALogin: signing in gets the code and a link; linking the Telegram account to an
// account gets the code only, with a warning — no link finishes linking (clients.VerifyLink).
func TestTheCodeOfALogin(t *testing.T) {
	f := newFixture(t)
	start := "/start " + clients.TelegramPrefix + strings.Repeat("a", 22)

	f.bot.opts.Logins = fakeLogins{clients.TelegramCode{Code: "123456", LinkURL: "https://krokosha.xyz/account/#login=1.abc", Lang: "en"}}
	calls := f.says(client, start)
	labels, targets := calls[len(calls)-1].Buttons()
	if text := calls[len(calls)-1].Text(); !strings.Contains(text, "<code>123456</code>") || len(labels) != 1 || targets[labels[0]] != "https://krokosha.xyz/account/#login=1.abc" {
		t.Errorf("a sign-in: %q %v %v", text, labels, targets)
	}

	f.bot.opts.Logins = fakeLogins{clients.TelegramCode{Code: "654321", Lang: "en", Adding: true, Joining: true}}
	calls = f.says(client, start)
	text := calls[len(calls)-1].Text()
	if labels, _ := calls[len(calls)-1].Buttons(); len(labels) != 0 {
		t.Errorf("linking offers a button: %v", labels)
	}
	for _, want := range []string{"<code>654321</code>", loginText("en", "add"), loginText("en", "joins"), loginText("en", "ignore_add")} {
		if !strings.Contains(text, want) {
			t.Errorf("linking: %q is missing from %q", want, text)
		}
	}
}
