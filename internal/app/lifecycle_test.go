package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestShutdownCancelsGoroutinesAndClosesLIFO(t *testing.T) {
	l := NewLifecycle()
	stopped := make(chan struct{})
	l.Go(func(ctx context.Context) { <-ctx.Done(); close(stopped) })

	var order []string
	l.OnShutdown("a", func() error { order = append(order, "a"); return nil })
	l.OnShutdown("b", func() error { order = append(order, "b"); return nil })

	require.NoError(t, l.Shutdown(time.Second))
	<-stopped
	require.Equal(t, []string{"b", "a"}, order)
}

func TestShutdownTimesOutOnStuckGoroutine(t *testing.T) {
	l := NewLifecycle()
	release := make(chan struct{})
	l.Go(func(context.Context) { <-release })
	require.Error(t, l.Shutdown(50*time.Millisecond))
	close(release)
	l.wg.Wait()
}
