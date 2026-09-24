// Package tgtest is a pretend Telegram Bot API for tests: it takes the calls the bot makes,
// remembers them, refuses them on demand, and hands updates out to long polling.
package tgtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Token is the only token the pretend API accepts.
const Token = "123456:TEST-token-that-must-never-be-logged"

// Call is one request the bot made.
type Call struct {
	Method string
	Params map[string]any
	Files  []File // uploaded with it (multipart/form-data)
}

// File is a file the bot uploaded.
type File struct {
	Field, Name string
	Size        int
	SHA256      string // hex, of the content
}

// FileID is what the pretend API calls an uploaded file: the same content always gets the same id,
// so a test sees whether the bot sent a file again by its id.
func (f File) FileID(kind string) string { return "file-" + f.SHA256[:12] + "-" + kind }

// Text returns the «text» of a call (messages and edits have one).
func (c Call) Text() string {
	text, _ := c.Params["text"].(string)
	return text
}

// ChatID returns the chat a call was about.
func (c Call) ChatID() int64 {
	id, _ := c.Params["chat_id"].(float64)
	return int64(id)
}

// Buttons returns the labels of the inline buttons of a call, row by row, and what each reports.
func (c Call) Buttons() (labels []string, data map[string]string) {
	data = map[string]string{}
	markup, _ := c.Params["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	for _, row := range rows {
		buttons, _ := row.([]any)
		for _, item := range buttons {
			button, _ := item.(map[string]any)
			label, _ := button["text"].(string)
			labels = append(labels, label)
			if value, ok := button["callback_data"].(string); ok {
				data[label] = value
			} else if value, ok := button["url"].(string); ok {
				data[label] = value
			}
		}
	}
	return labels, data
}

type failure struct {
	code        int
	description string
	retryAfter  int
	times       int // how many calls still fail; negative — all of them
}

// Server is the pretend API.
type Server struct {
	URL string

	mu        sync.Mutex
	calls     []Call
	failures  map[string]*failure
	updates   []json.RawMessage
	messageID int64
}

