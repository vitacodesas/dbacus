package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/store"
)

var monT0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// monTestClock no espera tiempo real: con block=true Sleep se queda hasta que el ctx termine (una sola muestra por
// ejecución); si no, avanza el tiempo al instante.
type monTestClock struct {
	mu    sync.Mutex
	now   time.Time
	block bool
}

func (c *monTestClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *monTestClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.block {
		<-ctx.Done()
		return ctx.Err()
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	time.Sleep(time.Millisecond)
	return ctx.Err()
}

type monEnv struct {
	*env
	svc    *MonitorService
	sid    string
	pid    string
	events chan namedEvent
	clock  *monTestClock
}

func newMonitorEnv(t *testing.T, block bool, profileMut ...func(*model.ConnectionProfile)) *monEnv {
	t.Helper()
	e := newEnv(t)
	e.drv.OnGlobalStatus = func(context.Context, []string) (map[string]uint64, error) {
		return map[string]uint64{"Uptime": 1000, "Questions": 50, "Threads_running": 2, "Threads_connected": 7}, nil
	}
	prof := e.profile("M")
	for _, m := range profileMut {
		m(&prof)
	}
	p, err := e.svc.Save(context.Background(), prof, ConnectionSecrets{})
	require.NoError(t, err)
	info, err := e.svc.Connect(context.Background(), p.ID, nil)
	require.NoError(t, err)

	me := &monEnv{env: e, sid: info.SessionID, pid: p.ID, events: make(chan namedEvent, 512), clock: &monTestClock{now: monT0, block: block}}
	me.svc = NewMonitorService(MonitorDeps{
		Sessions: e.sessions, Store: e.st, Clock: me.clock, Lease: 30 * time.Second,
		Emit: func(name string, data any) {
			select {
			case me.events <- namedEvent{name, data}:
			default:
			}
		},
	})
	t.Cleanup(func() { _ = me.svc.ServiceShutdown() })
	return me
}

func (m *monEnv) next(t *testing.T, name string, match func(any) bool) any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-m.events:
			if ev.name == name && (match == nil || match(ev.data)) {
				return ev.data
			}
		case <-deadline:
			t.Fatalf("no llegó el evento %q", name)
		}
	}
}

func stateIs(state string) func(any) bool {
	return func(d any) bool { return d.(MonitorStateEvent).State == state }
}

func (m *monEnv) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 5*time.Millisecond, what)
}

func (m *monEnv) activeRuns() int { m.svc.mu.Lock(); defer m.svc.mu.Unlock(); return len(m.svc.byProf) }
func (m *monEnv) watchers() int   { m.svc.mu.Lock(); defer m.svc.mu.Unlock(); return len(m.svc.watchers) }

func TestMonitorStartStopAndBusy(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, true)
	run, err := m.svc.Start(ctx, m.sid, model.MonitorConfig{IntervalSec: 5})
	require.NoError(t, err)
	require.NotEmpty(t, run.ID)
	require.Equal(t, m.pid, run.ProfileID)
	require.Equal(t, 5, run.IntervalSec)
	m.next(t, "monitor:state", stateIs("running"))
	m.next(t, "monitor:sample", nil)

	_, err = m.svc.Start(ctx, m.sid, model.MonitorConfig{})
	require.Equal(t, apperr.QueryFailed, code(t, err))
	require.Equal(t, "busy", detail(t, err))

	require.NoError(t, m.svc.Stop(ctx, run.ID))
	m.next(t, "monitor:state", stateIs("stopped"))
	runs, err := m.svc.Runs(ctx, m.pid)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, "stopped", runs[0].Reason)
	require.GreaterOrEqual(t, runs[0].Samples, 1)

	// El perfil queda libre al instante y otra ejecución puede empezar (sin intervalo ni fuentes: usa Settings y las tres).
	run2, err := m.svc.Start(ctx, m.sid, model.MonitorConfig{})
	require.NoError(t, err)
	require.Equal(t, model.DefaultSettings().MonitorIntervalSec, run2.IntervalSec)
	require.NotEqual(t, run.ID, run2.ID)

	err = m.svc.Stop(ctx, "999999")
	require.Equal(t, apperr.NotFound, code(t, err))
	require.Equal(t, apperr.NotFound, code(t, m.svc.Stop(ctx, "no-es-un-numero")))
}

