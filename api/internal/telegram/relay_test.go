package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram/tgtest"
)

func task(kind string, leadID, messageID int64) outbox.Task {
	payload, _ := json.Marshal(leads.TaskPayload{LeadID: leadID, MessageID: messageID})
	return outbox.Task{Channel: outbox.ChannelTelegram, Kind: kind, LeadID: leadID, Payload: payload}
}

func replyTo(call tgtest.Call) int64 {
	parameters, _ := call.Params["reply_parameters"].(map[string]any)
	id, _ := parameters["message_id"].(float64)
	return int64(id)
}

// TestAClientContinuesInTelegram: brief B10.5 — the bot relays between a client and the staff,
// and the client sees nothing but their own request.
func TestAClientContinuesInTelegram(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	ctx := context.Background()
	f.join(denis, RoleOwner)
	f.join(olena, RoleMember)
	lead := f.addLead(store, func(sub *leads.Submission) { sub.ContactMethod, sub.ContactValue = leads.MethodTelegram, "@ivan_p" })
	robot, err := store.Create(ctx, leads.Submission{Name: "Bot", ContactMethod: leads.MethodEmail, ContactValue: "x@spam.test", Direction: "networks",
		Description: "Buy cheap traffic for your website today", Lang: "en"}, leads.Verdict{Score: 100}, analytics.SessionSummary{}, "203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	f.announce(lead.ID)

	// An answer written before the client came: a bot cannot write first, so it waits.
	if _, err := store.Take(ctx, lead.ID, "Денис Гумен"); err != nil {
		t.Fatal(err)
	}
	answerID, err := store.Reply(ctx, lead.ID, "Денис Гумен", "Иван, добрый день! Сколько <коммутаторов> уже есть?")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.bot.Send(ctx, task(leads.TaskReply, lead.ID, answerID)); !outbox.IsNotReady(err) {
		t.Fatalf("an answer for a client who has not come yet: %v", err)
	}

	// Links that open nothing: a made-up one, a robot's request. The person is a passer-by.
	for i, token := range []string{"AAAAAAAAAAAAAAAAAAAAAA", robot.PublicToken, "", "../../etc/passwd"} {
		got := f.says(User{ID: 4000 + int64(i), LanguageCode: "en"}, "/start "+ClientPrefix+token)
		if len(got) != 1 || !strings.Contains(got[0].Text(), "This is the bot of") {
			t.Errorf("the link %q: %+v", token, got)
		}
	}

	// The client opens the link of their own request — texts follow the language of the page.
	hurried := f.hurried
	welcome := oneText(t, f.says(client, "/start "+ClientPrefix+lead.PublicToken))
	if !strings.Contains(welcome, "Заявка #K-0001 у нас, статус: в работе") || strings.Contains(welcome, "Денис") {
		t.Errorf("the welcome of a client: %q", welcome)
	}
	if f.hurried != hurried+1 || f.count(`SELECT COUNT(*) FROM lead_events WHERE lead_id = 1 AND action = 'client_linked'`) != 1 {
		t.Error("the waiting answer was not hurried, or the history does not know the client came")
	}
	// The link works once: another account gets nothing out of it; the same one may come again.
	if text := oneText(t, f.says(User{ID: 2002, LanguageCode: "ru"}, "/start "+ClientPrefix+lead.PublicToken)); !strings.Contains(text, "уже открыта в другом аккаунте") {
		t.Errorf("a second account with the same link: %q", text)
	}
	if text := oneText(t, f.says(client, "/start "+ClientPrefix+lead.PublicToken)); !strings.Contains(text, "Заявка #K-0001") {
		t.Errorf("the same client again: %q", text)
	}
	if n := f.count(`SELECT COUNT(*) FROM bot_clients`); n != 1 {
		t.Fatalf("clients linked: %d", n)
	}

	// Now the waiting answer goes out, in the bot's name, escaped.
	f.api.Forget()
	if err := f.bot.Send(ctx, task(leads.TaskReply, lead.ID, answerID)); err != nil {
		t.Fatal(err)
	}
	if text := oneText(t, f.api.Sent(client.ID)); !strings.HasPrefix(text, "💬 Ответ по заявке #K-0001:\n\n") || !strings.Contains(text, "Сколько &lt;коммутаторов&gt; уже есть?") {
		t.Errorf("the answer in the client's chat: %q", text)
	}
	if f.count(fmt.Sprintf(`SELECT COUNT(*) FROM lead_messages WHERE id = %d AND delivery = 'sent'`, answerID)) != 1 {
		t.Error("the answer is not marked as delivered")
	}

	// The client writes: it lands in the conversation, the request is ours again.
	kicks := f.kicks
	if text := oneText(t, f.says(client, "Два, оба <старые>. Когда сможете начать?")); text != "Передано." {
		t.Errorf("the acknowledgement: %q", text)
	}
	card, _ := store.Card(ctx, lead.ID)
	var incoming leads.Entry
	for _, entry := range card.Feed {
		if entry.Direction == "in" && entry.Channel == leads.ChannelTelegram {
			incoming = entry
		}
	}
	if card.Lead.Status != leads.StatusInProgress || incoming.Body != "Два, оба <старые>. Когда сможете начать?" || f.kicks != kicks+1 {
		t.Fatalf("the client's message: status %s, stored %q", card.Lead.Status, incoming.Body)
	}

	// Everybody on the staff is told, as a reply to the card in their own chat — once.
	f.api.Forget()
	f.api.Refuse("sendMessage", 1, http.StatusBadGateway, "Bad Gateway", 0)
	if err := f.bot.Send(ctx, task(leads.TaskClientMessage, lead.ID, incoming.MessageID)); err == nil {
		t.Fatal("a push with one failure must be retried")
	}
	if err := f.bot.Send(ctx, task(leads.TaskClientMessage, lead.ID, incoming.MessageID)); err != nil {
		t.Fatal(err)
	}
	_ = f.bot.Send(ctx, task(leads.TaskClientMessage, lead.ID, incoming.MessageID))
	if got := len(f.api.Sent(denis.ID)) + len(f.api.Sent(olena.ID)); got != 3 { // one of the three failed
		t.Fatalf("pushes sent: %d, want one per member plus the failed attempt", got)
	}
	push := lastCall(t, f.api.Sent(olena.ID))
	labels, data := push.Buttons()
	if !strings.Contains(push.Text(), "💬 <b>#K-0001</b> · Иван &lt;b&gt;Петров&lt;/b&gt; пишет в Telegram:") || !strings.Contains(push.Text(), "оба &lt;старые&gt;") ||
		fmt.Sprint(labels) != "[💬 Ответить 📇 Карточка]" || data["💬 Ответить"] != "l:reply:1" || replyTo(push) == 0 {
		t.Errorf("the push: %q %v reply_to=%d", push.Text(), labels, replyTo(push))
	}
	var cardMessage int64
	if err := f.db.QueryRow(`SELECT message_id FROM bot_messages WHERE lead_id = 1 AND kind = 'card' AND chat_id = ?`, olena.ID).Scan(&cardMessage); err != nil || replyTo(push) != cardMessage {
		t.Errorf("the push answers message %d, the card is %d (%v)", replyTo(push), cardMessage, err)
	}

	// An answer from the bot now follows the client into Telegram (the form said «telegram» anyway).
	f.presses(denis, "l:reply:1")
	f.presses(denis, "l:own:1")
	f.says(denis, "Можем начать в понедельник.")
	f.presses(denis, "l:send:1")
	if n := f.count(`SELECT COUNT(*) FROM outbox WHERE lead_id = 1 AND kind = 'lead.reply' AND channel = 'telegram'`); n != 2 {
		t.Errorf("answers queued for Telegram: %d", n)
	}

	// What a client can and cannot do here.
	for _, text := range []string{"/leads", "/users", "/lead K-0002", "/start", "/help"} {
		got := oneText(t, f.says(client, text))
		if !strings.Contains(got, "Заявка #K-0001, статус: в работе") || strings.Contains(got, "K-0002") {
			t.Errorf("%s from a client: %q", text, got)
		}
	}
	if calls := f.presses(client, "l:take:1"); len(calls) != 1 || calls[0].Params["text"] != "Нет доступа." {
		t.Errorf("a client pressed a staff button: %+v", calls)
	}
	if text := oneText(t, f.says(client, "")); !strings.Contains(text, "только текст") { // a sticker, a photo
		t.Errorf("something that is not text: %q", text)
	}
	for range 19 { // with the one above: twenty messages this hour
		f.says(client, "ещё одно сообщение")
	}
	if text := oneText(t, f.says(client, "и ещё")); !strings.Contains(text, "Слишком много сообщений") {
		t.Errorf("the twenty-first message: %q", text)
	}
	if n := f.count(`SELECT COUNT(*) FROM lead_messages WHERE lead_id = 1 AND direction = 'in' AND channel = 'telegram'`); n != 20 {
		t.Errorf("messages stored: %d, want 20", n)
	}

	// The client blocked the bot: the answer is marked as not delivered, nobody retries for days.
	lateID, _ := store.Reply(ctx, lead.ID, "Денис Гумен", "Напоминаю про понедельник.")
	f.api.Refuse("sendMessage", 1, http.StatusForbidden, "Forbidden: bot was blocked by the user", 0)
	if err := f.bot.Send(ctx, task(leads.TaskReply, lead.ID, lateID)); !outbox.IsPermanent(err) ||
		f.count(fmt.Sprintf(`SELECT COUNT(*) FROM lead_messages WHERE id = %d AND delivery = 'failed'`, lateID)) != 1 {
		t.Errorf("an answer to somebody who blocked the bot: %v", err)
	}

	// The client's data is deleted: for the bot they are a passer-by again.
	if err := store.Delete(ctx, lead.ID); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Hour)
	if text := oneText(t, f.says(client, "вы ещё там?")); !strings.Contains(text, "This is the bot of") {
		t.Errorf("after the deletion: %q", text)
	}
}

