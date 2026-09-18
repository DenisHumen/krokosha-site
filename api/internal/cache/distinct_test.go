package cache

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestDistinctCounts(t *testing.T) {
	server := miniredis.RunT(t)
	withRedis, err := New(context.Background(), "redis://"+server.Addr()+"/0", quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer withRedis.Close()
	inMemory, err := New(context.Background(), "", quiet)
	if err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]*Cache{"redis": withRedis, "memory": inMemory} {
		now := time.Unix(1_800_000_000, 0)
		c.now = func() time.Time { return now }
		ctx := context.Background()

		if got := c.Distinct(ctx, "clients:bots:2026091909"); got != 0 {
			t.Errorf("%s: a counter nobody fed says %d", name, got)
		}
		if got := c.AddDistinct(ctx, "clients:bots:2026091909", []string{"a", "b", "a"}, time.Hour); got != 2 {
			t.Errorf("%s: a, b, a = %d distinct, want 2", name, got)
		}
		// Told the same again — a poll that was repeated after a failed transaction — nothing changes.
		if got := c.AddDistinct(ctx, "clients:bots:2026091909", []string{"b", "c"}, time.Hour); got != 3 {
			t.Errorf("%s: + b, c = %d, want 3", name, got)
		}
		if got := c.AddDistinct(ctx, "clients:bots:2026091909", nil, time.Hour); got != 3 {
			t.Errorf("%s: adding nothing = %d, want 3", name, got)
		}
		if got := c.AddDistinct(ctx, "clients:bots:2026091910", []string{"a"}, time.Hour); got != 1 {
			t.Errorf("%s: another hour = %d, want 1", name, got)
		}

		// Members never become keys or values of their own: a HyperLogLog keeps what it needs
		// to estimate, no more.
		if name == "redis" {
			for _, key := range server.Keys() {
				if !strings.HasPrefix(key, "d:clients:") {
					t.Errorf("unexpected key in Redis: %q", key)
				}
			}
		}

		now = now.Add(2 * time.Hour)
		server.FastForward(2 * time.Hour)
		if got := c.Distinct(ctx, "clients:bots:2026091909"); got != 0 {
			t.Errorf("%s: the counter outlived its time: %d", name, got)
		}
	}
}

func TestDistinctSurvivesRedisOutage(t *testing.T) {
	server := miniredis.RunT(t)
	c, err := New(context.Background(), "redis://"+server.Addr()+"/0", quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	server.Close()
	if got := c.AddDistinct(ctx, "clients:other:2026091909", []string{"x", "y"}, time.Hour); got != 2 {
		t.Errorf("without Redis = %d, want 2 counted in memory", got)
	}
	if got := c.Distinct(ctx, "clients:other:2026091909"); got != 2 {
		t.Errorf("reading back without Redis = %d, want 2", got)
	}
}
