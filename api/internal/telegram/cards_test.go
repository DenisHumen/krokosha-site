package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram/tgtest"
)

var testForm = config.Form{Enabled: true, Directions: []config.Option{
	{ID: "networks", Label: config.Localized{"en": "Networks & hardware", "ru": "Сети и оборудование"}},
}}

// withLeads gives the bot of a fixture the requests to work with, the way main.go wires them.
func (f *fixture) withLeads() *leads.Store {
	f.t.Helper()
	store := leads.NewStore(f.db, func() time.Time { return f.now })
	f.bot.opts.DB, f.bot.opts.Leads = f.db, store
	f.bot.opts.Form = func() config.Form { return testForm }
	f.bot.opts.AdminURL = "https://krokosha.xyz/_secret1/"
	f.bot.opts.Kick = func() { f.kicks++ }
	store.OnChange(f.bot.Changed)
	store.OnErase(f.bot.Erasing)
	return store
}

// settle does what RunCards does in the service: redraws and wipes whatever is due.
func (f *fixture) settle() {
	ctx := context.Background()
	for {
		select {
		case id := <-f.bot.redraw:
			f.bot.redrawCards(ctx, id)
		case <-f.bot.wipe:
			f.bot.wipeMessages(ctx)
		default:
			return
		}
	}
}

func (f *fixture) addLead(store *leads.Store, change func(*leads.Submission)) *leads.Lead {
	f.t.Helper()
	sub := leads.Submission{
		Name: "Иван <b>Петров</b>", ContactMethod: leads.MethodEmail, ContactValue: "ivan@company.com", Direction: "networks",
		Description: "Нужно перестроить сеть офиса на 40 мест: MikroTik и два VLAN.", Budget: "$1–3k", Timeline: "2 weeks", Lang: "ru",
	}
	if change != nil {
		change(&sub)
	}
	session := analytics.SessionSummary{Known: true, SessionID: []byte{1, 2, 3, 4, 5, 6, 7, 8}, Source: "ads", UTMCampaign: "mikrotik", Country: "UA",
		Device: "mobile", Browser: "Safari", OS: "iOS", Sections: []string{"skills", "projects", "contacts"}, TimeOnSiteMs: 180000}
	lead, err := store.Create(context.Background(), sub, leads.Verdict{}, session, "203.0.113.0/24")
	if err != nil {
		f.t.Fatal(err)
	}
	return lead
}

func notifyTask(leadID int64) outbox.Task {
	payload, _ := json.Marshal(leads.TaskPayload{LeadID: leadID})
	return outbox.Task{Channel: outbox.ChannelTelegram, Kind: leads.TaskNotify, LeadID: leadID, Payload: payload}
}

// announce delivers the card of a request to everybody, as the outbox worker does.
func (f *fixture) announce(leadID int64) {
	f.t.Helper()
	if err := f.bot.Send(context.Background(), notifyTask(leadID)); err != nil {
		f.t.Fatalf("announcing the request: %v", err)
	}
}

// edits returns the texts a chat's messages were rewritten to, oldest first.
func (f *fixture) edits(chatID int64) []tgtest.Call {
	var out []tgtest.Call
	for _, call := range f.api.Calls("editMessageText") {
		if call.ChatID() == chatID {
			out = append(out, call)
		}
	}
	return out
}

func lastCall(t *testing.T, calls []tgtest.Call) tgtest.Call {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("no calls were made")
	}
	return calls[len(calls)-1]
}

func toast(t *testing.T, calls []tgtest.Call) (text string, alert bool) {
	t.Helper()
	for _, call := range calls {
		if call.Method == "answerCallbackQuery" {
			text, _ = call.Params["text"].(string)
			alert, _ = call.Params["show_alert"].(bool)
			return text, alert
		}
	}
	t.Fatalf("the button was left spinning: %+v", calls)
	return "", false
}

