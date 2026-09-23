//go:build !linux

package trace

import (
	"context"
	"errors"
	"net/netip"
	"syscall"
)

// probeHops needs the error queue of Linux sockets: elsewhere only the time to the address
// itself is measured.
func probeHops(context.Context, netip.Addr, Options) ([]Hop, netip.Addr, error) {
	return nil, netip.Addr{}, ErrUnsupported
}

// wsaConnRefused is WSAECONNREFUSED: a refused connection on Windows, where the tests of the
// developers run.
const wsaConnRefused = syscall.Errno(10061)

func isRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, wsaConnRefused)
}
