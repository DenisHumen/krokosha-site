package leads

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Files sent with a request (brief B10.1, B10.7). Off unless content/site.yaml says
// «contacts.form.attachments: true». At most three, ten megabytes each; pdf, png, jpg, txt and
// docx — told by what is inside a file, not by how it is named. They are kept outside the web
// root under random names, readable by the service alone, and handed out by the admin area as
// downloads. Nothing here ever runs, unpacks or renders a file.

// Limits of the brief.
const (
	MaxAttachments     = 3
	MaxAttachmentBytes = 10 << 20
	// maxUploadBytes caps a whole request with files: the files, the fields, the multipart framing.
	maxUploadBytes = MaxAttachments*MaxAttachmentBytes + 1<<20
)

// Kinds of files that are accepted.
const (
	KindPDF  = "pdf"
	KindPNG  = "png"
	KindJPG  = "jpg"
	KindTXT  = "txt"
	KindDOCX = "docx"
)

// Why a file is not accepted. The text is the code the form has a message for.
var (
	ErrFileTooBig   = errors.New("file_too_big")
	ErrFileType     = errors.New("file_type")
	ErrTooManyFiles = errors.New("too_many_files")
	ErrNoFilesHere  = errors.New("files_disabled")
)

var kindsByExtension = map[string]string{
	".pdf": KindPDF, ".png": KindPNG, ".jpg": KindJPG, ".jpeg": KindJPG, ".txt": KindTXT, ".docx": KindDOCX,
}

var (
	signaturePNG = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	signatureJPG = []byte{0xFF, 0xD8, 0xFF}
	signatureZIP = []byte{'P', 'K', 3, 4}
	signaturePDF = []byte("%PDF-")
)

// Inspect decides whether a file may come with a request, and what it is. The name only says
// what the sender claims; the content has to agree. Nothing is written anywhere.
func Inspect(filename string, size int64, content io.ReaderAt) (kind string, err error) {
	kind, known := kindsByExtension[strings.ToLower(path.Ext(CleanFilename(filename)))]
	switch {
	case !known, size <= 0:
		return "", ErrFileType // an empty file is not a document either
	case size > MaxAttachmentBytes:
		return "", ErrFileTooBig
	}

	head := make([]byte, 1024)
	n, err := content.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	head, err = head[:n], nil // a file shorter than a kilobyte ends early, which is no error

	matches := false
	switch kind {
	case KindPDF:
		// Readers accept a header anywhere in the first kilobyte; so do we.
		matches = bytes.Contains(head, signaturePDF)
	case KindPNG:
		matches = bytes.HasPrefix(head, signaturePNG)
	case KindJPG:
		matches = bytes.HasPrefix(head, signatureJPG)
	case KindTXT:
		matches, err = isPlainText(io.NewSectionReader(content, 0, size))
	case KindDOCX:
		matches = bytes.HasPrefix(head, signatureZIP) && isWordDocument(content, size)
	}
	if err != nil {
		return "", err
	}
	if !matches {
		return "", ErrFileType
	}
	return kind, nil
}

// isPlainText: no zero bytes and no control characters besides the ones text is made of. Any
// 8-bit encoding passes (people still send Windows-1251), programs and archives do not.
func isPlainText(content io.Reader) (bool, error) {
	buffer := make([]byte, 32<<10)
	for {
		n, err := content.Read(buffer)
		for _, b := range buffer[:n] {
			if b < 0x20 && b != '\t' && b != '\n' && b != '\r' && b != '\f' {
				return false, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
}

// isWordDocument looks at the list of files inside the archive — the list only, nothing is
// unpacked, so an archive built to explode costs nothing here.
func isWordDocument(content io.ReaderAt, size int64) bool {
	archive, err := zip.NewReader(content, size)
	if err != nil {
		return false
	}
	var types, document bool
	for _, file := range archive.File {
		switch file.Name {
		case "[Content_Types].xml":
			types = true
		case "word/document.xml":
			document = true
		}
	}
	return types && document
}

// CleanFilename makes a sender's file name safe to show and to offer on download: no folders,
// no control characters, not endless. What is left of «../../etc/passwd» is «passwd».
func CleanFilename(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError || strings.ContainsRune(`<>:"/|?*`, r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimRight(strings.TrimSpace(name), ". ")
	extension := path.Ext(name)
	if stem := []rune(strings.TrimSuffix(name, extension)); len(stem) > 100 {
		name = string(stem[:100]) + extension
	}
	if name == "" || name == extension {
		name = "file" + extension
	}
	return name
}

// Upload is a file that was checked and written to disk, waiting for its request to be stored.
type Upload struct {
	Filename string
	Kind     string
	Size     int64
	SHA256   []byte
	StoredAs string
}

// Files is the directory the files live in.
type Files struct {
	dir string
}

// NewFiles points at the directory; it is made on first use (the installer makes it on a server).
func NewFiles(dir string) *Files { return &Files{dir: dir} }

var reStoredName = regexp.MustCompile(`^[0-9a-f]{32}$`)

// where turns a stored name into a path — and refuses anything that is not one of our names,
// whatever the database says.
func (f *Files) where(storedAs string) (string, error) {
	if !reStoredName.MatchString(storedAs) {
		return "", fmt.Errorf("%q is not a name of a stored file", storedAs)
	}
	return filepath.Join(f.dir, storedAs), nil
}

// Save writes an inspected file under a new random name, for the service's eyes only.
func (f *Files) Save(filename, kind string, content io.Reader) (Upload, error) {
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return Upload{}, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return Upload{}, err
	}
	upload := Upload{Filename: CleanFilename(filename), Kind: kind, StoredAs: hex.EncodeToString(random)}
	target := filepath.Join(f.dir, upload.StoredAs)

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // the name is the random hex made above
	if err != nil {
		return Upload{}, err
	}
	hash := sha256.New()
	upload.Size, err = io.Copy(io.MultiWriter(out, hash), io.LimitReader(content, MaxAttachmentBytes+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && upload.Size > MaxAttachmentBytes {
		err = ErrFileTooBig
	}
	if err != nil {
		_ = os.Remove(target)
		return Upload{}, err
	}
	upload.SHA256 = hash.Sum(nil)
	return upload, nil
}

// Open gives a stored file for reading.
func (f *Files) Open(storedAs string) (*os.File, error) {
	target, err := f.where(storedAs)
	if err != nil {
		return nil, err
	}
	return os.Open(target) // checked by where: 32 hex digits inside our directory
}

// Remove deletes a stored file; one that is already gone is not an error.
func (f *Files) Remove(storedAs string) error {
	target, err := f.where(storedAs)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Sweep removes files nobody knows about: left by a request that failed half-way, or by a
// deletion that could not finish. Young files are left alone — their request may be on its way
// into the database right now.
func (f *Files) Sweep(known func(storedAs string) (bool, error), olderThan time.Time) (removed int, err error) {
	entries, err := os.ReadDir(f.dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || entry.IsDir() || !reStoredName.MatchString(entry.Name()) || info.ModTime().After(olderThan) {
			continue // not ours, or too fresh to judge
		}
		if keep, err := known(entry.Name()); err != nil {
			return removed, err
		} else if keep {
			continue
		}
		if err := f.Remove(entry.Name()); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
