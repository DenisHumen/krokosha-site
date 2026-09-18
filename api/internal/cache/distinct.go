package cache

import (
	"context"
	"time"
)

// Counting distinct things without keeping them: HyperLogLog in Redis (12 KB per counter, no
// members stored — only what is needed to estimate how many there were), a plain set in memory
// when Redis is away. Used for «how many different clients did nginx see this hour».

type distinctSet struct {
	members map[string]struct{}
	expires time.Time
}

// AddDistinct notes members under key and returns how many distinct ones the key has seen.
// The counter lives for ttl after its last update. The answer is an estimate (±1 %) with Redis
// and exact without it.
func (c *Cache) AddDistinct(ctx context.Context, key string, members []string, ttl time.Duration) int {
	if len(members) == 0 {
		return c.Distinct(ctx, key)
	}
	if c.redis != nil {
		values := make([]any, len(members))
		for i, member := range members {
			values[i] = member
		}
		pipe := c.redis.Pipeline()
		pipe.PFAdd(ctx, "d:"+key, values...)
		pipe.Expire(ctx, "d:"+key, ttl)
		count := pipe.PFCount(ctx, "d:"+key)
		if _, err := pipe.Exec(ctx); err == nil {
			c.markHealthy()
			return int(count.Val())
		} else if ctx.Err() == nil {
			c.markDegraded(err)
		}
	}

	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	set := c.sets[key]
	if set == nil || now.After(set.expires) {
		set = &distinctSet{members: map[string]struct{}{}}
		c.sets[key] = set
	}
	set.expires = now.Add(ttl)
	for _, member := range members {
		set.members[member] = struct{}{}
	}
	for other, candidate := range c.sets {
		if now.After(candidate.expires) {
			delete(c.sets, other)
		}
	}
	return len(set.members)
}

// Distinct returns the current count under key, 0 when there is none.
func (c *Cache) Distinct(ctx context.Context, key string) int {
	if c.redis != nil {
		count, err := c.redis.PFCount(ctx, "d:"+key).Result()
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
	if set := c.sets[key]; set != nil && c.now().Before(set.expires) {
		return len(set.members)
	}
	return 0
}
