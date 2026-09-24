package achievements

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/server"
)

const (
	// MinPlayers: below it a share says more about chance than about the egg, and the site shows none.
	MinPlayers = 10
	// RefreshEvery: the shares are recomputed from the daily counts this often.
	RefreshEvery = 5 * time.Minute

	helloPerHour = 6  // a browser says hello once; a few more for people behind one address
	foundPerHour = 40 // eight eggs, «all», and the receipts of eggs found before this existed
)

// Options configure the service.
type Options struct {
	DB     *sql.DB
	Cache  *cache.Cache
	Log    *slog.Logger
	Secret []byte // APP_SECRET: signs receipts and keys the rate limits
	// Location is the owner's time zone: the days of the counts are theirs.
	Location *time.Location
	// IgnoreCookie: requests carrying it are the owner's own; they get receipts but are not counted.
	IgnoreCookie string
	Now          func() time.Time
}

// Service counts players and finds, and keeps the shares.
type Service struct {
	opts Options

	mu     sync.Mutex
	shares Shares // the last read of the table, for the public endpoint
	read   time.Time
}

// Shares are the public statistics: the share of players who found each achievement.
type Shares struct {
	Updated time.Time          `json:"updated"`
	Eggs    map[string]float64 `json:"eggs"` // empty while there are fewer than MinPlayers
}

// Stat is one line of the admin's table.
type Stat struct {
	ID      string
	Found   int
	Players int
	Percent float64
}

// New builds the service.
func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	return &Service{opts: opts}
}

// count adds one to the day's row of id.
func (s *Service) count(ctx context.Context, id string) error {
	day := s.opts.Now().In(s.opts.Location).Format(time.DateOnly)
	_, err := s.opts.DB.ExecContext(ctx, `INSERT INTO achievement_daily (day, id, n) VALUES (?, ?, 1) ON DUPLICATE KEY UPDATE n = n + 1`, day, id)
	return err
}

