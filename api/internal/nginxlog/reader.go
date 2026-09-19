package nginxlog

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // a fingerprint that tells one log file from another, not a security measure
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
)

const (
	pollEvery       = 10 * time.Second
	maxLinesPerPoll = 50_000 // a burst is digested in several polls rather than one huge transaction
	maxLineBytes    = 64 << 10
	clientsTTL      = 3 * time.Hour // an hour's counter is final well before that
	// DefaultMaxPaths bounds the distinct addresses kept per day (see aggregate.storePaths).
	DefaultMaxPaths = 2000
)

// Options configure the reader.
type Options struct {
	Path     string // nginx's JSON access log
	DB       *sql.DB
	Cache    *cache.Cache
	Location *time.Location // days of the reports are the owner's days
	Log      *slog.Logger
	MaxPaths int
}

// Reader follows the access log and stores aggregates.
type Reader struct {
	opts Options
	// warned keeps the journal quiet: a missing log is reported once, not every ten seconds.
	warned bool
	// polled is the time of the last poll that went well, in Unix nanoseconds.
	polled atomic.Int64
}

// LastPoll says when the log was last looked at successfully — whether or not there was
// anything new in it. Zero before the first poll.
func (r *Reader) LastPoll() time.Time {
	if nanos := r.polled.Load(); nanos != 0 {
		return time.Unix(0, nanos)
	}
	return time.Time{}
}

// New builds a reader. Run starts it.
func New(opts Options) *Reader {
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	if opts.MaxPaths <= 0 {
		opts.MaxPaths = DefaultMaxPaths
	}
	return &Reader{opts: opts}
}

// Run polls the log until ctx is cancelled.
func (r *Reader) Run(ctx context.Context) {
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		for { // catch up: a backlog is read poll after poll without waiting
			lines, err := r.Poll(ctx)
			if err != nil && ctx.Err() == nil {
				r.opts.Log.Error("cannot read the access log", "path", r.opts.Path, "error", err)
			}
			if err != nil || lines < maxLinesPerPoll {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type position struct {
	fingerprint [16]byte
	offset      int64
	known       bool
}

// Poll reads what was appended since the last call and stores it. It returns the number of
// lines digested. Reading and remembering the position happen in one transaction, so a crash in
// between neither loses nor double-counts a request.
func (r *Reader) Poll(ctx context.Context) (int, error) {
	state, err := r.loadPosition(ctx)
	if err != nil {
		return 0, err
	}

	current, err := fingerprint(r.opts.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, errEmpty):
		if errors.Is(err, fs.ErrNotExist) && !r.warned {
			r.warned = true
			r.opts.Log.Warn("the access log is not there yet: the traffic dashboard stays empty", "path", r.opts.Path)
		}
		return 0, nil
	case errors.Is(err, fs.ErrPermission):
		if !r.warned {
			r.warned = true
			r.opts.Log.Warn("the access log is not readable for the service (it should be in the group adm)", "path", r.opts.Path)
		}
		return 0, nil
	case err != nil:
		return 0, err
	}
	r.warned = false

	agg := newAggregate(r.opts.Location)
	next := position{fingerprint: current, known: true}

	switch {
	case !state.known:
		// First run: take the history that is there.
	case state.fingerprint == current:
		next.offset = state.offset
	default:
		// The log was rotated. Whatever nginx wrote to the old file after our last visit is in
		// «<path>.1» (logrotate's delaycompress keeps it uncompressed for a day).
		rotated := r.opts.Path + ".1"
		if old, err := fingerprint(rotated); err == nil && old == state.fingerprint {
			if _, err := r.digest(rotated, state.offset, agg); err != nil {
				return 0, err
			}
		} else {
			r.opts.Log.Warn("the access log was rotated and its previous file is gone: some requests are not counted", "path", r.opts.Path)
		}
	}

	offset, err := r.digest(r.opts.Path, next.offset, agg)
	if err != nil {
		return 0, err
	}
	next.offset = offset
	if agg.lines == 0 && next == state {
		r.polled.Store(time.Now().UnixNano())
		return 0, nil
	}
	if agg.skipped > 0 {
		r.opts.Log.Warn("skipped lines of the access log that are not in the expected format", "lines", agg.skipped)
	}
	if err := r.store(ctx, agg, next); err != nil {
		return 0, err
	}
	r.polled.Store(time.Now().UnixNano())
	return agg.lines, nil
}

// digest reads complete lines from offset on and returns the offset after the last one.
// A file that is shorter than the offset was truncated in place: it is read from the start.
func (r *Reader) digest(path string, offset int64, agg *aggregate) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil {
		return offset, err
	} else if info.Size() < offset {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}

	reader := bufio.NewReaderSize(file, 256<<10)
	for agg.lines+agg.skipped < maxLinesPerPoll {
		line, err := reader.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			// Longer than any honest request line: skip to its end.
			skipped := int64(len(line))
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = reader.ReadSlice('\n')
				skipped += int64(len(line))
			}
			if err != nil {
				return offset, nil // the monster is still being written
			}
			offset += skipped
			agg.skipped++
			continue
		case err != nil:
			return offset, nil // EOF, possibly in the middle of a line: the rest comes with the next poll
		}
		offset += int64(len(line))
		if len(line) > maxLineBytes {
			agg.skipped++
			continue
		}
		entry, err := ParseLine(bytes.TrimSpace(line))
		if err != nil {
			agg.skipped++
			continue
		}
		agg.add(entry)
	}
	return offset, nil
}

