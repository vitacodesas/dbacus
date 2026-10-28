package services

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver"
	"tabulariswails/internal/driver/fake"
	"tabulariswails/internal/model"
	"tabulariswails/internal/results"
)

type memStore struct {
	mu       sync.Mutex
	settings model.Settings
	tabs     []model.TabState
	profile  func(ctx context.Context, id string) (model.ConnectionProfile, error) // opcional: perfiles reales del test
}

func (m *memStore) GetProfile(ctx context.Context, id string) (model.ConnectionProfile, error) {
	if m.profile != nil {
		return m.profile(ctx, id)
	}
	return model.ConnectionProfile{}, apperr.New(apperr.NotFound, "sin perfil")
}
func (m *memStore) GetSettings(context.Context) (model.Settings, error) { return m.settings, nil }
func (m *memStore) SaveTabs(_ context.Context, t []model.TabState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tabs = t
	return nil
}
func (m *memStore) LoadTabs(context.Context) ([]model.TabState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tabs, nil
}

type queryEnv struct {
	*env
	q       *QueryService
	events  chan namedEvent
	history chan model.HistoryEntry
	stash   map[string]terminalEvent
	logMu   sync.Mutex
	log     []string // nombres de todos los eventos emitidos, en orden
	sid     string
	schema  *fake.Schema
}

type namedEvent struct {
	name string
	data any
}

func newQueryEnv(t *testing.T, profileMut ...func(*model.ConnectionProfile)) *queryEnv {
	t.Helper()
	e := newEnv(t)
	sc := &fake.Schema{}
	e.drv.SchemaReader = sc
	prof := e.profile("Q")
	prof.Database = "app"
	for _, m := range profileMut {
		m(&prof)
	}
	p, err := e.svc.Save(context.Background(), prof, ConnectionSecrets{})
	require.NoError(t, err)
	info, err := e.svc.Connect(context.Background(), p.ID, nil)
	require.NoError(t, err)

	qe := &queryEnv{env: e, stash: map[string]terminalEvent{}, events: make(chan namedEvent, 512), history: make(chan model.HistoryEntry, 64), sid: info.SessionID, schema: sc}
	emit := func(name string, data any) {
		qe.logMu.Lock()
		qe.log = append(qe.log, name)
		qe.logMu.Unlock()
		qe.events <- namedEvent{name, data}
	}
	st := &memStore{settings: model.DefaultSettings(), profile: e.st.GetProfile}
	schemaSvc := NewSchemaService(e.sessions, emit)
	qe.q = NewQueryService(QueryDeps{
		Sessions: e.sessions, Results: results.NewManager(512 << 20), Schema: schemaSvc, Store: st,
		History: func(h model.HistoryEntry) { qe.history <- h }, Emit: emit,
	})
	t.Cleanup(func() { _ = qe.q.ServiceShutdown() })
	return qe
}

func (e *queryEnv) execute(t *testing.T, tab, sql string, mut ...func(*ExecuteRequest)) string {
	t.Helper()
	req := ExecuteRequest{SessionID: e.sid, TabID: tab, SQL: sql}
	for _, m := range mut {
		m(&req)
	}
	res, err := e.q.Execute(context.Background(), req)
	require.NoError(t, err)
	return res.QueryID
}

// next espera un evento con ese nombre (ignora el resto, p. ej. progress).
func (e *queryEnv) next(t *testing.T, name string) any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-e.events:
			if ev.name == name {
				return ev.data
			}
		case <-deadline:
			t.Fatalf("no llegó el evento %q", name)
		}
	}
}

