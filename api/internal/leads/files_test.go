package leads

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- files people really send, and files pretending to be them ----------------------------------

func pdfFile() []byte {
	return []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")
}

func pngFile() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{0, 1, 2, 3}, 64)...)
}

func jpgFile() []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, bytes.Repeat([]byte{7}, 200)...)
}

// archive builds a zip with the given file names inside.
func archive(t *testing.T, names ...string) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte("<xml/>"))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func docxFile(t *testing.T) []byte {
	return archive(t, "[Content_Types].xml", "_rels/.rels", "word/document.xml")
}

// A Windows program: what somebody who renames files hopes to get through.
func programFile() []byte {
	return append([]byte{'M', 'Z', 0x90, 0, 3, 0, 0, 0}, bytes.Repeat([]byte{0, 0xFF}, 300)...)
}

func TestInspectTellsFilesByWhatIsInside(t *testing.T) {
	cyrillic := []byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2, '\r', '\n', '\t', '1', '2', '3'} // «Привет» in Windows-1251
	junkThenPDF := append(bytes.Repeat([]byte{' '}, 300), pdfFile()...)
	for name, tc := range map[string]struct {
		filename string
		content  []byte
		size     int64 // 0 — the real one
		kind     string
		err      error
	}{
		"pdf":                             {filename: "Схема сети.pdf", content: pdfFile(), kind: KindPDF},
		"pdf with junk before the header": {filename: "scan.pdf", content: junkThenPDF, kind: KindPDF},
		"capital extension":               {filename: "SCAN.PDF", content: pdfFile(), kind: KindPDF},
		"png":                             {filename: "plan.png", content: pngFile(), kind: KindPNG},
		"jpg":                             {filename: "photo.jpg", content: jpgFile(), kind: KindJPG},
		"jpeg":                            {filename: "photo.jpeg", content: jpgFile(), kind: KindJPG},
		"text, utf-8":                     {filename: "notes.txt", content: []byte("Сеть на 40 мест\nдва VLAN\n"), kind: KindTXT},
		"text, an old encoding":           {filename: "notes.txt", content: cyrillic, kind: KindTXT},
		"docx":                            {filename: "ТЗ.docx", content: docxFile(t), kind: KindDOCX},

		"a program called pdf":                 {filename: "invoice.pdf", content: programFile(), err: ErrFileType},
		"a program called txt":                 {filename: "readme.txt", content: programFile(), err: ErrFileType},
		"a program called png":                 {filename: "plan.png", content: programFile(), err: ErrFileType},
		"an archive called docx":               {filename: "report.docx", content: archive(t, "payload.exe", "autorun.inf"), err: ErrFileType},
		"a spreadsheet called docx":            {filename: "table.docx", content: archive(t, "[Content_Types].xml", "xl/workbook.xml"), err: ErrFileType},
		"half an archive":                      {filename: "broken.docx", content: docxFile(t)[:40], err: ErrFileType},
		"a png called jpg":                     {filename: "photo.jpg", content: pngFile(), err: ErrFileType},
		"a program, honestly":                  {filename: "setup.exe", content: programFile(), err: ErrFileType},
		"a page":                               {filename: "index.html", content: []byte("<script>alert(1)</script>"), err: ErrFileType},
		"a vector picture":                     {filename: "logo.svg", content: []byte("<svg onload=alert(1)>"), err: ErrFileType},
		"two extensions":                       {filename: "report.pdf.exe", content: pdfFile(), err: ErrFileType},
		"no extension":                         {filename: "README", content: []byte("text"), err: ErrFileType},
		"an empty file":                        {filename: "empty.txt", content: nil, err: ErrFileType},
		"more than ten megabytes":              {filename: "big.pdf", content: pdfFile(), size: MaxAttachmentBytes + 1, err: ErrFileTooBig},
		"a folder in the name changes nothing": {filename: `..\..\windows\system32\evil.pdf`, content: programFile(), err: ErrFileType},
	} {
		size := tc.size
		if size == 0 {
			size = int64(len(tc.content))
		}
		kind, err := Inspect(tc.filename, size, bytes.NewReader(tc.content))
		if kind != tc.kind || !errors.Is(err, tc.err) {
			t.Errorf("%s: %q %v, want %q %v", name, kind, err, tc.kind, tc.err)
		}
	}
}

