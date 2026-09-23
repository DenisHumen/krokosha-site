package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

var quiet = slog.New(slog.DiscardHandler)

func TestAllowInMemory(t *testing.T) {
	c, err := New(context.Background(), "", quiet)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	c.now = func() time.Time { return now }
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if !c.Allow(ctx, "lead:203.0.113.7", 3, time.Hour) {
			t.Fatalf("hit %d of 3 was refused", i)
		}
	}
	if c.Allow(ctx, "lead:203.0.113.7", 3, time.Hour) {
		t.Error("the 4th hit within the window was allowed")
	}
	if !c.Allow(ctx, "lead:198.51.100.1", 3, time.Hour) {
		t.Error("another key was affected")
	}

	now = now.Add(time.Hour + time.Second)
	if !c.Allow(ctx, "lead:203.0.113.7", 3, time.Hour) {
		t.Error("the window did not reset after it expired")
	}
	if !errors.Is(c.Ping(ctx), ErrDisabled) {
		t.Error("Ping without Redis must report ErrDisabled")
	}
}

func TestAllowWithRedis(t *testing.T) {
	server := miniredis.RunT(t)
	c, err := New(context.Background(), "redis://"+server.Addr()+"/0", quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	for i := 1; i <= 2; i++ {
		if !c.Allow(ctx, "login:admin", 2, time.Minute) {
			t.Fatalf("hit %d of 2 was refused", i)
		}
	}
	if c.Allow(ctx, "login:admin", 2, time.Minute) {
		t.Error("the 3rd hit within the window was allowed")
	}
	if ttl := server.TTL("rl:login:admin"); ttl <= 0 || ttl > time.Minute {
		t.Errorf("TTL = %v, want the window length: a counter without expiry would block forever", ttl)
	}

	server.FastForward(61 * time.Second)
	if !c.Allow(ctx, "login:admin", 2, time.Minute) {
		t.Error("the window did not reset after it expired")
	}
}

func TestAllowSurvivesRedisOutage(t *testing.T) {
	server := miniredis.RunT(t)
	c, err := New(context.Background(), "redis://"+server.Addr()+"/0", quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	if !c.Allow(ctx, "e:203.0.113.7", 2, time.Minute) {
		t.Fatal("first hit refused")
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping with a healthy Redis: %v", err)
	}

	server.Close() // Redis goes away
	if err := c.Ping(ctx); err == nil || errors.Is(err, ErrDisabled) {
		t.Errorf("Ping with Redis down = %v, want a connection error", err)
	}
	// The limit keeps working from memory — starting over, which errs on the permissive side.
	for i := 1; i <= 2; i++ {
		if !c.Allow(ctx, "e:203.0.113.7", 2, time.Minute) {
			t.Fatalf("hit %d during the outage was refused", i)
		}
	}
	if c.Allow(ctx, "e:203.0.113.7", 2, time.Minute) {
		t.Error("the limit is not enforced while Redis is down")
	}

	if err := server.Restart(); err != nil {
		t.Fatal(err)
	}
	// The client needs a moment to notice: its first dial after the restart may still fail
	// (seen on a loaded CI machine). What matters is that it recovers by itself, and soon.
	recovered := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !recovered; time.Sleep(50 * time.Millisecond) {
		if !c.Allow(ctx, "e:after-recovery", 1_000_000, time.Minute) {
			t.Fatal("requests are refused after Redis came back")
		}
		c.mu.Lock()
		recovered = !c.degraded
		c.mu.Unlock()
	}
	if !recovered {
		t.Error("the cache still thinks Redis is down")
	}
}

func TestNewStartsEvenIfRedisIsDown(t *testing.T) {
	c, err := New(context.Background(), "redis://127.0.0.1:1/0", quiet)
	if err != nil {
		t.Fatalf("an unreachable Redis must not stop the service: %v", err)
	}
	defer c.Close()
	if !c.Allow(context.Background(), "k", 1, time.Minute) {
		t.Error("first hit refused")
	}
}

func TestNewRejectsMalformedURLWithoutLeakingIt(t *testing.T) {
	_, err := New(context.Background(), "redis://:s3cret@%zz", quiet)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); len(got) == 0 || containsSecret(got) {
		t.Errorf("error leaks the URL: %q", got)
	}
}

func containsSecret(s string) bool {
	for i := 0; i+6 <= len(s); i++ {
		if s[i:i+6] == "s3cret" {
			return true
		}
	}
	return false
}

func TestValuesInMemoryAreBounded(t *testing.T) {
	c, err := New(context.Background(), "", quiet)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC)
	c.SetClock(func() time.Time { return now })
	ctx := context.Background()
	for i := range maxValuesInMemory + 100 {
		c.Set(ctx, fmt.Sprintf("key-%d", i), "value", time.Minute)
	}
	if len(c.values) != maxValuesInMemory {
		t.Errorf("%d values kept, the limit is %d", len(c.values), maxValuesInMemory)
	}
	if _, ok := c.Get(ctx, "key-0"); !ok {
		t.Error("a value kept before the limit is lost")
	}
	// A minute later all of them are dead: the next value sweeps them out.
	now = now.Add(2 * time.Minute)
	c.Set(ctx, "fresh", "value", time.Minute)
	if len(c.values) != 1 {
		t.Errorf("%d values after the sweep", len(c.values))
	}
}
