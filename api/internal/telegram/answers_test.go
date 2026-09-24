package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram/tgtest"
)

// sample makes a file of a kind the store accepts: the signature, then filler that differs by seed.
func sample(kind string, seed byte) []byte {
	head := map[string][]byte{
		"jpg": {0xFF, 0xD8, 0xFF, 0xE0},
		"png": {0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A},
		"mp4": {0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'},
		"pdf": []byte("%PDF-1.7\n"),
	}[kind]
	return append(append([]byte(nil), head...), bytes.Repeat([]byte{seed}, 3000)...)
}

// withFiles gives the store places for the files of requests and of templates.
func (f *fixture) withFiles(store *leads.Store) *leads.Files {
	f.t.Helper()
	requests := leads.NewFiles(filepath.Join(f.t.TempDir(), "attachments"))
	store.UseFiles(requests)
	store.UseMedia(leads.NewFiles(filepath.Join(f.t.TempDir(), "templates")))
	return requests
}

func (f *fixture) file(dir *leads.Files, name string, content []byte) leads.Upload {
	f.t.Helper()
	upload, err := leads.SaveOutgoing(dir, name, int64(len(content)), bytes.NewReader(content))
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	return upload
}

// answer stores an answer with files and delivers it to the client once.
func (f *fixture) answer(store *leads.Store, leadID int64, text string, files ...leads.Upload) (int64, error) {
	f.t.Helper()
	id, err := store.ReplyWith(context.Background(), leadID, "Денис Гумен", leads.Answer{Text: text, Files: files})
	if err != nil {
		f.t.Fatal(err)
	}
	f.api.Forget()
	return id, f.bot.Send(context.Background(), task(leads.TaskReply, leadID, id))
}

func mediaTypes(call tgtest.Call) []string {
	var out []string
	items, _ := call.Params["media"].([]any)
	for _, item := range items {
		media, _ := item.(map[string]any)
		kind, _ := media["type"].(string)
		out = append(out, kind)
	}
	return out
}

func TestAnswersWithFilesReachTelegram(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	requests := f.withFiles(store)
	ctx := context.Background()
	f.join(denis, RoleOwner)
	lead := f.addLead(store, func(sub *leads.Submission) { sub.ContactMethod, sub.ContactValue = leads.MethodTelegram, "@ivan_p" })
	f.announce(lead.ID)
	f.says(client, "/start "+ClientPrefix+lead.PublicToken)
	sent := func(id int64) string {
		var delivery string
		var parts int
		if err := f.db.QueryRowContext(ctx, `SELECT COALESCE(delivery, ''), sent_parts FROM lead_messages WHERE id = ?`, id).Scan(&delivery, &parts); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s %d", delivery, parts)
	}

	// The text, then the photos and the video as one album, then the document.
	rack := sample("jpg", 1)
	answerID, err := f.answer(store, lead.ID, "Схема и фото стойки — ниже.",
		f.file(requests, "rack.jpg", rack), f.file(requests, "scheme.png", sample("png", 2)),
		f.file(requests, "tour.mp4", sample("mp4", 3)), f.file(requests, "Смета.pdf", sample("pdf", 4)))
	if err != nil {
		t.Fatal(err)
	}
	calls := f.api.Calls("")
	if len(calls) != 3 || calls[0].Method != "sendMessage" || calls[1].Method != "sendMediaGroup" || calls[2].Method != "sendDocument" {
		t.Fatalf("the parts of the answer: %+v", calls)
	}
	if !strings.HasPrefix(calls[0].Text(), "💬 Ответ по заявке #K-0001:\n\nСхема и фото стойки") {
		t.Errorf("the text: %q", calls[0].Text())
	}
	if got := mediaTypes(calls[1]); strings.Join(got, " ") != "photo photo video" || len(calls[1].Files) != 3 || calls[1].ChatID() != client.ID {
		t.Errorf("the album: %v, %d files uploaded", got, len(calls[1].Files))
	}
	if len(calls[2].Files) != 1 || calls[2].Files[0].Name != "Смета.pdf" || calls[2].Files[0].Size != len(sample("pdf", 4)) {
		t.Errorf("the document: %+v", calls[2].Files)
	}
	if got := sent(answerID); got != "sent 3" {
		t.Errorf("the answer: %s", got)
	}

	// The same photo in another answer goes by the id Telegram gave it: nothing is uploaded.
	if _, err := f.answer(store, lead.ID, "", f.file(requests, "rack-again.jpg", rack)); err != nil {
		t.Fatal(err)
	}
	calls = f.api.Calls("")
	if len(calls) != 2 || calls[0].Text() != "💬 Ответ по заявке #K-0001:" || calls[1].Method != "sendPhoto" || len(calls[1].Files) != 0 ||
		!strings.HasPrefix(fmt.Sprint(calls[1].Params["photo"]), "file-") {
		t.Errorf("a known photo: %+v", calls)
	}

	// Telegram fails in the middle: the retry sends only what did not go.
	f.api.Refuse("sendMediaGroup", 1, http.StatusBadGateway, "Bad Gateway", 0)
	resumed, err := f.answer(store, lead.ID, "Ещё два фото.", f.file(requests, "a.jpg", sample("jpg", 5)), f.file(requests, "b.jpg", sample("jpg", 6)))
	if err == nil || outbox.IsPermanent(err) || sent(resumed) != "queued 1" {
		t.Fatalf("a failure after the text: %v, %s", err, sent(resumed))
	}
	f.api.Forget()
	if err := f.bot.Send(ctx, task(leads.TaskReply, lead.ID, resumed)); err != nil {
		t.Fatal(err)
	}
	if calls = f.api.Calls(""); len(calls) != 1 || calls[0].Method != "sendMediaGroup" || sent(resumed) != "sent 2" {
		t.Errorf("the retry: %+v, %s", calls, sent(resumed))
	}

	// A photo Telegram cannot show goes as a document.
	f.api.Refuse("sendMediaGroup", 1, http.StatusBadRequest, "Bad Request: PHOTO_INVALID_DIMENSIONS", 0)
	if _, err := f.answer(store, lead.ID, "Панорама.", f.file(requests, "wide.png", sample("png", 7)), f.file(requests, "tall.png", sample("png", 8))); err != nil {
		t.Fatal(err)
	}
	if albums := f.api.Calls("sendMediaGroup"); len(albums) != 2 || strings.Join(mediaTypes(albums[1]), " ") != "document document" || len(albums[1].Files) != 2 {
		t.Errorf("photos refused as photos: %+v", albums)
	}

	// Telegram no longer knows an id it gave: the file is uploaded again, and the new id kept.
	f.api.Refuse("sendPhoto", 1, http.StatusBadRequest, "Bad Request: wrong file identifier/HTTP URL specified", 0)
	if _, err := f.answer(store, lead.ID, "", f.file(requests, "rack-third.jpg", rack)); err != nil {
		t.Fatal(err)
	}
	if photos := f.api.Calls("sendPhoto"); len(photos) != 2 || len(photos[0].Files) != 0 || len(photos[1].Files) != 1 {
		t.Errorf("a forgotten id: %+v", photos)
	}
	if n := f.count(`SELECT COUNT(*) FROM telegram_uploads WHERE kind = 'photo'`); n != 4 {
		t.Errorf("known photos: %d", n)
	}

	// A file gone from the disk: no retry brings it back.
	lost := f.file(requests, "lost.pdf", sample("pdf", 9))
	lostID, err := store.ReplyWith(ctx, lead.ID, "Денис Гумен", leads.Answer{Text: "Договор во вложении.", Files: []leads.Upload{lost}})
	if err != nil {
		t.Fatal(err)
	}
	if err := requests.Remove(lost.StoredAs); err != nil {
		t.Fatal(err)
	}
	if err := f.bot.Send(ctx, task(leads.TaskReply, lead.ID, lostID)); !outbox.IsPermanent(err) || sent(lostID) != "failed 1" {
		t.Errorf("a lost file: %v, %s", err, sent(lostID))
	}
}

func TestAnAnswerWithBigFilesGetsTime(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	requests := f.withFiles(store)
	lead := f.addLead(store, func(sub *leads.Submission) { sub.ContactMethod, sub.ContactValue = leads.MethodTelegram, "@ivan_p" })
	video := append(sample("mp4", 1), bytes.Repeat([]byte{1}, 3<<20)...)
	withVideo, err := store.ReplyWith(context.Background(), lead.ID, "Денис Гумен", leads.Answer{Text: "Видео.", Files: []leads.Upload{f.file(requests, "tour.mp4", video)}})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := store.Reply(context.Background(), lead.ID, "Денис Гумен", "Просто текст.")
	if err != nil {
		t.Fatal(err)
	}
	notice, _ := json.Marshal(leads.TaskPayload{LeadID: lead.ID})
	for name, tc := range map[string]struct {
		task outbox.Task
		want time.Duration
	}{
		"three megabytes of video": {task(leads.TaskReply, lead.ID, withVideo), 36 * time.Second},
		"a text":                   {task(leads.TaskReply, lead.ID, plain), 30 * time.Second},
		"not an answer":            {outbox.Task{Kind: leads.TaskNotify, Payload: notice}, 0},
	} {
		if got := f.bot.Timeout(context.Background(), tc.task); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func TestAnAnswerFromTheBotTakesTheFilesOfItsTemplate(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	f.withFiles(store)
	ctx := context.Background()
	f.join(denis, RoleOwner)
	lead := f.addLead(store, func(sub *leads.Submission) { sub.ContactMethod, sub.ContactValue = leads.MethodTelegram, "@ivan_p" })
	f.announce(lead.ID)
	f.says(client, "/start "+ClientPrefix+lead.PublicToken)

	id, err := store.SaveTemplate(ctx, leads.Template{Kind: "reply", Lang: "ru", Title: "Похожая сеть", Category: "portfolio", Moment: leads.MomentAny,
		Keywords: []string{"mikrotik"}, Body: "{name}, вот стойка похожего офиса и смета."})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"стойка.jpg": sample("jpg", 1), "смета.pdf": sample("pdf", 2)} {
		if _, err := store.AddTemplateMedia(ctx, id, name, int64(len(content)), bytes.NewReader(content)); err != nil {
			t.Fatal(err)
		}
	}

	// The template fits the request (MikroTik): it is among the first, with its files counted.
	f.presses(denis, "l:reply:1")
	labels, data := lastCall(t, f.api.Sent(denis.ID)).Buttons()
	button := data["★ Похожая сеть · 📎2"]
	if button == "" {
		t.Fatalf("the template with files is not offered first: %v", labels)
	}
	f.presses(denis, button)
	preview := lastCall(t, f.api.Sent(denis.ID)).Text()
	if !strings.Contains(preview, "📎 С ответом уйдут файлы шаблона: ") || !strings.Contains(preview, "стойка.jpg") || !strings.Contains(preview, "смета.pdf") {
		t.Fatalf("the preview does not name the files: %q", preview)
	}
	f.presses(denis, "l:send:1")
	var answerID int64
	var templates string
	if err := f.db.QueryRowContext(ctx, `SELECT id, COALESCE(templates, '') FROM lead_messages WHERE lead_id = ? AND direction = 'out' ORDER BY id DESC LIMIT 1`, lead.ID).
		Scan(&answerID, &templates); err != nil {
		t.Fatal(err)
	}
	if templates != fmt.Sprint(id) || f.count(fmt.Sprintf(`SELECT COUNT(*) FROM lead_attachments WHERE message_id = %d`, answerID)) != 2 {
		t.Fatalf("the answer from the bot: templates %q, files %d", templates, f.count(fmt.Sprintf(`SELECT COUNT(*) FROM lead_attachments WHERE message_id = %d`, answerID)))
	}

	// The client gets the text, the photo and the document.
	f.api.Forget()
	if err := f.bot.Send(ctx, task(leads.TaskReply, lead.ID, answerID)); err != nil {
		t.Fatal(err)
	}
	var methods []string
	for _, call := range f.api.Calls("") {
		if call.ChatID() == client.ID {
			methods = append(methods, call.Method)
		}
	}
	if strings.Join(methods, " ") != "sendMessage sendPhoto sendDocument" {
		t.Errorf("what the client got: %v", methods)
	}
}
