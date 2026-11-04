package monitor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vitacodesas/dbacus/internal/model"
)

type snapRecorder struct {
	mu    sync.Mutex
	snaps []model.ProcessSnapshot
}

func (s *snapRecorder) add(p model.ProcessSnapshot) {
	s.mu.Lock()
	s.snaps = append(s.snaps, p)
	s.mu.Unlock()
}
func (s *snapRecorder) n() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.snaps) }

func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("el Watcher no terminó")
	}
}

func TestWatcherLeaseExpires(t *testing.T) {
	clk, rec := newClock(), &snapRecorder{}
	rd := &fakeReader{procsFn: func(int) ([]model.Process, error) {
		return []model.Process{{ID: 1, Command: "Query", Info: "SELECT 1"}}, nil
	}}
	w := StartWatcher(context.Background(), WatcherDeps{Reader: rd, Clock: clk, OnSnapshot: rec.add}, 1000)
	waitFor(t, w.Done()) // el reloj falso avanza 1 s por vuelta: a los 30 s caduca
	if n := rec.n(); n < 29 || n > 32 {
		t.Fatalf("instantáneas antes de caducar: %d", n)
	}
	if rec.snaps[0].Rows[0].Info != "SELECT 1" {
		t.Fatal("la vista en vivo sí lleva INFO (solo en memoria)")
	}
	w.Stop() // idempotente tras terminar
}

func TestWatcherRenewExtendsAndClampsInterval(t *testing.T) {
	clk, rec := newClock(), &snapRecorder{}
	rd := &fakeReader{procsFn: func(int) ([]model.Process, error) { return nil, nil }}
	var wp atomic.Pointer[Watcher]
	clk.onSleep = func(i int) {
		if w := wp.Load(); w != nil && i < 100 {
			w.Renew(10) // pide 10 ms: se sube a 1 s
		}
	}
	w := StartWatcher(context.Background(), WatcherDeps{Reader: rd, Clock: clk, OnSnapshot: rec.add}, 10)
	wp.Store(w)
	waitFor(t, w.Done())
	if rec.n() < 100 {
		t.Fatalf("renovar alarga la vida: %d instantáneas", rec.n())
	}
	clk.mu.Lock()
	defer clk.mu.Unlock()
	for _, d := range clk.sleeps {
		if d < time.Second {
			t.Fatalf("intervalo por debajo de 1 s: %v", d)
		}
	}
}

func TestWatcherStopsWithContextAndStop(t *testing.T) {
	clk := newClock()
	clk.block = true
	rd := &fakeReader{procsFn: func(int) ([]model.Process, error) { return nil, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	w := StartWatcher(ctx, WatcherDeps{Reader: rd, Clock: clk}, 2000)
	cancel()
	waitFor(t, w.Done())

	w2 := StartWatcher(context.Background(), WatcherDeps{Reader: rd, Clock: clk}, 2000)
	w2.Stop()
	w2.Stop()
}

// TestWatcherReadsMaxConnectionsOncePerWatch: el máximo se lee una vez y viaja en cada instantánea; si la lectura falla
// se reintenta en la siguiente (la vista En vivo no cae por eso).
func TestWatcherReadsMaxConnectionsOncePerWatch(t *testing.T) {
	clk, rec := newClock(), &snapRecorder{}
	var calls atomic.Int32
	rd := &fakeReader{
		procsFn: func(int) ([]model.Process, error) { return []model.Process{{ID: 1, Command: "Query"}}, nil },
		maxConnFn: func() (uint64, error) {
			if calls.Add(1) == 1 {
				return 0, context.DeadlineExceeded
			}
			return 200, nil
		},
	}
	w := StartWatcher(context.Background(), WatcherDeps{Reader: rd, Clock: clk, OnSnapshot: rec.add}, 1000)
	waitFor(t, w.Done())
	if rec.n() < 3 {
		t.Fatalf("instantáneas: %d", rec.n())
	}
	if rec.snaps[0].MaxConnections != 0 {
		t.Fatalf("la primera lectura falló: máximo 0, no %d", rec.snaps[0].MaxConnections)
	}
	for _, s := range rec.snaps[1:] {
		if s.MaxConnections != 200 {
			t.Fatalf("el máximo debe viajar en cada instantánea: %d", s.MaxConnections)
		}
	}
	if c := calls.Load(); c != 2 {
		t.Fatalf("MaxConnections se leyó %d veces, quiero 2 (un fallo y un éxito; no una por instantánea)", c)
	}
}