func TestANewRequestComesToEverybodyAsACard(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	ctx := context.Background()

	// Nobody has joined yet: the card waits, however long it takes.
	lead := f.addLead(store, func(sub *leads.Submission) {
		sub.Description = strings.Repeat("Очень длинное описание задачи. ", 40)
	})
	if err := f.bot.Send(ctx, notifyTask(lead.ID)); !outbox.IsNotReady(err) {
		t.Fatalf("a card with nobody to go to: %v", err)
	}

	f.join(denis, RoleOwner)
	f.join(olena, RoleMember)
	revoked := User{ID: 1003, FirstName: "Former"}
	f.join(revoked, RoleMember)
	former, _ := f.access.Member(ctx, revoked.ID)
	if _, err := f.access.SetDisabled(ctx, former.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := f.access.Mute(ctx, olena.ID, f.now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Telegram fails for one of the two: the task fails, the other card is there already…
	f.api.Forget()
	f.api.Refuse("sendMessage", 1, http.StatusBadGateway, "Bad Gateway", 0)
	if err := f.bot.Send(ctx, notifyTask(lead.ID)); err == nil || outbox.IsPermanent(err) {
		t.Fatalf("a delivery with one failure: %v", err)
	}
	// …and the retry reaches only the one who was missed.
	f.announce(lead.ID)
	f.announce(lead.ID)
	if got := len(f.api.Sent(denis.ID)) + len(f.api.Sent(olena.ID)); got != 3 || len(f.api.Sent(revoked.ID)) != 0 {
		t.Fatalf("messages sent: %d (one failed), to the revoked member: %d", got, len(f.api.Sent(revoked.ID)))
	}
	if n := f.count(`SELECT COUNT(*) FROM bot_messages WHERE kind = 'card'`); n != 2 {
		t.Fatalf("cards remembered: %d, want one per member", n)
	}

	card := lastCall(t, f.api.Sent(olena.ID))
	text := card.Text()
	for _, want := range []string{
		"🆕 <b>Заявка #K-0001</b> · Сети и оборудование", "👤 Иван &lt;b&gt;Петров&lt;/b&gt;", "✉️ ivan@company.com", "💰 $1–3k   ⏱ 2 weeks",
		"… <i>(полностью — по кнопке)</i>", "🌍 реклама · utm_campaign=mikrotik · UA · телефон Safari · iOS",
		"👀 смотрел: skills → projects → contacts (3m0s)", "Статус: 🟡 Новая · 19.09 12:00",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the card lacks %q:\n%s", want, text)
		}
	}
	if len([]rune(text)) > 1200 {
		t.Errorf("the card is %d characters long: the task was not shortened", len([]rune(text)))
	}
	labels, data := card.Buttons()
	if fmt.Sprint(labels) != "[✅ Взять в работу ❌ Отклонить 💬 Ответить 📝 Заметка 📄 Полностью 🕘 История 🔗 В админке]" ||
		data["✅ Взять в работу"] != "l:take:1" || data["🔗 В админке"] != "https://krokosha.xyz/_secret1/leads/1" {
		t.Errorf("buttons: %v %v", labels, data)
	}
	// /mute: the card comes, the sound does not.
	if card.Params["disable_notification"] != true || lastCall(t, f.api.Sent(denis.ID)).Params["disable_notification"] == true {
		t.Error("muting is per person")
	}

	// Somebody blocked the bot: that does not hold up the others, nor fail the task.
	second := f.addLead(store, nil)
	f.api.Refuse("sendMessage", 1, http.StatusForbidden, "Forbidden: bot was blocked by the user", 0)
	f.announce(second.ID)
	// A request deleted before its card went out is nothing to retry.
	if err := store.Delete(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.bot.Send(ctx, notifyTask(second.ID)); !outbox.IsPermanent(err) {
		t.Errorf("a card for a deleted request: %v", err)
	}
	if err := f.bot.Send(ctx, outbox.Task{Kind: "lead.unknown", Payload: []byte(`{"lead_id":1}`)}); !outbox.IsPermanent(err) {
		t.Errorf("an unknown task: %v", err)
	}
}

func (f *fixture) count(query string) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(query).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// TestTheFirstPressTakesTheRequest: brief B10.4 — a request cannot be taken twice, and everybody
// sees who has it, wherever it was taken.
func TestTheFirstPressTakesTheRequest(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	f.join(denis, RoleOwner)
	f.join(olena, RoleMember)
	lead := f.addLead(store, nil)
	f.announce(lead.ID)

	f.now = f.now.Add(3 * time.Minute)
	if text, alert := toast(t, f.presses(denis, "l:take:1")); text != "Заявка #K-0001 ваша." || alert {
		t.Errorf("the first press: %q %v", text, alert)
	}
	f.settle()
	for _, chat := range []int64{denis.ID, olena.ID} {
		card := lastCall(t, f.edits(chat))
		labels, _ := card.Buttons()
		if !strings.Contains(card.Text(), "Статус: 🟢 В работе · взял(а) Денис Гумен · 19.09 12:03") || strings.Contains(fmt.Sprint(labels), "Взять") ||
			!strings.Contains(fmt.Sprint(labels), "✔️ Завершить") {
			t.Errorf("the card in chat %d after the request was taken:\n%s\n%v", chat, card.Text(), labels)
		}
	}
	// The colleague still had the old card on screen and pressed a moment later.
	if text, alert := toast(t, f.presses(olena, "l:take:1")); text != "Заявку #K-0001 уже взял(а) Денис Гумен." || !alert {
		t.Errorf("the second press: %q %v", text, alert)
	}

	// Taken in the admin area — the bot's cards follow, and the bot knows who was first.
	second := f.addLead(store, nil)
	f.announce(second.ID)
	if _, err := store.Take(context.Background(), second.ID, "denis-admin"); err != nil {
		t.Fatal(err)
	}
	f.api.Forget()
	f.settle()
	if card := lastCall(t, f.edits(olena.ID)); !strings.Contains(card.Text(), "#K-0002") || !strings.Contains(card.Text(), "взял(а) denis-admin") {
		t.Errorf("a card after «take» in the admin area:\n%s", card.Text())
	}
	if text, _ := toast(t, f.presses(olena, "l:take:2")); text != "Заявку #K-0002 уже взял(а) denis-admin." {
		t.Errorf("a press after «take» in the admin area: %q", text)
	}

	// Done, and back again: a mistake is never final.
	if text, _ := toast(t, f.presses(denis, "l:done:1")); text != "#K-0001: завершена" {
		t.Errorf("done: %q", text)
	}
	f.settle()
	labels, _ := lastCall(t, f.edits(denis.ID)).Buttons()
	if fmt.Sprint(labels) != "[↩️ Вернуть в работу 📝 Заметка 📄 Полностью 🕘 История 🔗 В админке]" {
		t.Errorf("buttons of a finished request: %v", labels)
	}
	if text, alert := toast(t, f.presses(olena, "l:done:1")); alert || text != "#K-0001: завершена" { // the same status again is not an error
		t.Errorf("done twice: %q %v", text, alert)
	}
	if text, _ := toast(t, f.presses(denis, "l:reopen:1")); text != "#K-0001: в работе" {
		t.Errorf("reopen: %q", text)
	}
	// A request that is gone, a number that never was, a button from another life.
	for data, want := range map[string]string{"l:take:999": "Заявки больше нет", "l:take:abc": "больше не работает", "l:fly:1": "больше не работает"} {
		if text, _ := toast(t, f.presses(denis, data)); !strings.Contains(text, want) {
			t.Errorf("%s: %q", data, text)
		}
	}
}

func TestAnsweringAClientFromTheBot(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	ctx := context.Background()
	f.join(denis, RoleOwner)
	lead := f.addLead(store, nil)
	f.announce(lead.ID)

	// «Ответить» offers the ready-made answers in the client's language.
	calls := f.presses(denis, "l:reply:1")
	offer := lastCall(t, f.api.Sent(denis.ID))
	labels, data := offer.Buttons()
	if !strings.Contains(offer.Text(), "уйдёт письмом на ivan@company.com") || !strings.Contains(fmt.Sprint(labels), "Нужны детали") ||
		strings.Contains(fmt.Sprint(labels), "Need details") || data["✍️ Свой текст"] != "l:own:1" {
		t.Fatalf("the offer: %q %v (calls: %d)", offer.Text(), labels, len(calls))
	}

	// The answers that fit the conversation come first, marked; no more than three of them.
	stars, details := 0, ""
	for i, label := range labels {
		if strings.HasPrefix(label, "★ ") {
			stars++
			if i >= 3 {
				t.Errorf("a marked answer below the others: %v", labels)
			}
		}
		if strings.TrimPrefix(label, "★ ") == "Нужны детали" {
			details = data[label]
		}
	}
	if stars == 0 || stars > 3 || details == "" {
		t.Fatalf("the answers offered: %v", labels)
	}

	// A template → a preview of what the client gets → send.
	f.presses(denis, details)
	preview := lastCall(t, f.api.Sent(denis.ID))
	labels, data = preview.Buttons()
	if !strings.Contains(preview.Text(), "Предпросмотр ответа по <b>#K-0001</b>") || !strings.Contains(preview.Text(), "Здравствуйте, Иван &lt;b&gt;Петров&lt;/b&gt;!") ||
		fmt.Sprint(labels) != "[✅ Отправить ✏️ Изменить Отмена]" {
		t.Fatalf("the preview: %q %v", preview.Text(), labels)
	}
	// «Изменить»: the bot waits for the text, shows it again, and only then sends.
	f.presses(denis, data["✏️ Изменить"])
	if ask := lastCall(t, f.api.Sent(denis.ID)); ask.Params["reply_markup"].(map[string]any)["force_reply"] != true {
		t.Fatalf("the reply field was not opened: %+v", ask.Params)
	}
	own := "Иван, добрый день! Сколько коммутаторов уже есть и какой бюджет на <железо>?"
	preview = lastCall(t, f.says(denis, own))
	if !strings.Contains(preview.Text(), "на &lt;железо&gt;?") {
		t.Fatalf("the preview of an own text: %q", preview.Text())
	}
	if text, alert := toast(t, f.presses(denis, "l:send:1")); alert || !strings.Contains(text, "в очередь") {
		t.Errorf("send: %q %v", text, alert)
	}
	f.settle()

	card, err := store.Card(ctx, lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	var last leads.Entry
	for _, entry := range card.Feed {
		if entry.Direction == "out" {
			last = entry
		}
	}
	if card.Lead.Status != leads.StatusWaitingClient || last.Body != own || last.Author != "Денис Гумен" || last.Delivery != "queued" {
		t.Errorf("the stored answer: %s %+v", card.Lead.Status, last)
	}
	if n := f.count(`SELECT COUNT(*) FROM outbox WHERE kind = 'lead.reply' AND channel = 'email' AND lead_id = 1`); n != 1 || f.kicks == 0 {
		t.Errorf("answers queued: %d, the outbox was hurried %d times", n, f.kicks)
	}
	if redrawn := lastCall(t, f.edits(denis.ID)); !strings.Contains(redrawn.Text(), "🔵 Ждём клиента") {
		t.Errorf("the card after the answer:\n%s", redrawn.Text())
	}
	// The same draft cannot be sent twice.
	if text, alert := toast(t, f.presses(denis, "l:send:1")); !alert || !strings.Contains(text, "Черновик не найден") {
		t.Errorf("sending twice: %q %v", text, alert)
	}
	if fmt.Sprint(f.audit) != "[bot:Денис Гумен lead.reply K-0001]" {
		t.Errorf("the journal: %q", f.audit)
	}

	// A client who left a phone number: nothing is sent, the «answer» is the record of the call.
	phone := f.addLead(store, func(sub *leads.Submission) { sub.ContactMethod, sub.ContactValue = leads.MethodPhone, "+380671234567" })
	f.announce(phone.ID)
	labels, _ = lastCall(t, f.api.Sent(denis.ID)).Buttons()
	if !strings.Contains(fmt.Sprint(labels), "📞 Итог звонка") {
		t.Errorf("buttons for a phone client: %v", labels)
	}
	f.presses(denis, "l:reply:2")
	if ask := lastCall(t, f.api.Sent(denis.ID)); !strings.Contains(ask.Text(), "+380671234567") || ask.Params["reply_markup"].(map[string]any)["input_field_placeholder"] != "Позвонил — итог: …" {
		t.Errorf("the prompt for a call: %+v", ask.Params)
	}
	preview = lastCall(t, f.says(denis, "Позвонил — итог: встречаемся в четверг."))
	if labels, _ = preview.Buttons(); !strings.Contains(preview.Text(), "клиенту ничего не отправляется") || labels[0] != "✅ Записать" {
		t.Errorf("the preview of a call: %q %v", preview.Text(), labels)
	}
	f.presses(denis, "l:send:2")
	if n := f.count(`SELECT COUNT(*) FROM outbox WHERE kind = 'lead.reply' AND lead_id = 2`); n != 0 {
		t.Error("a phone call was queued for delivery")
	}

	// /cancel, and a text nobody asked for.
	f.presses(denis, "l:own:1")
	if text := oneText(t, f.says(denis, "/cancel")); text != "Отменено." {
		t.Errorf("/cancel: %q", text)
	}
	if text := oneText(t, f.says(denis, "просто текст")); !strings.Contains(text, "нажмите кнопку на карточке") {
		t.Errorf("a text out of the blue: %q", text)
	}
	// A question forgotten for hours is not answered by whatever is typed next.
	f.presses(denis, "l:note:1")
	f.now = f.now.Add(dialogLifetime + time.Minute)
	if text := oneText(t, f.says(denis, "это не заметка")); !strings.Contains(text, "Не понял") {
		t.Errorf("a text after a forgotten question: %q", text)
	}
}

func TestNotesHistoryAndTheFullText(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	f.join(denis, RoleOwner)
	long := strings.Repeat("Строка описания задачи, довольно длинная, чтобы набрать объём.\n", 90) // ~5700 characters
	lead := f.addLead(store, func(sub *leads.Submission) { sub.Description = long })
	f.announce(lead.ID)

	f.presses(denis, "l:note:1")
	if text := oneText(t, f.says(denis, "Клиент от Петра, <важный>")); text != "📝 Заметка к #K-0001 сохранена." {
		t.Errorf("a note: %q", text)
	}
	if _, err := store.Take(context.Background(), lead.ID, "denis-admin"); err != nil {
		t.Fatal(err)
	}

	f.presses(denis, "l:history:1")
	history := lastCall(t, f.api.Sent(denis.ID)).Text()
	for _, want := range []string{"🕘 <b>История #K-0001</b>", "заявка получена", "📝 Денис Гумен: Клиент от Петра, &lt;важный&gt;", "denis-admin взял(а) в работу"} {
		if !strings.Contains(history, want) {
			t.Errorf("the history lacks %q:\n%s", want, history)
		}
	}

	// The full text does not fit into one message: it comes in pieces, nothing lost.
	f.presses(denis, "l:full:1")
	var whole strings.Builder
	parts := f.api.Sent(denis.ID)
	for _, part := range parts {
		if len([]rune(part.Text())) > 4096 {
			t.Errorf("a piece of %d characters", len([]rune(part.Text())))
		}
		whole.WriteString(part.Text())
	}
	if len(parts) < 2 || strings.Count(whole.String(), "Строка описания задачи") != 90 {
		t.Errorf("the full text: %d pieces, %d lines of 90", len(parts), strings.Count(whole.String(), "Строка описания задачи"))
	}
}

func TestRejectingARequest(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	ctx := context.Background()
	f.join(denis, RoleOwner)

	// A reason, then a polite letter from a template, looked at before it goes.
	first := f.addLead(store, nil)
	f.announce(first.ID)
	f.presses(denis, "l:reject:1")
	labels, data := lastCall(t, f.api.Sent(denis.ID)).Buttons()
	if fmt.Sprint(labels) != "[не мой профиль нет свободного времени бюджет не подходит 🚫 это спам ✍️ Своя причина Отмена]" {
		t.Fatalf("reasons: %v", labels)
	}
	f.presses(denis, data["бюджет не подходит"])
	labels, data = lastCall(t, f.api.Sent(denis.ID)).Buttons()
	if !strings.Contains(fmt.Sprint(labels), "✉️ Бюджет не подходит") || !strings.Contains(fmt.Sprint(labels), "Закрыть молча") || strings.Contains(fmt.Sprint(labels), "budget") {
		t.Fatalf("the letters offered: %v", labels)
	}
	f.presses(denis, data["✉️ Бюджет не подходит"])
	preview := lastCall(t, f.api.Sent(denis.ID))
	labels, _ = preview.Buttons()
	if !strings.Contains(preview.Text(), "причина: бюджет не подходит") || !strings.Contains(preview.Text(), "Здравствуйте, Иван") || labels[0] != "✅ Отправить и отклонить" {
		t.Fatalf("the preview of the refusal: %q %v", preview.Text(), labels)
	}
	if text, _ := toast(t, f.presses(denis, "l:send:1")); text != "Отклонена." {
		t.Errorf("send and reject: %q", text)
	}
	f.settle()
	card, _ := store.Card(ctx, first.ID)
	if card.Lead.Status != leads.StatusRejected || card.RejectReason != "бюджет не подходит" ||
		f.count(`SELECT COUNT(*) FROM outbox WHERE kind = 'lead.reply' AND lead_id = 1`) != 1 {
		t.Errorf("after the refusal with a letter: %s, %q", card.Lead.Status, card.RejectReason)
	}
	if redrawn := lastCall(t, f.edits(denis.ID)); !strings.Contains(redrawn.Text(), "❌ Отклонена") || !strings.Contains(redrawn.Text(), "Причина: бюджет не подходит") {
		t.Errorf("the card of a rejected request:\n%s", redrawn.Text())
	}

	// An own reason, no letter at all.
	second := f.addLead(store, nil)
	f.announce(second.ID)
	f.presses(denis, "l:reject:2")
	f.presses(denis, "l:why:2:own")
	f.says(denis, "клиент передумал сам")
	if text, _ := toast(t, f.presses(denis, "l:silent:2")); text != "Отклонена." {
		t.Errorf("rejecting silently: %q", text)
	}
	card, _ = store.Card(ctx, second.ID)
	if card.Lead.Status != leads.StatusRejected || card.RejectReason != "клиент передумал сам" ||
		f.count(`SELECT COUNT(*) FROM outbox WHERE kind = 'lead.reply' AND lead_id = 2`) != 0 {
		t.Errorf("after the silent refusal: %s, %q", card.Lead.Status, card.RejectReason)
	}

	// Spam goes to spam — a new request only; one in progress has no such button.
	third := f.addLead(store, nil)
	f.announce(third.ID)
	f.presses(denis, "l:reject:3")
	if text, _ := toast(t, f.presses(denis, "l:why:3:spam")); text != "В спам." {
		t.Errorf("spam: %q", text)
	}
	if card, _ = store.Card(ctx, third.ID); card.Lead.Status != leads.StatusSpam {
		t.Errorf("status: %s", card.Lead.Status)
	}
	fourth := f.addLead(store, nil)
	f.announce(fourth.ID)
	f.presses(denis, "l:take:4")
	f.presses(denis, "l:reject:4")
	if labels, _ = lastCall(t, f.api.Sent(denis.ID)).Buttons(); strings.Contains(fmt.Sprint(labels), "спам") {
		t.Errorf("reasons for a request in progress: %v", labels)
	}
	// Somebody finished it meanwhile in the admin area: the refusal does not go through.
	_ = store.SetStatus(ctx, fourth.ID, "denis-admin", leads.StatusDone, "")
	f.presses(denis, "l:why:4:0")
	if text, alert := toast(t, f.presses(denis, "l:silent:4")); !alert || !strings.Contains(text, "успели изменить") {
		t.Errorf("rejecting a finished request: %q %v", text, alert)
	}
	// A refusal out of nowhere (no reason chosen) does nothing.
	fifth := f.addLead(store, nil)
	if text, alert := toast(t, f.presses(denis, fmt.Sprint("l:silent:", fifth.ID))); !alert || !strings.Contains(text, "Причина не выбрана") {
		t.Errorf("a refusal without a reason: %q %v", text, alert)
	}
	// By phone there is no letter to offer.
	sixth := f.addLead(store, func(sub *leads.Submission) { sub.ContactMethod, sub.ContactValue = leads.MethodPhone, "+380671234567" })
	f.presses(denis, fmt.Sprint("l:why:", sixth.ID, ":1"))
	if labels, _ = lastCall(t, f.api.Sent(denis.ID)).Buttons(); fmt.Sprint(labels) != "[Закрыть молча Отмена]" {
		t.Errorf("letters offered to a phone client: %v", labels)
	}
}

// What is erased on the server must not live on in Telegram chats.
func TestErasedRequestsAreWipedFromTheChats(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	ctx := context.Background()
	f.join(denis, RoleOwner)
	f.join(olena, RoleMember)
	lead := f.addLead(store, nil)
	kept := f.addLead(store, nil)
	f.announce(lead.ID)
	f.announce(kept.ID)
	f.presses(denis, "l:full:1")
	f.presses(olena, "l:history:1")
	f.presses(denis, "l:reply:1")
	if n := f.count(`SELECT COUNT(*) FROM bot_messages WHERE lead_id = 1`); n != 5 {
		t.Fatalf("messages remembered about the request: %d, want 2 cards and 3 texts", n)
	}

	// Telegram is unwell when the client's data is deleted: the deletion does not wait for it…
	f.api.Forget()
	f.api.Refuse("editMessageText", -1, http.StatusBadGateway, "Bad Gateway", 0)
	if err := store.Delete(ctx, lead.ID); err != nil {
		t.Fatal(err)
	}
	f.settle()
	if n := f.count(`SELECT COUNT(*) FROM bot_messages WHERE lead_id = 1 AND wipe_after > '2026-09-19 12:00:00'`); n != 5 {
		t.Fatalf("messages waiting to be wiped: %d, want 5", n)
	}
	// …and the wiping is done when Telegram is back.
	f.api.Accept("editMessageText")
	f.api.Refuse("editMessageText", 1, http.StatusBadRequest, "Bad Request: message to edit not found", 0) // one was deleted by the person: fine
	f.api.Forget()
	f.now = f.now.Add(11 * time.Minute)
	f.bot.wipeMessages(ctx)
	wiped := f.api.Calls("editMessageText")
	if len(wiped) != 5 || f.count(`SELECT COUNT(*) FROM bot_messages WHERE lead_id = 1`) != 0 {
		t.Fatalf("wiped: %d messages, left in the table: %d", len(wiped), f.count(`SELECT COUNT(*) FROM bot_messages WHERE lead_id = 1`))
	}
	for _, call := range wiped {
		if labels, _ := call.Buttons(); call.Text() != "🗑 Заявка #K-0001: данные клиента удалены." || len(labels) != 0 {
			t.Errorf("a wiped message: %q %v", call.Text(), labels)
		}
	}
	// The other request is untouched; the old card's buttons say what happened.
	if n := f.count(`SELECT COUNT(*) FROM bot_messages WHERE lead_id = 2 AND wipe_after IS NULL`); n != 2 {
		t.Errorf("cards of the other request: %d", n)
	}
	if text, alert := toast(t, f.presses(denis, "l:take:1")); !alert || !strings.Contains(text, "данные клиента удалены") {
		t.Errorf("a button of an erased request: %q %v", text, alert)
	}

	// The end of the storage period does the same, and the card says so.
	f.api.Forget()
	if err := store.Anonymize(ctx, kept.ID); err != nil {
		t.Fatal(err)
	}
	f.settle()
	if wiped = f.api.Calls("editMessageText"); len(wiped) != 2 || !strings.Contains(wiped[0].Text(), "#K-0002: данные клиента удалены") {
		t.Errorf("after anonymisation: %+v", wiped)
	}
}

// TestOnlyNotifications: a person with the role «notify» gets the cards and can read them, but
// nothing can be done from their chat — no taking, no answers, no swipes.
func TestOnlyNotifications(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	ctx := context.Background()
	f.join(denis, RoleOwner)
	watcher := User{ID: 1004, FirstName: "Рома", Username: "roma_ops"}
	f.join(watcher, RoleNotify)
	lead := f.addLead(store, nil)
	f.announce(lead.ID)

	card := lastCall(t, f.api.Sent(watcher.ID))
	if labels, data := card.Buttons(); fmt.Sprint(labels) != "[📄 Полностью 🕘 История]" || data["📄 Полностью"] != "l:full:1" {
		t.Errorf("the buttons of a card that can only be read: %v", labels)
	}
	if labels, _ := lastCall(t, f.api.Sent(denis.ID)).Buttons(); len(labels) != 7 {
		t.Errorf("the owner's card lost its buttons: %v", labels)
	}
	// A button of somebody else's card, pressed anyway: refused, and the request stays new.
	if text, alert := toast(t, f.presses(watcher, "l:take:1")); !alert || !strings.Contains(text, "только к уведомлениям") {
		t.Errorf("taking from a notify-only chat: %q %v", text, alert)
	}
	if got, _ := store.Get(ctx, lead.ID); got.Status != leads.StatusNew {
		t.Errorf("the request was changed from a notify-only chat: %s", got.Status)
	}
	// Reading is allowed.
	if text, alert := toast(t, f.presses(watcher, "l:full:1")); alert || text != "" {
		t.Errorf("reading the full text: %q %v", text, alert)
	}
	// A swipe reply to the card answers nobody.
	var cardID int64
	if err := f.db.QueryRow(`SELECT message_id FROM bot_messages WHERE lead_id = ? AND chat_id = ? AND kind = 'card'`, lead.ID, watcher.ID).Scan(&cardID); err != nil {
		t.Fatal(err)
	}
	if text := oneText(t, f.replies(watcher, cardID, "Здравствуйте!")); !strings.Contains(text, "только к уведомлениям") {
		t.Errorf("a swipe from a notify-only chat: %q", text)
	}
	if n := f.count(`SELECT COUNT(*) FROM lead_messages WHERE direction = 'out'`); n != 0 {
		t.Errorf("answers written from a notify-only chat: %d", n)
	}
	if help := oneText(t, f.says(watcher, "/help")); strings.Contains(help, "/invite") || !strings.Contains(help, "только к уведомлениям") {
		t.Errorf("the help of a notify-only person: %q", help)
	}

	// The role changes; the last owner stays one; a colleague is let in by id; the admin sees who was here.
	member, _ := f.access.Member(ctx, watcher.ID)
	if !member.LastSeenAt.Valid || member.Username != "roma_ops" {
		t.Errorf("last seen: %+v", member)
	}
	if changed, err := f.access.SetRole(ctx, member.ID, RoleMember); err != nil || changed.Role != RoleMember {
		t.Errorf("a new role: %+v %v", changed, err)
	}
	owner, _ := f.access.Member(ctx, denis.ID)
	if _, err := f.access.SetRole(ctx, owner.ID, RoleNotify); !errors.Is(err, ErrLastOwner) {
		t.Errorf("the only owner made a watcher: %v", err)
	}
	added, err := f.access.Add(ctx, 1005, RoleNotify, "  Олег  ", "denis")
	if err != nil || added.Name != "Олег" || added.Role != RoleNotify || added.InvitedBy != "denis" {
		t.Errorf("added by id: %+v %v", added, err)
	}
	if _, err := f.access.Add(ctx, -5, RoleMember, "", "denis"); !errors.Is(err, ErrBadID) {
		t.Errorf("a group's id: %v", err)
	}
	if _, err := f.access.Add(ctx, 1006, "root", "", "denis"); !errors.Is(err, ErrBadRole) {
		t.Errorf("a made-up role: %v", err)
	}
}
