package analytics

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
)

const (
	sessionIdle   = 30 * time.Minute
	batchesPerMin = 120 // per address; a page sends a handful
	// ActiveSet is the name of the «now on the site» set in the cache.
	ActiveSet = "visitors"
)

// Options configure the service.
type Options struct {
	DB    *sql.DB
	Cache *cache.Cache
	Log   *slog.Logger
	// SiteHost is the public host name: links from the site to itself are not referrals.
	SiteHost string
	// Location is the owner's time zone: «today» in reports is their day, not the server's.
	Location *time.Location
	Now      func() time.Time
}

// Service accepts event batches and stores them.
type Service struct {
	opts   Options
	salts  *salts
	writer *writer

	mu        sync.Mutex
	listeners map[chan Live]struct{}
}

// Live is what the admin's activity feed shows about an accepted batch. No identifiers beyond
// the daily pseudonym, nothing that is not stored anyway.
type Live struct {
	At      time.Time
	Visitor string // first 8 hex characters of the daily pseudonym: enough to tell visitors apart on screen
	Path    string
	Type    string
	Target  string
}

// New builds the service. Call Run to start storing.
func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	return &Service{
		opts:      opts,
		salts:     newSalts(opts.DB),
		writer:    newWriter(opts.DB, opts.Log),
		listeners: map[chan Live]struct{}{},
	}
}

// Register adds the public endpoint to the mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/e", s.handleEvents)
}

// Run stores queued records until ctx is cancelled (and then what is left).
func (s *Service) Run(ctx context.Context) { s.writer.run(ctx) }

// Subscribe streams accepted events to the admin's live feed until cancel is called.
func (s *Service) Subscribe() (feed <-chan Live, cancel func()) {
	channel := make(chan Live, 64)
	s.mu.Lock()
	s.listeners[channel] = struct{}{}
	s.mu.Unlock()
	return channel, func() {
		s.mu.Lock()
		if _, ok := s.listeners[channel]; ok {
			delete(s.listeners, channel)
			close(channel)
		}
		s.mu.Unlock()
	}
}

func (s *Service) publish(item Live) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for listener := range s.listeners {
		select {
		case listener <- item:
		default: // a slow admin tab must not hold up visitors
		}
	}
}

// handleEvents answers 204 to everything it accepts or silently ignores — a page has no use for
// the reason — and 4xx only to requests that are malformed or too many.
func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request) {
	// The page script already stays silent for these visitors; this is the second lock on the door.
	if r.Header.Get("DNT") == "1" || r.Header.Get("Sec-GPC") == "1" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Browsers say where a request comes from; other sites have no business posting here.
	if origin := r.Header.Get("Origin"); origin != "" {
		if parsed, err := url.Parse(origin); err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request"})
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "unreadable body"})
		return
	}
	if len(body) > maxBodyBytes {
		server.WriteJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body too large"})
		return
	}
	batch, err := ParseBatch(body)
	if err != nil {
		s.opts.Log.Debug("rejected analytics batch", "error", err)
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}

	userAgent := r.UserAgent()
	if IsBot(userAgent) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ip := server.ClientIP(r.Context())
	if ip == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	now := s.opts.Now()
	salt, err := s.salts.forDay(r.Context(), now)
	if err != nil {
		s.opts.Log.Error("cannot load the daily salt", "error", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	address := addressKey(salt, ip)
	if !s.opts.Cache.Allow(r.Context(), "e:"+hex.EncodeToString(address[:]), batchesPerMin, time.Minute) {
		server.WriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests"})
		return
	}

	rec, err := s.buildRecord(r.Context(), batch, salt, ip, userAgent, now)
	if err != nil {
		s.opts.Log.Error("cannot build an analytics record", "error", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.writer.enqueue(rec)

	visitor := hex.EncodeToString(rec.visitor[:])
	s.opts.Cache.MarkActive(r.Context(), ActiveSet, visitor)
	for _, event := range batch.Events {
		if event.Type == TypeScroll || event.Type == TypeTime || event.Type == TypeSectionTime {
			continue // too chatty for a feed
		}
		s.publish(Live{At: now, Visitor: visitor[:8], Path: batch.Path, Type: event.Type, Target: event.Target})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) buildRecord(ctx context.Context, batch *Batch, salt [32]byte, ip []byte, userAgent string, now time.Time) (record, error) {
	rec := record{
		visitor:  visitorID(salt, ip, userAgent),
		path:     batch.Path,
		lang:     batch.Lang,
		utm:      batch.UTM,
		isAd:     IsPaid(batch.UTM, batch.AdClick),
		ipPrefix: TruncateIP(ip),
		client:   ParseUserAgent(userAgent),
	}
	raw, _ := hex.DecodeString(batch.Pageview) // validated by ParseBatch
	copy(rec.pageviewID[:], raw)
	rec.refKind, rec.refHost = ClassifyReferrer(batch.Referrer, s.opts.SiteHost)

	var fresh [8]byte
	if _, err := rand.Read(fresh[:]); err != nil {
		return rec, err
	}
	stored := s.opts.Cache.Touch(ctx, "as:"+hex.EncodeToString(rec.visitor[:]), hex.EncodeToString(fresh[:]), sessionIdle)
	session, err := hex.DecodeString(stored)
	if err != nil || len(session) != 8 {
		return rec, errors.New("malformed session id in the cache")
	}
	copy(rec.session[:], session)

	// The batch is sent right after its last event, so the page started about «largest offset» ago.
	var latest int64
	for _, event := range batch.Events {
		latest = max(latest, event.Offset)
	}
	rec.startedAt = now.Add(-time.Duration(latest) * time.Millisecond).UTC()
	rec.day = rec.startedAt.In(s.opts.Location).Format(time.DateOnly)

	for _, event := range batch.Events {
		switch event.Type {
		case TypePageview:
		case TypeScroll:
			rec.maxScroll = max(rec.maxScroll, event.Value)
		case TypeTime:
			rec.durationMs = max(rec.durationMs, event.Value)
		default:
			rec.events = append(rec.events, storedEvent{
				occurredAt: rec.startedAt.Add(time.Duration(event.Offset) * time.Millisecond),
				kind:       event.Type,
				target:     event.Target,
				value:      event.Value,
			})
		}
	}
	return rec, nil
}
