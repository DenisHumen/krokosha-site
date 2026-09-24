package telegram

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/server"
)

// How updates reach the bot (brief B10.3).
const (
	// ModeWebhook: Telegram calls us — through nginx, on a path nobody can guess, with a header
	// only Telegram knows. The normal way on a server.
	ModeWebhook = "webhook"
	// ModePolling: we call Telegram and wait. Works behind anything; the fallback, and the way
	// on a developer's machine, where Telegram cannot call in.
	ModePolling = "polling"
)

// WebhookPrefix is where nginx hands Telegram's calls over; the secret part follows it. The API's
// log writes it without the secret (server.loggedPath).
const WebhookPrefix = server.TelegramWebhookPrefix

// Secrets derives the two secrets of the webhook from the secret of the installation: the end
// of the address Telegram calls, and the header it proves itself with. Nothing to configure,
// nothing to keep in sync; both die with APP_SECRET.
func Secrets(appSecret []byte) (path, header string) {
	derive := func(purpose string) string {
		mac := hmac.New(sha256.New, appSecret)
		mac.Write([]byte(purpose))
		return hex.EncodeToString(mac.Sum(nil))
	}
	return derive("telegram webhook path")[:32], derive("telegram webhook header")
}

// Status is what the admin area shows about the bot.
type Status struct {
	Mode         string
	Username     string // empty until Telegram confirmed the token
	ConnectedAt  time.Time
	LastUpdateAt time.Time
	LastError    string
	LastErrorAt  time.Time
}

// RunnerOptions configure how updates are received.
type RunnerOptions struct {
	Mode    string
	SiteURL string // https://krokosha.xyz — the webhook lives under it
	Secret  []byte // APP_SECRET
	Log     *slog.Logger
	// Pause between attempts to reach Telegram; grows up to ten times. Tests make it tiny.
	RetryPause time.Duration
}

// Runner receives updates and feeds them to the bot one at a time, in the order they came.
type Runner struct {
	bot  *Bot
	opts RunnerOptions

	pathSecret, headerSecret string
	queue                    chan Update

	mu      sync.Mutex
	status  Status
	seen    [256]int64 // the last update ids: Telegram repeats a delivery it thinks has failed
	seenAt  int
	closing bool          // the service is stopping: nothing new is taken (drain)
	offset  int64         // polling: the update to confirm to Telegram — the last one taken, plus one
	polled  chan struct{} // closed when polling has stopped
}

// NewRunner builds the receiver.
func NewRunner(bot *Bot, opts RunnerOptions) *Runner {
	if opts.RetryPause <= 0 {
		opts.RetryPause = 5 * time.Second
	}
	r := &Runner{bot: bot, opts: opts, queue: make(chan Update, 256), status: Status{Mode: opts.Mode}, polled: make(chan struct{})}
	r.pathSecret, r.headerSecret = Secrets(opts.Secret)
	return r
}

// WebhookURL is the address given to Telegram.
func (r *Runner) WebhookURL() string {
	return strings.TrimRight(r.opts.SiteURL, "/") + WebhookPrefix + r.pathSecret
}

// Status reports how the bot is doing.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *Runner) failed(what string, err error) {
	r.opts.Log.Error("telegram: "+what, "error", err)
	r.mu.Lock()
	r.status.LastError, r.status.LastErrorAt = what+": "+err.Error(), time.Now()
	r.mu.Unlock()
}

// Register adds the webhook. It is there in both modes — in polling mode it simply never hears
// from Telegram — so that switching modes is a matter of one setting and a restart.
func (r *Runner) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST "+WebhookPrefix+"{secret}", r.webhook)
}