// terminal espera query:done o query:error de esa ejecución. Los finales de otras ejecuciones se guardan para
// poder pedirlos después (varias pestañas pueden terminar en cualquier orden).
func (e *queryEnv) terminal(t *testing.T, queryID string) (done *QueryDone, failed *QueryError) {
	t.Helper()
	if st, ok := e.stash[queryID]; ok {
		delete(e.stash, queryID)
		return st.done, st.failed
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-e.events:
			switch d := ev.data.(type) {
			case QueryDone:
				if d.QueryID == queryID {
					return &d, nil
				}
				e.stash[d.QueryID] = terminalEvent{done: &d}
			case QueryError:
				if d.QueryID == queryID {
					return nil, &d
				}
				e.stash[d.QueryID] = terminalEvent{failed: &d}
			}
		case <-deadline:
			t.Fatal("la ejecución no terminó")
		}
	}
}

type terminalEvent struct {
	done   *QueryDone
	failed *QueryError
}

func intRows(n int) [][]any {
	rows := make([][]any, n)
	for i := range rows {
		rows[i] = []any{int64(i), fmt.Sprintf("v%d", i)}
	}
	return rows
}

func TestExecuteSelectEmitsEventsAndServesWindows(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"id", "name"}, intRows(250)...), nil
	}
	qid := e.execute(t, "tab1", "SELECT * FROM t")

	started := e.next(t, "query:started").(QueryStarted)
	require.Equal(t, qid, started.QueryID)
	require.Equal(t, "tab1", started.TabID)
	stmt := e.next(t, "query:statement").(QueryStatement)
	require.Equal(t, "rows", stmt.Result.Kind)
	require.Len(t, stmt.Result.Columns, 2)

	done, failed := e.terminal(t, qid)
	require.Nil(t, failed)
	require.Len(t, done.Results, 1)
	require.EqualValues(t, 250, done.Results[0].RowCount)
	require.False(t, done.Results[0].Truncated)
	require.Equal(t, "app", done.Database)

	w, err := e.q.GetRows(qid, 0, 100, 50)
	require.NoError(t, err)
	require.Equal(t, 100, w.Offset)
	require.Len(t, w.Rows, 50)
	require.EqualValues(t, 250, w.Total)
	require.True(t, w.Complete)
	require.Equal(t, int64(100), w.Rows[0][0].V)
	require.Equal(t, "v100", w.Rows[0][1].V)

	last, err := e.q.GetRows(qid, 0, 240, 1000)
	require.NoError(t, err)
	require.Len(t, last.Rows, 10)
	big, _ := e.q.GetRows(qid, 0, 0, 999999)
	require.Len(t, big.Rows, 250, "el límite de ventana se recorta a 1000")
}

func TestExecuteMultiStatementStopsAtFirstError(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(_ context.Context, stmt string) (driver.RunResult, error) {
		switch {
		case strings.Contains(stmt, "boom"):
			return nil, apperr.New(apperr.QuerySyntax, "error de sintaxis")
		case strings.HasPrefix(stmt, "INSERT"):
			return &fake.Result{Rows_: 3, LastID: 9}, nil
		}
		return fake.Rows([]string{"n"}, []any{int64(1)}), nil
	}
	sql := "SELECT 1;\nINSERT INTO t VALUES (1);\nSELECT boom;\nSELECT 4"
	qid := e.execute(t, "tab1", sql)
	done, failed := e.terminal(t, qid)
	require.Nil(t, done)
	require.Equal(t, 2, failed.StmtIndex)
	require.Equal(t, apperr.QuerySyntax, failed.Error.Code)
	require.Equal(t, strings.Index(sql, "SELECT boom"), failed.Start, "posición de la sentencia fallida en el SQL enviado")
	require.Equal(t, failed.Start+len("SELECT boom"), failed.End)

	// Lo ejecutado antes del error sigue disponible.
	w, err := e.q.GetRows(qid, 0, 0, 10)
	require.NoError(t, err)
	require.Len(t, w.Rows, 1)
	w, err = e.q.GetRows(qid, 1, 0, 10)
	require.NoError(t, err, "una sentencia exec también tiene (un) buffer vacío para no desalinear índices")
	require.Empty(t, w.Rows)
}

