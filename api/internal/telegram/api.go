// Package telegram is the bot people work with requests in (brief B10.3–B10.5): access by
// invitation, cards of requests with buttons, answers to clients, relaying for clients who
// continued in Telegram.
//
// This file is the little of the Bot API the bot needs, spoken over plain HTTPS: no library,
// nothing that could pull the token into a log.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultAPI is where Telegram listens. Tests and the staging machine point elsewhere.
const DefaultAPI = "https://api.telegram.org"

// User is a Telegram account.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	// LanguageCode is the language of the person's Telegram: strangers are greeted in it.
	LanguageCode string `json:"language_code"`
}

// DisplayName is how a person is called on cards: what they call themselves in Telegram.
func (u User) DisplayName() string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	if name == "" {
		name = fmt.Sprintf("id%d", u.ID)
	}
	if runes := []rune(name); len(runes) > 64 {
		name = string(runes[:64])
	}
	return name
}

// Chat is where a message was written. The bot works in private chats only.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private | group | supergroup | channel
}

// Message is an incoming or a sent message.
type Message struct {
	MessageID      int64    `json:"message_id"`
	From           *User    `json:"from"`
	Chat           Chat     `json:"chat"`
	Date           int64    `json:"date"`
	Text           string   `json:"text"`
	ReplyToMessage *Message `json:"reply_to_message"`
}

// CallbackQuery is a press of an inline button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Update is one thing that happened. Only messages and button presses are asked for.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// Button is an inline button: it either reports Data back to the bot or opens URL.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Keyboard is the rows of buttons under a message.
type Keyboard [][]Button

type replyMarkup struct {
	InlineKeyboard Keyboard `json:"inline_keyboard,omitempty"`
	ForceReply     bool     `json:"force_reply,omitempty"`
	Placeholder    string   `json:"input_field_placeholder,omitempty"`
}

// Outgoing is a message to send, or the new look of one that was sent.
type Outgoing struct {
	ChatID int64
	Text   string // HTML: everything that did not come from this program goes through Escape
	// Buttons under the message; nil — none.
	Buttons Keyboard
	// ForceReply opens the reply field for the person: the next thing they type answers this message.
	ForceReply  bool
	Placeholder string
	ReplyTo     int64
	Silent      bool // no sound: the digest at night, a card for somebody who muted the bot
}

// Escape makes any text safe inside an HTML message: Telegram knows three special characters.
func Escape(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}

// APIError is Telegram saying no.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration // set with 429: the pause Telegram asks for
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// Gone reports whether the chat will never take messages again: the person blocked the bot or
// deleted the account. Retrying is pointless.
func (e *APIError) Gone() bool {
	return e.Code == http.StatusForbidden || (e.Code == http.StatusBadRequest && strings.Contains(e.Description, "chat not found"))
}

// IsNotModified: an edit that changes nothing — the card already looks the way it should.
func IsNotModified(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Code == http.StatusBadRequest && strings.Contains(api.Description, "message is not modified")
}

// Client talks to the Bot API.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient builds a client. base is DefaultAPI unless a test or the staging machine says otherwise.
func NewClient(token, base string) *Client {
	if base == "" {
		base = DefaultAPI
	}
	return &Client{
		base: strings.TrimRight(base, "/"), token: token,
		// Long polling holds a request for 50 seconds; everything else answers in a moment.
		http: &http.Client{Timeout: 70 * time.Second},
	}
}

// call runs one method. The address contains the token, and errors of net/http quote the address:
// every error from here is rewritten so that the token never reaches a log or a screen.
func (c *Client) call(ctx context.Context, method string, params, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: cannot build the request", method)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("telegram %s: %s", method, strings.ReplaceAll(err.Error(), c.token, "<token>"))
	}
	defer response.Body.Close()

	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("telegram %s: reading the answer failed", method)
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return &APIError{Method: method, Code: response.StatusCode, Description: "the answer is not JSON"}
	}
	if !envelope.OK {
		code := envelope.ErrorCode
		if code == 0 {
			code = response.StatusCode
		}
		return &APIError{Method: method, Code: code, Description: envelope.Description, RetryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second}
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, result)
}

// GetMe asks who the token belongs to — the check that the token works.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var me User
	err := c.call(ctx, "getMe", struct{}{}, &me)
	return me, err
}

func (o Outgoing) params() map[string]any {
	params := map[string]any{
		"chat_id": o.ChatID, "text": o.Text, "parse_mode": "HTML",
		// A link in a client's text must not unfold into a preview of somebody's page.
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	switch {
	case o.ForceReply:
		params["reply_markup"] = replyMarkup{ForceReply: true, Placeholder: o.Placeholder}
	case o.Buttons != nil:
		params["reply_markup"] = replyMarkup{InlineKeyboard: o.Buttons}
	}
	if o.ReplyTo != 0 {
		params["reply_parameters"] = map[string]any{"message_id": o.ReplyTo, "allow_sending_without_reply": true}
	}
	if o.Silent {
		params["disable_notification"] = true
	}
	return params
}

// Send sends a message and returns what Telegram made of it (the id is what matters).
func (c *Client) Send(ctx context.Context, message Outgoing) (Message, error) {
	var sent Message
	err := c.call(ctx, "sendMessage", message.params(), &sent)
	return sent, err
}

// Edit replaces the text and the buttons of a sent message. «Nothing changed» is not an error.
func (c *Client) Edit(ctx context.Context, messageID int64, message Outgoing) error {
	params := message.params()
	params["message_id"] = messageID
	delete(params, "reply_parameters")
	delete(params, "disable_notification")
	if message.Buttons == nil {
		params["reply_markup"] = replyMarkup{InlineKeyboard: Keyboard{}}
	}
	err := c.call(ctx, "editMessageText", params, nil)
	if IsNotModified(err) {
		return nil
	}
	return err
}

// AnswerCallback ends the spinner on a pressed button; alert shows the text in a window the
// person has to close — for things they must not miss («уже взял Денис»).
func (c *Client) AnswerCallback(ctx context.Context, id, text string, alert bool) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text, "show_alert": alert}, nil)
}

// allowedUpdates: the bot asks for nothing else, so nothing else is ever delivered.
var allowedUpdates = []string{"message", "callback_query"}

// SetWebhook tells Telegram where to deliver updates. secret comes back in a header of every
// delivery, which is how the receiver knows the sender.
func (c *Client) SetWebhook(ctx context.Context, url, secret string) error {
	return c.call(ctx, "setWebhook", map[string]any{
		"url": url, "secret_token": secret, "allowed_updates": allowedUpdates, "max_connections": 4,
	}, nil)
}

// DeleteWebhook switches to «we ask, you answer» (long polling).
func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", map[string]any{}, nil)
}

// GetUpdates waits up to the given time for updates after offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, wait time.Duration) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": int(wait.Seconds()), "allowed_updates": allowedUpdates,
	}, &updates)
	return updates, err
}

// Command is an entry of the bot's menu.
type Command struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetCommands fills the menu next to the input field.
func (c *Client) SetCommands(ctx context.Context, commands []Command) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil)
}