func (r *Runner) webhook(w http.ResponseWriter, request *http.Request) {
	// A wrong address looks like any other address that does not exist.
	if subtle.ConstantTimeCompare([]byte(request.PathValue("secret")), []byte(r.pathSecret)) != 1 {
		http.NotFound(w, request)
		return
	}
	if subtle.ConstantTimeCompare([]byte(request.Header.Get("X-Telegram-Bot-Api-Secret-Token")), []byte(r.headerSecret)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, request.Body, 1<<20))
	if err != nil {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	var update Update
	if err := json.Unmarshal(body, &update); err != nil {
		// Answered with «fine»: Telegram would otherwise repeat what we cannot read for days.
		r.opts.Log.Warn("telegram: an update that is not JSON", "error", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if !r.accept(update) {
		// The queue is full: Telegram keeps the update and delivers it again in a moment.
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// accept queues an update unless it was seen already; false means «no room, come again» — or
// «stopping, come to the next process».
func (r *Runner) accept(update Update) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return false
	}
	for _, id := range r.seen {
		if id == update.UpdateID && id != 0 {
			return true
		}
	}
	select {
	case r.queue <- update:
	default:
		return false
	}
	r.seen[r.seenAt%len(r.seen)] = update.UpdateID
	r.seenAt++
	r.status.LastUpdateAt = time.Now()
	return true
}

// pause waits before the next attempt; false means the service is stopping.
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// retry repeats a call to Telegram until it works, with growing pauses. A refusal that waiting
// cannot cure — a wrong token — is retried too, slowly: the owner fixes the setting and restarts
// the service, but the rest of the site must not care.
func (r *Runner) retry(ctx context.Context, what string, call func(context.Context) error) bool {
	wait := r.opts.RetryPause
	for {
		attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := call(attempt)
		cancel()
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		r.failed(what, err)
		var api *APIError
		if errors.As(err, &api) && api.RetryAfter > wait {
			wait = api.RetryAfter
		}
		if !pause(ctx, wait) {
			return false
		}
		if wait < 10*r.opts.RetryPause {
			wait *= 2
		}
	}
}

// Run connects to Telegram and then handles updates until the context ends.
func (r *Runner) Run(ctx context.Context) {
	var me User
	if !r.retry(ctx, "cannot reach Telegram (getMe)", func(ctx context.Context) (err error) {
		me, err = r.bot.opts.API.GetMe(ctx)
		return err
	}) {
		return
	}
	r.bot.me.Store(&me)
	if !r.retry(ctx, "cannot set the menu of commands", func(ctx context.Context) error { return r.bot.opts.API.SetCommands(ctx, Commands()) }) {
		return
	}
	connect := func(ctx context.Context) error { return r.bot.opts.API.SetWebhook(ctx, r.WebhookURL(), r.headerSecret) }
	if r.opts.Mode == ModePolling {
		connect = r.bot.opts.API.DeleteWebhook
	}
	if !r.retry(ctx, "cannot choose how updates are delivered", connect) {
		return
	}
	r.mu.Lock()
	r.status.Username, r.status.ConnectedAt, r.status.LastError = me.Username, time.Now(), ""
	r.mu.Unlock()
	r.opts.Log.Info("telegram: the bot is connected", "username", me.Username, "mode", r.opts.Mode)

	if r.opts.Mode == ModePolling {
		go r.poll(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			r.drain()
			return
		case update := <-r.queue:
			// One update must not hold up the rest for long, whatever Telegram is doing.
			handling, cancel := context.WithTimeout(ctx, 30*time.Second)
			r.bot.Handle(handling, update)
			cancel()
		}
	}
}

// drainFor is how long a stopping service goes on handling what it has taken.
const drainFor = 20 * time.Second

// drain: the service is stopping — every update.sh restarts it. What was taken is handled before
// it goes: Telegram counts it as delivered and would not bring it again, and a client's message
// would be lost. Nothing new is taken meanwhile: a webhook answers «busy», and Telegram brings the
// update to the next process. In polling mode Telegram is then told what was handled.
func (r *Runner) drain() {
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	deadline := time.Now().Add(drainFor)
	for more := true; more && time.Now().Before(deadline); {
		select {
		case update := <-r.queue:
			handling, cancel := context.WithDeadline(context.Background(), deadline)
			r.bot.Handle(handling, update)
			cancel()
		default:
			more = false
		}
	}
	if r.opts.Mode == ModePolling {
		r.confirm()
	}
}

// confirm tells Telegram which updates are handled: getUpdates with an offset confirms all before it.
// Without this the next process would get them again.
func (r *Runner) confirm() {
	select {
	case <-r.polled: // the long poll has returned: two at once make Telegram answer «conflict»
	case <-time.After(5 * time.Second):
	}
	r.mu.Lock()
	offset := r.offset
	r.mu.Unlock()
	if offset == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.bot.opts.API.GetUpdates(ctx, offset, 0); err != nil {
		r.opts.Log.Warn("telegram: cannot confirm the last updates; the next start may see them again", "error", err)
	}
}

// poll asks Telegram for updates and waits up to 50 seconds for an answer, again and again.
func (r *Runner) poll(ctx context.Context) {
	defer close(r.polled)
	for ctx.Err() == nil {
		r.mu.Lock()
		offset := r.offset
		r.mu.Unlock()
		updates, err := r.bot.opts.API.GetUpdates(ctx, offset, 50*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.failed("cannot get updates", err)
			wait := r.opts.RetryPause
			var api *APIError
			if errors.As(err, &api) && api.RetryAfter > wait {
				wait = api.RetryAfter
			}
			if !pause(ctx, wait) {
				return
			}
			continue
		}
		for _, update := range updates {
			for !r.accept(update) { // the queue is full: wait for the bot to catch up
				if !pause(ctx, 100*time.Millisecond) {
					return
				}
			}
			r.mu.Lock()
			r.offset = update.UpdateID + 1 // confirms the update to Telegram with the next request
			r.mu.Unlock()
		}
	}
}
