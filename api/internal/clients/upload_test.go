package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// upload sends a message with files the way the account's page does: multipart, to its own path.
func (b *browser) upload(number, text string, files map[string][]byte, edit ...func(*http.Request)) answer {
	b.f.t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if text != "" {
		_ = form.WriteField("text", text)
	}
	for name, content := range files {
		part, _ := form.CreateFormFile("files", name)
		_, _ = part.Write(content)
	}
	_ = form.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/account/upload/leads/"+number+"/messages", &body)
	request.Host = "krokosha.com"
	request.RemoteAddr = "127.0.0.1:40000"
	request.Header.Set("X-Real-IP", b.ip)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Origin", "https://krokosha.com")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if b.csrf != "" {
		request.Header.Set("X-CSRF-Token", b.csrf)
	}
	for name, value := range b.cookies {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	for _, change := range edit {
		change(request)
	}
	recorder := httptest.NewRecorder()
	b.f.handler.ServeHTTP(recorder, request)
	out := answer{status: recorder.Code, raw: recorder.Body.String()}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out.body)
	return out
}

// A client writes with files in the account, as with a letter or in Telegram: the files join the
// conversation, the admin area sees them. A request that is over takes no more messages there.
func TestMessagesWithFilesAndClosedRequests(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	f.leads.UseFiles(leads.NewFiles(dir))
	b := f.browser("203.0.113.21")
	b.signInByEmail("maria@example.com")
	number := f.submit(b, "maria@example.com", nil)["id"].(string)
	id := mustNumber(t, number)
	ctx := context.Background()
	jpg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{7}, 500)...)
	pdf := []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n")
	onDisk := func() int {
		entries, _ := os.ReadDir(dir)
		n := 0
		for _, entry := range entries {
			if !entry.IsDir() {
				n++
			}
		}
		return n
	}
	lastFiles := func() (body string, files []leads.Attachment) {
		t.Helper()
		card, err := f.leads.Card(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range card.Feed {
			if entry.Kind == "message" && entry.Direction == "in" {
				body, files = entry.Body, entry.Files
			}
		}
		return body, files
	}

	if view := b.send(http.MethodGet, "/api/account/leads/"+number, nil); view.body["lead"].(map[string]any)["can_write"] != true {
		t.Fatalf("a new request takes messages: %s", view.raw)
	}
	tasks := f.tasks(leads.TaskClientMessage)
	if got := b.upload(number, "Схема и смета", map[string][]byte{"стойка.jpg": jpg, "Смета.pdf": pdf}); got.status != http.StatusOK {
		t.Fatalf("a message with files: %d %s", got.status, got.raw)
	}
	if body, files := lastFiles(); body != "Схема и смета" || len(files) != 2 || f.tasks(leads.TaskClientMessage) != tasks+2 {
		t.Errorf("the message with files: %q %+v", body, files)
	}
	// Files alone: the account shows no made-up words for them.
	if got := b.upload(number, "", map[string][]byte{"фото.jpg": append(jpg, 1)}); got.status != http.StatusOK {
		t.Fatalf("files alone: %d %s", got.status, got.raw)
	}
	feed := b.send(http.MethodGet, "/api/account/leads/"+number, nil).body["lead"].(map[string]any)["feed"].([]any)
	lastEntry := feed[len(feed)-1].(map[string]any)
	if lastEntry["body"] != nil || len(lastEntry["files"].([]any)) != 1 {
		t.Errorf("files alone in the account: %v", lastEntry)
	}

	// A file that is not what its name says stops the message, and says which; nothing is left.
	files := onDisk()
	got := b.upload(number, "ещё", map[string][]byte{"invoice.pdf": []byte("MZ\x90\x00 a program"), "ok.jpg": jpg})
	if got.status != http.StatusUnprocessableEntity || got.body["error"] != "file_type" || got.body["file"] != "invoice.pdf" || onDisk() != files {
		t.Errorf("a program called .pdf: %d %s, %d files on disk (was %d)", got.status, got.raw, onDisk(), files)
	}
	six := map[string][]byte{}
	for _, name := range []string{"1.jpg", "2.jpg", "3.jpg", "4.jpg", "5.jpg", "6.jpg"} {
		six[name] = jpg
	}
	if got := b.upload(number, "много", six); got.status != http.StatusUnprocessableEntity || got.body["error"] != "too_many_files" {
		t.Errorf("six files: %d %s", got.status, got.raw)
	}
	// The rules of every change: the site's own page, the token of the session, a form with files.
	if got := b.upload(number, "x", map[string][]byte{"a.jpg": jpg}, func(r *http.Request) { r.Header.Del("X-CSRF-Token") }); got.status != http.StatusForbidden {
		t.Errorf("without the token: %d", got.status)
	}
	if got := b.upload(number, "x", map[string][]byte{"a.jpg": jpg}, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }); got.status != http.StatusForbidden {
		t.Errorf("from another site: %d", got.status)
	}
	if got := b.send(http.MethodPost, "/api/account/upload/leads/"+number+"/messages", map[string]string{"text": "json"}); got.status != http.StatusUnsupportedMediaType {
		t.Errorf("JSON to the path of files: %d", got.status)
	}
	if got := f.browser("203.0.113.22").upload(number, "x", map[string][]byte{"a.jpg": jpg}); got.status != http.StatusUnauthorized {
		t.Errorf("signed out: %d", got.status)
	}
	stranger := f.browser("203.0.113.23")
	stranger.signInByEmail("oleg@example.com")
	files = onDisk()
	if got := stranger.upload(number, "чужое", map[string][]byte{"a.jpg": jpg}); got.status != http.StatusNotFound || onDisk() != files {
		t.Errorf("into somebody else's request: %d, %d files on disk (was %d)", got.status, onDisk(), files)
	}

	// Done: the account takes no more messages into it — a new question is an inquiry.
	if err := f.leads.SetStatus(ctx, id, "denis", leads.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.leads.SetStatus(ctx, id, "denis", leads.StatusDone, ""); err != nil {
		t.Fatal(err)
	}
	if view := b.send(http.MethodGet, "/api/account/leads/"+number, nil); view.body["lead"].(map[string]any)["can_write"] != false {
		t.Errorf("a finished request still takes messages: %s", view.raw)
	}
	if got := b.send(http.MethodPost, "/api/account/leads/"+number+"/messages", map[string]string{"text": "Ещё вопрос"}); got.status != http.StatusConflict || got.body["error"] != "closed" {
		t.Errorf("a message into a finished request: %d %s", got.status, got.raw)
	}
	files = onDisk()
	if got := b.upload(number, "и файл", map[string][]byte{"a.jpg": jpg}); got.status != http.StatusConflict || got.body["error"] != "closed" || onDisk() != files {
		t.Errorf("files into a finished request: %d %s", got.status, got.raw)
	}
	if got := b.send(http.MethodPost, "/api/account/inquiries", map[string]string{"text": "Вопрос по заявке", "parent": number}); got.status != http.StatusCreated {
		t.Errorf("a question about a finished request: %d %s", got.status, got.raw)
	}
}
