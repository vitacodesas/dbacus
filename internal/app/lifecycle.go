package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Lifecycle es dueño de las goroutines y recursos de larga vida (spec 01: "Shutdown").
// Shutdown cancela el ctx, espera a las goroutines con timeout y cierra recursos en orden inverso.
type Lifecycle struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	closers []closer
}

type closer struct {
	name string
	fn   func() error
}

func NewLifecycle() *Lifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &Lifecycle{ctx: ctx, cancel: cancel}
}

func (l *Lifecycle) Context() context.Context { return l.ctx }

// Go lanza fn en una goroutine rastreada; fn debe terminar cuando ctx se cancele.
func (l *Lifecycle) Go(fn func(ctx context.Context)) {
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		fn(l.ctx)
	}()
}

// OnShutdown registra un cierre; se ejecutan en orden LIFO.
func (l *Lifecycle) OnShutdown(name string, fn func() error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closers = append(l.closers, closer{name, fn})
}

func (l *Lifecycle) Shutdown(timeout time.Duration) error {
	l.cancel()

	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()

	var errs []error
	select {
	case <-done:
	case <-time.After(timeout):
		errs = append(errs, fmt.Errorf("goroutines no terminaron en %s", timeout))
	}

	l.mu.Lock()
	closers := l.closers
	l.closers = nil
	l.mu.Unlock()
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i].fn(); err != nil {
			slog.Error("cierre falló", "resource", closers[i].name, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", closers[i].name, err))
		}
	}
	return errors.Join(errs...)
}
