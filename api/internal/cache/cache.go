// Package cache is the fast, disposable storage of the service: rate limits now, live counters
// and buffers later (docs/architecture.md §6). It talks to Redis and, whenever Redis is absent or
// down, quietly falls back to the memory of the process — losing Redis never loses data, and the
// API keeps answering.
package cache

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache is safe for concurrent use.
type Cache struct {
	redis *redis.Client // nil: memory only
	log   *slog.Logger
	now   func() time.Time

	mu      sync.Mutex
	windows map[string]*window
	values  map[string]*value
	active  map[string]map[string]time.Time
	sets    map[string]*distinctSet
	// degraded remembers that Redis failed, so the log gets one line per outage, not one per request.
	degraded bool
}

type window struct {
	count   int
	expires time.Time
}

type value struct {
	data    string
	expires time.Time
}

// New connects to Redis at url. An empty url means memory only. A Redis that cannot be reached
// right now is not an error: the cache starts degraded and recovers by itself.
func New(ctx context.Context, url string, log *slog.Logger) (*Cache, error) {
	c := &Cache{
		log: log, now: time.Now,
		windows: map[string]*window{}, values: map[string]*value{}, active: map[string]map[string]time.Time{}, sets: map[string]*distinctSet{},
	}
	if url == "" {
		log.Info("REDIS_URL is not set: rate limits and live data stay in memory")
		return c, nil
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("REDIS_URL is malformed") // never echo the URL: it contains the password
	}
	options.DialTimeout = 2 * time.Second
	options.ReadTimeout = time.Second
	options.WriteTimeout = time.Second
	options.PoolSize = 8
	options.MaxRetries = 1
	c.redis = redis.NewClient(options)
	if err := c.Ping(ctx); err != nil {
		c.markDegraded(err)
	}
	return c, nil
}

// SetClock replaces the time source. For tests that move time forward.
func (c *Cache) SetClock(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// Close releases the Redis connections.
func (c *Cache) Close() error {
	if c.redis == nil {
		return nil
	}
	return c.redis.Close()
}

// ErrDisabled is returned by Ping when the cache works without Redis by configuration.
var ErrDisabled = errors.New("redis is not configured")

// Ping reports the state of Redis for the health endpoint.
func (c *Cache) Ping(ctx context.Context) error {
	if c.redis == nil {
		return ErrDisabled
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.redis.Ping(ctx).Err()
}

func (c *Cache) markDegraded(err error) {
	c.mu.Lock()
	first := !c.degraded
	c.degraded = true
	c.mu.Unlock()
	if first {
		c.log.Warn("Redis is unavailable, falling back to memory", "error", err)
	}
}

func (c *Cache) markHealthy() {
	c.mu.Lock()
	recovered := c.degraded
	c.degraded = false
	c.mu.Unlock()
	if recovered {
		c.log.Info("Redis is back")
	}
}

// allowScript counts a hit in a fixed window and sets the expiry with the first one, atomically.
var allowScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

// Allow counts one action under key and reports whether it is within limit per window.
// It never fails: with Redis down the count continues in memory, per process.
func (c *Cache) Allow(ctx context.Context, key string, limit int, per time.Duration) bool {
	if c.redis != nil {
		count, err := allowScript.Run(ctx, c.redis, []string{"rl:" + key}, per.Milliseconds()).Int()
		if err == nil {
			c.markHealthy()
			return count <= limit
		}
		if ctx.Err() != nil {
			return true // the client went away; nothing to protect
		}
		c.markDegraded(err)
	}
	return c.allowInMemory(key, limit, per)
}

func (c *Cache) allowInMemory(key string, limit int, per time.Duration) bool {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.windows[key]
	if !ok || now.After(w.expires) {
		w = &window{expires: now.Add(per)}
		c.windows[key] = w
	}
	w.count++
	// Opportunistic clean-up keeps the map from growing under an address scan.
	if len(c.windows) > 10_000 {
		for k, v := range c.windows {
			if now.After(v.expires) {
				delete(c.windows, k)
			}
		}
	}
	return w.count <= limit
}
