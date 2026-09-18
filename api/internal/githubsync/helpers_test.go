package githubsync

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// noSleep removes the back-off between retries for the duration of a test.
func noSleep(t *testing.T) {
	t.Helper()
	previous := retrySleep
	retrySleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { retrySleep = previous })
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
