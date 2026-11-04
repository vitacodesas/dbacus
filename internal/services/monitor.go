package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/monitor"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/store"
)

// Eventos del monitoreo (spec 02 / spec 22). Los ID de ejecución viajan como texto.

type MonitorSampleEvent struct {
	RunID            string    `json:"runId"`
	At               time.Time `json:"at"`
	QPS              float64   `json:"qps"`
	ThreadsRunning   float64   `json:"threadsRunning"`
	ThreadsConnected float64   `json:"threadsConnected"`
	LongestSec       int64     `json:"longestSec"`
}

type MonitorStateEvent struct {
	RunID     string `json:"runId"`
	ProfileID string `json:"profileId"`
	State     string `json:"state"` // running | stopped | disconnected | degraded
	Source    string `json:"source,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type MonitorProcessesEvent struct {
	SessionID string                `json:"sessionId"`
	Snapshot  model.ProcessSnapshot `json:"snapshot"`
}

func init() {
	application.RegisterEvent[MonitorSampleEvent]("monitor:sample")
	application.RegisterEvent[MonitorStateEvent]("monitor:state")
	application.RegisterEvent[MonitorProcessesEvent]("monitor:processes")
}

// MonitorStore es lo que MonitorService necesita de la persistencia (lo implementa *store.Store).
type MonitorStore interface {
	monitor.Store
	GetProfile(ctx context.Context, id string) (model.ConnectionProfile, error)
	GetSettings(ctx context.Context) (model.Settings, error)
	ListMonitorRuns(ctx context.Context, profileID string) ([]store.MonitorRun, error)
	CloseDanglingMonitorRuns(ctx context.Context) (int, error)
	AddMonitorAnnotation(ctx context.Context, a store.MonitorAnnotation) (store.MonitorAnnotation, error)
	DeleteMonitorAnnotation(ctx context.Context, id int64) error
	PurgeMonitorData(ctx context.Context, profileID string, before *time.Time) error
	PurgeMonitor(ctx context.Context, now time.Time, r store.MonitorRetention) (store.MonitorPurgeResult, error)
	TopDigests(ctx context.Context, req model.TopRequest) ([]model.DigestStat, error)
	CompareDigests(ctx context.Context, profileID string, a, b model.TimeRange) (model.Comparison, error)
	DigestDetail(ctx context.Context, profileID, digest string, from, to time.Time) (model.DigestDetail, error)
	SeriesData(ctx context.Context, req model.SeriesRequest) ([]model.Series, error)
}

type MonitorDeps struct {
	Sessions *session.Manager
	Store    MonitorStore
	Emit     func(name string, data any) // opcional

	// Solo para tests: reloj del Runner/Watcher, tiempos y periodo del mantenimiento.
	Clock             monitor.Clock
	SourceTimeout     time.Duration
	Lease             time.Duration
	MaintenanceEvery  time.Duration // por defecto 1 h
	ProbeTimeout      time.Duration // por defecto 10 s
	DefaultWatchEvery time.Duration // por defecto 2 s
}

const (
	monIntervalMin  = 5
	monIntervalMax  = 900
	monTopKMax      = 200
	monAnnotMax     = 200
	monWatchMaxMs   = 60_000
	monWatchDefault = 2000
)

type monRun struct {
	cancel    context.CancelFunc
	runner    *monitor.Runner
	runID     int64
	profileID string
	sessionID string
}

type monWatch struct {
	w *monitor.Watcher
}

// MonitorService expone el monitoreo DBA (spec 22): vista de procesos en vivo, sesiones de muestreo y lecturas del
// histórico. Todo es de solo lectura sobre el servidor, así que funciona también en perfiles de solo lectura.
type MonitorService struct {
	d MonitorDeps

	mu       sync.Mutex
	closed   bool
	starting map[string]bool    // perfiles con un Start en curso
	byProf   map[string]*monRun // una ejecución por perfil
	byID     map[int64]*monRun
	watchers map[string]*monWatch
	caps     map[string]model.MonitorCaps
	wg       sync.WaitGroup
}

func NewMonitorService(d MonitorDeps) *MonitorService {
	if d.MaintenanceEvery <= 0 {
		d.MaintenanceEvery = time.Hour
	}
	if d.ProbeTimeout <= 0 {
		d.ProbeTimeout = 10 * time.Second
	}
	return &MonitorService{
		d: d, starting: map[string]bool{}, byProf: map[string]*monRun{}, byID: map[int64]*monRun{},
		watchers: map[string]*monWatch{}, caps: map[string]model.MonitorCaps{},
	}
}

func (s *MonitorService) emit(name string, data any) {
	if s.d.Emit != nil {
		s.d.Emit(name, data)
	}
}

func activityReader(sess *session.Session) (driver.ActivityReader, error) {
	r, ok := sess.Pool.(driver.ActivityReader)
	if !ok {
		return nil, apperr.New(apperr.Unsupported, "Este motor no tiene monitoreo en Dbacus")
	}
	return r, nil
}

func (s *MonitorService) session(id string) (*session.Session, driver.ActivityReader, error) {
	sess, err := s.d.Sessions.Get(id)
	if err != nil {
		return nil, nil, toAppErr(err)
	}
	r, err := activityReader(sess)
	if err != nil {
		return nil, nil, err
	}
	return sess, r, nil
}

// probe sondea y cachea las capacidades de la sesión; refresh fuerza una lectura nueva.
func (s *MonitorService) probe(ctx context.Context, sess *session.Session, r driver.ActivityReader, refresh bool) (model.MonitorCaps, error) {
	s.mu.Lock()
	s.dropClosedCapsLocked()
	c, ok := s.caps[sess.ID]
	s.mu.Unlock()
	if ok && !refresh {
		return c, nil
	}
	pctx, cancel := context.WithTimeout(ctx, s.d.ProbeTimeout)
	defer cancel()
	c, err := r.Probe(pctx)
	if err != nil {
		return model.MonitorCaps{}, toAppErr(err)
	}
	s.mu.Lock()
	s.caps[sess.ID] = c
	s.mu.Unlock()
	return c, nil
}

func (s *MonitorService) dropClosedCapsLocked() {
	for id := range s.caps {
		if _, err := s.d.Sessions.Get(id); err != nil {
			delete(s.caps, id)
		}
	}
}

// Capabilities devuelve qué fuentes se pueden leer (cacheado por sesión). Una fuente no disponible es un estado,
// no un error (spec 22 ⚑D9).
func (s *MonitorService) Capabilities(ctx context.Context, sessionID string) (model.MonitorCaps, error) {
	sess, r, err := s.session(sessionID)
	if err != nil {
		return model.MonitorCaps{}, err
	}
	return s.probe(ctx, sess, r, false)
}

// RefreshCapabilities vuelve a sondear (p. ej. tras conceder un privilegio).
func (s *MonitorService) RefreshCapabilities(ctx context.Context, sessionID string) (model.MonitorCaps, error) {
	sess, r, err := s.session(sessionID)
	if err != nil {
		return model.MonitorCaps{}, err
	}
	return s.probe(ctx, sess, r, true)
}

func validateMonitorConfig(cfg model.MonitorConfig) error {
	if cfg.IntervalSec < monIntervalMin || cfg.IntervalSec > monIntervalMax {
		return apperr.New(apperr.Validation, "El intervalo debe estar entre 5 y 900 segundos").WithDetail("interval")
	}
	if cfg.TopK < 1 || cfg.TopK > monTopKMax {
		return apperr.New(apperr.Validation, "topK debe estar entre 1 y 200").WithDetail("topK")
	}
	return nil
}

func toMonitorRun(r store.MonitorRun) model.MonitorRun {
	return model.MonitorRun{
		ID: strconv.FormatInt(r.ID, 10), ProfileID: r.ProfileID, StartedAt: r.StartedAt, EndedAt: r.EndedAt, Reason: r.Reason,
		IntervalSec: r.IntervalSec, ServerVersion: r.ServerVersion, ServerID: r.ServerID, Samples: r.Samples, Gaps: r.Gaps,
	}
}

// Start abre una sesión de monitoreo para la conexión. Solo hay una por perfil (QUERY_FAILED/busy).
func (s *MonitorService) Start(ctx context.Context, sessionID string, cfg model.MonitorConfig) (model.MonitorRun, error) {
	sess, r, err := s.session(sessionID)
	if err != nil {
		return model.MonitorRun{}, err
	}
	settings, err := s.d.Store.GetSettings(ctx)
	if err != nil {
		return model.MonitorRun{}, toAppErr(err)
	}
	if cfg.IntervalSec == 0 {
		cfg.IntervalSec = settings.MonitorIntervalSec
	}
	if cfg.TopK == 0 {
		cfg.TopK = 30
	}
	if !cfg.Processes && !cfg.Status && !cfg.Digest {
		cfg.Processes, cfg.Status, cfg.Digest = true, true, true
	}
	if err := validateMonitorConfig(cfg); err != nil {
		return model.MonitorRun{}, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return model.MonitorRun{}, apperr.New(apperr.SessionClosed, "La aplicación se está cerrando")
	}
	if s.byProf[sess.ProfileID] != nil || s.starting[sess.ProfileID] {
		s.mu.Unlock()
		return model.MonitorRun{}, apperr.New(apperr.QueryFailed, "Ya hay un monitoreo en marcha para esta conexión").WithDetail("busy")
	}
	s.starting[sess.ProfileID] = true
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		delete(s.starting, sess.ProfileID)
		s.mu.Unlock()
	}

	caps, err := s.probe(ctx, sess, r, false)
	if err != nil {
		release()
		return model.MonitorRun{}, err
	}
	if !(cfg.Processes && caps.Processlist.State != model.SourceUnavailable) &&
		!(cfg.Status && caps.Status.State != model.SourceUnavailable) &&
		!(cfg.Digest && caps.Digest.State != model.SourceUnavailable) {
		release()
		return model.MonitorRun{}, apperr.New(apperr.Unsupported, "Ninguna fuente de monitoreo está disponible en esta conexión")
	}

	runCtx, cancel := context.WithCancel(sess.Context())
	runner, err := monitor.StartRunner(runCtx, monitor.RunnerDeps{
		Reader: r, Store: s.d.Store, Clock: s.d.Clock, ProfileID: sess.ProfileID, Caps: caps, Config: cfg,
		SourceTimeout: s.d.SourceTimeout,
		OnSample: func(m monitor.Sample) {
			s.emit("monitor:sample", MonitorSampleEvent{RunID: strconv.FormatInt(m.RunID, 10), At: m.At, QPS: m.QPS,
				ThreadsRunning: m.ThreadsRunning, ThreadsConnected: m.ThreadsConnected, LongestSec: m.LongestSec})
		},
		OnState: func(st monitor.State) {
			s.emit("monitor:state", MonitorStateEvent{RunID: strconv.FormatInt(st.RunID, 10), ProfileID: st.ProfileID,
				State: st.State, Source: st.Source, Reason: st.Reason})
		},
	})
	if err != nil {
		cancel()
		release()
		return model.MonitorRun{}, toAppErr(err)
	}
	run := runner.Run()
	e := &monRun{runner: runner, runID: run.ID, profileID: sess.ProfileID, sessionID: sess.ID}

	s.mu.Lock()
	delete(s.starting, sess.ProfileID)
	s.byProf[sess.ProfileID] = e
	s.byID[run.ID] = e
	s.wg.Add(1)
	s.mu.Unlock()
	e.cancel = cancel
	go func() { // dueña de la limpieza: termina cuando el Runner termina (Stop, sesión cerrada o servidor distinto)
		defer s.wg.Done()
		<-runner.Done()
		s.forget(e)
	}()
	slog.Info("monitoreo iniciado", "run", run.ID, "intervalSec", cfg.IntervalSec)
	return toMonitorRun(run), nil
}

// Stop detiene una ejecución y espera a que quede cerrada. Detener una ya terminada no es error si existe en el
// histórico; un ID desconocido es NOT_FOUND.
func (s *MonitorService) Stop(ctx context.Context, runID string) error {
	id, err := strconv.ParseInt(runID, 10, 64)
	if err != nil || id <= 0 {
		return apperr.New(apperr.NotFound, "La ejecución no existe")
	}
	s.mu.Lock()
	e := s.byID[id]
	s.mu.Unlock()
	if e == nil {
		return apperr.New(apperr.NotFound, "La ejecución no está activa")
	}
	e.runner.Stop()
	s.forget(e) // libera el perfil ya, sin esperar a la goroutine de limpieza: un Start inmediato no debe ver «busy»
	return nil
}

// forget quita una ejecución terminada de los índices. Es idempotente.
func (s *MonitorService) forget(e *monRun) {
	e.cancel()
	s.mu.Lock()
	if s.byProf[e.profileID] == e {
		delete(s.byProf, e.profileID)
	}
	delete(s.byID, e.runID)
	s.mu.Unlock()
}

// Runs lista las ejecuciones guardadas de un perfil, la más reciente primero.
func (s *MonitorService) Runs(ctx context.Context, profileID string) ([]model.MonitorRun, error) {
	if profileID == "" {
		return nil, apperr.New(apperr.Validation, "Falta el perfil").WithDetail("profileId")
	}
	rs, err := s.d.Store.ListMonitorRuns(ctx, profileID)
	if err != nil {
		return nil, toAppErr(err)
	}
	out := make([]model.MonitorRun, 0, len(rs))
	for _, r := range rs {
		out = append(out, toMonitorRun(r))
	}
	return out, nil
}

// WatchProcesses inicia la vista En vivo o renueva su arrendamiento (30 s); es idempotente. Funciona sin ejecución.
func (s *MonitorService) WatchProcesses(ctx context.Context, sessionID string, intervalMs int) error {
	sess, r, err := s.session(sessionID)
	if err != nil {
		return err
	}
	if intervalMs == 0 {
		intervalMs = monWatchDefault
	}
	if intervalMs < 0 || intervalMs > monWatchMaxMs {
		return apperr.New(apperr.Validation, "El intervalo de la vista en vivo debe estar entre 1 y 60 segundos").WithDetail("interval")
	}
	s.mu.Lock()
	if w := s.watchers[sessionID]; w != nil {
		w.w.Renew(intervalMs)
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	caps, err := s.probe(ctx, sess, r, false)
	if err != nil {
		return err
	}
	if caps.Processlist.State == model.SourceUnavailable {
		return apperr.New(apperr.Unsupported, "La lista de procesos no está disponible en esta conexión")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return apperr.New(apperr.SessionClosed, "La aplicación se está cerrando")
	}
	if w := s.watchers[sessionID]; w != nil { // otro WatchProcesses ganó la carrera
		w.w.Renew(intervalMs)
		return nil
	}
	w := monitor.StartWatcher(sess.Context(), monitor.WatcherDeps{
		Reader: r, Clock: s.d.Clock, Lease: s.d.Lease, SourceTimeout: s.d.SourceTimeout,
		OnSnapshot: func(snap model.ProcessSnapshot) {
			s.emit("monitor:processes", MonitorProcessesEvent{SessionID: sessionID, Snapshot: snap})
		},
	}, intervalMs)
	mw := &monWatch{w: w}
	s.watchers[sessionID] = mw
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		<-w.Done()
		s.mu.Lock()
		if s.watchers[sessionID] == mw {
			delete(s.watchers, sessionID)
		}
		s.mu.Unlock()
	}()
	return nil
}

// UnwatchProcesses detiene la vista En vivo de la sesión. Es idempotente.
func (s *MonitorService) UnwatchProcesses(sessionID string) error {
	s.mu.Lock()
	w := s.watchers[sessionID]
	s.mu.Unlock()
	if w != nil {
		w.w.Stop()
	}
	return nil
}

func (s *MonitorService) Top(ctx context.Context, req model.TopRequest) ([]model.DigestStat, error) {
	out, err := s.d.Store.TopDigests(ctx, req)
	if err != nil {
		return nil, toAppErr(err)
	}
	if out == nil {
		out = []model.DigestStat{}
	}
	return out, nil
}

func (s *MonitorService) Digest(ctx context.Context, profileID, digest string, from, to time.Time) (model.DigestDetail, error) {
	d, err := s.d.Store.DigestDetail(ctx, profileID, digest, from, to)
	return d, toAppErr(err)
}

func (s *MonitorService) Series(ctx context.Context, req model.SeriesRequest) ([]model.Series, error) {
	out, err := s.d.Store.SeriesData(ctx, req)
	if err != nil {
		return nil, toAppErr(err)
	}
	if out == nil {
		out = []model.Series{}
	}
	return out, nil
}

func (s *MonitorService) Compare(ctx context.Context, profileID string, rangeA, rangeB model.TimeRange) (model.Comparison, error) {
	c, err := s.d.Store.CompareDigests(ctx, profileID, rangeA, rangeB)
	if err != nil {
		return model.Comparison{}, toAppErr(err)
	}
	if c.Regressions == nil {
		c.Regressions = []model.DigestStat{}
	}
	if c.New == nil {
		c.New = []model.DigestStat{}
	}
	if c.Gone == nil {
		c.Gone = []model.DigestStat{}
	}
	return c, nil
}

func validAnnotationText(t string) error {
	if t == "" || utf8.RuneCountInString(t) > monAnnotMax {
		return apperr.New(apperr.Validation, "La nota debe tener entre 1 y 200 caracteres").WithDetail("text")
	}
	for _, r := range t {
		if unicode.IsControl(r) {
			return apperr.New(apperr.Validation, "La nota no admite caracteres de control").WithDetail("text")
		}
	}
	return nil
}

// Annotate añade una nota sobre la línea de tiempo del perfil. at vacío = ahora.
func (s *MonitorService) Annotate(ctx context.Context, profileID string, at time.Time, text string) (model.Annotation, error) {
	if profileID == "" {
		return model.Annotation{}, apperr.New(apperr.Validation, "Falta el perfil").WithDetail("profileId")
	}
	if err := validAnnotationText(text); err != nil {
		return model.Annotation{}, err
	}
	if _, err := s.d.Store.GetProfile(ctx, profileID); err != nil {
		return model.Annotation{}, toAppErr(err)
	}
	if at.IsZero() {
		at = time.Now()
	}
	a, err := s.d.Store.AddMonitorAnnotation(ctx, store.MonitorAnnotation{ProfileID: profileID, At: at, Text: text})
	if err != nil {
		return model.Annotation{}, toAppErr(err)
	}
	return model.Annotation{ID: strconv.FormatInt(a.ID, 10), ProfileID: a.ProfileID, At: a.At, Text: a.Text}, nil
}

func (s *MonitorService) DeleteAnnotation(ctx context.Context, id string) error {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return apperr.New(apperr.Validation, "Identificador de nota no válido").WithDetail("id")
	}
	return toAppErr(s.d.Store.DeleteMonitorAnnotation(ctx, n))
}

// Purge borra datos del perfil (todo, o lo anterior a before). Borrarlo todo con una ejecución activa no se permite.
func (s *MonitorService) Purge(ctx context.Context, profileID string, before *time.Time) error {
	if profileID == "" {
		return apperr.New(apperr.Validation, "Falta el perfil").WithDetail("profileId")
	}
	if before == nil {
		s.mu.Lock()
		busy := s.byProf[profileID] != nil || s.starting[profileID]
		s.mu.Unlock()
		if busy {
			return apperr.New(apperr.QueryFailed, "Detén el monitoreo antes de borrar sus datos").WithDetail("busy")
		}
	}
	return toAppErr(s.d.Store.PurgeMonitorData(ctx, profileID, before))
}

// CopyKill arma el KILL de un proceso para copiarlo; no ejecuta nada (spec 22 ⚑D8).
func (s *MonitorService) CopyKill(sessionID string, processID int64, queryOnly bool) (string, error) {
	if _, err := s.d.Sessions.Get(sessionID); err != nil {
		return "", toAppErr(err)
	}
	if processID <= 0 {
		return "", apperr.New(apperr.Validation, "Identificador de proceso no válido").WithDetail("processId")
	}
	if queryOnly {
		return fmt.Sprintf("KILL QUERY %d;", processID), nil
	}
	return fmt.Sprintf("KILL %d;", processID), nil
}

// autoStart arranca el monitoreo con los valores por defecto tras conectar un perfil con MonitorAutoStart. Es de mejor
// esfuerzo y no bloquea la conexión: un fallo se registra solo con su código.
func (s *MonitorService) autoStart(sessionID string, p model.ConnectionProfile) {
	if !p.MonitorAutoStart {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		sess, err := s.d.Sessions.Get(sessionID)
		if err != nil {
			return
		}
		if _, err := s.Start(sess.Context(), sessionID, model.MonitorConfig{}); err != nil {
			slog.Warn("el monitoreo automático no arrancó", "code", apperr.From(err).Code)
		}
	}()
}

// runMaintenance cierra las ejecuciones que quedaron abiertas por un cierre brusco, purga lo caducado y repite cada
// hora hasta que ctx termine (spec 22 ⚑D10, ⚑D15). Se lanza con Lifecycle.Go a través de MonitorBackground.
func (s *MonitorService) runMaintenance(ctx context.Context) {
	if n, err := s.d.Store.CloseDanglingMonitorRuns(ctx); err != nil {
		slog.Warn("cierre de ejecuciones colgadas falló", "code", apperr.From(err).Code)
	} else if n > 0 {
		slog.Info("ejecuciones de monitoreo colgadas cerradas", "n", n)
	}
	s.purgeOnce(ctx)
	t := time.NewTicker(s.d.MaintenanceEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.purgeOnce(ctx)
		}
	}
}

func (s *MonitorService) purgeOnce(ctx context.Context) {
	st, err := s.d.Store.GetSettings(ctx)
	if err == nil {
		_, err = s.d.Store.PurgeMonitor(ctx, time.Now(), store.MonitorRetentionFromSettings(st))
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("purga del monitoreo falló", "code", apperr.From(err).Code)
	}
}

// MonitorBackground es lo que app.go engancha al ciclo de vida (conexión y arranque). No es un servicio de Wails:
// sus métodos no se exponen a Vue (regla de oro 3: solo los contratos del spec 02).
type MonitorBackground struct{ s *MonitorService }

func BackgroundOf(s *MonitorService) MonitorBackground { return MonitorBackground{s} }

// AutoStart es el gancho de ConnectionDeps.OnConnected.
func (b MonitorBackground) AutoStart(sessionID string, p model.ConnectionProfile) {
	b.s.autoStart(sessionID, p)
}

// RunMaintenance se lanza con Lifecycle.Go.
func (b MonitorBackground) RunMaintenance(ctx context.Context) { b.s.runMaintenance(ctx) }

// ServiceShutdown detiene todas las ejecuciones y vigilancias y espera a sus goroutines.
func (s *MonitorService) ServiceShutdown() error {
	s.mu.Lock()
	s.closed = true
	runs := make([]*monRun, 0, len(s.byID))
	for _, e := range s.byID {
		runs = append(runs, e)
	}
	ws := make([]*monWatch, 0, len(s.watchers))
	for _, w := range s.watchers {
		ws = append(ws, w)
	}
	s.mu.Unlock()
	for _, e := range runs {
		e.runner.Stop()
	}
	for _, w := range ws {
		w.w.Stop()
	}
	s.wg.Wait()
	return nil
}
