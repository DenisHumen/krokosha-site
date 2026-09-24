package telegram

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/leads"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// Answers of the staff to clients who continued in Telegram (brief B10.5), with the files of the
// answer (docs/quick-replies.md): the text first — in pieces, Telegram takes 4096 characters a
// message — then the photos and videos as one album, then the documents. Every part that went out
// is counted (lead_deliveries.sent_parts, per chat): a retry after a failure, Telegram busy or the
// line cut, sends only the rest.

// part is one message of an answer: a piece of the text, or files.
type part struct {
	text  Outgoing
	files []leads.Attachment
}

// progress is where an answer stands in one chat: how many of its parts went out, and what became of
// it. An answer queued before deliveries existed keeps both on the message itself.
type progress struct {
	parts func(ctx context.Context) (int, error)
	sent  func(ctx context.Context, parts int) error
	mark  func(ctx context.Context, status string) error
}

// answerClient delivers an answer to the client: to the chat of its delivery (leads.Reach). site: an
// answer queued before deliveries existed, which was in the personal account already, and this is
// the notice of it in the account's Telegram — with the files as well.
func (b *Bot) answerClient(ctx context.Context, payload leads.TaskPayload, site bool) error {
	leadID, messageID := payload.LeadID, payload.MessageID
	lead, err := b.opts.Leads.Get(ctx, leadID)
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Permanent(errors.New("the request is gone (deleted on the client's demand?)"))
	}
	if err != nil {
		return err
	}
	at := progress{
		parts: func(ctx context.Context) (int, error) { return b.opts.Leads.SentParts(ctx, messageID) },
		sent:  func(ctx context.Context, parts int) error { return b.opts.Leads.MarkSentParts(ctx, messageID, parts) },
		mark: func(ctx context.Context, status string) error {
			if site {
				return nil // the notice of an answer in the account: the answer itself is there
			}
			return b.opts.Leads.MarkDelivery(ctx, messageID, status, "")
		},
	}
	var chatID int64
	lang := lead.Lang
	if payload.DeliveryID > 0 {
		delivery, err := b.opts.Leads.Delivery(ctx, payload.DeliveryID)
		if errors.Is(err, leads.ErrNotFound) {
			return outbox.Permanent(errors.New("the delivery is gone (the request was deleted?)"))
		}
		if err != nil {
			return err
		}
		if delivery.To == "" {
			// The request's link into the bot: the answer waits until the client opens it.
			if chatID, lang, err = b.clientChat(ctx, lead, false); err != nil {
				return err
			}
		} else if chatID, err = strconv.ParseInt(delivery.To, 10, 64); err != nil {
			return outbox.Permanent(fmt.Errorf("not a Telegram chat: %q", delivery.To))
		}
		at = progress{
			parts: func(context.Context) (int, error) { return delivery.SentParts, nil },
			sent: func(ctx context.Context, parts int) error {
				delivery.SentParts = parts
				return b.opts.Leads.MarkTargetParts(ctx, delivery.ID, parts)
			},
			mark: func(ctx context.Context, status string) error {
				return b.opts.Leads.MarkTarget(ctx, delivery.ID, status, "")
			},
		}
	} else if chatID, lang, err = b.clientChat(ctx, lead, site); err != nil {
		return err
	}
	body, _, err := b.opts.Leads.Message(ctx, leadID, messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return outbox.Permanent(errors.New("the answer is gone"))
	}
	if err != nil {
		return err
	}
	files, err := b.opts.Leads.MessageFiles(ctx, leadID, messageID)
	if err != nil {
		return err
	}
	done, err := at.parts(ctx)
	if err != nil {
		return err
	}
	failed := func() { _ = at.mark(ctx, "failed") }
	parts := b.answerParts(lead, chatID, lang, body, files)
	for i := done; i < len(parts); i++ {
		if parts[i].files == nil {
			_, err = b.opts.API.Send(ctx, parts[i].text)
		} else {
			err = b.sendFiles(ctx, chatID, parts[i].files)
		}
		var refused *APIError
		switch {
		case err == nil:
			if err := at.sent(ctx, i+1); err != nil {
				return err
			}
		case errors.As(err, &refused) && refused.Gone():
			failed()
			if !site {
				b.blocked(ctx, lead, messageID)
			}
			return outbox.Permanent(err) // the client blocked the bot: no retry will help
		case refusedForGood(err) || errors.Is(err, leads.ErrNotFound):
			// Telegram will not take this part whatever happens (after sending it as documents), or
			// a file of the answer is gone: the staff see «not delivered» and decide.
			failed()
			return outbox.Permanent(fmt.Errorf("part %d of %d: %w", i+1, len(parts), err))
		default:
			return err // tried again later, from this very part
		}
	}
	return at.mark(ctx, "sent")
}

// answerParts lays an answer out into messages. The same answer is always laid out the same way:
// that is what makes counting the parts sent enough to resume.
func (b *Bot) answerParts(lead *leads.Lead, chatID int64, lang, body string, files []leads.Attachment) []part {
	header := Escape(fmt.Sprintf(clientText(lang, "answer"), lead.Number()))
	var parts []part
	if strings.TrimSpace(body) == "" {
		parts = append(parts, part{text: Outgoing{ChatID: chatID, Text: header}}) // an answer of files alone
	} else {
		for i, piece := range chunks(body, messageLimit-300) {
			text := Escape(piece)
			if i == 0 {
				text = header + "\n\n" + text
			}
			parts = append(parts, part{text: Outgoing{ChatID: chatID, Text: text}})
		}
	}
	if lead.ClientID > 0 && b.opts.SiteURL != "" {
		// A client with a personal account has the whole conversation there as well.
		parts[len(parts)-1].text.Buttons = Keyboard{{{Text: clientText(lang, "account"), URL: strings.TrimRight(b.opts.SiteURL, "/") + accountPath(lang) + "#" + lead.Number()}}}
	}
	var visual, documents []leads.Attachment
	for _, file := range files {
		if mediaKind(file.Kind) == MediaDocument {
			documents = append(documents, file)
		} else {
			visual = append(visual, file)
		}
	}
	for _, group := range [][]leads.Attachment{visual, documents} {
		for len(group) > 0 {
			n := min(len(group), AlbumMax)
			parts = append(parts, part{files: group[:n]})
			group = group[n:]
		}
	}
	return parts
}

