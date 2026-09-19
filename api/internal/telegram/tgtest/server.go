// Package tgtest is a pretend Telegram Bot API for tests: it takes the calls the bot makes,
// remembers them, refuses them on demand, and hands updates out to long polling.
package tgtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
}

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
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &params)

	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: method, Params: params})
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
	case "getUpdates":
		result = s.takeUpdates(r, params)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
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