func TestCleanFilename(t *testing.T) {
	for given, want := range map[string]string{
		"spec.pdf": "spec.pdf",
		"  Схема сети (финал).PDF ":        "Схема сети (финал).PDF",
		"../../etc/passwd":                 "passwd",
		`C:\Users\ivan\Desktop\plan.png`:   "plan.png",
		"report\r\nContent-Type: x.pdf":    "reportContent-Type x.pdf",
		`a<b>:"c"|d?.txt`:                  "abcd.txt",
		"....":                             "file",
		".pdf":                             "file.pdf",
		strings.Repeat("я", 300) + ".docx": strings.Repeat("я", 100) + ".docx",
	} {
		if got := CleanFilename(given); got != want {
			t.Errorf("%q → %q, want %q", given, got, want)
		}
	}
}

func TestFilesAreKeptForTheServiceAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attachments")
	files := NewFiles(dir)
	upload, err := files.Save("../Схема.pdf", KindPDF, bytes.NewReader(pdfFile()))
	if err != nil {
		t.Fatal(err)
	}
	if upload.Filename != "Схема.pdf" || upload.Size != int64(len(pdfFile())) || len(upload.SHA256) != 32 || !reStoredName.MatchString(upload.StoredAs) {
		t.Errorf("upload: %+v", upload)
	}
	// On disk the file has a random name — nothing of what the sender typed — and no access for others.
	info, err := os.Stat(filepath.Join(dir, upload.StoredAs))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("mode of a stored file: %o, want 600", mode)
		}
		if parent, _ := os.Stat(dir); parent.Mode().Perm() != 0o700 {
			t.Errorf("mode of the directory: %o, want 700", parent.Mode().Perm())
		}
	}

	stored, err := files.Open(upload.StoredAs)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(stored)
	_ = stored.Close()
	if !bytes.Equal(content, pdfFile()) {
		t.Error("the stored file differs from what was sent")
	}

	// Only our own names open anything, whatever ends up in the database.
	for _, name := range []string{"../" + upload.StoredAs, "..", "", upload.StoredAs + "/../x", "G" + upload.StoredAs[1:], "passwd"} {
		if opened, err := files.Open(name); err == nil {
			_ = opened.Close()
			t.Errorf("%q was opened", name)
		}
	}

	// A file over the limit is not kept, even if the declared size lied.
	if _, err := files.Save("big.txt", KindTXT, io.LimitReader(zeroes{}, MaxAttachmentBytes+5)); !errors.Is(err, ErrFileTooBig) {
		t.Errorf("an oversized file: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("files in the directory: %d, want 1", len(entries))
	}

	if err := files.Remove(upload.StoredAs); err != nil {
		t.Fatal(err)
	}
	if err := files.Remove(upload.StoredAs); err != nil {
		t.Errorf("removing what is already gone: %v", err)
	}
}

type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestSweepRemovesFilesNobodyKnows(t *testing.T) {
	dir := t.TempDir()
	files := NewFiles(dir)
	save := func(age time.Duration) string {
		upload, err := files.Save("note.txt", KindTXT, strings.NewReader("text"))
		if err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(filepath.Join(dir, upload.StoredAs), when, when); err != nil {
			t.Fatal(err)
		}
		return upload.StoredAs
	}
	known, orphan, fresh := save(72*time.Hour), save(72*time.Hour), save(time.Minute)
	foreign := filepath.Join(dir, "README.txt") // not ours: somebody put it there, somebody needs it
	if err := os.WriteFile(foreign, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	_ = os.Chtimes(foreign, old, old)

	removed, err := files.Sweep(func(name string) (bool, error) { return name == known, nil }, time.Now().Add(-24*time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("swept: %d %v, want 1", removed, err)
	}
	for name, want := range map[string]bool{known: true, orphan: false, fresh: true, "README.txt": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if exists := err == nil; exists != want {
			t.Errorf("%s exists: %v, want %v", name, exists, want)
		}
	}
	// No directory yet — nothing to sweep, and nothing wrong with that.
	if removed, err := NewFiles(filepath.Join(dir, "missing")).Sweep(nil, time.Now()); err != nil || removed != 0 {
		t.Errorf("sweeping a missing directory: %d %v", removed, err)
	}
}