// Refresh recomputes the totals and the shares from the daily counts.
func (s *Service) Refresh(ctx context.Context) error {
	rows, err := s.opts.DB.QueryContext(ctx, `SELECT id, SUM(n) FROM achievement_daily GROUP BY id`)
	if err != nil {
		return err
	}
	totals := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			_ = rows.Close()
			return err
		}
		totals[id] = n
	}
	if err := rows.Close(); err != nil {
		return err
	}

	now := s.opts.Now().UTC()
	tx, err := s.opts.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range append(append([]string{}, Eggs...), All) {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO achievements (id, found, players, percent, updated_at) VALUES (?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE found = VALUES(found), players = VALUES(players), percent = VALUES(percent), updated_at = VALUES(updated_at)`,
			id, totals[id], totals[players], share(totals[id], totals[players]), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// share is found / players in per cent, two decimals, never above 100: a browser that never said
// hello (its request was lost) still counts its finds.
func share(found, players int) float64 {
	if players <= 0 || found <= 0 {
		return 0
	}
	return math.Min(100, math.Round(float64(found)*10000/float64(players))/100)
}

// Stats reads the table for the admin area: every achievement, in the order of the eggs.
func (s *Service) Stats(ctx context.Context) ([]Stat, time.Time, error) {
	rows, err := s.opts.DB.QueryContext(ctx, `SELECT id, found, players, percent, updated_at FROM achievements`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	byID := map[string]Stat{}
	var updated time.Time
	for rows.Next() {
		var stat Stat
		var at time.Time
		if err := rows.Scan(&stat.ID, &stat.Found, &stat.Players, &stat.Percent, &at); err != nil {
			return nil, time.Time{}, err
		}
		byID[stat.ID] = stat
		if at.After(updated) {
			updated = at
		}
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	out := make([]Stat, 0, len(Eggs)+1)
	for _, id := range append(append([]string{}, Eggs...), All) {
		stat, ok := byID[id]
		if !ok {
			stat = Stat{ID: id}
		}
		out = append(out, stat)
	}
	return out, updated, nil
}

// Daily returns the counts of the days in [from, to]: day → id → n.
func (s *Service) Daily(ctx context.Context, from, to time.Time) (map[string]map[string]int, error) {
	rows, err := s.opts.DB.QueryContext(ctx, `SELECT day, id, n FROM achievement_daily WHERE day BETWEEN ? AND ? ORDER BY day`,
		from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var day time.Time
		var id string
		var n int
		if err := rows.Scan(&day, &id, &n); err != nil {
			return nil, err
		}
		key := day.Format(time.DateOnly)
		if out[key] == nil {
			out[key] = map[string]int{}
		}
		out[key][id] = n
	}
	return out, rows.Err()
}

// Shares returns the public statistics, read from the table at most once a minute.
func (s *Service) Shares(ctx context.Context) (Shares, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.read.IsZero() && s.opts.Now().Sub(s.read) < time.Minute {
		return s.shares, nil
	}
	stats, updated, err := s.Stats(ctx)
	if err != nil {
		return Shares{}, err
	}
	out := Shares{Updated: updated, Eggs: map[string]float64{}}
	for _, stat := range stats {
		if stat.Players >= MinPlayers {
			out.Eggs[stat.ID] = stat.Percent
		}
	}
	s.shares, s.read = out, s.opts.Now()
	return out, nil
}

// Run refreshes the shares shortly after the start and then every few minutes.
func (s *Service) Run(ctx context.Context) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			s.opts.Log.Error("achievements: cannot refresh the shares", "error", err)
		}
		timer.Reset(RefreshEvery)
	}
}

// --- HTTP ------------------------------------------------------------------------------------------

// Register adds the routes of the public API.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/eggs", s.serveShares)
	mux.HandleFunc("POST /api/eggs/hello", s.hello)
	mux.HandleFunc("POST /api/eggs/{id}", s.found)
}

func (s *Service) serveShares(w http.ResponseWriter, r *http.Request) {
	shares, err := s.Shares(r.Context())
	if err != nil {
		s.opts.Log.Error("achievements: cannot read the shares", "error", err)
		server.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "not_ready"})
		return
	}
	// The numbers change every few minutes; a browser may keep them for as long.
	w.Header().Set("Cache-Control", "public, max-age=300")
	server.WriteJSON(w, http.StatusOK, shares)
}

// visitor says what the request is: from another site (refused), a robot's (not counted, nothing
// signed), the owner's (signed, not counted), or a player's.
type visitor int

const (
	foreign visitor = iota
	robot
	owner
	player
)

func (s *Service) who(r *http.Request) visitor {
	if origin := r.Header.Get("Origin"); origin != "" {
		if parsed, err := url.Parse(origin); err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			return foreign
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return foreign
	}
	if analytics.IsBot(r.UserAgent()) {
		return robot
	}
	if s.opts.IgnoreCookie != "" {
		if _, err := r.Cookie(s.opts.IgnoreCookie); err == nil {
			return owner
		}
	}
	return player
}

// allow is the rate limit per address; the address itself never reaches the cache, its keyed hash does.
func (s *Service) allow(r *http.Request, action string, limit int) bool {
	h := hmac.New(sha256.New, s.opts.Secret)
	h.Write([]byte("eggs:"))
	if ip := server.ClientIP(r.Context()); ip != nil {
		h.Write(ip)
	}
	return s.opts.Cache.Allow(r.Context(), "eggs-"+action+":"+hex.EncodeToString(h.Sum(nil)[:8]), limit, time.Hour)
}

// optedOut: «Do Not Track» or Global Privacy Control — the browser gets its receipts (the discount
// depends on them), and is counted nowhere.
func optedOut(r *http.Request) bool {
	return r.Header.Get("DNT") == "1" || r.Header.Get("Sec-GPC") == "1"
}

// hello counts a browser that found its first egg: the players the shares are of.
func (s *Service) hello(w http.ResponseWriter, r *http.Request) {
	switch s.who(r) {
	case foreign:
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request"})
		return
	case player:
		if !optedOut(r) && s.allow(r, "hello", helloPerHour) {
			if err := s.count(r.Context(), players); err != nil {
				s.opts.Log.Error("achievements: cannot count a player", "error", err)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// foundBody is what the page sends with «all»: the receipts of every egg.
type foundBody struct {
	Receipts []string `json:"receipts"`
}

// found counts a find and answers with its receipt. «all» is signed only for the receipts of every
// egg, and says how long it took from the first to the last.
func (s *Service) found(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !Known(id) {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_achievement"})
		return
	}
	visitor := s.who(r)
	switch visitor {
	case foreign:
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request"})
		return
	case robot:
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.allow(r, "found", foundPerHour) {
		server.WriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
		return
	}
	now := s.opts.Now()
	receipt := Sign(s.opts.Secret, id, now)
	if id == All {
		var body foundBody
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		if err := decoder.Decode(&body); err != nil || len(body.Receipts) != len(Eggs) {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "receipts_required"})
			return
		}
		first, last, err := Complete(s.opts.Secret, body.Receipts)
		if err != nil {
			server.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "bad_receipts"})
			return
		}
		receipt = SignAll(s.opts.Secret, now, last.Sub(first))
	}
	if visitor == player && !optedOut(r) {
		if err := s.count(r.Context(), id); err != nil && !errors.Is(err, context.Canceled) {
			s.opts.Log.Error("achievements: cannot count a find", "achievement", id, "error", err)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	server.WriteJSON(w, http.StatusOK, map[string]string{"receipt": receipt})
}