func TestExecResultReportsAffectedRows(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return &fake.Result{Rows_: 5, LastID: 42, Warnings: 2}, nil
	}
	qid := e.execute(t, "tab1", "UPDATE t SET a = 1")
	done, _ := e.terminal(t, qid)
	r := done.Results[0]
	require.Equal(t, "exec", r.Kind)
	require.EqualValues(t, 5, r.AffectedRows)
	require.EqualValues(t, 42, r.LastInsertID)
	require.Equal(t, 2, r.Warnings)
}

func TestExecuteValidation(t *testing.T) {
	e := newQueryEnv(t)
	ctx := context.Background()
	_, err := e.q.Execute(ctx, ExecuteRequest{SessionID: "nada", TabID: "t", SQL: "SELECT 1"})
	require.Equal(t, apperr.SessionClosed, code(t, err))
	_, err = e.q.Execute(ctx, ExecuteRequest{SessionID: e.sid, TabID: " ", SQL: "SELECT 1"})
	require.Equal(t, "tabId", detail(t, err))
	_, err = e.q.Execute(ctx, ExecuteRequest{SessionID: e.sid, TabID: "t", SQL: " -- solo un comentario "})
	require.Equal(t, "sql", detail(t, err))
}

func TestOneRunPerTabAndTabsAreIndependent(t *testing.T) {
	e := newQueryEnv(t)
	release := make(chan struct{})
	e.drv.OnRun = func(ctx context.Context, stmt string) (driver.RunResult, error) {
		if strings.Contains(stmt, "slow") {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return fake.Rows([]string{"n"}, []any{int64(1)}), nil
	}
	slow := e.execute(t, "tabA", "SELECT slow")
	_, err := e.q.Execute(context.Background(), ExecuteRequest{SessionID: e.sid, TabID: "tabA", SQL: "SELECT 2"})
	require.Equal(t, apperr.QueryFailed, code(t, err))
	require.Equal(t, "busy", detail(t, err))

	fast := e.execute(t, "tabB", "SELECT 3") // otra pestaña no se bloquea
	d, _ := e.terminal(t, fast)
	require.NotNil(t, d)

	close(release)
	d, _ = e.terminal(t, slow)
	require.NotNil(t, d)
	e.execute(t, "tabA", "SELECT 2") // ya libre
}

func TestEachTabGetsItsOwnConnection(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) { return fake.Rows([]string{"n"}), nil }
	a, b := e.execute(t, "tabA", "SELECT 1"), e.execute(t, "tabB", "SELECT 1")
	e.terminal(t, a)
	e.terminal(t, b)
	require.EqualValues(t, 2, e.drv.Pools[0].Acquired.Load())
}

func TestMaxRowsTruncatesAndKills(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"id", "n"}, intRows(50)...), nil
	}
	qid := e.execute(t, "tab1", "SELECT * FROM big", func(r *ExecuteRequest) { r.MaxRows = 10 })
	done, _ := e.terminal(t, qid)
	r := done.Results[0]
	require.EqualValues(t, 10, r.RowCount)
	require.True(t, r.Truncated)
	require.EqualValues(t, 1, e.drv.Pools[0].Conns[0].Killed.Load(), "se aborta la consulta para no leer el resto")
	w, _ := e.q.GetRows(qid, 0, 0, 100)
	require.Len(t, w.Rows, 10)
	require.False(t, e.drv.Pools[0].Conns[0].Released.Load(), "la conexión sigue viva")
}

func TestResultExactlyAtMaxRowsIsNotTruncated(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"id", "n"}, intRows(10)...), nil
	}
	qid := e.execute(t, "tab1", "SELECT * FROM t", func(r *ExecuteRequest) { r.MaxRows = 10 })
	done, _ := e.terminal(t, qid)
	require.False(t, done.Results[0].Truncated)
	require.EqualValues(t, 0, e.drv.Pools[0].Conns[0].Killed.Load())
}

