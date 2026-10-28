package schemacache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestCachesAndExpires(t *testing.T) {
	c := New(time.Minute)
	now := time.Now()
	c.now = func() time.Time { return now }
	var calls atomic.Int32
	load := func(context.Context) (int, error) { return int(calls.Add(1)), nil }

	for range 3 {
		v, err := Load(context.Background(), c, context.Background(), 0, "k", load)
		require.NoError(t, err)
		require.Equal(t, 1, v)
	}
	now = now.Add(2 * time.Minute)
	v, _ := Load(context.Background(), c, context.Background(), 0, "k", load)
	require.Equal(t, 2, v, "tras el TTL se vuelve a cargar")
}

func TestConcurrentCallersShareOneLoad(t *testing.T) {
	c := New(time.Minute)
	var calls atomic.Int32
	release := make(chan struct{})
	load := func(context.Context) (string, error) { calls.Add(1); <-release; return "ok", nil }

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := Load(context.Background(), c, context.Background(), 0, "k", load)
			require.NoError(t, err)
			require.Equal(t, "ok", v)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
}

func TestErrorsAreNotCached(t *testing.T) {
	c := New(time.Minute)
	n := 0
	load := func(context.Context) (int, error) {
		n++
		if n == 1 {
			return 0, errors.New("boom")
		}
		return n, nil
	}
	_, err := Load(context.Background(), c, context.Background(), 0, "k", load)
	require.Error(t, err)
	v, err := Load(context.Background(), c, context.Background(), 0, "k", load)
	require.NoError(t, err)
	require.Equal(t, 2, v)
}

func TestInvalidateByPrefix(t *testing.T) {
	c := New(time.Minute)
	var calls atomic.Int32
	load := func(context.Context) (int, error) { return int(calls.Add(1)), nil }
	for _, k := range []string{"db:a:tables", "db:a:table:t", "db:b:tables", "dbs"} {
		_, _ = Load(context.Background(), c, context.Background(), 0, k, load)
	}
	require.EqualValues(t, 4, calls.Load())

	c.Invalidate("db:a:")
	for _, k := range []string{"db:a:tables", "db:a:table:t", "db:b:tables", "dbs"} {
		_, _ = Load(context.Background(), c, context.Background(), 0, k, load)
	}
	require.EqualValues(t, 6, calls.Load(), "solo se recargaron las 2 de la base a")

	c.Invalidate("")
	_, _ = Load(context.Background(), c, context.Background(), 0, "dbs", load)
	require.EqualValues(t, 7, calls.Load())
}

func TestInvalidateDuringLoadDoesNotRepopulateStale(t *testing.T) {
	c := New(time.Minute)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	load := func(context.Context) (int, error) {
		n := int(calls.Add(1))
		if n == 1 {
			close(started)
			<-release
		}
		return n, nil
	}
	done := make(chan int)
	go func() {
		v, _ := Load(context.Background(), c, context.Background(), 0, "k", load)
		done <- v
	}()
	<-started
	c.Invalidate("") // el esquema cambió mientras se leía
	close(release)
	require.Equal(t, 1, <-done)

	v, _ := Load(context.Background(), c, context.Background(), 0, "k", load)
	require.Equal(t, 2, v, "el resultado viejo no quedó cacheado")
}

func TestBaseContextCancelsLoad(t *testing.T) {
	c := New(time.Minute)
	base, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	load := func(ctx context.Context) (int, error) { close(started); <-ctx.Done(); return 0, ctx.Err() }

	errc := make(chan error, 1)
	go func() {
		_, err := Load(context.Background(), c, base, 0, "k", load)
		errc <- err
	}()
	<-started
	cancel() // p. ej. la sesión se desconecta
	require.ErrorIs(t, <-errc, context.Canceled)
}

func TestCallerLeavingDoesNotAbortSharedLoad(t *testing.T) {
	c := New(time.Minute)
	release := make(chan struct{})
	load := func(context.Context) (int, error) { <-release; return 7, nil }

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := Load(ctx, c, context.Background(), 0, "k", load)
		first <- err
	}()
	time.Sleep(30 * time.Millisecond)
	second := make(chan int, 1)
	go func() {
		v, _ := Load(context.Background(), c, context.Background(), 0, "k", load)
		second <- v
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	require.ErrorIs(t, <-first, context.Canceled)
	close(release)
	require.Equal(t, 7, <-second)
}

func TestTimeout(t *testing.T) {
	c := New(time.Minute)
	_, err := Load(context.Background(), c, context.Background(), 30*time.Millisecond, "k", func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