func TestMonitorSessionCloseMarksRunDisconnected(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, true)
	_, err := m.svc.Start(ctx, m.sid, model.MonitorConfig{IntervalSec: 5})
	require.NoError(t, err)
	m.next(t, "monitor:sample", nil)

	require.NoError(t, m.sessions.Close(m.sid))
	m.next(t, "monitor:state", stateIs("disconnected"))
	m.waitFor(t, "la ejecución se retira del índice", func() bool { return m.activeRuns() == 0 })
	runs, err := m.svc.Runs(ctx, m.pid)
	require.NoError(t, err)
	require.Equal(t, "disconnected", runs[0].Reason)
}

func TestMonitorWorksInReadOnlyProfile(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, true, func(p *model.ConnectionProfile) { p.ReadOnly = true })
	run, err := m.svc.Start(ctx, m.sid, model.MonitorConfig{IntervalSec: 5})
	require.NoError(t, err)
	m.next(t, "monitor:sample", nil)
	require.NoError(t, m.svc.WatchProcesses(ctx, m.sid, 1000))
	require.NoError(t, m.svc.Stop(ctx, run.ID))
	_, err = m.svc.CopyKill(m.sid, 7, true)
	require.NoError(t, err)
}

func TestMonitorValidation(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, true)

	for _, c := range []struct {
		cfg model.MonitorConfig
		det string
	}{
		{model.MonitorConfig{IntervalSec: 4}, "interval"},
		{model.MonitorConfig{IntervalSec: 901}, "interval"},
		{model.MonitorConfig{IntervalSec: 30, TopK: 201}, "topK"},
		{model.MonitorConfig{IntervalSec: 30, TopK: -1}, "topK"},
	} {
		_, err := m.svc.Start(ctx, m.sid, c.cfg)
		require.Equal(t, apperr.Validation, code(t, err), c.cfg)
		require.Equal(t, c.det, detail(t, err))
	}
	require.Zero(t, m.activeRuns(), "una validación fallida no deja el perfil ocupado")

	_, err := m.svc.CopyKill(m.sid, 0, false)
	require.Equal(t, "processId", detail(t, err))
	_, err = m.svc.CopyKill(m.sid, -3, true)
	require.Equal(t, "processId", detail(t, err))
	got, err := m.svc.CopyKill(m.sid, 42, false)
	require.NoError(t, err)
	require.Equal(t, "KILL 42;", got)
	got, err = m.svc.CopyKill(m.sid, 42, true)
	require.NoError(t, err)
	require.Equal(t, "KILL QUERY 42;", got)
	_, err = m.svc.CopyKill("no-existe", 42, true)
	require.Equal(t, apperr.SessionClosed, code(t, err))

	require.Equal(t, "interval", detail(t, m.svc.WatchProcesses(ctx, m.sid, 61_000)))
	require.Equal(t, "interval", detail(t, m.svc.WatchProcesses(ctx, m.sid, -1)))

	for _, text := range []string{"", strings.Repeat("a", 201), "con\x00control", "salto\nde linea"} {
		_, err := m.svc.Annotate(ctx, m.pid, monT0, text)
		require.Equal(t, "text", detail(t, err), text)
	}
	_, err = m.svc.Annotate(ctx, "", monT0, "x")
	require.Equal(t, "profileId", detail(t, err))
	_, err = m.svc.Annotate(ctx, "perfil-inexistente", monT0, "x")
	require.Equal(t, apperr.NotFound, code(t, err))
	require.Equal(t, "id", detail(t, m.svc.DeleteAnnotation(ctx, "abc")))
	require.Equal(t, apperr.NotFound, code(t, m.svc.DeleteAnnotation(ctx, "123456")))

	from, to := monT0.Add(-time.Hour), monT0
	_, err = m.svc.Top(ctx, model.TopRequest{ProfileID: m.pid, From: to, To: from, OrderBy: model.OrderTotalTime, Limit: 10})
	require.Equal(t, "range", detail(t, err))
	_, err = m.svc.Top(ctx, model.TopRequest{ProfileID: m.pid, From: from, To: to, OrderBy: "nope", Limit: 10})
	require.Equal(t, "orderBy", detail(t, err))
	_, err = m.svc.Top(ctx, model.TopRequest{ProfileID: m.pid, From: from, To: to, OrderBy: model.OrderTotalTime, Limit: 5000})
	require.Equal(t, "topK", detail(t, err))
	_, err = m.svc.Series(ctx, model.SeriesRequest{ProfileID: m.pid, From: from, To: to, Metrics: []string{"no_existe"}})
	require.Equal(t, "metric", detail(t, err))
	_, err = m.svc.Series(ctx, model.SeriesRequest{ProfileID: m.pid, From: to, To: from, Metrics: []string{"qps"}})
	require.Equal(t, "range", detail(t, err))
	_, err = m.svc.Runs(ctx, "")
	require.Equal(t, "profileId", detail(t, err))

	// Las lecturas vacías son listas, no null (Vue no distingue).
	top, err := m.svc.Top(ctx, model.TopRequest{ProfileID: m.pid, From: from, To: to, OrderBy: model.OrderTotalTime, Limit: 10})
	require.NoError(t, err)
	require.NotNil(t, top)
	cmp, err := m.svc.Compare(ctx, m.pid, model.TimeRange{From: from, To: to}, model.TimeRange{From: to, To: to.Add(time.Hour)})
	require.NoError(t, err)
	require.NotNil(t, cmp.Regressions)
	require.NotNil(t, cmp.New)
	require.NotNil(t, cmp.Gone)
}