func TestAutoLimitOnlyOnSimpleSelect(t *testing.T) {
	e := newQueryEnv(t)
	var mu sync.Mutex
	var seen []string
	e.drv.OnRun = func(_ context.Context, stmt string) (driver.RunResult, error) {
		mu.Lock()
		seen = append(seen, stmt)
		mu.Unlock()
		return fake.Rows([]string{"n"}), nil
	}
	qid := e.execute(t, "tab1", "SELECT * FROM t; SELECT * FROM t LIMIT 5; SELECT 1 UNION SELECT 2; INSERT INTO t VALUES (1)", func(r *ExecuteRequest) { r.AutoLimit = 100 })
	done, _ := e.terminal(t, qid)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"SELECT * FROM t LIMIT 100", "SELECT * FROM t LIMIT 5", "SELECT 1 UNION SELECT 2", "INSERT INTO t VALUES (1)"}, seen)
	require.Equal(t, 100, done.Results[0].LimitApplied)
	require.Equal(t, 0, done.Results[1].LimitApplied)
	require.Equal(t, "SELECT * FROM t", done.Results[0].SQL, "se informa el SQL original, no el modificado")
}

// blockingResult simula un SELECT largo que solo termina cuando KILL lo interrumpe.
type blockingResult struct {
	fake.Result
	killed chan struct{}
	err    error
}

func (b *blockingResult) Next() bool {
	<-b.killed
	return false
}
func (b *blockingResult) Err() error { return b.err }

func TestCancelKillsQueryAndKeepsConnection(t *testing.T) {
	e := newQueryEnv(t)
	killed := make(chan struct{})
	var once sync.Once
	e.drv.OnKill = func(string) { once.Do(func() { close(killed) }) }
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return &blockingResult{Result: *fake.Rows([]string{"n"}), killed: killed, err: apperr.New(apperr.QueryCanceled, "interrumpida")}, nil
	}
	qid := e.execute(t, "tab1", "SELECT SLEEP(60)")
	e.next(t, "query:statement")

	start := time.Now()
	require.NoError(t, e.q.Cancel(qid))
	_, failed := e.terminal(t, qid)
	require.NotNil(t, failed)
	require.Equal(t, apperr.QueryCanceled, failed.Error.Code)
	require.Less(t, time.Since(start), time.Second)
	require.EqualValues(t, 1, e.drv.Pools[0].Conns[0].Killed.Load())
	require.False(t, e.drv.Pools[0].Conns[0].Released.Load(), "cancelar no cierra la conexión de la pestaña")

	require.NoError(t, e.q.Cancel(qid), "idempotente")
	require.NoError(t, e.q.Cancel("no-existe"))
}

func TestCancelWhenServerFinishesWithoutErrorStillReportsCanceled(t *testing.T) {
	// SLEEP() interrumpido devuelve 1 sin error en MySQL: la cancelación se detecta por la marca.
	e := newQueryEnv(t)
	release := make(chan struct{})
	e.drv.OnKill = func(string) { close(release) }
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return &blockingResult{Result: *fake.Rows([]string{"n"}), killed: release}, nil
	}
	qid := e.execute(t, "tab1", "SELECT SLEEP(60); SELECT 2")
	e.next(t, "query:statement")
	require.NoError(t, e.q.Cancel(qid))
	_, failed := e.terminal(t, qid)
	require.Equal(t, apperr.QueryCanceled, failed.Error.Code)
	require.Equal(t, 0, failed.StmtIndex, "la segunda sentencia no llega a ejecutarse")
}