func (r *Reader) loadPosition(ctx context.Context) (position, error) {
	var state position
	var print []byte
	err := r.opts.DB.QueryRowContext(ctx, `SELECT fingerprint, position FROM traffic_state WHERE log = ?`, stateKey(r.opts.Path)).
		Scan(&print, &state.offset)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	copy(state.fingerprint[:], print)
	state.known = true
	return state, nil
}

func (r *Reader) store(ctx context.Context, agg *aggregate, next position) error {
	tx, err := r.opts.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := agg.store(ctx, tx, r.opts.MaxPaths); err != nil {
		return err
	}
	// Distinct clients: the counters forget nothing when told the same client twice, so a
	// transaction that fails and is repeated does no harm.
	for hour, groups := range agg.clients {
		counts := map[bool]int{}
		for isBot, hashes := range groups {
			members := make([]string, 0, len(hashes))
			for hash := range hashes {
				members = append(members, hash)
			}
			kind := "other"
			if isBot {
				kind = "bots"
			}
			counts[isBot] = r.opts.Cache.AddDistinct(ctx, "clients:"+kind+":"+hour.Format("2006010215"), members, clientsTTL)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO traffic_hours (hour, bot_clients, other_clients) VALUES (?, ?, ?)
			ON DUPLICATE KEY UPDATE bot_clients = GREATEST(bot_clients, VALUES(bot_clients)),
				other_clients = GREATEST(other_clients, VALUES(other_clients))`, hour, counts[true], counts[false]); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO traffic_state (log, fingerprint, position, updated_at) VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE fingerprint = VALUES(fingerprint), position = VALUES(position), updated_at = VALUES(updated_at)`,
		stateKey(r.opts.Path), next.fingerprint[:], next.offset, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// stateKey fits the column: the tail of a long path identifies the log well enough.
func stateKey(path string) string {
	if len(path) > 100 {
		return path[len(path)-100:]
	}
	return path
}

var errEmpty = errors.New("no complete line yet")

// fingerprint identifies a log file by its first line. nginx writes the time of the request
// first, so two files never start alike — and unlike an inode number the fingerprint survives
// a restart, a copy to another server, and works on every operating system.
func fingerprint(path string) ([16]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [16]byte{}, err
	}
	defer file.Close()
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return [16]byte{}, err
	}
	head = head[:n]
	if end := bytes.IndexByte(head, '\n'); end >= 0 {
		head = head[:end]
	} else if n < len(head) {
		return [16]byte{}, errEmpty // the first line is still being written
	}
	return md5.Sum(head), nil //nolint:gosec // see the import
}
