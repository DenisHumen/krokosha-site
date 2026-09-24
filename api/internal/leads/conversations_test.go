package leads

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

// The messenger's list: tabs, what the staff have not read, and the pulse that notices a client
// writing — whatever channel it came through.
func TestConversationsCountWhatIsUnread(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	ctx := context.Background()
	first := f.seed(nil)
	second := f.seed(func(v map[string]string) { v["contact_method"], v["contact_value"] = "telegram", "@olena_sh" })

	counts, err := store.ConversationCounts(ctx)
	if err != nil || counts[TabAll] != 2 || counts[TabUnanswered] != 2 || counts[TabWork] != 2 || counts[TabWaiting] != 0 {
		t.Fatalf("counts of two new requests: %v %v", counts, err)
	}
	// A new request is a message nobody has read: the text of the form.
	list, err := store.Conversations(ctx, ConversationFilter{})
	if err != nil || len(list) != 2 || list[0].Unread != 1 || !list[0].Waiting {
		t.Fatalf("the list: %+v %v", list, err)
	}
	pulse, err := store.PulseOf(ctx, first)
	if err != nil || pulse.Unread != 2 || pulse.Last == 0 || pulse.LeadLast == 0 || pulse.LeadLast >= pulse.Last {
		t.Fatalf("the pulse: %+v %v", pulse, err)
	}

	// Opened: read up to that moment. A letter a moment later is unread again — and the pulse grows.
	f.now = f.now.Add(time.Minute)
	if err := store.MarkStaffSeen(ctx, first, f.now); err != nil {
		t.Fatal(err)
	}
	if one, err := store.Conversation(ctx, first); err != nil || one.Unread != 0 {
		t.Errorf("read: %+v %v", one, err)
	}
	f.now = f.now.Add(time.Minute)
	if _, err := store.ClientWrote(ctx, first, Incoming{Channel: ChannelEmail, Text: "Забыл: ещё камеры", FromAddress: "ivan@company.com"}); err != nil {
		t.Fatal(err)
	}
	if one, _ := store.Conversation(ctx, first); one.Unread != 1 {
		t.Errorf("a letter after the look: %d unread", one.Unread)
	}
	after, err := store.PulseOf(ctx, first)
	if err != nil || after.LeadLast <= pulse.LeadLast || after.Last <= pulse.Last {
		t.Errorf("the pulse after a letter: %+v, before %+v", after, pulse)
	}
	// Seen again, with the moment of an older look: it does not go back in time.
	if err := store.MarkStaffSeen(ctx, first, f.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if one, _ := store.Conversation(ctx, first); one.Unread != 1 {
		t.Errorf("an older look must not unread or read anything: %d", one.Unread)
	}

	// An answer is read by whoever writes it; the request waits for the client.
	f.now = f.now.Add(time.Minute)
	if _, err := store.Reply(ctx, first, "denis", "Добавим камеры."); err != nil {
		t.Fatal(err)
	}
	if one, _ := store.Conversation(ctx, first); one.Unread != 0 || one.Waiting || one.Status != StatusWaitingClient {
		t.Errorf("after the answer: %+v", one)
	}
	counts, _ = store.ConversationCounts(ctx)
	if counts[TabUnanswered] != 1 || counts[TabWaiting] != 1 || counts[TabWork] != 1 {
		t.Errorf("counts after the answer: %v", counts)
	}
	if total, err := store.UnreadTotal(ctx); err != nil || total != 1 {
		t.Errorf("unread in all: %d %v", total, err)
	}
	// A note is read by whoever writes it too.
	if err := store.AddNote(ctx, second, "denis", "Перезвонить завтра"); err != nil {
		t.Fatal(err)
	}
	if total, _ := store.UnreadTotal(ctx); total != 0 {
		t.Errorf("unread after a note: %d", total)
	}

	// Tabs and the search.
	for tab, want := range map[string]string{TabUnanswered: "", TabWaiting: "Иван", TabWork: "", TabAll: "Иван", TabClosed: "", TabSpam: ""} {
		items, err := store.Conversations(ctx, ConversationFilter{Tab: tab})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, item := range items {
			if item.ID == first {
				names = append(names, "Иван")
			}
		}
		if strings.Join(names, " ") != want {
			t.Errorf("tab %q: %v", tab, names)
		}
	}
	if items, _ := store.Conversations(ctx, ConversationFilter{Query: "olena"}); len(items) != 1 || items[0].ID != second {
		t.Errorf("search by contact: %+v", items)
	}
	if items, _ := store.Conversations(ctx, ConversationFilter{Query: Number(second)}); len(items) != 1 || items[0].ID != second {
		t.Errorf("search by number: %+v", items)
	}
	if _, err := store.Conversation(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("a conversation that is not there: %v", err)
	}
}

func TestPriority(t *testing.T) {
	budgets := []string{"up to $500", "$500–1k", "$1–3k", "$3–10k", "over $10k", "not sure yet"}
	for budget, want := range map[string]int{"up to $500": 0, "$500–1k": 1, "$1–3k": 2, "$3–10k": 2, "over $10k": 3, "not sure yet": 0, "": 0, "$99": 0} {
		if got := BudgetLevel(budgets, budget); got != want {
			t.Errorf("budget %q: %d, want %d", budget, got, want)
		}
	}
	for _, c := range []struct {
		orders int
		spent  float64
		want   int
	}{{0, 0, 0}, {1, 100, 1}, {2, 0, 2}, {0, 3000, 2}, {3, 2999, 2}, {4, 0, 3}, {1, 8000, 3}} {
		if got := Activity(config.DefaultPriority, c.orders, c.spent); got != c.want {
			t.Errorf("activity of %d orders, $%.0f: %d, want %d", c.orders, c.spent, got, c.want)
		}
	}
	// Thresholds of the content: one order is enough to be a regular client, ten to be a key one.
	rules := config.LoyaltyPriority{Middle: config.PriorityLevel{Orders: 1}, High: config.PriorityLevel{Orders: 10, Spent: 50000}}
	if Activity(rules, 1, 0) != PriorityMid || Activity(rules, 9, 49999) != PriorityMid || Activity(rules, 2, 50000) != PriorityHigh {
		t.Error("the thresholds of the content are not the ones counted by")
	}
	// Halfway rounds up: a new client with a big budget is in the middle.
	for _, c := range [][3]int{{0, 0, 0}, {0, 1, 1}, {0, 3, 2}, {3, 0, 2}, {3, 3, 3}, {1, 2, 2}} {
		if got := Priority(c[0], c[1]); got != c[2] {
			t.Errorf("priority of %d and %d: %d, want %d", c[0], c[1], got, c[2])
		}
	}
}

// A letter that came back from one address fails that delivery only: the same answer reached the
// other address, and says so.
func TestABounceFailsOneAddress(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	ctx := context.Background()
	id := f.seed(nil)
	if _, err := store.ClientWrote(ctx, id, Incoming{Channel: ChannelEmail, Text: "С рабочей почты", FromAddress: "ivan@work.example"}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	messageID, err := store.ReplyWith(ctx, id, "denis", Answer{Text: "Ответ на оба адреса"})
	if err != nil {
		t.Fatal(err)
	}
	byMessage, err := store.Deliveries(ctx, id)
	if err != nil || len(byMessage[messageID]) != 2 {
		t.Fatalf("deliveries: %+v %v", byMessage, err)
	}
	for _, d := range byMessage[messageID] {
		if err := store.MarkTarget(ctx, d.ID, "sent", "reply-7@krokosha.test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UndeliveredTo(ctx, id, "reply-7@krokosha.test", "IVAN@work.example", "ivan@work.example 5.1.1 no such user", nil); err != nil {
		t.Fatal(err)
	}
	byMessage, _ = store.Deliveries(ctx, id)
	var states []string
	for _, d := range byMessage[messageID] {
		states = append(states, d.To+":"+d.Status)
	}
	if got := strings.Join(states, " "); got != "Ivan.Petrov@company.com:sent ivan@work.example:failed" {
		t.Errorf("after the bounce: %s", got)
	}
	var summary string
	if err := f.db.QueryRow(`SELECT delivery FROM lead_messages WHERE id = ?`, messageID).Scan(&summary); err != nil || summary != "sent" {
		t.Errorf("the answer reached one address: %q %v", summary, err)
	}
	// A report that names no address, of a letter that went to two: which one, nobody knows —
	// nothing is unsaid.
	if err := store.UndeliveredTo(ctx, id, "reply-7@krokosha.test", "", "returned", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT delivery FROM lead_messages WHERE id = ?`, messageID).Scan(&summary); err != nil || summary != "sent" {
		t.Errorf("an unclear report: %q %v", summary, err)
	}

	// Mail and Telegram: the letter comes back, and the report names no address. The one address
	// it went to failed — and the answer stays delivered, as Telegram took it.
	other := f.seed(func(v map[string]string) { v["contact_value"] = "olena@company.com" })
	if _, err := f.db.Exec(`INSERT INTO bot_clients (lead_id, telegram_id, linked_at) VALUES (?, 888, ?)`, other, f.now); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	both, err := store.ReplyWith(ctx, other, "denis", Answer{Text: "Почтой и в Telegram"})
	if err != nil {
		t.Fatal(err)
	}
	byMessage, _ = store.Deliveries(ctx, other)
	if len(byMessage[both]) != 2 {
		t.Fatalf("deliveries by mail and Telegram: %+v", byMessage[both])
	}
	for _, d := range byMessage[both] {
		id := ""
		if d.Channel == ChannelEmail {
			id = "reply-9@krokosha.test"
		}
		if err := store.MarkTarget(ctx, d.ID, "sent", id); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UndeliveredTo(ctx, other, "reply-9@krokosha.test", "", "Host or domain name not found", nil); err != nil {
		t.Fatal(err)
	}
	byMessage, _ = store.Deliveries(ctx, other)
	states = nil
	for _, d := range byMessage[both] {
		states = append(states, d.Channel+":"+d.Status)
	}
	if got := strings.Join(states, " "); got != "email:failed telegram:sent" {
		t.Errorf("a letter back, Telegram took it: %s", got)
	}
	if err := f.db.QueryRow(`SELECT delivery FROM lead_messages WHERE id = ?`, both).Scan(&summary); err != nil || summary != "sent" {
		t.Errorf("the answer reached the client in Telegram: %q %v", summary, err)
	}
}