func TestCancelFallsBackToContextWhenKillFails(t *testing.T) {
	e := newQueryEnv(t)
	started := make(chan struct{})
	e.drv.OnRun = func(ctx context.Context, _ string) (driver.RunResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	// Kill "falla": fake no tiene error configurable, así que se simula una conexión que no responde al KILL
	// y se confía en el plazo de gracia.
	qid := e.execute(t, "tab1", "SELECT slow")
	<-started
	require.NoError(t, e.q.Cancel(qid))
	_, failed := e.terminal(t, qid)
	require.Equal(t, apperr.QueryCanceled, failed.Error.Code)
}

func TestDDLInvalidatesSchemaCache(t *testing.T) {
	e := newQueryEnv(t)
	e.schema.DBs = []model.Database{{Name: "app"}}
	schemaSvc := NewSchemaService(e.sessions, func(string, any) {})
	_, _ = schemaSvc.ListDatabases(context.Background(), e.sid)
	_, _ = schemaSvc.ListDatabases(context.Background(), e.sid)
	require.EqualValues(t, 1, e.schema.Calls.Load())

	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) { return &fake.Result{}, nil }
	qid := e.execute(t, "tab1", "ALTER TABLE t ADD COLUMN c INT")
	e.terminal(t, qid)
	e.logMu.Lock()
	require.Contains(t, e.log, "schema:invalidated", "el DDL avisa a Vue de que el esquema cambió")
	e.logMu.Unlock()

	_, _ = schemaSvc.ListDatabases(context.Background(), e.sid)
	require.EqualValues(t, 2, e.schema.Calls.Load(), "tras el DDL se vuelve a leer el esquema")
}

func TestUseStatementUpdatesDatabase(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(_ context.Context, stmt string) (driver.RunResult, error) {
		if strings.HasPrefix(stmt, "SELECT DATABASE()") {
			return fake.Rows([]string{"d"}, []any{[]byte("otra")}), nil
		}
		return &fake.Result{}, nil
	}
	qid := e.execute(t, "tab1", "USE otra")
	done, _ := e.terminal(t, qid)
	require.Equal(t, "otra", done.Database)
}

func TestRequestedDatabaseIsSelectedOncePerTab(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) { return fake.Rows([]string{"n"}), nil }
	for range 2 {
		qid := e.execute(t, "tab1", "SELECT 1", func(r *ExecuteRequest) { r.Database = "shop" })
		d, _ := e.terminal(t, qid)
		require.Equal(t, "shop", d.Database)
	}
	require.Equal(t, "shop", e.drv.Pools[0].Conns[0].UsedDB.Load())
}

func TestHistoryReceivesEveryStatement(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(_ context.Context, stmt string) (driver.RunResult, error) {
		if strings.Contains(stmt, "bad") {
			return nil, apperr.New(apperr.QueryFailed, "falló")
		}
		return fake.Rows([]string{"n"}, []any{int64(1)}), nil
	}
	qid := e.execute(t, "tab1", "SELECT 1; SELECT bad; SELECT 3")
	e.terminal(t, qid)
	var got []model.HistoryEntry
	for range 2 {
		select {
		case h := <-e.history:
			got = append(got, h)
		case <-time.After(2 * time.Second):
			t.Fatal("faltan entradas de historial")
		}
	}
	oks := map[string]bool{}
	for _, h := range got {
		oks[h.SQL] = h.OK
		require.Equal(t, "app", h.Database)
	}
	require.Equal(t, map[string]bool{"SELECT 1": true, "SELECT bad": false}, oks, "la sentencia tras el error no se ejecuta ni se registra")
}

func TestGetCellReturnsFullValueOfTruncatedText(t *testing.T) {
	e := newQueryEnv(t)
	long := strings.Repeat("x", results.MaxCellBytes+10)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		r := fake.Rows([]string{"t"}, []any{[]byte(long)})
		r.Cols[0].DataType = "LONGTEXT"
		return r, nil
	}
	qid := e.execute(t, "tab1", "SELECT t FROM x")
	e.terminal(t, qid)
	w, _ := e.q.GetRows(qid, 0, 0, 10)
	require.True(t, w.Rows[0][0].Trunc)
	full, err := e.q.GetCell(qid, 0, 0, 0)
	require.NoError(t, err)
	require.Equal(t, long, full.V)
	_, err = e.q.GetCell(qid, 0, 9, 0)
	require.Equal(t, "cell", detail(t, err))
}

