package monitor

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/store"
)

// Clock permite probar sin esperar: el Runner y el Watcher solo miden tiempo por aquí.
type Clock interface {
	Now() time.Time
	// Sleep espera d o hasta que ctx termine (devuelve ctx.Err()).
	Sleep(ctx context.Context, d time.Duration) error
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Store es lo que el Runner necesita de *store.Store.
type Store interface {
	OpenMonitorRun(ctx context.Context, r store.MonitorRun) (store.MonitorRun, error)
	CloseMonitorRun(ctx context.Context, id int64, reason string, endedAt time.Time) error
	InsertSample(ctx context.Context, in store.SampleInput) (int64, error)
	MissingDigestTexts(ctx context.Context, profileID string, keys []store.DigestKey) ([]store.DigestKey, error)
}

// Estados que emite el Runner (evento monitor:state).
const (
	StateRunning      = "running"
	StateStopped      = "stopped"
	StateDisconnected = "disconnected"
	StateDegraded     = "degraded"
)

// Fuentes (campo Source de State).
const (
	SourceProcesses = "processlist"
	SourceStatus    = "status"
	SourceDigest    = "digest"
	SourceSampler   = "sampler" // la muestra tarda demasiado: el intervalo se espacia
	SourceStore     = "store"
	SourceServer    = "server" // el servidor cambió durante la ejecución
)

// Sample es lo que recibe el evento monitor:sample.
type Sample struct {
	RunID            int64
	ProfileID        string
	At               time.Time
	QPS              float64
	ThreadsRunning   float64
	ThreadsConnected float64
	LongestSec       int64
}

// State es lo que recibe el evento monitor:state. Source y Reason solo van en degradaciones y avisos.
type State struct {
	RunID     int64
	ProfileID string
	State     string
	Source    string
	Reason    string
}

const (
	minIntervalSec = 5
	maxIntervalSec = 900
	defaultTopK    = 30
	maxTopK        = 200
	failuresToDeg  = 3
)

type RunnerDeps struct {
	Reader    driver.ActivityReader
	Store     Store
	Clock     Clock // por defecto RealClock
	ProfileID string
	Caps      model.MonitorCaps // resultado de Probe: decide qué fuentes se leen y fija la identidad del servidor
	Config    model.MonitorConfig

	OnSample func(Sample) // opcionales; se llaman desde la goroutine del Runner, no deben bloquear
	OnState  func(State)

	SourceTimeout time.Duration // por fuente; por defecto 5 s
	ServerIDEvery int           // cada cuántas muestras se vuelve a sondear la identidad del servidor; por defecto 10
}

// Runner es una sesión de monitoreo: una goroutine, una muestra a la vez.
type Runner struct {
	d      RunnerDeps
	parent context.Context
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	done   chan struct{}
	once   sync.Once

	run           store.MonitorRun
	stopRequested atomic.Bool
	serverChanged bool

	eng       *Engine
	own       map[string]bool   // digest → es una sentencia fija del monitor (⚑D16); solo los clasificados
	texts     map[string]string // textos leídos al clasificar en esta muestra; evitan pedirlos otra vez
	interval  time.Duration
	effective time.Duration
	hasPrev   bool
	prevAt    time.Time
	nSamples  int
	fails     map[string]int
	degraded  map[string]bool
}

// StartRunner valida la configuración, abre la ejecución en el store y arranca la goroutine. Termina cuando se llama
// a Stop (razón stopped) o cuando parent se cancela, p. ej. al cerrarse la sesión (razón disconnected).
func StartRunner(parent context.Context, d RunnerDeps) (*Runner, error) {
	if d.Clock == nil {
		d.Clock = RealClock{}
	}
	if d.SourceTimeout <= 0 {
		d.SourceTimeout = 5 * time.Second
	}
	if d.ServerIDEvery <= 0 {
		d.ServerIDEvery = 10
	}
	if d.Config.IntervalSec == 0 {
		d.Config.IntervalSec = 30
	}
	if d.Config.IntervalSec < minIntervalSec || d.Config.IntervalSec > maxIntervalSec {
		return nil, apperr.New(apperr.Validation, "El intervalo debe estar entre 5 y 900 segundos").WithDetail("interval")
	}
	if d.Config.TopK == 0 {
		d.Config.TopK = defaultTopK
	}
	if d.Config.TopK < 1 || d.Config.TopK > maxTopK {
		return nil, apperr.New(apperr.Validation, "topK debe estar entre 1 y 200").WithDetail("topK")
	}
	run, err := d.Store.OpenMonitorRun(parent, store.MonitorRun{
		ProfileID: d.ProfileID, StartedAt: d.Clock.Now(), IntervalSec: d.Config.IntervalSec,
		ServerVersion: d.Caps.ServerVersion, ServerID: d.Caps.ServerID,
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	r := &Runner{
		d: d, parent: parent, ctx: ctx, cancel: cancel, done: make(chan struct{}), run: run, eng: NewEngine(), own: map[string]bool{},
		interval: time.Duration(d.Config.IntervalSec) * time.Second, fails: map[string]int{}, degraded: map[string]bool{},
	}
	r.effective = r.interval
	r.emitState(StateRunning, "", "")
	r.wg.Add(1)
	go r.loop()
	return r, nil
}

// Run devuelve la ejecución tal como se abrió.
func (r *Runner) Run() store.MonitorRun { return r.run }

// Done se cierra cuando la goroutine terminó y la ejecución quedó cerrada en el store.
func (r *Runner) Done() <-chan struct{} { return r.done }

// Stop detiene el muestreo y espera a que termine. Es idempotente.
func (r *Runner) Stop() {
	r.once.Do(func() {
		r.stopRequested.Store(true)
		r.cancel()
	})
	r.wg.Wait()
	<-r.done
}

func (r *Runner) loop() {
	defer close(r.done)
	defer r.wg.Done()
	for {
		start := r.d.Clock.Now()
		r.sample(start)
		if r.ctx.Err() != nil || r.serverChanged {
			break
		}
		dur := r.d.Clock.Now().Sub(start)
		r.adjust(dur)
		if err := r.d.Clock.Sleep(r.ctx, max(r.effective-dur, 0)); err != nil {
			break
		}
	}
	r.finish()
}

// adjust espacia el intervalo si la muestra tarda más de la mitad (la sonda no debe ser el problema) y lo recupera
// cuando vuelve a ser rápida.
func (r *Runner) adjust(dur time.Duration) {
	switch {
	case dur*2 > r.effective:
		next := min(dur*2, maxIntervalSec*time.Second)
		if next > r.effective {
			r.effective = next
			r.emitState(StateDegraded, SourceSampler, "slow")
		}
	case r.effective > r.interval && dur*4 < r.effective:
		r.effective = max(r.interval, r.effective/2)
	}
}

func (r *Runner) finish() {
	reason, state := store.RunStopped, StateStopped
	if !r.stopRequested.Load() && !r.serverChanged && r.parent.Err() != nil {
		reason, state = store.RunDisconnected, StateDisconnected
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.parent), 5*time.Second)
	defer cancel()
	_ = r.d.Store.CloseMonitorRun(ctx, r.run.ID, reason, r.d.Clock.Now())
	r.emitState(state, "", "")
}

func (r *Runner) emitState(state, source, reason string) {
	if r.d.OnState != nil {
		r.d.OnState(State{RunID: r.run.ID, ProfileID: r.d.ProfileID, State: state, Source: source, Reason: reason})
	}
}

// record lleva la cuenta de fallos seguidos por fuente: tres seguidos la marcan degradada, y un éxito la recupera.
func (r *Runner) record(source string, err error) {
	if err == nil {
		r.fails[source] = 0
		if r.degraded[source] {
			r.degraded[source] = false
			r.emitState(StateRunning, source, "recovered")
		}
		return
	}
	r.fails[source]++
	if r.fails[source] >= failuresToDeg && !r.degraded[source] {
		r.degraded[source] = true
		r.emitState(StateDegraded, source, "error")
	}
}

func (r *Runner) withTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.ctx, r.d.SourceTimeout)
}