// bare no implementa driver.ActivityReader: el servicio responde UNSUPPORTED sin tocar nada.
type bare struct{ driver.Pool }

func TestMonitorUnsupportedWithoutActivityReader(t *testing.T) {
	_, err := activityReader(&session.Session{Pool: bare{}})
	require.Equal(t, apperr.Unsupported, code(t, err))
}

func TestMonitorUnsupportedWhenNoSourceIsAvailable(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, true)
	off := model.SourceState{State: model.SourceUnavailable, Reason: model.ReasonNoPrivilege, Hint: "GRANT …"}
	m.drv.Caps = &model.MonitorCaps{ServerVersion: "x", ServerID: "s", Processlist: off, Digest: off, Status: off}

	caps, err := m.svc.Capabilities(ctx, m.sid)
	require.NoError(t, err, "una fuente no disponible es un estado, no un error")
	require.Equal(t, model.ReasonNoPrivilege, caps.Digest.Reason)
	require.Equal(t, "GRANT …", caps.Digest.Hint)

	_, err = m.svc.Start(ctx, m.sid, model.MonitorConfig{IntervalSec: 5})
	require.Equal(t, apperr.Unsupported, code(t, err))
	require.Zero(t, m.activeRuns())
	require.Equal(t, apperr.Unsupported, code(t, m.svc.WatchProcesses(ctx, m.sid, 1000)))

	// Con una fuente disponible, Start sigue adelante y la que falta se omite.
	m.drv.Caps = &model.MonitorCaps{ServerVersion: "x", ServerID: "s", Processlist: off, Digest: off,
		Status: model.SourceState{State: model.SourceOK, Reason: model.ReasonOK}}
	caps, err = m.svc.RefreshCapabilities(ctx, m.sid)
	require.NoError(t, err)
	require.Equal(t, model.SourceOK, caps.Status.State)
	_, err = m.svc.Start(ctx, m.sid, model.MonitorConfig{IntervalSec: 5})
	require.NoError(t, err)
}