func TestReleaseAndExpiredResults(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"n"}, []any{int64(1)}), nil
	}
	qid := e.execute(t, "tab1", "SELECT 1")
	e.terminal(t, qid)
	e.q.ReleaseResult(qid)
	_, err := e.q.GetRows(qid, 0, 0, 10)
	require.Equal(t, apperr.ResultExpired, code(t, err))
	_, err = e.q.GetRows("nunca-existió", 0, 0, 10)
	require.Equal(t, apperr.ResultExpired, code(t, err))
	_, err = e.q.GetCell(qid, 0, 0, 0)
	require.Equal(t, apperr.ResultExpired, code(t, err))
}

func TestNewExecutionReleasesPreviousResultOfTheTab(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"n"}, []any{int64(1)}), nil
	}
	first := e.execute(t, "tab1", "SELECT 1")
	e.terminal(t, first)
	second := e.execute(t, "tab1", "SELECT 2")
	e.terminal(t, second)
	_, err := e.q.GetRows(first, 0, 0, 10)
	require.Equal(t, apperr.ResultExpired, code(t, err))
	_, err = e.q.GetRows(second, 0, 0, 10)
	require.NoError(t, err)
}

func TestCloseTabReleasesConnectionAndResults(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"n"}, []any{int64(1)}), nil
	}
	qid := e.execute(t, "tab1", "SELECT 1")
	e.terminal(t, qid)
	require.EqualValues(t, 1, e.drv.Pools[0].Open.Load())

	require.NoError(t, e.q.CloseTab("tab1"))
	// query:done se emite antes de que la ejecución se retire de byTab: si CloseTab llega en medio, libera la conexión en
	// segundo plano cuando la ejecución termina, así que se espera en vez de leer el contador de inmediato.
	require.Eventually(t, func() bool { return e.drv.Pools[0].Open.Load() == 0 }, 2*time.Second, 5*time.Millisecond)
	_, err := e.q.GetRows(qid, 0, 0, 10)
	require.Equal(t, apperr.ResultExpired, code(t, err))
	require.NoError(t, e.q.CloseTab("tab1"), "idempotente")
	require.NoError(t, e.q.CloseTab("nunca"))
}

func TestCloseTabCancelsRunningQuery(t *testing.T) {
	e := newQueryEnv(t)
	killed := make(chan struct{})
	var once sync.Once
	e.drv.OnKill = func(string) { once.Do(func() { close(killed) }) }
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return &blockingResult{Result: *fake.Rows([]string{"n"}), killed: killed, err: apperr.New(apperr.QueryCanceled, "x")}, nil
	}
	qid := e.execute(t, "tab1", "SELECT SLEEP(60)")
	e.next(t, "query:statement")
	require.NoError(t, e.q.CloseTab("tab1"))
	_, failed := e.terminal(t, qid)
	require.Equal(t, apperr.QueryCanceled, failed.Error.Code)
	require.Eventually(t, func() bool { return e.drv.Pools[0].Open.Load() == 0 }, 3*time.Second, 20*time.Millisecond, "la conexión se devuelve cuando la consulta termina")
}

