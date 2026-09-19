package leads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type testFile struct {
	name    string
	content []byte
}

// upload sends the form the way a browser does when it has a file field: multipart/form-data.
func (f *fixture) upload(values url.Values, files []testFile, headers map[string]string) reply {
	f.t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, all := range values {
		for _, value := range all {
			_ = writer.WriteField(key, value)
		}
	}
	for _, file := range files {
		part, err := writer.CreateFormFile("files", file.name)
		if err != nil {
			f.t.Fatal(err)
		}
		_, _ = part.Write(file.content)
	}
	if err := writer.Close(); err != nil {
		f.t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/leads", &body)
	request.Host = "krokosha.xyz"
	request.RemoteAddr = "127.0.0.1:40000"
	request.Header.Set("X-Real-IP", "203.0.113.7")
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", "https://krokosha.xyz")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	out := reply{status: recorder.Code, location: recorder.Header().Get("Location"), body: recorder.Body.String()}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out.json)
	return out
}

// onDisk counts the files in the attachments directory.
func (f *fixture) onDisk() int {
	f.t.Helper()
	entries, err := os.ReadDir(f.filesDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal(err)
	}
	return len(entries)
}

func TestFilesComeWithARequest(t *testing.T) {
	f := newFixture(t)
	f.acceptFiles = true
	store := NewStore(f.db, func() time.Time { return f.now })
	store.UseFiles(f.files)
	ctx := context.Background()

	values := validValues()
	values.Set("altcha", f.proof())
	f.now = f.now.Add(2 * time.Minute)
	files := []testFile{{"Схема сети.pdf", pdfFile()}, {`C:\photos\шкаф.JPG`, jpgFile()}, {"", nil}} // the last one: a file field nobody touched
	got := f.upload(values, files, asJSON)
	if got.status != http.StatusCreated || got.json["id"] != "K-0001" {
		t.Fatalf("a form with files: %d %s", got.status, got.body)
	}
	if f.onDisk() != 2 {
		t.Fatalf("files on disk: %d, want 2", f.onDisk())
	}

	// The card shows them with the message they came with.
	card, err := store.Card(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var shown []Attachment
	for _, entry := range card.Feed {
		if entry.Channel == "form" {
			shown = entry.Files
		}
	}
	if len(shown) != 2 || shown[0].Filename != "Схема сети.pdf" || shown[0].Kind != KindPDF || shown[0].Size != int64(len(pdfFile())) ||
		shown[1].Filename != "шкаф.JPG" || shown[1].Kind != KindJPG {
		t.Fatalf("files of the first message: %+v", shown)
	}

	// A file is handed out only under its own request.
	file, content, err := store.OpenAttachment(ctx, 1, shown[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(content)
	_ = content.Close()
	if file.Filename != "Схема сети.pdf" || !bytes.Equal(body, pdfFile()) {
		t.Errorf("the stored file: %+v, %d bytes", file, len(body))
	}
	if _, _, err := store.OpenAttachment(ctx, 2, shown[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a file asked for under another request: %v", err)
	}
	if _, _, err := store.OpenAttachment(ctx, 1, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("a file that does not exist: %v", err)
	}

	// Without JavaScript the same form ends on the «thank you» page.
	values.Set("contact_value", "second@company.com")
	if got := f.upload(values, []testFile{{"notes.txt", []byte("Сеть на 40 мест")}}, map[string]string{"X-Real-IP": "203.0.113.8"}); got.status != http.StatusSeeOther || !strings.HasPrefix(got.location, "/api/leads/thanks?t=") {
		t.Errorf("a plain form with a file: %d → %q", got.status, got.location)
	}
	if f.onDisk() != 3 {
		t.Errorf("files on disk: %d, want 3", f.onDisk())
	}

	// Deleting a client's data deletes the files too (brief B10.6).
	if err := store.Delete(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if f.onDisk() != 1 || f.count(`SELECT COUNT(*) FROM lead_attachments`) != 1 {
		t.Errorf("after deleting the first request: %d files, %d rows, want 1 and 1", f.onDisk(), f.count(`SELECT COUNT(*) FROM lead_attachments`))
	}
}

func TestFilesThatAreNotAccepted(t *testing.T) {
	f := newFixture(t)
	values := validValues()
	files := []testFile{{"spec.pdf", pdfFile()}}

	// The form of this site takes no files (the default): a script that sends one is told so.
	if got := f.upload(values, files, asJSON); got.status != http.StatusUnprocessableEntity || fmt.Sprint(got.json["errors"]) != "map[files:files_disabled]" {
		t.Errorf("files while the form takes none: %d %s", got.status, got.body)
	}
	// …and an upload cannot be used to make the server read megabytes.
	big := []testFile{{"big.txt", bytes.Repeat([]byte("x"), 200<<10)}}
	if got := f.upload(values, big, asJSON); got.status != http.StatusRequestEntityTooLarge {
		t.Errorf("200 KB while the form takes no files: %d %s", got.status, got.body)
	}

	f.acceptFiles = true
	for name, tc := range map[string]struct {
		files []testFile
		code  string
	}{
		"four files":             {[]testFile{{"1.txt", []byte("a")}, {"2.txt", []byte("b")}, {"3.txt", []byte("c")}, {"4.txt", []byte("d")}}, "too_many_files"},
		"a program called pdf":   {[]testFile{{"spec.pdf", pdfFile()}, {"invoice.pdf", programFile()}}, "file_type"},
		"a script":               {[]testFile{{"run.sh", []byte("#!/bin/sh\nrm -rf /\n")}}, "file_type"},
		"an empty file":          {[]testFile{{"empty.txt", nil}}, "file_type"},
		"more than 10 megabytes": {[]testFile{{"big.txt", bytes.Repeat([]byte("x"), MaxAttachmentBytes+1)}}, "file_too_big"},
	} {
		got := f.upload(values, tc.files, asJSON)
		if got.status != http.StatusUnprocessableEntity || fmt.Sprint(got.json["errors"]) != "map[files:"+tc.code+"]" {
			t.Errorf("%s: %d %s, want 422 %s", name, got.status, got.body, tc.code)
		}
	}
	// A request that would not fit even with three full files.
	huge := []testFile{{"1.txt", bytes.Repeat([]byte("x"), MaxAttachmentBytes)}, {"2.txt", bytes.Repeat([]byte("x"), MaxAttachmentBytes)},
		{"3.txt", bytes.Repeat([]byte("x"), MaxAttachmentBytes)}, {"4.txt", bytes.Repeat([]byte("x"), 2<<20)}}
	if got := f.upload(values, huge, asJSON); got.status != http.StatusRequestEntityTooLarge || fmt.Sprint(got.json["errors"]) != "map[files:file_too_big]" {
		t.Errorf("32 MB: %d %s", got.status, got.body)
	}

	// Nothing was stored, nothing was written — and none of it used up the three requests an hour.
	if f.count(`SELECT COUNT(*) FROM leads`) != 0 || f.onDisk() != 0 {
		t.Errorf("after the refusals: %d requests, %d files", f.count(`SELECT COUNT(*) FROM leads`), f.onDisk())
	}
	for i := range leadsPerHour {
		if got := f.upload(values, files, asJSON); got.status != http.StatusCreated {
			t.Fatalf("request %d after the refusals: %d %s", i+1, got.status, got.body)
		}
	}
	// The fourth is one too many; its file is not kept either.
	if got := f.upload(values, files, asJSON); got.status != http.StatusTooManyRequests {
		t.Errorf("the fourth request: %d", got.status)
	}
	if f.onDisk() != leadsPerHour {
		t.Errorf("files on disk: %d, want %d", f.onDisk(), leadsPerHour)
	}
}

func TestARobotsFilesAreNotKept(t *testing.T) {
	f := newFixture(t)
	f.acceptFiles = true
	values := validValues()
	values.Set("website", "https://spam.example") // the trap field
	got := f.upload(values, []testFile{{"offer.pdf", pdfFile()}, {"price.txt", []byte("cheap")}}, asJSON)
	if got.status != http.StatusCreated {
		t.Fatalf("a robot gets the usual answer: %d %s", got.status, got.body)
	}
	if f.onDisk() != 0 || f.count(`SELECT COUNT(*) FROM lead_attachments`) != 0 {
		t.Errorf("a robot's files were kept: %d on disk", f.onDisk())
	}
	var details string
	if err := f.db.QueryRow(`SELECT details FROM lead_events WHERE lead_id = 1 AND action = 'created'`).Scan(&details); err != nil ||
		!strings.Contains(details, "вложения не сохранены (2)") {
		t.Errorf("the history of the request: %q %v", details, err)
	}
}