func TestMonitorCapabilitiesAreCachedPerSession(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, true)
	c1, err := m.svc.Capabilities(ctx, m.sid)
	require.NoError(t, err)
	m.drv.Caps = &model.MonitorCaps{ServerVersion: "otra", ServerID: "s",
		Processlist: model.SourceState{State: model.SourceOK}, Digest: model.SourceState{State: model.SourceOK}, Status: model.SourceState{State: model.SourceOK}}
	c2, err := m.svc.Capabilities(ctx, m.sid)
	require.NoError(t, err)
	require.Equal(t, c1, c2, "la segunda lectura sale de la caché")
	c3, err := m.svc.RefreshCapabilities(ctx, m.sid)
	require.NoError(t, err)
	require.Equal(t, "otra", c3.ServerVersion)
	_, err = m.svc.Capabilities(ctx, "no-existe")
	require.Equal(t, apperr.SessionClosed, code(t, err))
}

func TestMonitorWatchLeaseExpiresAndRestarts(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, false) // el reloj avanza solo: el arrendamiento de 30 s caduca sin esperar
	m.drv.OnProcesses = func(context.Context) ([]model.Process, error) {
		return []model.Process{{ID: 9, User: "app", Command: "Query", TimeSec: 3, Info: "SELECT 1"}}, nil
	}
	require.NoError(t, m.svc.WatchProcesses(ctx, m.sid, 1000))
	ev := m.next(t, "monitor:processes", nil).(MonitorProcessesEvent)
	require.Equal(t, m.sid, ev.SessionID)
	require.Len(t, ev.Snapshot.Rows, 1)
	require.Equal(t, "SELECT 1", ev.Snapshot.Rows[0].Info, "INFO llega a la vista en vivo, solo en memoria")

	m.waitFor(t, "el arrendamiento caduca y se retira el observador", func() bool { return m.watchers() == 0 })
	require.NoError(t, m.svc.WatchProcesses(ctx, m.sid, 1000), "tras caducar, vigilar de nuevo arranca otro")
	m.next(t, "monitor:processes", nil)
	require.NoError(t, m.svc.UnwatchProcesses(m.sid))
	require.NoError(t, m.svc.UnwatchProcesses(m.sid), "idempotente")
	m.waitFor(t, "unwatch retira el observador", func() bool { return m.watchers() == 0 })
}

func TestMonitorWatchRenewIsIdempotent(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, true)
	for i := 0; i < 3; i++ {
		require.NoError(t, m.svc.WatchProcesses(ctx, m.sid, 2000))
	}
	require.Equal(t, 1, m.watchers(), "renovar no crea otro observador")
	require.NoError(t, m.sessions.Close(m.sid))
	m.waitFor(t, "cerrar la sesión detiene la vista en vivo", func() bool { return m.watchers() == 0 })
}

func TestMonitorAnnotationsAndPurge(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, true)
	a, err := m.svc.Annotate(ctx, m.pid, monT0, "deploy v2.14")
	require.NoError(t, err)
	require.NotEmpty(t, a.ID)
	require.Equal(t, "deploy v2.14", a.Text)
	require.NoError(t, m.svc.DeleteAnnotation(ctx, a.ID))
	require.Equal(t, apperr.NotFound, code(t, m.svc.DeleteAnnotation(ctx, a.ID)))

	run, err := m.svc.Start(ctx, m.sid, model.MonitorConfig{IntervalSec: 5})
	require.NoError(t, err)
	err = m.svc.Purge(ctx, m.pid, nil)
	require.Equal(t, apperr.QueryFailed, code(t, err))
	require.Equal(t, "busy", detail(t, err))
	old := monT0.Add(-24 * time.Hour)
	require.NoError(t, m.svc.Purge(ctx, m.pid, &old), "purgar lo antiguo sí se puede con una ejecución activa")

	require.NoError(t, m.svc.Stop(ctx, run.ID))
	require.NoError(t, m.svc.Purge(ctx, m.pid, nil))
	runs, err := m.svc.Runs(ctx, m.pid)
	require.NoError(t, err)
	require.Empty(t, runs)
}

