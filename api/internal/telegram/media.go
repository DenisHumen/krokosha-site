package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"strconv"
)

// Files for clients (answers with photos, videos and documents, docs/quick-replies.md). The Bot
// API takes a file either as a multipart upload or by the file_id Telegram gave it the first time;
// uploads are streamed from the disk, never held in memory whole.

// How Telegram shows a file.
const (
	MediaPhoto    = "photo"
	MediaVideo    = "video"
	MediaDocument = "document"
)

// Album is how many files Telegram groups into one message.
const (
	AlbumMin = 2
	AlbumMax = 10
)

// FileRef is a file of a sent message, as Telegram knows it.
type FileRef struct {
	FileID string `json:"file_id"`
}

// FileID is the id of the file a sent message carries, when it went out as the kind it was sent
// as: the biggest size of a photo, a video, a document. A video Telegram turned into an animation
// has none — it is uploaded again next time rather than sent by an id of another kind.
func (m Message) FileID(kind string) string {
	switch {
	case kind == MediaPhoto && len(m.Photo) > 0:
		return m.Photo[len(m.Photo)-1].FileID
	case kind == MediaVideo && m.Video != nil:
		return m.Video.FileID
	case kind == MediaDocument && m.Document != nil:
		return m.Document.FileID
	}
	return ""
}

// InputFile is a file to send: one Telegram knows already (FileID) or one to upload (Open).
type InputFile struct {
	Kind   string // MediaPhoto | MediaVideo | MediaDocument
	FileID string // "" — upload
	Name   string // the name the client sees on a document
	Open   func() (io.ReadCloser, error)
}

var sendMethods = map[string]string{MediaPhoto: "sendPhoto", MediaVideo: "sendVideo", MediaDocument: "sendDocument"}

// SendFile sends one file as a message of its own.
func (c *Client) SendFile(ctx context.Context, chatID int64, file InputFile) (Message, error) {
	method, ok := sendMethods[file.Kind]
	if !ok {
		return Message{}, fmt.Errorf("telegram: no way to send a file of the kind %q", file.Kind)
	}
	var sent Message
	if file.FileID != "" {
		params := map[string]any{"chat_id": chatID, file.Kind: file.FileID}
		if file.Kind == MediaVideo {
			params["supports_streaming"] = true
		}
		return sent, c.call(ctx, method, params, &sent)
	}
	fields := []formField{{"chat_id", strconv.FormatInt(chatID, 10)}}
	if file.Kind == MediaVideo {
		fields = append(fields, formField{"supports_streaming", "true"})
	}
	return sent, c.upload(ctx, method, fields, []formFile{{field: file.Kind, file: file}}, &sent)
}

// SendAlbum sends 2–10 files as one message: photos and videos together, or documents alone.
// Telegram answers with a message per file, in the same order.
func (c *Client) SendAlbum(ctx context.Context, chatID int64, files []InputFile) ([]Message, error) {
	if len(files) < AlbumMin || len(files) > AlbumMax {
		return nil, fmt.Errorf("telegram: an album holds %d to %d files, not %d", AlbumMin, AlbumMax, len(files))
	}
	type inputMedia struct {
		Type      string `json:"type"`
		Media     string `json:"media"`
		Streaming bool   `json:"supports_streaming,omitempty"`
	}
	media := make([]inputMedia, len(files))
	var uploads []formFile
	for i, file := range files {
		media[i] = inputMedia{Type: file.Kind, Media: file.FileID, Streaming: file.Kind == MediaVideo}
		if file.FileID == "" {
			field := "file" + strconv.Itoa(i)
			media[i].Media = "attach://" + field
			uploads = append(uploads, formFile{field: field, file: file})
		}
	}
	var sent []Message
	if len(uploads) == 0 {
		return sent, c.call(ctx, "sendMediaGroup", map[string]any{"chat_id": chatID, "media": media}, &sent)
	}
	list, err := json.Marshal(media)
	if err != nil {
		return nil, err
	}
	fields := []formField{{"chat_id", strconv.FormatInt(chatID, 10)}, {"media", string(list)}}
	return sent, c.upload(ctx, "sendMediaGroup", fields, uploads, &sent)
}

type formField struct{ name, value string }

type formFile struct {
	field string
	file  InputFile
}

// upload runs a method as multipart/form-data. The files are opened first — a file missing from
// the disk is a clear error, not a broken connection — and then written while the request is being
// sent, straight from the disk; if the request ends early, the writing stops with it.
func (c *Client) upload(ctx context.Context, method string, fields []formField, files []formFile, result any) error {
	contents := make([]io.ReadCloser, 0, len(files))
	closeAll := func() {
		for _, content := range contents {
			_ = content.Close()
		}
	}
	for _, part := range files {
		content, err := part.file.Open()
		if err != nil {
			closeAll()
			return fmt.Errorf("telegram %s: %w", method, err)
		}
		contents = append(contents, content)
	}
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		defer closeAll()
		_ = writer.CloseWithError(writeForm(form, fields, files, contents))
	}()
	err := c.post(ctx, c.uploads, method, reader, form.FormDataContentType(), result)
	_ = reader.Close() // a request that failed early leaves the writer nothing to wait for
	return err
}

func writeForm(form *multipart.Writer, fields []formField, files []formFile, contents []io.ReadCloser) error {
	for _, field := range fields {
		if err := form.WriteField(field.name, field.value); err != nil {
			return err
		}
	}
	for i, part := range files {
		target, err := form.CreateFormFile(part.field, part.file.Name)
		if err != nil {
			return err
		}
		if _, err := io.Copy(target, contents[i]); err != nil {
			return err
		}
	}
	return form.Close()
}
