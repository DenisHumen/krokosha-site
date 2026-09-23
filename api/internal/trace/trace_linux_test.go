//go:build linux

package trace

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

// The loopback address is one hop away and answers the probes itself, «port unreachable», as any
// address without a listener on the port does — with no privileges and no network needed.
func TestTraceToThisMachine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	loopback := netip.MustParseAddr("127.0.0.1")
	result, err := Run(ctx, loopback, Options{MaxHops: 5, Wait: 500 * time.Millisecond, Ports: []int{1}, Connect: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reached || len(result.Hops) != 1 {
		t.Fatalf("the way to 127.0.0.1: %+v", result)
	}
	hop := result.Hops[0]
	if hop.Addr != loopback || !hop.Reached || hop.Sent != 3 || len(hop.RTTs) != 3 {
		t.Errorf("the only hop: %+v", hop)
	}
	if result.From != loopback {
		t.Errorf("from %s", result.From)
	}
	if result.Connect == nil || len(result.Connect.RTTs) != 1 {
		t.Errorf("port 1 of this machine refuses, and that is an answer: %+v", result.Connect)
	}
}

func TestTraceIPv6(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := Run(ctx, netip.MustParseAddr("::1"), Options{MaxHops: 3, Wait: 500 * time.Millisecond, Ports: []int{1}, Connect: 1})
	if err != nil {
		t.Skipf("no IPv6 here: %v", err)
	}
	if !result.Reached || len(result.Hops) != 1 || result.Hops[0].Addr != netip.MustParseAddr("::1") {
		t.Fatalf("the way to ::1: %+v", result)
	}
}