func TestMonitorAutoStartFollowsProfileFlag(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, true)
	prof, err := m.st.GetProfile(ctx, m.pid)
	require.NoError(t, err)

	m.svc.autoStart(m.sid, prof) // sin la opción no hace nada
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, m.activeRuns())

	// Con la opción, y enganchado a Connect como en app.go.
	prof.MonitorAutoStart = true
	saved, err := m.env.svc.Save(ctx, prof, ConnectionSecrets{})
	require.NoError(t, err)
	cs := NewConnectionService(ConnectionDeps{Store: m.st, Secrets: m.sec, Sessions: m.sessions, KnownHosts: m.kh, OnConnected: BackgroundOf(m.svc).AutoStart})
	_, err = cs.Connect(ctx, saved.ID, nil)
	require.NoError(t, err)
	m.waitFor(t, "el monitoreo arranca solo tras conectar", func() bool { return m.activeRuns() == 1 })
	m.next(t, "monitor:state", stateIs("running"))
}

func TestMonitorMaintenanceClosesDanglingRunsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMonitorEnv(t, true)
	run, err := m.st.OpenMonitorRun(ctx, store.MonitorRun{ProfileID: m.pid, StartedAt: monT0, IntervalSec: 30})
	require.NoError(t, err)
	require.Equal(t, store.RunRunning, run.Reason)

	m.svc.d.MaintenanceEvery = 10 * time.Millisecond
	done := make(chan struct{})
	go func() { defer close(done); BackgroundOf(m.svc).RunMaintenance(ctx) }()
	m.waitFor(t, "la ejecución colgada se cierra", func() bool {
		rs, err := m.st.ListMonitorRuns(ctx, m.pid)
		return err == nil && len(rs) == 1 && rs[0].Reason == store.RunAppClosed
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunMaintenance no terminó con el contexto")
	}
}

// Los logs del monitoreo llevan códigos y contadores, nunca SQL ni INFO ni texto de digest (regla de oro 5, spec 10).
func TestMonitorLogsNoSQL(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := context.Background()
	m := newMonitorEnv(t, true)
	const secret = "hunter2"
	m.drv.OnProcesses = func(context.Context) ([]model.Process, error) {
		return []model.Process{{ID: 1, Info: "SELECT * FROM users WHERE pw='" + secret + "'"}}, nil
	}
	m.drv.OnDigests = func(context.Context) ([]model.DigestCounters, error) {
		return nil, errors.New("fallo: SELECT " + secret + " FROM digest")
	}
	run, err := m.svc.Start(ctx, m.sid, model.MonitorConfig{IntervalSec: 5})
	require.NoError(t, err)
	m.next(t, "monitor:sample", nil)
	require.NoError(t, m.svc.WatchProcesses(ctx, m.sid, 1000))
	m.next(t, "monitor:processes", nil)
	require.NoError(t, m.svc.Stop(ctx, run.ID))
	require.NoError(t, m.svc.UnwatchProcesses(m.sid))
	_, _ = m.svc.Start(ctx, m.sid, model.MonitorConfig{IntervalSec: 1}) // un fallo de validación tampoco loguea SQL

	mu.Lock()
	logs := buf.String()
	mu.Unlock()
	require.NotEmpty(t, logs, "el test debe ver al menos el log de arranque")
	require.NotContains(t, logs, secret)
	require.NotContains(t, strings.ToUpper(logs), "SELECT")
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestMonitorNoGoroutineLeaksOnShutdown(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, true)
	before := goleak.IgnoreCurrent() // la base SQLite y el gestor de sesiones ya existen: solo vigilamos lo del monitoreo
	_, err := m.svc.Start(ctx, m.sid, model.MonitorConfig{IntervalSec: 5})
	require.NoError(t, err)
	m.next(t, "monitor:sample", nil)
	require.NoError(t, m.svc.WatchProcesses(ctx, m.sid, 1000))
	m.next(t, "monitor:processes", nil)

	require.NoError(t, m.svc.ServiceShutdown())
	require.Zero(t, m.activeRuns())
	require.Zero(t, m.watchers())
	_, err = m.svc.Start(ctx, m.sid, model.MonitorConfig{IntervalSec: 5})
	require.Equal(t, apperr.SessionClosed, code(t, err), "tras cerrar el servicio no arrancan ejecuciones nuevas")

	m.sessions.CloseAll()
	require.NoError(t, goleak.Find(before))
}