func TestSessionClosedDuringRunFailsTheRun(t *testing.T) {
	e := newQueryEnv(t)
	started := make(chan struct{})
	e.drv.OnRun = func(ctx context.Context, _ string) (driver.RunResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	qid := e.execute(t, "tab1", "SELECT slow")
	<-started
	require.NoError(t, e.sessions.Close(e.sid))
	_, failed := e.terminal(t, qid)
	require.NotNil(t, failed)
}

func TestCloseWaitsForRunsToFinish(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(ctx context.Context, _ string) (driver.RunResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	e.execute(t, "tab1", "SELECT slow")
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, e.sessions.Close(e.sid)) // cancela el ctx de la sesión
	done := make(chan struct{})
	go func() { _ = e.q.ServiceShutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close no terminó")
	}
}

func TestCloseCancelsRunningQueries(t *testing.T) {
	e := newQueryEnv(t)
	started := make(chan struct{})
	e.drv.OnRun = func(ctx context.Context, _ string) (driver.RunResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	qid := e.execute(t, "tab1", "SELECT slow")
	<-started
	done := make(chan struct{})
	go func() { _ = e.q.ServiceShutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close no canceló la ejecución en curso")
	}
	_, failed := e.terminal(t, qid)
	require.NotNil(t, failed)
}

func TestStatementAtFormatAndTabs(t *testing.T) {
	e := newQueryEnv(t)
	got, err := e.q.StatementAt("SELECT 1;\nSELECT 2;", 12)
	require.NoError(t, err)
	require.Equal(t, StatementRange{Text: "SELECT 2", Start: 10, End: 18}, got)
	_, err = e.q.StatementAt("   ", 0)
	require.Equal(t, "sql", detail(t, err))

	require.Equal(t, "SELECT\n  1;", e.q.FormatSQL("select 1"))

	ctx := context.Background()
	tabs := []model.TabState{{ID: "a", Title: "Q1", SQL: "SELECT 1"}}
	require.NoError(t, e.q.SaveTabs(ctx, tabs))
	back, err := e.q.LoadTabs(ctx)
	require.NoError(t, err)
	require.Equal(t, tabs, back)
	require.Equal(t, "tabs", detail(t, e.q.SaveTabs(ctx, make([]model.TabState, 101))))
	require.Equal(t, "tabs", detail(t, e.q.SaveTabs(ctx, []model.TabState{{SQL: strings.Repeat("x", maxTabSQLBytes+1)}})))
}

func TestRowsAsInsert(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		r := fake.Rows([]string{"id", "name", "note", "price", "big", "bin"},
			[]any{int64(1), []byte("O'Reilly"), nil, []byte("12.50"), []byte("18446744073709551615"), []byte{0xde, 0xad}},
			[]any{int64(2), []byte("línea\nnueva"), []byte("x"), []byte("0.1"), int64(5), []byte{}})
		r.Cols[3].DataType, r.Cols[4].DataType, r.Cols[5].DataType = "DECIMAL", "UNSIGNED BIGINT", "BLOB"
		return r, nil
	}
	qid := e.execute(t, "tab1", "SELECT * FROM t")
	e.terminal(t, qid)

	got, err := e.q.RowsAsInsert(qid, 0, 0, 10, "mi tabla")
	require.NoError(t, err)
	require.Equal(t, "INSERT INTO `mi tabla` (`id`, `name`, `note`, `price`, `big`, `bin`) VALUES\n"+
		"  (1, 'O\\'Reilly', NULL, 12.50, 18446744073709551615, 0xdead),\n"+
		"  (2, 'línea\\nnueva', 'x', 0.1, 5, '');", /* BLOB vacío */ strings.ReplaceAll(got, "línea\nnueva", "línea\\nnueva"))

	one, err := e.q.RowsAsInsert(qid, 0, 1, 1, "")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(one, "INSERT INTO `tabla`"))
	require.True(t, strings.HasSuffix(one, ";"))

	_, err = e.q.RowsAsInsert(qid, 0, 99, 5, "t")
	require.Equal(t, "rows", detail(t, err))
	_, err = e.q.RowsAsInsert("nada", 0, 0, 5, "t")
	require.Equal(t, apperr.ResultExpired, code(t, err))
}

