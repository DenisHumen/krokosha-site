package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// TestStrangersMessagesReachTheStaff: somebody who writes to the bot without the link of a request
// gets the greeting, and the staff see what they wrote — once in six hours per person.
func TestStrangersMessagesReachTheStaff(t *testing.T) {
	f := newFixture(t)
	f.withLeads()
	f.join(denis, RoleOwner)
	f.join(olena, RoleMember)
	stranger := User{ID: 5001, FirstName: "Пётр", Username: "petr_k", LanguageCode: "ru"}

	if got := f.says(stranger, "Здравствуйте! Сколько стоит <сайт>?"); len(got) != 1 || !strings.Contains(got[0].Text(), "Это бот сайта") {
		t.Fatalf("the greeting: %+v", got)
	}
	f.says(stranger, "Алло?")
	f.says(stranger, "/help") // a command is nothing to pass on
	var tasks []outbox.Task
	rows, err := f.db.Query(`SELECT kind, payload FROM outbox WHERE kind = ?`, TaskStranger)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var task outbox.Task
		if err := rows.Scan(&task.Kind, &task.Payload); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	_ = rows.Close()
	if len(tasks) != 1 {
		t.Fatalf("messages passed on: %d, want 1", len(tasks))
	}

	f.api.Forget()
	if err := f.bot.Send(context.Background(), tasks[0]); err != nil {
		t.Fatal(err)
	}
	for _, member := range []User{denis, olena} {
		text := oneText(t, f.api.Sent(member.ID))
		for _, want := range []string{"Сообщение в бот без заявки", "Пётр · @petr_k", `href="https://t.me/petr_k"`, "Сколько стоит &lt;сайт&gt;?"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: %q lacks %q", member.FirstName, text, want)
			}
		}
	}

	// Six hours later the next message is passed on again.
	f.now = f.now.Add(6*time.Hour + time.Minute)
	f.says(stranger, "Вы тут?")
	var queued int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE kind = ?`, TaskStranger).Scan(&queued)
	if queued != 2 {
		t.Errorf("after six hours: %d messages passed on, want 2", queued)
	}
}
