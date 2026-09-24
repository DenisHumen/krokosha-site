package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// Files people send the bot (docs/telegram.md): a client's photo, video or document joins the
// conversation of their request like a letter's files do — the admin area and the personal account
// show it — and a member's file goes with the answer being written, to the client everywhere. A
// file is downloaded from Telegram, told by its content (leads.InspectMedia) and kept with the files
// of requests; nothing is run or shown here.

// MaxDownloadBytes is what Telegram hands a bot at most.
const MaxDownloadBytes = 20 << 20

// TelegramFile is what getFile says about a file: where to download it from.
type TelegramFile struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

// GetFile asks where a file somebody sent can be downloaded.
func (c *Client) GetFile(ctx context.Context, fileID string) (TelegramFile, error) {
	var file TelegramFile
	err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &file)
	return file, err
}

// reFilePath is what Telegram's file paths look like («photos/file_12.jpg»): nothing that could
// climb out of the bot's own address.
var reFilePath = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:/[A-Za-z0-9_.-]+)*$`)

// Download fetches a file by the path getFile gave, at most limit bytes. The address contains the
// token: like every error of the client, a failure here never quotes it.
func (c *Client) Download(ctx context.Context, path string, limit int64) ([]byte, error) {
	if !reFilePath.MatchString(path) || strings.Contains(path, "..") {
		return nil, errors.New("telegram download: not a file path of Telegram")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/file/bot"+c.token+"/"+path, nil)
	if err != nil {
		return nil, errors.New("telegram download: cannot build the request")
	}
	response, err := c.uploads.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("telegram download: %s", strings.ReplaceAll(err.Error(), c.token, "<token>"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram download: %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, errors.New("telegram download: reading the file failed")
	}
	if int64(len(data)) > limit {
		return nil, leads.ErrFileTooBig
	}
	return data, nil
}

// incomingFile is the file a message carries and the name it is kept under: the biggest size of a
// photo, a video, a document. ok is false for what the bot does not take (a voice, a sticker…);
// ref is nil when there is no file at all.
func incomingFile(message *Message, location *time.Location) (ref *FileRef, name string, ok bool) {
	when := time.Unix(message.Date, 0).In(location).Format("2006-01-02_15-04-05")
	switch {
	case message.Animation != nil || message.Voice != nil || message.Audio != nil || message.Sticker != nil || message.VideoNote != nil:
		return nil, "", false
	case len(message.Photo) > 0:
		return &message.Photo[len(message.Photo)-1], "photo_" + when + ".jpg", true // Telegram keeps photos as JPEG
	case message.Video != nil:
		name = message.Video.FileName
		if name == "" {
			name = "video_" + when + ".mp4"
		}
		return message.Video, name, true
	case message.Document != nil:
		name = message.Document.FileName
		if name == "" {
			name = "document_" + when
		}
		return message.Document, name, true
	}
	return nil, "", true
}

// hasMedia: the message carries something besides words — a file, or what the bot does not take.
func hasMedia(message *Message) bool {
	ref, _, ok := incomingFile(message, time.UTC)
	return ref != nil || !ok
}

// takeFile downloads a file somebody sent and keeps it with the files of requests, told by its
// content. The file is the caller's until a message owns it.
func (b *Bot) takeFile(ctx context.Context, ref *FileRef, name string) (leads.Upload, error) {
	dir := b.opts.Leads.Files()
	if dir == nil {
		return leads.Upload{}, leads.ErrNoFilesHere
	}
	if ref.FileSize > MaxDownloadBytes {
		return leads.Upload{}, leads.ErrFileTooBig
	}
	file, err := b.opts.API.GetFile(ctx, ref.FileID)
	if err != nil {
		return leads.Upload{}, err
	}
	if file.FileSize > MaxDownloadBytes {
		return leads.Upload{}, leads.ErrFileTooBig
	}
	data, err := b.opts.API.Download(ctx, file.FilePath, MaxDownloadBytes)
	if err != nil {
		return leads.Upload{}, err
	}
	return leads.SaveFromClient(dir, name, int64(len(data)), bytes.NewReader(data))
}

// fileProblem says in the client's words why a file was not taken.
func fileProblem(err error) string {
	switch {
	case errors.Is(err, leads.ErrFileTooBig):
		return "toobig"
	case errors.Is(err, leads.ErrFileType), errors.Is(err, leads.ErrNoFilesHere):
		return "filetype"
	}
	return "failed"
}

// albumKey remembers the message the first file of an album became: the rest join it.
func albumKey(from int64, group string) string {
	return "tg-album:" + strconv.FormatInt(from, 10) + ":" + group
}

// albumTTL: Telegram delivers the files of an album within seconds.
const albumTTL = 5 * time.Minute

// clientFile takes a file from a client into the conversation of their request. The files of an
// album become one message; its caption is the text. Only the first file of an album is counted
// against the limit of messages, and only it is answered «passed on».
func (b *Bot) clientFile(ctx context.Context, message *Message, lead *leads.Lead, say func(string, ...any)) {
	ref, name, ok := incomingFile(message, b.opts.Location)
	if !ok || ref == nil {
		say("unsupported")
		return
	}
	caption, _ := cut(message.Caption, 4000)
	key, joins := "", int64(0)
	if message.MediaGroupID != "" {
		key = albumKey(message.From.ID, message.MediaGroupID)
		if value, found := b.opts.Cache.Get(ctx, key); found && strings.TrimSpace(caption) == "" {
			joins, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	if joins == 0 && !b.opts.Cache.Allow(ctx, "tg-client:"+strconv.FormatInt(message.From.ID, 10), 20, time.Hour) {
		say("slow")
		return
	}
	upload, err := b.takeFile(ctx, ref, name)
	if err != nil {
		if problem := fileProblem(err); problem == "failed" {
			b.opts.Log.Error("telegram: cannot take a client's file", "lead", lead.Number(), "error", err)
		}
		say(fileProblem(err))
		return
	}
	if joins > 0 {
		if err := b.opts.Leads.AttachFiles(ctx, lead.ID, joins, []leads.Upload{upload}); err == nil {
			return // the album goes on: one «passed on» was enough
		}
		// The first message of the album is gone: this file makes a message of its own.
	}
	messageID, err := b.opts.Leads.ClientWrote(ctx, lead.ID, leads.Incoming{Channel: leads.ChannelTelegram, Text: caption, Files: []leads.Upload{upload}})
	if err != nil {
		_ = b.opts.Leads.Files().Remove(upload.StoredAs)
		b.opts.Log.Error("telegram: cannot store a client's file", "lead", lead.Number(), "error", err)
		say("failed")
		return
	}
	if key != "" {
		// The staff hear of an album at the worker's own pace, a moment later, when its files are in.
		b.opts.Cache.Set(ctx, key, strconv.FormatInt(messageID, 10), albumTTL)
	} else {
		b.opts.Kick()
	}
	say("relayed")
}

// staffFile takes a file a member sent the bot into the answer being written: to the request of the
// answer the bot waits for, or to the one whose message the file replies to (a swipe). The file waits
// in the draft with its caption and goes with «✅ Отправить», to the client everywhere.
func (b *Bot) staffFile(ctx context.Context, message *Message, member *Member) {
	chatID := message.Chat.ID
	say := func(text string) { b.say(ctx, Outgoing{ChatID: chatID, Text: text}) }
	ref, name, ok := incomingFile(message, b.opts.Location)
	if !ok || ref == nil {
		say("Такой файл клиенту не отправить: подойдут фото, видео MP4, PDF, DOCX и TXT.")
		return
	}
	dialog, err := b.opts.Access.Dialog(ctx, member.TelegramID)
	if err != nil {
		b.opts.Log.Error("telegram: cannot read a dialog", "error", err)
		say("Не получилось. Попробуйте ещё раз.")
		return
	}
	if dialog != nil && dialog.Kind != dialogReply {
		dialog = nil
	}
	// A swipe to a message about a request: the file is for that request.
	if message.ReplyToMessage != nil && b.opts.DB != nil {
		var leadID int64
		err := b.opts.DB.QueryRowContext(ctx, `SELECT lead_id FROM bot_messages WHERE chat_id = ? AND message_id = ? AND wipe_after IS NULL`,
			chatID, message.ReplyToMessage.MessageID).Scan(&leadID)
		switch {
		case err == nil && (dialog == nil || dialog.LeadID != leadID):
			dialog = &Dialog{Kind: dialogReply, LeadID: leadID}
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			b.opts.Log.Error("telegram: cannot tell what a reply answers", "error", err)
		}
	}
	if dialog == nil {
		say("Чтобы отправить файл клиенту, нажмите «💬 Ответить» на карточке заявки или ответьте на неё свайпом — и пришлите файл.")
		return
	}
	card, err := b.opts.Leads.Card(ctx, dialog.LeadID)
	switch {
	case errors.Is(err, leads.ErrNotFound):
		_ = b.opts.Access.SetDialog(ctx, member.TelegramID, nil)
		say("Заявки больше нет: данные клиента удалены.")
		return
	case err != nil:
		b.opts.Log.Error("telegram: cannot read a request", "error", err)
		say("Не получилось. Попробуйте ещё раз.")
		return
	case card.Lead.Status == leads.StatusSpam || card.AnonymizedAt.Valid:
		say("По заявке #" + card.Lead.Number() + " ответить нельзя: это спам или данные клиента уже удалены.")
		return
	case card.ReplyVia == leads.MethodPhone:
		say("Клиент оставил только телефон: файлы по звонку не уходят. Запишите итог разговора текстом.")
		return
	case len(dialog.Files) >= leads.MaxOutgoingFiles:
		say(fmt.Sprintf("Не больше %d файлов в одном ответе — столько Telegram показывает альбомом.", leads.MaxOutgoingFiles))
		return
	}
	upload, err := b.takeFile(ctx, ref, name)
	if err != nil {
		switch fileProblem(err) {
		case "toobig":
			say("Файл больше, чем можно отправить: фото — до 10 МБ, документ и видео — до 20 МБ.")
		case "filetype":
			say("Такой файл клиенту не отправить: подойдут фото, видео MP4, PDF, DOCX и TXT.")
		default:
			b.opts.Log.Error("telegram: cannot take a member's file", "error", err)
			say("Не получилось принять файл. Попробуйте ещё раз.")
		}
		return
	}
	dialog.Files = append(dialog.Files, upload)
	if caption := strings.TrimSpace(message.Caption); caption != "" {
		dialog.Draft = strings.TrimSpace(strings.TrimSpace(dialog.Draft) + "\n\n" + caption)
	}
	continues := message.MediaGroupID != "" && message.MediaGroupID == dialog.Album
	dialog.Album = message.MediaGroupID
	if continues {
		// The rest of an album: the preview above sends every file of the draft.
		if err := b.opts.Access.SetDialog(ctx, member.TelegramID, dialog); err != nil {
			b.opts.Log.Error("telegram: cannot remember a draft", "error", err)
			_ = b.opts.Leads.Files().Remove(upload.StoredAs)
			return
		}
		say(fmt.Sprintf("📎 %s — тоже в ответе (файлов: %d). «✅ Отправить» выше отправит всё.", Escape(upload.Filename), len(dialog.Files)))
		return
	}
	b.preview(ctx, member, chatID, card, dialog)
}

// dropFiles removes the files of a draft that will not be sent.
func (b *Bot) dropFiles(dialog *Dialog) {
	if dialog == nil || b.opts.Leads == nil || b.opts.Leads.Files() == nil {
		return
	}
	for _, file := range dialog.Files {
		_ = b.opts.Leads.Files().Remove(file.StoredAs)
	}
}
