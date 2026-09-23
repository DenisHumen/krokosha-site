package trace

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

// extendedError is what Linux puts into a control message of IP_RECVERR: struct sock_extended_err,
// then the address of whoever answered.
func extendedError(origin, kind, code uint8, offender netip.Addr) []byte {
	data := make([]byte, 16)
	binary.NativeEndian.PutUint32(data, 113) // EHOSTUNREACH, say: the errno is not read
	data[4], data[5], data[6] = origin, kind, code
	if offender.Is4() {
		address := make([]byte, 16)
		binary.NativeEndian.PutUint16(address, familyInet)
		a := offender.As4()
		copy(address[4:], a[:])
		return append(data, address...)
	}
	address := make([]byte, 28)
	binary.NativeEndian.PutUint16(address, familyInet6)
	a := offender.As16()
	copy(address[8:], a[:])
	return append(data, address...)
}

func TestAnswersOfRouters(t *testing.T) {
	router, target := netip.MustParseAddr("185.1.222.191"), netip.MustParseAddr("2001:db8::7")
	for _, tc := range []struct {
		name    string
		data    []byte
		verdict verdict
		from    netip.Addr
		refusal string
	}{
		{"time exceeded", extendedError(originICMP, 11, 0, router), passedOn, router, ""},
		{"port unreachable: the address itself", extendedError(originICMP, 3, 3, router), arrived, router, ""},
		{"administratively prohibited", extendedError(originICMP, 3, 13, router), stopped, router, "prohibited"},
		{"host unreachable", extendedError(originICMP, 3, 1, router), stopped, router, "host unreachable"},
		{"ICMPv6 time exceeded", extendedError(originICMP6, 3, 0, target), passedOn, target, ""},
		{"ICMPv6 port unreachable", extendedError(originICMP6, 1, 4, target), arrived, target, ""},
		{"ICMPv6 prohibited", extendedError(originICMP6, 1, 1, target), stopped, target, "prohibited"},
	} {
		answer, ok := parseExtendedError(tc.data)
		if !ok || answer.verdict() != tc.verdict || answer.offender != tc.from {
			t.Errorf("%s: %+v %v", tc.name, answer, ok)
		}
		if tc.verdict == stopped && answer.refusal() != tc.refusal {
			t.Errorf("%s: %q", tc.name, answer.refusal())
		}
	}
	for name, data := range map[string][]byte{
		"too short":      extendedError(originICMP, 11, 0, router)[:17],
		"not from ICMP":  extendedError(1, 11, 0, router), // SO_EE_ORIGIN_LOCAL: the kernel's own complaint
		"unknown family": append(extendedError(originICMP, 11, 0, router)[:16], 7, 0, 0, 0, 1, 2, 3, 4),
	} {
		if _, ok := parseExtendedError(data); ok {
			t.Errorf("%s: read as an answer", name)
		}
	}
}

func TestTheTimeToTheAddressItself(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	loopback := netip.MustParseAddr("127.0.0.1")

	result := connect(context.Background(), loopback, Options{Ports: []int{port}, Connect: 3}.withDefaults())
	if result == nil || result.Port != port || result.Sent != 3 || len(result.RTTs) != 3 {
		t.Fatalf("a port that answers: %+v", result)
	}

	// A closed port answers with a refusal: that is the address answering too.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	result = connect(context.Background(), loopback, Options{Ports: []int{closedPort}, Connect: 2}.withDefaults())
	if result == nil || len(result.RTTs) != 2 {
		t.Fatalf("a refusing port: %+v", result)
	}
	for _, rtt := range result.RTTs {
		if rtt < 0 || rtt > time.Second { // on loopback it may be too short for the clock of Windows
			t.Errorf("a refusal took %s", rtt)
		}
	}
}
