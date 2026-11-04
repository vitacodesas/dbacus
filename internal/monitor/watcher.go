package monitor

import (
	"context"
	"sync"
	"time"

	"github.com/vitacodesas/dbacus/internal/model"
)

const (
	// DefaultLease es lo que dura la vista En vivo sin que alguien la renueve (spec 22).
	DefaultLease = 30 * time.Second
	minWatchMs   = 1000
)

// ProcessReader es lo único que el Watcher necesita del driver.
type ProcessReader interface {
	Processes(ctx context.Context) ([]model.Process, error)
	MaxConnections(ctx context.Context) (uint64, error)
}

type WatcherDeps struct {
	Reader        ProcessReader
	Clock         Clock // por defecto RealClock
	OnSnapshot    func(model.ProcessSnapshot)
	Lease         time.Duration // por defecto DefaultLease
	SourceTimeout time.Duration // por defecto 5 s
}

// Watcher alimenta la vista En vivo: lee los procesos cada intervalo y emite una instantánea (con INFO, solo en
// memoria). Funciona sin ejecución de monitoreo. Vive mientras alguien renueve su arrendamiento.
type Watcher struct {
	d      WatcherDeps
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	mu         sync.Mutex
	interval   time.Duration
	leaseUntil time.Time

	maxConn uint64 // solo lo toca la goroutine del bucle
}

// StartWatcher arranca la goroutine; termina con parent, con Stop o cuando el arrendamiento caduca.
func StartWatcher(parent context.Context, d WatcherDeps, intervalMs int) *Watcher {
	if d.Clock == nil {
		d.Clock = RealClock{}
	}
	if d.Lease <= 0 {
		d.Lease = DefaultLease
	}
	if d.SourceTimeout <= 0 {
		d.SourceTimeout = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(parent)
	w := &Watcher{d: d, cancel: cancel, done: make(chan struct{})}
	w.Renew(intervalMs)
	go w.loop(ctx)
	return w
}

// Renew alarga el arrendamiento y fija el intervalo (mínimo 1 s). Es idempotente.
func (w *Watcher) Renew(intervalMs int) {
	intervalMs = max(intervalMs, minWatchMs)
	w.mu.Lock()
	w.interval = time.Duration(intervalMs) * time.Millisecond
	w.leaseUntil = w.d.Clock.Now().Add(w.d.Lease)
	w.mu.Unlock()
}

func (w *Watcher) Done() <-chan struct{} { return w.done }

// Stop detiene la vigilancia y espera a la goroutine. Es idempotente.
func (w *Watcher) Stop() {
	w.once.Do(w.cancel)
	<-w.done
}

func (w *Watcher) loop(ctx context.Context) {
	defer close(w.done)
	for {
		w.mu.Lock()
		expired, interval := w.d.Clock.Now().After(w.leaseUntil), w.interval
		w.mu.Unlock()
		if expired || ctx.Err() != nil {
			return
		}
		start := w.d.Clock.Now()
		pctx, cancel := context.WithTimeout(ctx, w.d.SourceTimeout)
		rows, err := w.d.Reader.Processes(pctx)
		cancel()
		if err == nil && ctx.Err() == nil && w.d.OnSnapshot != nil {
			snap := Snapshot(start, rows)
			snap.MaxConnections = w.maxConnections(ctx)
			w.d.OnSnapshot(snap)
		}
		if w.d.Clock.Sleep(ctx, max(interval-w.d.Clock.Now().Sub(start), 0)) != nil {
			return
		}
	}
}

// maxConnections lee @@global.max_connections una vez por vigilancia; si falla devuelve 0 y se reintenta en la siguiente
// instantánea (un fallo aquí no debe tumbar la vista En vivo).
func (w *Watcher) maxConnections(ctx context.Context) uint64 {
	if w.maxConn > 0 {
		return w.maxConn
	}
	pctx, cancel := context.WithTimeout(ctx, w.d.SourceTimeout)
	defer cancel()
	if n, err := w.d.Reader.MaxConnections(pctx); err == nil {
		w.maxConn = n
	}
	return w.maxConn
}
