package leads

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// seed sends a request through the real form handler and returns its id.
func (f *fixture) seed(change func(values map[string]string)) int64 {
	f.t.Helper()
	values := validValues()
	fields := map[string]string{}
	if change != nil {
		change(fields)
	}
	for key, value := range fields {
		values.Set(key, value)
	}
	// Every request from another address: the hourly limit is not what these tests are about.
	f.created = nil
	got := f.do(http.MethodPost, "/api/leads", values, map[string]string{"Accept": "application/json", "X-Real-IP": fmt.Sprintf("198.51.100.%d", 1+f.count(`SELECT COUNT(*) FROM leads`))})
	if got.status != http.StatusCreated || len(f.created) != 1 {
		f.t.Fatalf("seeding a request: %d %s", got.status, got.body)
	}
	return f.created[0].ID
}

func TestOnlyOneCanTakeARequest(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	id := f.seed(nil)
	ctx := context.Background()

	// Denis in the admin area and a colleague in Telegram press «take» at the same moment
	// (brief B10.4: the first press wins, the second one is told who was faster).
	const people = 8
	var wg sync.WaitGroup
	results := make([]error, people)
	winners := make([]string, people)
	start := make(chan struct{})
	for i := range people {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			winners[i], results[i] = store.Take(ctx, id, fmt.Sprintf("person-%d", i))
		}()
	}
	close(start)
	wg.Wait()

	took := 0
	var winner string
	for i, err := range results {
		switch {
		case err == nil:
			took++
			winner = winners[i]
		case !errors.Is(err, ErrAlreadyTaken):
			t.Errorf("person-%d: %v", i, err)
		}
	}
	if took != 1 {
		t.Fatalf("%d people took the request, want exactly one", took)
	}
	for i, err := range results {
		if errors.Is(err, ErrAlreadyTaken) && winners[i] != winner {
			t.Errorf("person-%d was told the request went to %q, but it went to %q", i, winners[i], winner)
		}
	}
	card, err := store.Card(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if card.Lead.Status != StatusInProgress || card.Assignee != winner || !card.AssignedAt.Valid {
		t.Errorf("after the race: status %s, assignee %q", card.Lead.Status, card.Assignee)
	}
	if n := f.count(`SELECT COUNT(*) FROM lead_events WHERE action = 'assigned'`); n != 1 {
		t.Errorf("%d «assigned» records in the history, want 1", n)
	}
	if _, err := store.Take(ctx, 999, "denis"); !errors.Is(err, ErrNotFound) {
		t.Errorf("taking a request that does not exist: %v", err)
	}
}

