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
	if err := check(kind, size, content); err != nil {
		return "", err
	}
	return kind, nil
}

// check makes sure the content is what the kind says, by its signature.
func check(kind string, size int64, content io.ReaderAt) error {
	head := make([]byte, 1024)
	n, err := content.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
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
	case KindMP4:
		matches = isMP4(head)
	}
	if err != nil {
		return err
	}
	if !matches {
		return ErrFileType
	}
	return nil
}

// --- what goes to a client: the files of templates and of answers ----------------------------------

// KindMP4 is the one kind of video: the one Telegram plays in the chat (brief of quick answers).
const KindMP4 = "mp4"

// Limits of the files that go to a client. Telegram takes a photo up to 10 MB and anything else up
// to 50 MB from a bot; documents are kept to what a letter can still carry.
const (
	MaxPhotoBytes    = 10 << 20
	MaxVideoBytes    = 50 << 20
	MaxDocumentBytes = 20 << 20
	// MaxOutgoingFiles: an album of Telegram holds ten.
	MaxOutgoingFiles = 10
)

var mediaKinds = map[string]string{
	".jpg": KindJPG, ".jpeg": KindJPG, ".png": KindPNG, ".mp4": KindMP4, ".m4v": KindMP4,
	".pdf": KindPDF, ".docx": KindDOCX, ".txt": KindTXT,
}

// ContentTypeOf is the media type a file of a kind is sent with: to a mail program, to a browser.
func ContentTypeOf(kind string) string {
	switch kind {
	case KindJPG:
		return "image/jpeg"
	case KindPNG:
		return "image/png"
	case KindMP4:
		return "video/mp4"
	case KindPDF:
		return "application/pdf"
	case KindTXT:
		return "text/plain; charset=utf-8"
	case KindDOCX:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	}
	return "application/octet-stream"
}

// MaxBytesOf is the limit of a file of a kind that goes to a client.
func MaxBytesOf(kind string) int64 {
	switch kind {
	case KindJPG, KindPNG:
		return MaxPhotoBytes
	case KindMP4:
		return MaxVideoBytes
	default:
		return MaxDocumentBytes
	}
}

// IsPhoto and IsVideo: what Telegram shows in the chat rather than as a file to download.
func IsPhoto(kind string) bool { return kind == KindJPG || kind == KindPNG }
func IsVideo(kind string) bool { return kind == KindMP4 }

// InspectMedia decides whether a file may go to a client — a photo, a video or a document — and
// what it is, by its content, like Inspect does for the files of the form.
func InspectMedia(filename string, size int64, content io.ReaderAt) (kind string, err error) {
	kind, known := mediaKinds[strings.ToLower(path.Ext(CleanFilename(filename)))]
	switch {
	case !known, size <= 0:
		return "", ErrFileType
	case size > MaxBytesOf(kind):
		return "", ErrFileTooBig
	}
	if err := check(kind, size, content); err != nil {
		return "", err
	}
	return kind, nil
}

// isMP4: an ISO media file whose brand is one of MPEG-4's — «ftyp» right after the size of the
// first box. QuickTime («qt  ») plays nowhere but on Apple's devices, and is not taken.
func isMP4(head []byte) bool {
	if len(head) < 12 || string(head[4:8]) != "ftyp" {
		return false
	}
	switch string(head[8:12]) {
	case "isom", "iso2", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "M4V ", "MSNV", "dash", "mmp4":
		return true
	}
	return false
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

// Save writes an inspected file of the form under a new random name, for the service's eyes only.
func (f *Files) Save(filename, kind string, content io.Reader) (Upload, error) {
	return f.SaveLimited(filename, kind, content, MaxAttachmentBytes)
}

// SaveLimited is Save with a limit of its own: a video for a client is bigger than a file of the form.
func (f *Files) SaveLimited(filename, kind string, content io.Reader, limit int64) (Upload, error) {
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
	upload.Size, err = io.Copy(io.MultiWriter(out, hash), io.LimitReader(content, limit+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && upload.Size > limit {
		err = ErrFileTooBig
	}
	if err != nil {
		_ = os.Remove(target)
		return Upload{}, err
	}
	upload.SHA256 = hash.Sum(nil)
	return upload, nil
}

// Link gives a file of another directory a name of its own here: a hard link, so that the file of
// a template goes into an answer without taking the disk twice, and deleting one leaves the other.
// Where a link cannot be made (another file system), the file is copied.
func (f *Files) Link(from *Files, storedAs string, upload Upload) (Upload, error) {
	source, err := from.where(storedAs)
	if err != nil {
		return Upload{}, err
	}
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return Upload{}, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return Upload{}, err
	}
	upload.StoredAs = hex.EncodeToString(random)
	target := filepath.Join(f.dir, upload.StoredAs)
	if err := os.Link(source, target); err == nil {
		return upload, nil
	}
	in, err := os.Open(source) // checked by where: 32 hex digits inside the other directory
	if err != nil {
		return Upload{}, err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // the random name made above
	if err != nil {
		return Upload{}, err
	}
	_, err = io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(target)
		return Upload{}, err
	}
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