// Start runs the pretend API until the test ends.
func Start(t testing.TB) *Server {
	t.Helper()
	s := &Server{failures: map[string]*failure{}, messageID: 1000}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	s.URL = server.URL
	return s
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	prefix := "/bot" + Token + "/"
	w.Header().Set("Content-Type", "application/json")
	if !strings.HasPrefix(r.URL.Path, prefix) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, prefix)
	var params map[string]any
	var files []File
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		params, files = readForm(w, r)
	} else {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &params)
	}

	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Params: params, Files: files})
	refusal := s.failures[method]
	if refusal != nil && refusal.times != 0 {
		if refusal.times > 0 {
			refusal.times--
		}
	} else {
		refusal = nil
	}
	s.messageID++
	messageID := s.messageID
	s.mu.Unlock()

	if refusal != nil {
		w.WriteHeader(refusal.code)
		answer := map[string]any{"ok": false, "error_code": refusal.code, "description": refusal.description}
		if refusal.retryAfter > 0 {
			answer["parameters"] = map[string]any{"retry_after": refusal.retryAfter}
		}
		_ = json.NewEncoder(w).Encode(answer)
		return
	}

	var result any = true
	switch method {
	case "getMe":
		result = map[string]any{"id": 123456, "is_bot": true, "first_name": "Krokosha", "username": "krokosha_test_bot"}
	case "sendMessage":
		result = map[string]any{"message_id": messageID, "date": time.Now().Unix(), "chat": map[string]any{"id": params["chat_id"], "type": "private"}, "text": params["text"]}
	case "sendPhoto", "sendVideo", "sendDocument":
		kind := strings.ToLower(strings.TrimPrefix(method, "send"))
		result = sentFile(messageID, params, kind, fileID(params[kind], kind, files))
	case "sendMediaGroup":
		items, _ := params["media"].([]any)
		list := make([]any, 0, len(items))
		for i, item := range items {
			media, _ := item.(map[string]any)
			kind, _ := media["type"].(string)
			list = append(list, sentFile(messageID*100+int64(i), params, kind, fileID(media["media"], kind, files)))
		}
		result = list
	case "getUpdates":
		result = s.takeUpdates(r, params)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// readForm reads a multipart call the way Telegram does: chat_id is a number, media is JSON.
func readForm(w http.ResponseWriter, r *http.Request) (map[string]any, []File) {
	params := map[string]any{}
	// What Telegram takes at most, with room to spare.
	r.Body = http.MaxBytesReader(w, r.Body, 128<<20)
	if err := r.ParseMultipartForm(64 << 20); err != nil { //nolint:gosec // capped by MaxBytesReader above
		return params, nil
	}
	for name, values := range r.MultipartForm.Value {
		value := values[0]
		switch name {
		case "chat_id":
			number, _ := strconv.ParseFloat(value, 64)
			params[name] = number
		case "media":
			var list []any
			_ = json.Unmarshal([]byte(value), &list)
			params[name] = list
		default:
			params[name] = value
		}
	}
	var files []File
	for field, headers := range r.MultipartForm.File {
		for _, header := range headers {
			content, err := header.Open()
			if err != nil {
				continue
			}
			sum := sha256.New()
			size, _ := io.Copy(sum, content)
			_ = content.Close()
			files = append(files, File{Field: field, Name: header.Filename, Size: int(size), SHA256: hex.EncodeToString(sum.Sum(nil))})
		}
	}
	return params, files
}

// fileID names the file of a send: an upload by its content, a known file by the id it was sent by.
func fileID(ref any, kind string, files []File) string {
	id, _ := ref.(string)
	field := strings.TrimPrefix(id, "attach://")
	if id == "" {
		field = kind // a single upload: the field is named after the kind
	}
	for _, file := range files {
		if file.Field == field {
			return file.FileID(kind)
		}
	}
	return id
}

// sentFile is the message Telegram returns for a sent file.
func sentFile(messageID int64, params map[string]any, kind, id string) map[string]any {
	message := map[string]any{"message_id": messageID, "date": time.Now().Unix(), "chat": map[string]any{"id": params["chat_id"], "type": "private"}}
	switch kind {
	case "photo":
		message["photo"] = []any{map[string]any{"file_id": id + "-small"}, map[string]any{"file_id": id}}
	default:
		message[kind] = map[string]any{"file_id": id}
	}
	return message
}

// takeUpdates behaves like long polling: it answers at once when there is something, otherwise
// waits a little (never as long as the real thing — tests have no time for that).
func (s *Server) takeUpdates(r *http.Request, params map[string]any) []json.RawMessage {
	offset, _ := params["offset"].(float64)
	deadline := time.Now().Add(150 * time.Millisecond)
	for {
		s.mu.Lock()
		var ready []json.RawMessage
		for _, raw := range s.updates {
			var head struct {
				UpdateID int64 `json:"update_id"`
			}
			_ = json.Unmarshal(raw, &head)
			if head.UpdateID >= int64(offset) {
				ready = append(ready, raw)
			}
		}
		s.updates = ready // what is below the offset was confirmed and is forgotten
		s.mu.Unlock()
		if len(ready) > 0 || time.Now().After(deadline) || r.Context().Err() != nil {
			if ready == nil {
				ready = []json.RawMessage{}
			}
			return ready
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Push queues an update for long polling.
func (s *Server) Push(update any) {
	raw, err := json.Marshal(update)
	if err != nil {
		panic(fmt.Sprintf("tgtest: %v", err))
	}
	s.mu.Lock()
	s.updates = append(s.updates, raw)
	s.mu.Unlock()
}

// Calls returns the calls of one method ("" — all of them), oldest first.
func (s *Server) Calls(method string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, call := range s.calls {
		if method == "" || call.Method == method {
			out = append(out, call)
		}
	}
	return out
}

// Sent returns the messages sent to one chat.
func (s *Server) Sent(chatID int64) []Call {
	var out []Call
	for _, call := range s.Calls("sendMessage") {
		if call.ChatID() == chatID {
			out = append(out, call)
		}
	}
	return out
}

// Forget drops the remembered calls: what follows in a test starts from a clean page.
func (s *Server) Forget() {
	s.mu.Lock()
	s.calls = nil
	s.mu.Unlock()
}

// Refuse makes the next calls of a method fail the way Telegram does. times < 0 — until Accept.
func (s *Server) Refuse(method string, times, code int, description string, retryAfter int) {
	s.mu.Lock()
	s.failures[method] = &failure{code: code, description: description, retryAfter: retryAfter, times: times}
	s.mu.Unlock()
}

// Accept ends what Refuse started.
func (s *Server) Accept(method string) {
	s.mu.Lock()
	delete(s.failures, method)
	s.mu.Unlock()
}