func TestStatusesFollowTheRules(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	id := f.seed(nil)
	ctx := context.Background()

	if err := store.SetStatus(ctx, id, "denis", StatusDone, ""); !errors.Is(err, ErrBadTransition) {
		t.Errorf("new → done: %v, want a refusal (brief B10.4)", err)
	}
	for _, step := range []struct{ to, reason string }{
		{StatusInProgress, ""}, {StatusWaitingClient, ""}, {StatusInProgress, ""}, {StatusRejected, "бюджет не подходит"},
		{StatusInProgress, "передумали"}, {StatusDone, ""},
	} {
		f.now = f.now.Add(time.Minute)
		if err := store.SetStatus(ctx, id, "denis", step.to, step.reason); err != nil {
			t.Fatalf("→ %s: %v", step.to, err)
		}
	}
	card, err := store.Card(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if card.Lead.Status != StatusDone || card.Assignee != "denis" || card.RejectReason != "бюджет не подходит" {
		t.Errorf("card: status %s, assignee %q, reason %q", card.Lead.Status, card.Assignee, card.RejectReason)
	}
	// Every step is in the history, in order, with who did it.
	var trail []string
	for _, entry := range card.Feed {
		if entry.Kind == "event" && entry.Action == "status" {
			trail = append(trail, entry.From+"→"+entry.To+" "+entry.Author)
		}
	}
	want := "new→in_progress denis|in_progress→waiting_client denis|waiting_client→in_progress denis|in_progress→rejected denis|rejected→in_progress denis|in_progress→done denis"
	if strings.Join(trail, "|") != want {
		t.Errorf("history:\n got %s\nwant %s", strings.Join(trail, "|"), want)
	}
	if err := store.SetStatus(ctx, id, "denis", "archived", ""); !errors.Is(err, ErrBadTransition) {
		t.Errorf("an unknown status: %v", err)
	}
	if err := store.SetStatus(ctx, 999, "denis", StatusDone, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("a request that does not exist: %v", err)
	}
}

func TestARequestRescuedFromSpamIsAnnounced(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	id := f.seed(func(v map[string]string) { v["website"] = "https://spam.example" })
	if n := f.count(`SELECT COUNT(*) FROM outbox`); n != 0 {
		t.Fatalf("spam queued %d notifications", n)
	}
	if err := store.SetStatus(context.Background(), id, "denis", StatusNew, "не спам: реальный клиент"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM outbox WHERE kind = 'lead.notify' AND status = 'pending'`); n != 2 {
		t.Errorf("notifications after the rescue: %d, want 2 (mail and Telegram)", n)
	}
}

func TestRepliesAndNotes(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	id := f.seed(nil)
	ctx := context.Background()

	if err := store.AddNote(ctx, id, "denis", "  Клиент из рекламы, бюджет маленький.  "); err != nil {
		t.Fatal(err)
	}
	if err := store.AddNote(ctx, id, "denis", " \n "); !errors.Is(err, ErrEmptyText) {
		t.Errorf("an empty note: %v", err)
	}
	if err := store.AddNote(ctx, 999, "denis", "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a note on nothing: %v", err)
	}

	f.now = f.now.Add(42 * time.Minute)
	messageID, err := store.Reply(ctx, id, "denis", "Спасибо, изучу и отвечу до конца дня.")
	if err != nil {
		t.Fatal(err)
	}
	card, _ := store.Card(ctx, id)
	if card.Lead.Status != StatusWaitingClient || card.Assignee != "denis" || !card.FirstResponseAt.Valid {
		t.Errorf("after the first answer: %s, assignee %q, first response %v", card.Lead.Status, card.Assignee, card.FirstResponseAt)
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM outbox WHERE kind = 'lead.reply' AND channel = 'email' AND JSON_EXTRACT(payload, '$.message_id') = %d`, messageID)); n != 1 {
		t.Error("the answer is not queued for delivery by email")
	}
	if body, author, err := store.Message(ctx, id, messageID); err != nil || body != "Спасибо, изучу и отвечу до конца дня." || author != "denis" {
		t.Errorf("stored answer: %q by %q, %v", body, author, err)
	}

	// The clock of «time to first reaction» stops at the first answer, not the last.
	first := card.FirstResponseAt.Time
	f.now = f.now.Add(3 * time.Hour)
	if _, err := store.Reply(ctx, id, "denis", "Коммерческое предложение во вложении."); err != nil {
		t.Fatal(err)
	}
	if card, _ = store.Card(ctx, id); !card.FirstResponseAt.Time.Equal(first) {
		t.Error("the second answer moved the time of the first reaction")
	}

	// The feed tells the story in order: the form, the note, the answers — and the client never
	// sees the note (it is a different kind of entry, not a message to anybody).
	var kinds []string
	for _, entry := range card.Feed {
		if entry.Kind != "event" {
			kinds = append(kinds, entry.Kind+":"+entry.Direction)
		}
	}
	if got := strings.Join(kinds, " "); got != "message:in note:note message:out message:out" {
		t.Errorf("feed: %s", got)
	}

	if err := store.MarkDelivery(ctx, messageID, "sent", "lead-1.reply.abc@krokosha.xyz"); err != nil {
		t.Fatal(err)
	}
	if ids, err := store.ThreadIDs(ctx, id); err != nil || len(ids) != 1 || ids[0] != "lead-1.reply.abc@krokosha.xyz" {
		t.Errorf("thread: %v %v", ids, err)
	}

	// A client who left a phone number: the «answer» is the record of a call, nothing is sent.
	phone := f.seed(func(v map[string]string) { v["contact_method"], v["contact_value"] = "phone", "+380671234567" })
	if _, err := store.Reply(ctx, phone, "denis", "Позвонил — итог: встречаемся в четверг."); err != nil {
		t.Fatal(err)
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM outbox WHERE lead_id = %d AND kind = 'lead.reply'`, phone)); n != 0 {
		t.Error("a phone call was queued for delivery")
	}
	// An answer to a closed request does not reopen it.
	_ = store.SetStatus(ctx, phone, "denis", StatusDone, "")
	if _, err := store.Reply(ctx, phone, "denis", "Напоминаю про акт."); err != nil {
		t.Fatal(err)
	}
	if card, _ := store.Card(ctx, phone); card.Lead.Status != StatusDone {
		t.Errorf("status after answering a closed request: %s", card.Lead.Status)
	}
}

// «The client is waiting» is shown where somebody owes an answer — and nowhere else.
func TestWhoIsWaitingForAnAnswer(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	ctx := context.Background()
	untouched := f.seed(nil)
	taken := f.seed(nil)
	answered := f.seed(nil)
	called := f.seed(func(v map[string]string) { v["contact_method"], v["contact_value"] = "phone", "+380671234567" })
	closed := f.seed(nil)
	for _, id := range []int64{taken, answered, called, closed} {
		if _, err := store.Take(ctx, id, "denis"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Reply(ctx, answered, "denis", "Спасибо, отвечу сегодня."); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reply(ctx, called, "denis", "Позвонил, договорились на четверг."); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(ctx, closed, "denis", StatusDone, ""); err != nil {
		t.Fatal(err)
	}

	all, _, err := store.List(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	waiting := map[int64]bool{}
	for _, item := range all {
		waiting[item.ID] = item.LastFromUser
	}
	want := map[int64]bool{untouched: false, taken: true, answered: false, called: false, closed: false}
	if fmt.Sprint(waiting) != fmt.Sprint(want) {
		t.Errorf("waiting for an answer: %v, want %v", waiting, want)
	}
}

func TestListSearchAndFunnel(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	ctx := context.Background()
	first := f.seed(nil)
	second := f.seed(func(v map[string]string) {
		v["name"], v["contact_method"], v["contact_value"] = "Olena Shevchenko", "telegram", "@olena_sh"
		v["description"] = "Kubernetes cluster for a small team, 100% uptime is not required."
		v["lang"], v["direction"] = "en", "devops"
	})
	spam := f.seed(func(v map[string]string) { v["website"] = "x" })

	f.now = f.now.Add(30 * time.Minute)
	if _, err := store.Reply(ctx, first, "denis", "Спасибо, отвечу сегодня."); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Take(ctx, second, "denis"); err != nil {
		t.Fatal(err)
	}
	_ = store.SetStatus(ctx, second, "denis", StatusDone, "")

	all, total, err := store.List(ctx, Filter{})
	if err != nil || total != 2 || len(all) != 2 || all[0].ID != second || all[1].ID != first {
		t.Fatalf("list without spam: %d of %d, %v", len(all), total, err)
	}
	if all[1].Status != StatusWaitingClient || all[1].Messages != 2 || all[1].LastFromUser || all[0].Number() != "K-0002" {
		t.Errorf("summaries: %+v", all)
	}
	for name, tc := range map[string]struct {
		filter Filter
		want   []int64
	}{
		"spam only":            {Filter{Status: StatusSpam}, []int64{spam}},
		"everything":           {Filter{Status: "all"}, []int64{spam, second, first}},
		"by name":              {Filter{Query: "shevchenko"}, []int64{second}},
		"by contact":           {Filter{Query: "@olena"}, []int64{second}},
		"by words of the task": {Filter{Query: "MikroTik"}, []int64{first}},
		"by number":            {Filter{Query: "#K-0002"}, []int64{second}},
		"by bare number":       {Filter{Query: "1"}, []int64{first}},
		"% is a character":     {Filter{Query: "100%"}, []int64{second}},
		"_ is a character":     {Filter{Query: "olena_sh"}, []int64{second}},
		"nothing":              {Filter{Query: "zzzz"}, nil},
		"second page":          {Filter{Status: "all", Limit: 1, Offset: 1}, []int64{second}},
	} {
		got, _, err := store.List(ctx, tc.filter)
		var ids []int64
		for _, item := range got {
			ids = append(ids, item.ID)
		}
		if err != nil || fmt.Sprint(ids) != fmt.Sprint(tc.want) {
			t.Errorf("%s: %v %v, want %v", name, ids, err, tc.want)
		}
	}

	counts, err := store.Counts(ctx)
	if err != nil || counts[StatusWaitingClient] != 1 || counts[StatusDone] != 1 || counts[StatusSpam] != 1 || counts[StatusNew] != 0 {
		t.Errorf("counts: %v %v", counts, err)
	}
	funnel, err := store.Funnel(ctx, noon.Add(-time.Hour), noon.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if funnel.Total != 3 || funnel.Waiting != 1 || funnel.Done != 1 || funnel.Spam != 1 || funnel.FirstResponse != 30*time.Minute {
		t.Errorf("funnel: %+v", funnel)
	}
	if empty, err := store.Funnel(ctx, noon.Add(-48*time.Hour), noon.Add(-24*time.Hour)); err != nil || empty.Total != 0 || empty.FirstResponse != 0 {
		t.Errorf("an empty period: %+v %v", empty, err)
	}

	// Where requests come from: by source and advertising campaign, spam left out.
	for id, origin := range map[int64][2]string{first: {"ads", "mikrotik-kyiv"}, second: {"ads", "mikrotik-kyiv"}, spam: {"search", ""}} {
		if _, err := f.db.ExecContext(ctx, `UPDATE leads SET source = ?, utm_campaign = NULLIF(?, '') WHERE id = ?`, origin[0], origin[1], id); err != nil {
			t.Fatal(err)
		}
	}
	funnel, err = store.Funnel(ctx, noon.Add(-time.Hour), noon.Add(24*time.Hour))
	if err != nil || len(funnel.Sources) != 1 || funnel.Sources[0] != (SourceStat{Source: "ads", Campaign: "mikrotik-kyiv", Requests: 2, Done: 1}) {
		t.Errorf("sources: %+v %v", funnel.Sources, err)
	}

	var rows [][]string
	if err := store.Export(ctx, func(row []string) error { rows = append(rows, append([]string(nil), row...)); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || rows[0][0] != "number" || rows[1][0] != "K-0001" || rows[2][4] != "Olena Shevchenko" || len(rows[1]) != len(rows[0]) {
		t.Errorf("export: %v", rows)
	}
}

func TestDeletingAClientsData(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	ctx := context.Background()
	id := f.seed(nil)
	keep := f.seed(func(v map[string]string) {
		v["name"], v["description"] = "Another Client", "A storage cluster for backups, three nodes to start with."
	})
	_ = store.AddNote(ctx, id, "denis", "заметка")
	if _, err := store.Reply(ctx, id, "denis", "ответ клиенту, ещё не доставлен"); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"leads", "lead_messages", "lead_events", "outbox"} {
		column := "lead_id"
		if table == "leads" {
			column = "id"
		}
		if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s = %d`, table, column, id)); n != 0 {
			t.Errorf("%s still has %d rows of the deleted request", table, n)
		}
	}
	if n := f.count(`SELECT COUNT(*) FROM leads WHERE name LIKE '%Петров%' OR description LIKE '%MikroTik%'`); n != 0 {
		t.Error("the client's words survived the deletion")
	}
	if _, err := store.Card(ctx, keep); err != nil {
		t.Errorf("another client's request was touched: %v", err)
	}
	if err := store.Delete(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestTemplates(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	ctx := context.Background()

	seeded, err := store.Templates(ctx, "")
	if err != nil || len(seeded) == 0 {
		t.Fatalf("the ready-made answers of the brief are not there: %d %v", len(seeded), err)
	}
	replies, _ := store.Templates(ctx, "reply")
	rejects, _ := store.Templates(ctx, "reject")
	if len(replies)+len(rejects) != len(seeded) || len(replies) < 12 || len(rejects) < 9 {
		t.Errorf("templates: %d replies, %d refusals", len(replies), len(rejects))
	}

	if err := store.SaveTemplate(ctx, Template{Kind: "reply", Lang: "ru", Title: "Созвон", Body: "Здравствуйте, {name}! По заявке {id}: давайте созвонимся."}); err != nil {
		t.Fatal(err)
	}
	replies, _ = store.Templates(ctx, "reply")
	var added Template
	for _, item := range replies {
		if item.Title == "Созвон" {
			added = item
		}
	}
	if added.ID == 0 {
		t.Fatal("the new template is not in the list")
	}
	lead, _ := store.Get(ctx, f.seed(nil))
	if got := FillTemplate(added.Body, lead); got != "Здравствуйте, Иван Петров! По заявке #K-0001: давайте созвонимся." {
		t.Errorf("filled template: %q", got)
	}

	added.Body = "Новый текст"
	if err := store.SaveTemplate(ctx, added); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]Template{
		"no title":        {Kind: "reply", Lang: "ru", Body: "текст"},
		"no body":         {Kind: "reply", Lang: "ru", Title: "заголовок"},
		"unknown kind":    {Kind: "spam", Lang: "ru", Title: "a", Body: "b"},
		"unknown lang":    {Kind: "reply", Lang: "de", Title: "a", Body: "b"},
		"missing to edit": {ID: 99999, Kind: "reply", Lang: "ru", Title: "a", Body: "b"},
	} {
		if err := store.SaveTemplate(ctx, bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := store.DeleteTemplate(ctx, added.ID); err != nil {
		t.Fatal(err)
	}
	if after, _ := store.Templates(ctx, "reply"); len(after) != len(replies)-1 {
		t.Errorf("after deleting: %d templates, want %d", len(after), len(replies)-1)
	}
}

// A client writes again after the form — through the bot or by answering a letter (brief B10.5).
func TestTheClientWritesAgain(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	ctx := context.Background()
	var changed []int64
	store.OnChange(func(id int64) { changed = append(changed, id) })
	lead := f.seed(nil) // the contact of the form is an email address

	// Before the client writes anything, an answer goes where the form said.
	if card, err := store.Card(ctx, lead); err != nil || card.ReplyVia != MethodEmail {
		t.Fatalf("the way of the first answer: %+v %v", card, err)
	}
	if _, err := store.Take(ctx, lead, "denis"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reply(ctx, lead, "denis", "Сколько коммутаторов уже есть?"); err != nil {
		t.Fatal(err)
	}

	// The client continues in Telegram: the request is ours again, and everybody is told.
	f.now = f.now.Add(time.Hour)
	messageID, err := store.ClientMessage(ctx, lead, ChannelTelegram, "  Два, оба старые.\x00  ")
	if err != nil {
		t.Fatal(err)
	}
	card, err := store.Card(ctx, lead)
	if err != nil {
		t.Fatal(err)
	}
	if card.Lead.Status != StatusInProgress || card.ReplyVia != ChannelTelegram {
		t.Errorf("after the client's message: %s, answers go by %s", card.Lead.Status, card.ReplyVia)
	}
	if body, channel, err := store.IncomingMessage(ctx, lead, messageID); err != nil || body != "Два, оба старые." || channel != ChannelTelegram {
		t.Errorf("the stored message: %q %q %v", body, channel, err)
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM outbox WHERE lead_id = %d AND kind = 'lead.client_message' AND status = 'pending'`, lead)); n != 2 {
		t.Errorf("notices queued: %d, want one for Telegram and one for mail", n)
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM lead_events WHERE lead_id = %d AND action = 'client_replied' AND from_status = 'waiting_client' AND to_status = 'in_progress'`, lead)); n != 1 {
		t.Error("the history does not say that the client answered")
	}
	if len(changed) == 0 || changed[len(changed)-1] != lead {
		t.Errorf("listeners were not told: %v", changed)
	}

	// The next answer follows the client into Telegram.
	if _, err := store.Reply(ctx, lead, "denis", "Тогда меняем оба."); err != nil {
		t.Fatal(err)
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM outbox WHERE lead_id = %d AND kind = 'lead.reply' AND channel = 'telegram'`, lead)); n != 1 {
		t.Errorf("answers queued for Telegram: %d", n)
	}
	all, _, _ := store.List(ctx, Filter{})
	if len(all) != 1 || all[0].LastFromUser {
		t.Errorf("after our answer nobody is waiting: %+v", all)
	}

	// A closed request stays closed — the people decide —, but they do hear about the message.
	_ = store.SetStatus(ctx, lead, "denis", StatusDone, "")
	if _, err := store.ClientMessage(ctx, lead, ChannelTelegram, "Спасибо!"); err != nil {
		t.Fatal(err)
	}
	if card, _ = store.Card(ctx, lead); card.Lead.Status != StatusDone {
		t.Errorf("a message reopened a finished request: %s", card.Lead.Status)
	}
	// Nothing, from nobody, to nowhere.
	if _, err := store.ClientMessage(ctx, lead, ChannelTelegram, " \n "); !errors.Is(err, ErrEmptyText) {
		t.Errorf("an empty message: %v", err)
	}
	if _, err := store.ClientMessage(ctx, 999, ChannelTelegram, "hello"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a message for a request that is not: %v", err)
	}
	// What a robot «adds» to its request is stored with it and announced to nobody.
	robot := f.seed(func(v map[string]string) { v["website"] = "x" })
	if _, err := store.ClientMessage(ctx, robot, ChannelTelegram, "buy backlinks"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM outbox WHERE lead_id = %d`, robot)); n != 0 {
		t.Errorf("a robot's message was announced: %d tasks", n)
	}
}

func TestOpenMineAndUnclaimed(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	ctx := context.Background()
	untouched, mine, hers, finished := f.seed(nil), f.seed(nil), f.seed(nil), f.seed(nil)
	_ = f.seed(func(v map[string]string) { v["website"] = "x" }) // spam is nobody's business
	for id, who := range map[int64]string{mine: "Денис", hers: "Олена", finished: "Денис"} {
		if _, err := store.Take(ctx, id, who); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.SetStatus(ctx, finished, "Денис", StatusDone, "")

	ids := func(filter Filter) string {
		found, _, err := store.List(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, item := range found {
			out = append(out, item.ID)
		}
		return fmt.Sprint(out)
	}
	if got := ids(Filter{Status: "open"}); got != fmt.Sprint([]int64{hers, mine, untouched}) {
		t.Errorf("open: %s", got)
	}
	if got := ids(Filter{Status: "open", Assignee: "Денис"}); got != fmt.Sprint([]int64{mine}) {
		t.Errorf("mine: %s", got)
	}

	// Half an hour and nobody took it: one reminder, never a second one.
	f.now = f.now.Add(31 * time.Minute)
	due, err := store.Unclaimed(ctx, f.now.Add(-30*time.Minute))
	if err != nil || fmt.Sprint(due) != fmt.Sprint([]int64{untouched}) {
		t.Fatalf("unclaimed: %v %v", due, err)
	}
	if err := store.MarkReminded(ctx, untouched); err != nil {
		t.Fatal(err)
	}
	if due, _ = store.Unclaimed(ctx, f.now); len(due) != 0 {
		t.Errorf("reminded twice: %v", due)
	}
}

// TestAnswersFollowTheClientIntoTelegram: a client who left an address and then opened the bot by
// the link of the «thank you» page was told the answer would come to that chat — so it goes there,
// until the client writes a letter again.
func TestAnswersFollowTheClientIntoTelegram(t *testing.T) {
	f := newFixture(t)
	store := NewStore(f.db, func() time.Time { return f.now })
	id := f.seed(nil)
	ctx := context.Background()
	queued := func(messageID int64) string {
		t.Helper()
		var channel string
		if err := f.db.QueryRow(`SELECT channel FROM outbox WHERE kind = 'lead.reply' AND JSON_EXTRACT(payload, '$.message_id') = ?`, messageID).Scan(&channel); err != nil {
			t.Fatal(err)
		}
		return channel
	}
	answer := func(text string) string {
		t.Helper()
		f.now = f.now.Add(time.Minute)
		messageID, err := store.Reply(ctx, id, "denis", text)
		if err != nil {
			t.Fatal(err)
		}
		return queued(messageID)
	}

	if channel := answer("Добрый день! Уточните, пожалуйста, сроки."); channel != ChannelEmail {
		t.Errorf("before the client opened the bot: %s", channel)
	}
	f.now = f.now.Add(time.Minute)
	if err := store.ClientLinked(ctx, id); err != nil {
		t.Fatal(err)
	}
	if channel := answer("Вижу, вы в Telegram: продолжим здесь."); channel != ChannelTelegram {
		t.Errorf("after the client opened the bot: %s", channel)
	}
	f.now = f.now.Add(time.Minute)
	if _, err := store.ClientMessage(ctx, id, ChannelEmail, "Лучше пишите на почту."); err != nil {
		t.Fatal(err)
	}
	if channel := answer("Хорошо, пишу на почту."); channel != ChannelEmail {
		t.Errorf("after a letter of the client: %s", channel)
	}
}
