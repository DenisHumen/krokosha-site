package cache

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// touchScript returns the value under the key and extends its life, or stores a fresh one —
// atomically, so two requests of one visitor arriving together get the same session.
var touchScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return current
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return ARGV[1]
`)

// Touch returns the value stored under key and extends its life by ttl; when there is none
// (or it expired) it stores fresh and returns that. Used for visit sessions: «the same visitor,
// no longer than 30 minutes between requests».
func (c *Cache) Touch(ctx context.Context, key, fresh string, ttl time.Duration) string {
	if c.redis != nil {
		stored, err := touchScript.Run(ctx, c.redis, []string{"v:" + key}, fresh, ttl.Milliseconds()).Text()
		if err == nil {
			c.markHealthy()
			return stored
		}
		if ctx.Err() != nil {
			return fresh
		}
		c.markDegraded(err)
	}

	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.values[key]; ok && now.Before(v.expires) {
		v.expires = now.Add(ttl)
		return v.data
	}
	c.values[key] = &value{data: fresh, expires: now.Add(ttl)}
	if len(c.values) > 10_000 {
		for k, v := range c.values {
			if now.After(v.expires) {
				delete(c.values, k)
			}
		}
	}
	return fresh
}

// MarkActive notes that member was seen now. CountActive answers «how many in the last N minutes»
// — the «now on the site» number of the admin dashboard.
func (c *Cache) MarkActive(ctx context.Context, set, member string) {
	now := c.now()
	if c.redis != nil {
		key := "live:" + set
		pipe := c.redis.Pipeline()
		pipe.ZAdd(ctx, key, redis.Z{Score: float64(now.UnixMilli()), Member: member})
		// Anything older than an hour is of no use to anyone; the set cleans itself as it is written.
		pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now.Add(-time.Hour).UnixMilli(), 10))
		pipe.Expire(ctx, key, 2*time.Hour)
		if _, err := pipe.Exec(ctx); err == nil {
			c.markHealthy()
			return
		} else if ctx.Err() == nil {
			c.markDegraded(err)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	members, ok := c.active[set]
	if !ok {
		members = map[string]time.Time{}
		c.active[set] = members
	}
	members[member] = now
	if len(members) > 10_000 {
		for m, seen := range members {
			if now.Sub(seen) > time.Hour {
				delete(members, m)
			}
		}
	}
}

// CountActive returns how many distinct members of set were marked active within the window.
func (c *Cache) CountActive(ctx context.Context, set string, window time.Duration) int {
	since := c.now().Add(-window)
	if c.redis != nil {
		count, err := c.redis.ZCount(ctx, "live:"+set, strconv.FormatInt(since.UnixMilli(), 10), "+inf").Result()
		if err == nil {
			c.markHealthy()
			return int(count)
		}
		if ctx.Err() == nil {
			c.markDegraded(err)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, seen := range c.active[set] {
		if seen.After(since) {
			count++
		}
	}
	return count
}

// Set stores a short-lived value: a login that waits for its one-time code, a dialogue state of
// the bot. Like everything in this package it is best effort — callers must cope with Get
// returning nothing.
func (c *Cache) Set(ctx context.Context, key, data string, ttl time.Duration) {
	if c.redis != nil {
		if err := c.redis.Set(ctx, "v:"+key, data, ttl).Err(); err == nil {
			c.markHealthy()
			return
		} else if ctx.Err() == nil {
			c.markDegraded(err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	// Without Redis the values live in this process: the expired ones are swept out when there
	// are many, and past the limit nothing more is kept — a value is a favour, not a promise.
	if len(c.values) >= sweepValuesAt {
		for k, v := range c.values {
			if !now.Before(v.expires) {
				delete(c.values, k)
			}
		}
		if _, known := c.values[key]; !known && len(c.values) >= maxValuesInMemory {
			return
		}
	}
	c.values[key] = &value{data: data, expires: now.Add(ttl)}
}

// How many values the memory of the process keeps while Redis is away.
const (
	sweepValuesAt     = 4096
	maxValuesInMemory = 16384
)

// Get returns the value stored under key, if it is still alive.
func (c *Cache) Get(ctx context.Context, key string) (string, bool) {
	if c.redis != nil {
		data, err := c.redis.Get(ctx, "v:"+key).Result()
		switch {
		case err == nil:
			c.markHealthy()
			return data, true
		case errors.Is(err, redis.Nil):
			c.markHealthy()
			// Fall through: the value may have been written to memory during an outage.
		case ctx.Err() == nil:
			c.markDegraded(err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.values[key]; ok && c.now().Before(v.expires) {
		return v.data, true
	}
	return "", false
}

// Del removes a value.
func (c *Cache) Del(ctx context.Context, key string) {
	if c.redis != nil {
		if err := c.redis.Del(ctx, "v:"+key).Err(); err != nil && ctx.Err() == nil {
			c.markDegraded(err)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, key)
}