func TestMonitorKillProcess(t *testing.T) {
	ctx := context.Background()
	m := newMonitorEnv(t, true)
	var killed []string
	m.drv.OnProcesses = func(context.Context) ([]model.Process, error) {
		return []model.Process{{ID: 42, User: "app", Host: "10.0.0.5:5000", Command: "Query", TimeSec: 99}}, nil
	}
	m.drv.OnKillProcess = func(id uint64, queryOnly bool) error {
		killed = append(killed, fmt.Sprintf("%d/%v", id, queryOnly))
		return nil
	}

	require.NoError(t, m.svc.KillProcess(ctx, m.sid, 42, "app", "10.0.0.5:5000", true))
	require.NoError(t, m.svc.KillProcess(ctx, m.sid, 42, "app", "10.0.0.5:5000", false))
	require.Equal(t, []string{"42/true", "42/false"}, killed)

	// El id se reutilizó por otro usuario o por otra conexión: no se mata nada.
	for _, c := range []struct{ user, host string }{{"otro", "10.0.0.5:5000"}, {"app", "10.0.0.9:1"}} {
		err := m.svc.KillProcess(ctx, m.sid, 42, c.user, c.host, true)
		require.Equal(t, apperr.NotFound, code(t, err))
	}
	require.Equal(t, apperr.NotFound, code(t, m.svc.KillProcess(ctx, m.sid, 7, "app", "10.0.0.5:5000", true)))
	require.Len(t, killed, 2)

	require.Equal(t, "processId", detail(t, m.svc.KillProcess(ctx, m.sid, 0, "app", "h", true)))
	require.Equal(t, "user", detail(t, m.svc.KillProcess(ctx, m.sid, 42, "", "h", true)))
	require.Equal(t, "host", detail(t, m.svc.KillProcess(ctx, m.sid, 42, "app", "", true)))
	require.Equal(t, apperr.SessionClosed, code(t, m.svc.KillProcess(ctx, "no-existe", 42, "app", "h", true)))

	// Un error del servidor (p. ej. sin privilegio) llega tipado.
	m.drv.OnKillProcess = func(uint64, bool) error {
		return apperr.New(apperr.QueryFailed, "sin privilegio").WithDetail("kill_denied")
	}
	require.Equal(t, "kill_denied", detail(t, m.svc.KillProcess(ctx, m.sid, 42, "app", "10.0.0.5:5000", false)))
}

func TestMonitorKillProcessRejectedInReadOnlyProfile(t *testing.T) {
	m := newMonitorEnv(t, true, func(p *model.ConnectionProfile) { p.ReadOnly = true })
	m.drv.OnProcesses = func(context.Context) ([]model.Process, error) {
		return []model.Process{{ID: 42, User: "app", Host: "h"}}, nil
	}
	called := false
	m.drv.OnKillProcess = func(uint64, bool) error { called = true; return nil }
	err := m.svc.KillProcess(context.Background(), m.sid, 42, "app", "h", true)
	require.Equal(t, apperr.QueryFailed, code(t, err))
	require.Equal(t, "readonly", detail(t, err))
	require.False(t, called, "en solo lectura el KILL ni se envía")
}