// mediaKind is how a file of a request goes to Telegram.
func mediaKind(kind string) string {
	switch {
	case leads.IsPhoto(kind):
		return MediaPhoto
	case leads.IsVideo(kind):
		return MediaVideo
	}
	return MediaDocument
}

// sendFiles sends the files of one part: an album of 2–10, or a single file.
func (b *Bot) sendFiles(ctx context.Context, chatID int64, files []leads.Attachment) error {
	inputs := make([]InputFile, len(files))
	cached := false
	for i, file := range files {
		inputs[i] = b.inputFile(ctx, file, mediaKind(file.Kind))
		cached = cached || inputs[i].FileID != ""
	}
	sent, err := b.sendInputs(ctx, chatID, inputs)
	if cached && badRequest(err) {
		// Telegram no longer knows an id it gave (the bot changed hands?): upload them all.
		for i := range inputs {
			if inputs[i].FileID != "" {
				b.forgetFile(ctx, files[i].SHA256, inputs[i].Kind)
				inputs[i].FileID = ""
			}
		}
		sent, err = b.sendInputs(ctx, chatID, inputs)
	}
	if badRequest(err) && inputs[0].Kind != MediaDocument {
		// A photo Telegram cannot show — too long, too wide — or a video it cannot read: they go as
		// documents, the way a mail program attaches them.
		for i, file := range files {
			inputs[i] = b.inputFile(ctx, file, MediaDocument)
		}
		sent, err = b.sendInputs(ctx, chatID, inputs)
	}
	if err != nil {
		return err
	}
	for i, message := range sent {
		if i < len(inputs) && inputs[i].FileID == "" {
			if id := message.FileID(inputs[i].Kind); id != "" {
				b.rememberFile(ctx, files[i].SHA256, inputs[i].Kind, id)
			}
		}
	}
	return nil
}

func (b *Bot) sendInputs(ctx context.Context, chatID int64, inputs []InputFile) ([]Message, error) {
	if len(inputs) == 1 {
		message, err := b.opts.API.SendFile(ctx, chatID, inputs[0])
		return []Message{message}, err
	}
	return b.opts.API.SendAlbum(ctx, chatID, inputs)
}

// inputFile is a file of a request as Telegram is sent it: by the id Telegram gave the same
// content before, else uploaded from the disk.
func (b *Bot) inputFile(ctx context.Context, file leads.Attachment, kind string) InputFile {
	input := InputFile{Kind: kind, Name: file.Filename, Open: func() (io.ReadCloser, error) {
		_, content, err := b.opts.Leads.OpenAttachment(ctx, file.LeadID, file.ID)
		if err != nil {
			return nil, err
		}
		return content, nil
	}}
	if len(file.SHA256) == sha256.Size {
		err := b.opts.DB.QueryRowContext(ctx, `SELECT file_id FROM telegram_uploads WHERE sha256 = ? AND kind = ?`, file.SHA256, kind).Scan(&input.FileID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
			b.opts.Log.Warn("telegram: cannot look up a known file", "error", err) // it is uploaded again, that is all
		}
	}
	return input
}

func (b *Bot) rememberFile(ctx context.Context, sum []byte, kind, fileID string) {
	if len(sum) != sha256.Size || len(fileID) > 255 {
		return
	}
	if _, err := b.opts.DB.ExecContext(ctx, `
		INSERT INTO telegram_uploads (sha256, kind, file_id, updated_at) VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE file_id = VALUES(file_id), updated_at = VALUES(updated_at)`, sum, kind, fileID, b.opts.Now().UTC()); err != nil && ctx.Err() == nil {
		b.opts.Log.Warn("telegram: cannot remember a sent file", "error", err)
	}
}

func (b *Bot) forgetFile(ctx context.Context, sum []byte, kind string) {
	_, _ = b.opts.DB.ExecContext(ctx, `DELETE FROM telegram_uploads WHERE sha256 = ? AND kind = ?`, sum, kind)
}

// badRequest: Telegram refused what was sent, not the chat — a file, an id, a size.
func badRequest(err error) bool {
	var refused *APIError
	return errors.As(err, &refused) && refused.Code == http.StatusBadRequest && !refused.Gone()
}

// refusedForGood: what no retry changes — a request Telegram cannot take, a file too big for it.
func refusedForGood(err error) bool {
	var refused *APIError
	return errors.As(err, &refused) && (refused.Code == http.StatusBadRequest || refused.Code == http.StatusRequestEntityTooLarge)
}

// Timeout gives an answer with files the time its upload may need (outbox.Patient): half a minute
// for the text and two seconds a megabyte — a slow line — for the files; the outbox allows up to
// five minutes.
func (b *Bot) Timeout(ctx context.Context, task outbox.Task) time.Duration {
	if task.Kind != leads.TaskReply && task.Kind != leads.TaskSiteReply {
		return 0
	}
	var payload leads.TaskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return 0
	}
	files, err := b.opts.Leads.MessageFiles(ctx, payload.LeadID, payload.MessageID)
	if err != nil {
		return 0
	}
	var size int64
	for _, file := range files {
		size += file.Size
	}
	return 30*time.Second + time.Duration(size>>20)*2*time.Second
}