func usable(s model.SourceState) bool { return s.State != model.SourceUnavailable }

// sample toma una muestra: estado global, procesos y digest, en ese orden (un reinicio detectado en el estado descarta
// la línea base del digest antes de leerlo). Una fuente que falla no tumba la muestra.
func (r *Runner) sample(now time.Time) {
	in := store.SampleInput{RunID: r.run.ID, ProfileID: r.d.ProfileID, At: now, Metrics: map[string]float64{}}
	if r.hasPrev {
		span := now.Sub(r.prevAt)
		in.SpanMs = span.Milliseconds()
		in.AfterGap = span > 2*r.effective
	}
	collected := false
	var longest int64

	if r.nSamples > 0 && r.nSamples%r.d.ServerIDEvery == 0 && r.d.Caps.ServerID != "" {
		ctx, cancel := r.withTimeout()
		caps, err := r.d.Reader.Probe(ctx)
		cancel()
		if err == nil && caps.ServerID != "" && caps.ServerID != r.d.Caps.ServerID {
			r.serverChanged = true
			r.emitState(StateStopped, SourceServer, "server_changed")
			return
		}
	}
	r.nSamples++

	if r.d.Config.Status && usable(r.d.Caps.Status) {
		ctx, cancel := r.withTimeout()
		vals, err := r.d.Reader.GlobalStatus(ctx, StatusNames())
		cancel()
		if r.ctx.Err() != nil {
			return
		}
		r.record(SourceStatus, err)
		if err == nil {
			m, _ := r.eng.Status(now, vals)
			for k, v := range m {
				in.Metrics[k] = v
			}
			collected = true
		}
	}
	if r.d.Config.Processes && usable(r.d.Caps.Processlist) {
		ctx, cancel := r.withTimeout()
		rows, err := r.d.Reader.Processes(ctx)
		cancel()
		if r.ctx.Err() != nil {
			return
		}
		r.record(SourceProcesses, err)
		if err == nil {
			sum, long := Aggregate(rows)
			for k, v := range ProcessMetrics(sum, long) {
				in.Metrics[k] = v
			}
			in.ProcJSON = ProcJSON(rows, long)
			longest = sum.LongestSec
			collected = true
		}
	}
	if r.d.Config.Digest && usable(r.d.Caps.Digest) {
		ctx, cancel := r.withTimeout()
		list, err := r.d.Reader.Digests(ctx)
		cancel()
		if r.ctx.Err() != nil {
			return
		}
		if err == nil {
			// Si no se puede clasificar, esta muestra no escribe digest y la línea base no avanza: el delta de la
			// siguiente cubre ambos intervalos, sin pérdida.
			err = r.classify(list)
		}
		r.record(SourceDigest, err)
		if err == nil {
			deltas, _ := r.eng.DigestExcluding(list, r.d.Config.TopK, func(d string) bool { return r.own[d] })
			in.Digests = r.fillTexts(deltas)
			collected = true
		}
	}
	if !collected {
		return
	}
	if r.ctx.Err() != nil {
		return
	}
	_, err := r.d.Store.InsertSample(r.ctx, in)
	r.record(SourceStore, err)
	if err != nil {
		return
	}
	r.hasPrev, r.prevAt = true, now
	if r.d.OnSample != nil {
		r.d.OnSample(Sample{RunID: r.run.ID, ProfileID: r.d.ProfileID, At: now, QPS: in.Metrics["qps"],
			ThreadsRunning: in.Metrics["threads_running"], ThreadsConnected: in.Metrics["threads_connected"], LongestSec: longest})
	}
}

