package monitor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/store"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// fakeClock avanza solo cuando alguien duerme o lo adelanta; así los tests no esperan tiempo real.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  []time.Duration
	extra   func(i int) time.Duration // tiempo extra que pasa durante el Sleep i (1..n)
	onSleep func(i int)
	block   bool // Sleep espera a ctx en vez de avanzar
}

func newClock() *fakeClock { return &fakeClock{now: t0} }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.block {
		<-ctx.Done()
		return ctx.Err()
	}
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	i := len(c.sleeps)
	adv := d
	if c.extra != nil {
		adv += c.extra(i)
	}
	c.now = c.now.Add(adv)
	cb := c.onSleep
	c.mu.Unlock()
	if cb != nil {
		cb(i)
	}
	return ctx.Err()
}

type fakeStore struct {
	mu       sync.Mutex
	runs     []store.MonitorRun
	samples  []store.SampleInput
	closed   map[int64]string
	have     map[store.DigestKey]bool
	missing  [][]store.DigestKey
	insertFn func(n int) error
}

func newStore() *fakeStore {
	return &fakeStore{closed: map[int64]string{}, have: map[store.DigestKey]bool{}}
}

func (s *fakeStore) OpenMonitorRun(_ context.Context, r store.MonitorRun) (store.MonitorRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = int64(len(s.runs) + 1)
	s.runs = append(s.runs, r)
	return r, nil
}
func (s *fakeStore) CloseMonitorRun(_ context.Context, id int64, reason string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.closed[id]; !ok {
		s.closed[id] = reason
	}
	return nil
}
func (s *fakeStore) InsertSample(_ context.Context, in store.SampleInput) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.insertFn != nil {
		if err := s.insertFn(len(s.samples) + 1); err != nil {
			return 0, err
		}
	}
	s.samples = append(s.samples, in)
	for _, d := range in.Digests {
		if d.Text != "" {
			s.have[store.DigestKey{Schema: d.Schema, Digest: d.Digest}] = true
		}
	}
	return int64(len(s.samples)), nil
}
func (s *fakeStore) MissingDigestTexts(_ context.Context, _ string, keys []store.DigestKey) ([]store.DigestKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.DigestKey
	for _, k := range keys {
		if !s.have[k] {
			out = append(out, k)
		}
	}
	s.missing = append(s.missing, out)
	return out, nil
}
func (s *fakeStore) snapshot() []store.SampleInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.SampleInput(nil), s.samples...)
}
func (s *fakeStore) reason(id int64) string { s.mu.Lock(); defer s.mu.Unlock(); return s.closed[id] }

// fakeReader implementa driver.ActivityReader con respuestas por número de llamada (desde 1).
type fakeReader struct {
	statusFn  func(n int) (map[string]uint64, error)
	maxConnFn func() (uint64, error)
	procsFn   func(n int) ([]model.Process, error)
	digestFn  func(n int) ([]model.DigestCounters, error)
	textsFn   func(want []string) (map[string]string, error)
	probeFn   func(n int) (model.MonitorCaps, error)
	onCall    func() // se llama al entrar a cualquier lectura (para adelantar el reloj)

	inflight, maxInflight                   atomic.Int32
	nStatus, nProcs, nDigest, nProbe, nText atomic.Int32
	textWanted                              [][]string
	mu                                      sync.Mutex
}

func (f *fakeReader) enter() func() {
	n := f.inflight.Add(1)
	for {
		m := f.maxInflight.Load()
		if n <= m || f.maxInflight.CompareAndSwap(m, n) {
			break
		}
	}
	if f.onCall != nil {
		f.onCall()
	}
	time.Sleep(time.Millisecond)
	return func() { f.inflight.Add(-1) }
}

func (f *fakeReader) Probe(context.Context) (model.MonitorCaps, error) {
	defer f.enter()()
	n := int(f.nProbe.Add(1))
	if f.probeFn != nil {
		return f.probeFn(n)
	}
	return okCaps(), nil
}
func (f *fakeReader) Processes(context.Context) ([]model.Process, error) {
	defer f.enter()()
	n := int(f.nProcs.Add(1))
	if f.procsFn != nil {
		return f.procsFn(n)
	}
	return nil, nil
}
func (f *fakeReader) GlobalStatus(_ context.Context, _ []string) (map[string]uint64, error) {
	defer f.enter()()
	n := int(f.nStatus.Add(1))
	if f.statusFn != nil {
		return f.statusFn(n)
	}
	return nil, errors.New("sin respuesta")
}
func (f *fakeReader) MaxConnections(context.Context) (uint64, error) {
	if f.maxConnFn != nil {
		return f.maxConnFn()
	}
	return 151, nil
}
func (f *fakeReader) Digests(context.Context) ([]model.DigestCounters, error) {
	defer f.enter()()
	n := int(f.nDigest.Add(1))
	if f.digestFn != nil {
		return f.digestFn(n)
	}
	return nil, nil
}
func (f *fakeReader) DigestTexts(_ context.Context, want []string) (map[string]string, error) {
	defer f.enter()()
	f.nText.Add(1)
	f.mu.Lock()
	f.textWanted = append(f.textWanted, append([]string(nil), want...))
	f.mu.Unlock()
	if f.textsFn != nil {
		return f.textsFn(want)
	}
	out := map[string]string{}
	for _, d := range want {
		out[d] = "SELECT ? FROM t_" + d
	}
	return out, nil
}

func okCaps() model.MonitorCaps {
	ok := model.SourceState{State: model.SourceOK, Reason: model.ReasonOK}
	return model.MonitorCaps{ServerVersion: "8.0.36", ServerID: "uuid-1", Processlist: ok, Digest: ok, Status: ok}
}

type events struct {
	mu      sync.Mutex
	states  []State
	samples []Sample
}

func (e *events) onState(s State)   { e.mu.Lock(); e.states = append(e.states, s); e.mu.Unlock() }
func (e *events) onSample(s Sample) { e.mu.Lock(); e.samples = append(e.samples, s); e.mu.Unlock() }
func (e *events) has(state, source string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.states {
		if s.State == state && s.Source == source {
			return true
		}
	}
	return false
}
func (e *events) count(state, source string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, s := range e.states {
		if s.State == state && s.Source == source {
			n++
		}
	}
	return n
}

// startRunner arranca un Runner con el reloj falso y espera a que termine sola (el test cancela desde onSleep).
func startRunner(t *testing.T, parent context.Context, rd *fakeReader, st *fakeStore, clk *fakeClock, ev *events, cfg model.MonitorConfig) *Runner {
	t.Helper()
	r, err := StartRunner(parent, RunnerDeps{Reader: rd, Store: st, Clock: clk, ProfileID: "p1", Caps: okCaps(), Config: cfg, OnSample: ev.onSample, OnState: ev.onState})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func waitDone(t *testing.T, r *Runner) {
	t.Helper()
	select {
	case <-r.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("el Runner no terminó")
	}
}

func statusVals(n int) map[string]uint64 {
	return map[string]uint64{"Uptime": 1000 + uint64(n)*30, "Questions": uint64(n) * 3000, "Threads_connected": 10, "Threads_running": 2}
}