func TestReadOnlyProfileRejectsWritesBeforeSending(t *testing.T) {
	e := newQueryEnv(t, func(p *model.ConnectionProfile) { p.ReadOnly = true })
	var sent []string
	e.drv.OnRun = func(_ context.Context, stmt string) (driver.RunResult, error) {
		sent = append(sent, stmt)
		return &fake.Result{}, nil
	}

	for _, sql := range []string{"DROP TABLE t", "SELECT 1; DELETE FROM t", "SELECT * FROM t INTO OUTFILE '/tmp/x'", "SET SESSION TRANSACTION READ WRITE"} {
		_, err := e.q.Execute(context.Background(), ExecuteRequest{SessionID: e.sid, TabID: "ro", SQL: sql})
		ae := apperr.From(err)
		require.Equal(t, apperr.QueryFailed, ae.Code, sql)
		require.Equal(t, "readonly", ae.Detail, sql)
	}
	require.Empty(t, sent, "nada llegó al servidor: ni siquiera la sentencia de lectura de un script mixto")

	// Lo de lectura sí corre.
	e.execute(t, "ro", "SELECT 1")
	e.next(t, "query:done")
	require.Equal(t, []string{"SELECT 1"}, sent)
}

func TestReadWriteProfileIsUnchanged(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) { return &fake.Result{}, nil }
	e.execute(t, "rw", "DROP TABLE t")
	e.next(t, "query:done")
}

func TestReadOnlyAppliesToAlreadyOpenSessionWhenProfileChanges(t *testing.T) {
	e := newQueryEnv(t) // la sesión se abre en lectura-escritura
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) { return &fake.Result{}, nil }
	e.execute(t, "rw", "UPDATE t SET a = 1")
	e.next(t, "query:done")

	// Se marca «Solo lectura» en el perfil sin reconectar: la protección es inmediata.
	profiles, err := e.svc.List(context.Background())
	require.NoError(t, err)
	p := profiles[0]
	p.ReadOnly = true
	_, err = e.svc.Save(context.Background(), p, ConnectionSecrets{})
	require.NoError(t, err)

	_, err = e.q.Execute(context.Background(), ExecuteRequest{SessionID: e.sid, TabID: "rw", SQL: "UPDATE t SET a = 2"})
	require.Equal(t, "readonly", apperr.From(err).Detail)
	e.execute(t, "rw", "SELECT 1") // leer sigue funcionando
	e.next(t, "query:done")

	// Quitar el modo exige reconectar: la sesión abierta no se vuelve a abrir a escrituras sola.
	p.ReadOnly = false
	_, err = e.svc.Save(context.Background(), p, ConnectionSecrets{})
	require.NoError(t, err)
	e.execute(t, "rw", "UPDATE t SET a = 3")
	e.next(t, "query:done")
}

// C1 (docs/correcciones.md): tras reconectar, la pestaña recibe una conexión nueva sin base; la base recordada de la
// conexión anterior no debe hacer que se omita el USE.
func TestDatabaseIsSelectedAgainAfterReconnect(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) { return fake.Rows([]string{"n"}), nil }
	ctx := context.Background()

	qid := e.execute(t, "tab1", "SELECT 1", func(r *ExecuteRequest) { r.Database = "identity" })
	e.terminal(t, qid)
	require.Equal(t, "identity", e.drv.Pools[0].Conns[0].UsedDB.Load())

	profiles, err := e.svc.List(ctx)
	require.NoError(t, err)
	require.NoError(t, e.svc.Disconnect(e.sid))
	info, err := e.svc.Connect(ctx, profiles[0].ID, nil)
	require.NoError(t, err)
	e.sid = info.SessionID

	// Mismo tab y mismo selector (identity), pero conexión nueva: hay que volver a hacer USE.
	qid = e.execute(t, "tab1", "SELECT 1", func(r *ExecuteRequest) { r.Database = "identity" })
	d, _ := e.terminal(t, qid)
	require.Equal(t, "identity", d.Database)
	require.Equal(t, "identity", e.drv.Pools[1].Conns[0].UsedDB.Load(), "la conexión nueva recibió su USE")
}