func TestCommandsOfTheStaff(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	ctx := context.Background()
	f.join(denis, RoleOwner)
	f.join(olena, RoleMember)
	first, second, third := f.addLead(store, nil), f.addLead(store, func(sub *leads.Submission) {
		sub.Name, sub.Description = "Olena Shevchenko", "Kubernetes для небольшой команды."
	}), f.addLead(store, nil)
	if _, err := store.Take(ctx, second.ID, "Денис Гумен"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Take(ctx, third.ID, "Олена"); err != nil {
		t.Fatal(err)
	}
	_ = store.SetStatus(ctx, third.ID, "Олена", leads.StatusDone, "")

	// /leads: what is open, without anybody's name — a list cannot be wiped when a client is.
	list := lastCall(t, f.says(denis, "/leads"))
	labels, data := list.Buttons()
	if !strings.Contains(list.Text(), "<b>Открытые заявки</b> — 2") || !strings.Contains(list.Text(), "🟢 <b>#K-0002</b> · Сети и оборудование · 19.09 · Денис Гумен · <i>ждёт ответа</i>") ||
		strings.Contains(list.Text(), "Shevchenko") || strings.Contains(list.Text(), "K-0003") {
		t.Errorf("/leads:\n%s", list.Text())
	}
	if fmt.Sprint(labels) != "[#K-0002 #K-0001 · Открытые · Новые Мои Ждут клиента]" || data["#K-0001"] != "l:card:1" || data["Мои"] != "ls:mine" {
		t.Errorf("buttons of /leads: %v %v", labels, data)
	}
	// «Мои» redraws the same message.
	calls := f.presses(denis, "ls:mine")
	mine := lastCall(t, calls)
	if mine.Method != "editMessageText" || !strings.Contains(mine.Text(), "<b>Мои заявки</b> — 1") || strings.Contains(mine.Text(), "K-0001") {
		t.Errorf("«Мои»: %s %q", mine.Method, mine.Text())
	}
	if other := lastCall(t, f.presses(olena, "ls:mine")); !strings.Contains(other.Text(), "Пусто.") {
		t.Errorf("«Мои» of somebody with nothing in work: %q", other.Text())
	}

	// A card by its number — and it follows the request like the first one.
	f.presses(denis, data["#K-0001"])
	if card := lastCall(t, f.api.Sent(denis.ID)); !strings.Contains(card.Text(), "<b>Заявка #K-0001</b>") {
		t.Errorf("a card from the list: %q", card.Text())
	}
	for _, number := range []string{"K-0002", "#k-2", "2", "0002"} {
		if card := lastCall(t, f.says(denis, "/lead "+number)); !strings.Contains(card.Text(), "<b>Заявка #K-0002</b>") {
			t.Errorf("/lead %s: %q", number, card.Text())
		}
	}
	if n := f.count(`SELECT COUNT(*) FROM bot_messages WHERE kind = 'card' AND lead_id IN (1, 2)`); n != 5 {
		t.Errorf("cards remembered: %d, want 5", n)
	}
	f.settle() // what happened to the requests above has been drawn
	f.api.Forget()
	if _, err := store.Take(ctx, first.ID, "Олена"); err != nil {
		t.Fatal(err)
	}
	f.settle()
	if redrawn := f.edits(denis.ID); len(redrawn) != 1 || !strings.Contains(redrawn[0].Text(), "взял(а) Олена") {
		t.Errorf("the card sent by /lead does not follow the request: %+v", redrawn)
	}
	for argument, want := range map[string]string{"": "Нужен номер заявки", "abc": "Нужен номер заявки", "K-0999": "Заявки #K-0999 нет"} {
		if text := oneText(t, f.says(denis, "/lead "+argument)); !strings.Contains(text, want) {
			t.Errorf("/lead %q: %q", argument, text)
		}
	}

	// /search
	found := lastCall(t, f.says(olena, "/search kubernetes"))
	if labels, _ = found.Buttons(); !strings.Contains(found.Text(), "<b>Поиск: kubernetes</b> — 1") || fmt.Sprint(labels) != "[#K-0002]" {
		t.Errorf("/search: %q %v", found.Text(), labels)
	}
	if text := oneText(t, f.says(olena, "/search")); !strings.Contains(text, "Что искать?") {
		t.Errorf("/search without words: %q", text)
	}
	if text := oneText(t, f.says(olena, "/search <script>")); !strings.Contains(text, "Поиск: &lt;script&gt;</b> — 0") || !strings.Contains(text, "Пусто.") {
		t.Errorf("/search with markup: %q", text)
	}

	// /stats
	if text := oneText(t, f.says(denis, "/stats")); !strings.Contains(text, "Заявок: 3 (и ещё спама: 0)") || !strings.Contains(text, "✅ завершено: 1") ||
		!strings.Contains(text, "Дошло до работы: 100%") || !strings.Contains(text, "• ads · mikrotik — 3") {
		t.Errorf("/stats:\n%s", text)
	}

	// /mute
	if text := oneText(t, f.says(olena, "/mute 2h")); !strings.Contains(text, "Тишина до 19.09 14:00") {
		t.Errorf("/mute 2h: %q", text)
	}
	if member, _ := f.access.Member(ctx, olena.ID); !member.MutedUntil.Valid || !member.MutedUntil.Time.Equal(f.now.Add(2*time.Hour)) {
		t.Errorf("muted until: %+v", member.MutedUntil)
	}
	if text := oneText(t, f.says(olena, "/mute")); !strings.Contains(text, "Сейчас тишина до 19.09 14:00") {
		t.Errorf("/mute: %q", text)
	}
	for argument, want := range map[string]string{"30м": "Тишина до 19.09 12:30", "1d": "Тишина до 20.09 12:00", "30d": "От минуты до семи дней", "off": "Звук включён."} {
		if text := oneText(t, f.says(olena, "/mute "+argument)); !strings.Contains(text, want) {
			t.Errorf("/mute %s: %q", argument, text)
		}
	}
	if text := oneText(t, f.says(olena, "/help")); !strings.Contains(text, "/leads") || !strings.Contains(text, "/mute") || strings.Contains(text, "/users") {
		t.Errorf("/help of a member: %q", text)
	}
}

func TestReminderAndMorningDigest(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	ctx := context.Background()
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	f.bot.opts.Location, f.bot.opts.RemindAfter, f.bot.opts.DigestAt = kyiv, 30*time.Minute, "09:00"
	f.now = time.Date(2026, 9, 19, 10, 0, 0, 0, kyiv)

	untouched, taken := f.addLead(store, nil), f.addLead(store, nil)
	// Nobody in the bot yet: there is nobody to remind, and the reminder is not used up.
	f.now = f.now.Add(40 * time.Minute)
	f.bot.remind(ctx)
	if due, _ := store.Unclaimed(ctx, f.now.Add(-30*time.Minute)); len(due) != 2 {
		t.Fatalf("requests waiting for a reminder: %v", due)
	}

	f.join(denis, RoleOwner)
	f.join(olena, RoleMember)
	f.announce(untouched.ID)
	f.announce(taken.ID)
	if _, err := store.Take(ctx, taken.ID, "Олена"); err != nil {
		t.Fatal(err)
	}
	_ = f.access.Mute(ctx, olena.ID, f.now.Add(time.Hour))
	f.api.Forget()
	f.bot.remind(ctx)
	f.bot.remind(ctx) // a minute later: once is enough
	reminders := f.api.Calls("sendMessage")
	if len(reminders) != 2 {
		t.Fatalf("reminders sent: %d, want one per member, for the untaken request only", len(reminders))
	}
	reminder := lastCall(t, f.api.Sent(olena.ID))
	labels, _ := reminder.Buttons()
	if !strings.Contains(reminder.Text(), "⏰ <b>#K-0001</b>") || !strings.Contains(reminder.Text(), "ждёт уже 40 мин") || fmt.Sprint(labels) != "[✅ Взять в работу 📇 Карточка]" ||
		replyTo(reminder) == 0 || reminder.Params["disable_notification"] != true {
		t.Errorf("the reminder: %q %v", reminder.Text(), labels)
	}
	if n := f.count(`SELECT COUNT(*) FROM bot_messages WHERE lead_id = 1 AND ref = 'remind'`); n != 2 {
		t.Errorf("reminders remembered for wiping: %d", n)
	}

	// The digest: at nine by the owner's clock, once a day.
	f.api.Forget()
	for _, moment := range []time.Time{
		time.Date(2026, 9, 20, 8, 59, 0, 0, kyiv), // too early
		time.Date(2026, 9, 20, 9, 0, 30, 0, kyiv), // now
		time.Date(2026, 9, 20, 9, 1, 30, 0, kyiv), // already sent
		time.Date(2026, 9, 20, 23, 0, 0, 0, kyiv), // already sent
		time.Date(2026, 9, 21, 15, 0, 0, 0, kyiv), // the service was down all morning: no digest in the afternoon
		time.Date(2026, 9, 22, 9, 40, 0, 0, kyiv), // a little late is fine
	} {
		f.now = moment
		f.bot.digest(ctx)
	}
	digests := f.api.Sent(denis.ID)
	if len(digests) != 2 || len(f.api.Sent(olena.ID)) != 2 {
		t.Fatalf("digests sent to the owner: %d, want 2 (the 20th and the 22nd)", len(digests))
	}
	labels, _ = digests[0].Buttons()
	if !strings.Contains(digests[0].Text(), "☀️ Доброе утро! 🟡 новых: 1 · 🟢 в работе: 1 · 🔵 ждём клиента: 0") || !strings.Contains(digests[0].Text(), "<b>#K-0002</b>") ||
		strings.Contains(digests[0].Text(), "Петров") || fmt.Sprint(labels) != "[#K-0002 #K-0001]" {
		t.Errorf("the digest: %q %v", digests[0].Text(), labels)
	}

	// A quiet morning needs no message; «off» means off.
	_ = store.SetStatus(ctx, untouched.ID, "denis", leads.StatusRejected, "")
	_ = store.SetStatus(ctx, taken.ID, "denis", leads.StatusDone, "")
	f.api.Forget()
	f.now = time.Date(2026, 9, 23, 9, 0, 0, 0, kyiv)
	f.bot.digest(ctx)
	f.bot.opts.DigestAt, f.bot.opts.RemindAfter = "", 0
	f.now = time.Date(2026, 9, 24, 9, 0, 0, 0, kyiv)
	_ = f.addLead(store, nil)
	f.bot.digest(ctx)
	f.now = f.now.Add(2 * time.Hour)
	f.bot.remind(ctx)
	if got := f.api.Calls("sendMessage"); len(got) != 0 {
		t.Errorf("messages on a quiet morning and with everything switched off: %+v", got)
	}

	// Letters nobody could place are worth a line — a number only, and even on a morning without requests.
	_ = store.SetStatus(ctx, 3, "denis", leads.StatusRejected, "")
	f.bot.opts.DigestAt, f.bot.opts.Letters = "09:00", func(context.Context) int { return 2 }
	f.now = time.Date(2026, 9, 25, 9, 0, 0, 0, kyiv)
	f.bot.digest(ctx)
	if got := f.api.Sent(denis.ID); len(got) != 1 || !strings.Contains(got[0].Text(), "📥 писем без заявки: 2 — раздел «Входящие» в админке") {
		t.Errorf("the digest about letters: %+v", got)
	}
}

// TestLettersInTelegram: brief B10.5 — a client's letter reaches the staff as a reply to the card;
// its files stay on the server. And a letter of ours that came back is not kept a secret.
func TestLettersInTelegram(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	ctx := context.Background()
	f.join(denis, RoleOwner)
	lead := f.addLead(store, nil)
	f.announce(lead.ID)

	files := leads.NewFiles(t.TempDir())
	store.UseFiles(files)
	upload, err := files.Save("схема <сети>.pdf", leads.KindPDF, strings.NewReader("%PDF-1.7 the scheme"))
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := store.ClientWrote(ctx, lead.ID, leads.Incoming{Channel: leads.ChannelEmail, Text: "Схема <во вложении>.", Files: []leads.Upload{upload}})
	if err != nil {
		t.Fatal(err)
	}
	f.api.Forget()
	if err := f.bot.Send(ctx, task(leads.TaskClientMessage, lead.ID, messageID)); err != nil {
		t.Fatal(err)
	}
	push := lastCall(t, f.api.Sent(denis.ID))
	if !strings.Contains(push.Text(), "пишет письмом:") || !strings.Contains(push.Text(), "Схема &lt;во вложении&gt;.") ||
		!strings.Contains(push.Text(), "📎 файлов: 1 — в админке") || strings.Contains(push.Text(), "схема") || replyTo(push) == 0 {
		t.Errorf("the push about a letter: %q reply_to=%d", push.Text(), replyTo(push))
	}

	// A mail server returned our answer.
	payload, _ := json.Marshal(leads.TaskPayload{LeadID: lead.ID, Note: "ivan@compny.test 5.4.4 Host <not> found"})
	returned := outbox.Task{ID: 77, Channel: outbox.ChannelTelegram, Kind: leads.TaskUndelivered, LeadID: lead.ID, Payload: payload}
	f.api.Forget()
	if err := f.bot.Send(ctx, returned); err != nil {
		t.Fatal(err)
	}
	_ = f.bot.Send(ctx, returned) // delivered again after a restart: nobody hears it twice
	sent := f.api.Sent(denis.ID)
	if len(sent) != 1 {
		t.Fatalf("messages about the returned letter: %d", len(sent))
	}
	if text := sent[0].Text(); !strings.Contains(text, "⚠️ <b>#K-0001</b> · письмо клиенту не доставлено") || !strings.Contains(text, "5.4.4 Host &lt;not&gt; found") || replyTo(sent[0]) == 0 {
		t.Errorf("the message: %q reply_to=%d", text, replyTo(sent[0]))
	}
	// It is about a person: remembered, to be wiped with the request.
	if f.count(`SELECT COUNT(*) FROM bot_messages WHERE lead_id = 1 AND ref = 'undelivered:77'`) != 1 {
		t.Error("the message about the returned letter is not remembered")
	}
}

// An alert about the server is for those who manage it, not for everybody who answers clients.
func TestAlertsGoToOwners(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	payload, _ := json.Marshal(outbox.Alert{Subject: "Резервная копия не сделана", Text: "backup.sh stopped at line 102 <exit code 1>"})
	alert := outbox.Task{ID: 5, Channel: outbox.ChannelTelegram, Kind: outbox.KindAlert, Payload: payload}
	if err := f.bot.Send(ctx, alert); !outbox.IsNotReady(err) {
		t.Fatalf("an alert with no owner in the bot yet: %v", err)
	}
	f.join(olena, RoleMember)
	if err := f.bot.Send(ctx, alert); !outbox.IsNotReady(err) {
		t.Fatalf("an alert with members only: %v", err)
	}
	f.join(denis, RoleOwner)
	f.api.Forget()
	if err := f.bot.Send(ctx, alert); err != nil {
		t.Fatal(err)
	}
	if len(f.api.Sent(olena.ID)) != 0 {
		t.Error("a member was told about the server")
	}
	if text := oneText(t, f.api.Sent(denis.ID)); !strings.Contains(text, "🛠 <b>Резервная копия не сделана</b>") || !strings.Contains(text, "&lt;exit code 1&gt;") {
		t.Errorf("the alert: %q", text)
	}
}