// maxOwnCache acota la memoria de la clasificación; si se llena se vacía (reclasificar es barato).
const maxOwnCache = 50_000

// classify lee el texto de los digests con cambios que aún no están clasificados y marca los que son sentencias del
// propio monitor (⚑D16). Los textos leídos quedan en r.texts para fillTexts.
func (r *Runner) classify(list []model.DigestCounters) error {
	r.texts = nil
	var todo []string
	for _, d := range r.eng.ChangedDigests(list) {
		if _, ok := r.own[d]; !ok {
			todo = append(todo, d)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	ctx, cancel := r.withTimeout()
	defer cancel()
	texts, err := r.d.Reader.DigestTexts(ctx, todo)
	if err != nil {
		return err
	}
	if len(r.own) > maxOwnCache {
		r.own = map[string]bool{}
	}
	for _, d := range todo {
		if t, ok := texts[d]; ok { // un digest desalojado entre lecturas no se clasifica: se reintenta si vuelve
			r.own[d] = IsOwnStatement(t)
		}
	}
	r.texts = texts
	return nil
}

// fillTexts pide el texto solo de los digests que el diccionario local no tiene. Si falla, los deltas se guardan sin
// texto y se vuelve a pedir en la siguiente muestra.
func (r *Runner) fillTexts(deltas []store.DigestDelta) []store.DigestDelta {
	var keys []store.DigestKey
	for _, d := range deltas {
		if d.Digest != store.OtherDigest && d.Digest != OverflowDigest {
			keys = append(keys, store.DigestKey{Schema: d.Schema, Digest: d.Digest})
		}
	}
	if len(keys) == 0 {
		return deltas
	}
	ctx, cancel := r.withTimeout()
	defer cancel()
	missing, err := r.d.Store.MissingDigestTexts(ctx, r.d.ProfileID, keys)
	if err != nil || len(missing) == 0 {
		return deltas
	}
	want := make([]string, 0, len(missing))
	texts := make(map[string]string, len(missing))
	for _, k := range missing {
		if t, ok := r.texts[k.Digest]; ok { // ya se leyó al clasificar
			texts[k.Digest] = t
			continue
		}
		want = append(want, k.Digest)
	}
	if len(want) > 0 {
		more, err := r.d.Reader.DigestTexts(ctx, want)
		if err != nil {
			return deltas
		}
		for k, v := range more {
			texts[k] = v
		}
	}
	for i := range deltas {
		if t, ok := texts[deltas[i].Digest]; ok && deltas[i].Text == "" {
			deltas[i].Text = t
		}
	}
	return deltas
}
