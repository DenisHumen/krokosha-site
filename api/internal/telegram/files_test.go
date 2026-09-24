package telegram

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram/tgtest"
)

// sends hands the bot a private message with whatever it carries and returns what the bot answered
// in that chat.
func (f *fixture) sends(who User, message Message) []tgtest.Call {
	f.t.Helper()
	f.api.Forget()
	f.update++
	message.MessageID, message.From, message.Chat, message.Date = f.update, &who, Chat{ID: who.ID, Type: "private"}, f.now.Unix()
	f.bot.Handle(context.Background(), Update{UpdateID: f.update, Message: &message})
	return f.api.Sent(who.ID)
}

var (
	testJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, bytes.Repeat([]byte{7}, 2000)...)
	testPDF  = []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")
)

// A client's photos and documents join the conversation of the request like the files of a letter;
// an album is one message; what the bot does not take is explained, and nothing is stored of it.
func TestAClientSendsFiles(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	files := leads.NewFiles(t.TempDir())
	store.UseFiles(files)
	f.join(denis, RoleOwner)
	lead := f.addLead(store, nil)
	f.says(client, "/start "+ClientPrefix+lead.PublicToken)
	ctx := context.Background()
	stored := func() (messages, attachments int) {
		t.Helper()
		messages = f.count(fmt.Sprintf(`SELECT COUNT(*) FROM lead_messages WHERE lead_id = %d AND direction = 'in' AND channel = 'telegram'`, lead.ID))
		attachments = f.count(fmt.Sprintf(`SELECT COUNT(*) FROM lead_attachments WHERE lead_id = %d`, lead.ID))
		return
	}

	// A photo with a caption: the biggest size is taken, the caption is the text.
	photo := f.api.Store(testJPEG)
	kicks := f.kicks
	if text := oneText(t, f.sends(client, Message{Photo: []FileRef{{FileID: "thumbnail"}, {FileID: photo, FileSize: int64(len(testJPEG))}}, Caption: "Схема стойки"})); text != "Передано." {
		t.Errorf("a photo: %q", text)
	}
	if messages, attachments := stored(); messages != 1 || attachments != 1 || f.kicks != kicks+1 {
		t.Errorf("after a photo: %d messages, %d files, %d kicks", messages, attachments, f.kicks-kicks)
	}
	card, err := store.Card(ctx, lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := card.Feed[len(card.Feed)-1]
	for i := len(card.Feed) - 1; i >= 0 && last.Kind != "message"; i-- {
		last = card.Feed[i]
	}
	if last.Body != "Схема стойки" || len(last.Files) != 1 || last.Files[0].Kind != leads.KindJPG || !strings.HasPrefix(last.Files[0].Filename, "photo_2026-09-19_") {
		t.Errorf("the photo in the conversation: %+v", last)
	}

	// An album: two photos, one message, one «passed on»; the staff hear of it at the worker's pace.
	first, second := f.api.Store(append(testJPEG, 1)), f.api.Store(append(testJPEG, 2))
	kicks = f.kicks
	if text := oneText(t, f.sends(client, Message{MediaGroupID: "album-1", Photo: []FileRef{{FileID: first}}})); text != "Передано." {
		t.Errorf("the first photo of an album: %q", text)
	}
	if got := f.sends(client, Message{MediaGroupID: "album-1", Photo: []FileRef{{FileID: second}}}); len(got) != 0 {
		t.Errorf("the second photo of an album is answered: %+v", got)
	}
	if messages, attachments := stored(); messages != 2 || attachments != 3 || f.kicks != kicks {
		t.Errorf("after an album: %d messages, %d files, %d kicks", messages, attachments, f.kicks-kicks)
	}

	// A document keeps its name; a program with the name of a PDF is refused by its content.
	document := f.api.Store(testPDF)
	if text := oneText(t, f.sends(client, Message{Document: &FileRef{FileID: document, FileName: "смета.pdf", FileSize: int64(len(testPDF))}})); text != "Передано." {
		t.Errorf("a document: %q", text)
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM lead_attachments WHERE lead_id = %d AND filename = 'смета.pdf' AND kind = 'pdf'`, lead.ID)); n != 1 {
		t.Errorf("the document is not kept under its name: %d", n)
	}
	program := f.api.Store([]byte("MZ\x90\x00 this is no document at all"))
	if text := oneText(t, f.sends(client, Message{Document: &FileRef{FileID: program, FileName: "invoice.pdf"}})); !strings.Contains(text, "не принимается") {
		t.Errorf("a program called .pdf: %q", text)
	}
	// Too big to download: said at once, without asking Telegram for the file.
	f.api.Forget()
	if text := oneText(t, f.sends(client, Message{Document: &FileRef{FileID: "huge", FileName: "backup.pdf", FileSize: 30 << 20}})); !strings.Contains(text, "слишком большой") ||
		len(f.api.Calls("getFile")) != 0 {
		t.Errorf("a file too big: %q", text)
	}
	for name, message := range map[string]Message{
		"a voice":      {Voice: &FileRef{FileID: "voice"}},
		"a sticker":    {Sticker: &FileRef{FileID: "sticker"}},
		"an animation": {Animation: &FileRef{FileID: "gif"}, Document: &FileRef{FileID: "gif", FileName: "a.mp4"}},
		"a location":   {},
	} {
		if text := oneText(t, f.sends(client, message)); !strings.Contains(text, "Такое я не передам") {
			t.Errorf("%s: %q", name, text)
		}
	}
	if messages, attachments := stored(); messages != 3 || attachments != 4 {
		t.Errorf("what was refused was stored: %d messages, %d files", messages, attachments)
	}

	// The staff hear how many files came — not their names (brief B10.7).
	f.api.Forget()
	if err := f.bot.Send(ctx, task(leads.TaskClientMessage, lead.ID, lastIncoming(t, f, lead.ID))); err != nil {
		t.Fatal(err)
	}
	if sent := f.api.Sent(denis.ID); len(sent) != 1 || !strings.Contains(sent[0].Text(), "📎 файлов: 1 — в админке") || strings.Contains(sent[0].Text(), "смета") {
		t.Errorf("the staff's notice of a file: %+v", sent)
	}
}

func lastIncoming(t *testing.T, f *fixture, leadID int64) int64 {
	t.Helper()
	var id int64
	if err := f.db.QueryRow(`SELECT MAX(id) FROM lead_messages WHERE lead_id = ? AND direction = 'in'`, leadID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A member's file goes with the answer being written: to the client everywhere, and into the
// conversation on the site.
func TestAMemberAnswersWithAFile(t *testing.T) {
	f := newFixture(t)
	store := f.withLeads()
	store.UseFiles(leads.NewFiles(t.TempDir()))
	f.join(denis, RoleOwner)
	lead := f.addLead(store, nil)
	photo := f.api.Store(testJPEG)

	// Without an answer being written, the bot says how.
	if text := oneText(t, f.sends(denis, Message{Photo: []FileRef{{FileID: photo}}})); !strings.Contains(text, "нажмите «💬 Ответить»") {
		t.Errorf("a file out of the blue: %q", text)
	}
	f.presses(denis, "l:reply:1")
	f.presses(denis, "l:own:1")
	preview := oneText(t, f.sends(denis, Message{Photo: []FileRef{{FileID: photo, FileSize: int64(len(testJPEG))}}, Caption: "Вот схема стойки"}))
	if !strings.Contains(preview, "Предпросмотр ответа") || !strings.Contains(preview, "Вот схема стойки") || !strings.Contains(preview, "📎 Ваши файлы: photo_") {
		t.Errorf("the preview of an answer with a file: %q", preview)
	}
	// «Изменить»: a new text, the same file.
	f.presses(denis, "l:own:1")
	if text := oneText(t, f.says(denis, "Вот схема стойки, посмотрите.")); !strings.Contains(text, "📎 Ваши файлы: photo_") {
		t.Errorf("the file was lost with a new text: %q", text)
	}
	f.presses(denis, "l:send:1")
	var body string
	var files int
	if err := f.db.QueryRow(`SELECT m.body, (SELECT COUNT(*) FROM lead_attachments a WHERE a.message_id = m.id) FROM lead_messages m WHERE m.lead_id = ? AND m.direction = 'out'`, lead.ID).
		Scan(&body, &files); err != nil || body != "Вот схема стойки, посмотрите." || files != 1 {
		t.Errorf("the answer: %q with %d files, %v", body, files, err)
	}
	if n := f.count(fmt.Sprintf(`SELECT COUNT(*) FROM outbox WHERE lead_id = %d AND kind = 'lead.reply'`, lead.ID)); n != 1 {
		t.Errorf("deliveries of the answer queued: %d", n)
	}

	// A draft given up takes its files away.
	f.presses(denis, "l:own:1")
	f.sends(denis, Message{Photo: []FileRef{{FileID: photo}}})
	before := f.count(`SELECT COUNT(*) FROM lead_attachments`)
	f.presses(denis, "l:cancel:1")
	if dialog, _ := f.access.Dialog(context.Background(), denis.ID); dialog != nil || f.count(`SELECT COUNT(*) FROM lead_attachments`) != before {
		t.Errorf("a cancelled draft: %+v", dialog)
	}
}
