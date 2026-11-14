package services

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver/fake"
	"github.com/vitacodesas/dbacus/internal/model"
)

type migEnv struct {
	*queryEnv
	svc    *MigrationService
	eng    *fake.MigEngine
	mu     sync.Mutex
	events []namedEvent
	hist   []model.HistoryEntry
}

func newMigEnv(t *testing.T, profileMut ...func(*model.ConnectionProfile)) *migEnv {
	t.Helper()
	qe := newQueryEnv(t, profileMut...)
	e := &migEnv{queryEnv: qe, eng: fake.NewMigEngine()}
	qe.drv.Mig = e.eng
	schemaSvc := NewSchemaService(qe.sessions, func(string, any) {})
	e.svc = NewMigrationService(MigrationDeps{
		Sessions: qe.sessions, Schema: schemaSvc, Store: qe.st,
		Emit: func(name string, data any) {
			e.mu.Lock()
			e.events = append(e.events, namedEvent{name, data})
			e.mu.Unlock()
		},
		History: func(h model.HistoryEntry) { e.mu.Lock(); e.hist = append(e.hist, h); e.mu.Unlock() },
	})
	t.Cleanup(func() { _ = e.svc.ServiceShutdown() })
	pk := model.ChunkKeySpec{Kind: model.ChunkPK, Columns: []string{"id"}}
	tb := func(n string, fks ...model.ForeignKey) model.MigrationTable {
		return model.MigrationTable{Name: n, Columns: []string{"id", "v"}, CreateSQL: "CREATE TABLE `" + n + "`", HasPrimaryKey: true, Key: pk, ForeignKeys: fks, AvgRowLen: 16}
	}
	e.eng.AddTable("app", tb("customers"), 20)
	e.eng.AddTable("app", tb("orders", model.ForeignKey{Name: "fk_c", Columns: []string{"cid"}, RefSchema: "app", RefTable: "customers", RefColumns: []string{"id"}}), 50)
	e.eng.DBs["app"].Views = []model.MigrationCode{{Name: "v1", Kind: "view", CreateSQL: "CREATE VIEW `v1` AS select * from `app`.`orders`"}}
	return e
}

func (e *migEnv) req(opts model.MigrationOptions) model.MigrationRequest {
	return model.MigrationRequest{Source: model.MigrationEndpoint{SessionID: e.sid, Database: "app"}, Target: model.MigrationEndpoint{SessionID: e.sid, Database: "copia"}, Options: opts}
}

func (e *migEnv) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.events {
		out = append(out, ev.name)
	}
	return out
}

func (e *migEnv) waitDone(t *testing.T, id string) model.MigrationResult {
	t.Helper()
	require.Eventually(t, func() bool {
		r, err := e.svc.Job(id)
		return err == nil && r.Job.State != model.MigRunning && r.Job.State != model.MigPlanning && r.Job.State != model.MigCancelling
	}, 10*time.Second, 10*time.Millisecond)
	r, _ := e.svc.Job(id)
	return r
}

func appErr(t *testing.T, err error) *apperr.AppError {
	t.Helper()
	require.Error(t, err)
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	return ae
}

func TestMigrationPlanStartAndEvents(t *testing.T) {
	e := newMigEnv(t)
	ctx := context.Background()
	plan, err := e.svc.Plan(ctx, e.req(model.MigrationOptions{Workers: 2}))
	require.NoError(t, err)
	require.Equal(t, model.RouteServer, plan.Route, "misma sesión: dentro del servidor")
	require.NotEmpty(t, plan.PlanHash)
	require.Len(t, plan.Tables, 2)
	require.Empty(t, e.eng.Log(), "Plan no crea nada")

	job, err := e.svc.Start(ctx, e.req(model.MigrationOptions{Workers: 2}), plan.PlanHash)
	require.NoError(t, err)
	r := e.waitDone(t, job.ID)
	require.Equal(t, model.MigDone, r.Job.State, "%+v", r.Failed)
	require.Equal(t, 20, e.eng.RowCount("copia", "customers"))
	require.Equal(t, 50, e.eng.RowCount("copia", "orders"))

	names := e.names()
	require.Contains(t, names, "migrate:state")
	require.Contains(t, names, "migrate:progress")
	require.Equal(t, "migrate:done", names[len(names)-1], "migrate:done es lo último")
	e.mu.Lock()
	var done MigrateDoneEvent
	for _, ev := range e.events {
		if d, ok := ev.data.(MigrateDoneEvent); ok {
			done = d
		}
	}
	e.mu.Unlock()
	require.Equal(t, job.ID, done.JobID)
	require.Equal(t, model.MigDone, done.State)

	require.Len(t, e.hist, 1, "una entrada resumen, no una por sentencia")
	require.True(t, strings.HasPrefix(e.hist[0].SQL, "-- Migración de schema `app` → `copia`"))
	require.True(t, e.hist[0].OK)
	require.EqualValues(t, 70, e.hist[0].RowCount)
	require.NotContains(t, e.hist[0].SQL, "CREATE", "ni DDL ni datos en el historial")
}

func TestMigrationStartRejectsStalePlan(t *testing.T) {
	e := newMigEnv(t)
	plan, err := e.svc.Plan(context.Background(), e.req(model.MigrationOptions{}))
	require.NoError(t, err)
	e.eng.DBs["app"].Tables["customers"].MigrationTable.CreateSQL = "CREATE TABLE `customers` (id int)" // el origen cambió
	_, err = e.svc.Start(context.Background(), e.req(model.MigrationOptions{}), plan.PlanHash)
	ae := appErr(t, err)
	require.Equal(t, apperr.Validation, ae.Code)
	require.Equal(t, "stale_plan", ae.Detail)
	_, err = e.svc.Start(context.Background(), e.req(model.MigrationOptions{}), "")
	require.Equal(t, "stale_plan", appErr(t, err).Detail, "sin hash no se ejecuta")
	require.Empty(t, e.eng.Log(), "no se creó nada")
}

func TestMigrateReadOnlyTargetRejected(t *testing.T) {
	e := newMigEnv(t, func(p *model.ConnectionProfile) { p.ReadOnly = true })
	_, err := e.svc.Plan(context.Background(), e.req(model.MigrationOptions{}))
	ae := appErr(t, err)
	require.Equal(t, apperr.QueryFailed, ae.Code)
	require.Equal(t, "readonly", ae.Detail)
	_, err = e.svc.Start(context.Background(), e.req(model.MigrationOptions{}), "x")
	require.Equal(t, "readonly", appErr(t, err).Detail)
	require.Empty(t, e.eng.Log(), "se rechaza antes de crear nada")
}

func TestMigrationValidation(t *testing.T) {
	e := newMigEnv(t)
	ctx := context.Background()
	r := e.req(model.MigrationOptions{})
	r.Source.SessionID = ""
	require.Equal(t, "source", appErr(t, func() error { _, err := e.svc.Plan(ctx, r); return err }()).Detail)
	r = e.req(model.MigrationOptions{})
	r.Target.SessionID = ""
	require.Equal(t, "target", appErr(t, func() error { _, err := e.svc.Plan(ctx, r); return err }()).Detail)
	r = e.req(model.MigrationOptions{})
	r.Target.SessionID = "no-existe"
	require.Equal(t, apperr.SessionClosed, appErr(t, func() error { _, err := e.svc.Plan(ctx, r); return err }()).Code)
	r = e.req(model.MigrationOptions{})
	r.Target.Database = "mysql"
	require.Equal(t, "system_schema", appErr(t, func() error { _, err := e.svc.Plan(ctx, r); return err }()).Detail)
	r = e.req(model.MigrationOptions{})
	r.Target.Database = "app"
	require.Equal(t, "same_schema", appErr(t, func() error { _, err := e.svc.Plan(ctx, r); return err }()).Detail)
	_, err := e.svc.Job("nope")
	require.Equal(t, apperr.NotFound, appErr(t, err).Code)
	require.Equal(t, apperr.NotFound, appErr(t, e.svc.Cancel("nope")).Code)
}

func TestMigrationBusyWhileRunningAndCancel(t *testing.T) {
	e := newMigEnv(t)
	e.eng.AddTable("app", func() model.MigrationTable {
		return model.MigrationTable{Name: "big", Columns: []string{"id", "v"}, CreateSQL: "CREATE TABLE `big`", HasPrimaryKey: true, Key: model.ChunkKeySpec{Kind: model.ChunkPK, Columns: []string{"id"}}, AvgRowLen: 16}
	}(), 30000)
	e.eng.Latency = 40 * time.Millisecond
	ctx := context.Background()
	req := e.req(model.MigrationOptions{Workers: 1})
	plan, err := e.svc.Plan(ctx, req)
	require.NoError(t, err)
	job, err := e.svc.Start(ctx, req, plan.PlanHash)
	require.NoError(t, err)

	req2 := req
	req2.Target.Database = "otra"
	plan2, err := e.svc.Plan(ctx, req2)
	require.NoError(t, err)
	_, err = e.svc.Start(ctx, req2, plan2.PlanHash)
	require.Equal(t, "busy", appErr(t, err).Detail, "una migración por sesión")

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, e.svc.Cancel(job.ID))
	r := e.waitDone(t, job.ID)
	e.eng.Latency = 0
	require.Equal(t, model.MigCanceled, r.Job.State)
	// Terminada la anterior, la sesión queda libre.
	j2, err := e.svc.Start(ctx, req2, plan2.PlanHash) // Latency ya es 0: no se toca con el job en marcha (el motor falso la lee sin cerrojo)
	require.NoError(t, err)
	require.NoError(t, e.svc.Cancel(j2.ID))
	e.waitDone(t, j2.ID)
}

func TestMigrationClosingSessionCancelsJob(t *testing.T) {
	e := newMigEnv(t)
	e.eng.Latency = 40 * time.Millisecond
	ctx := context.Background()
	plan, err := e.svc.Plan(ctx, e.req(model.MigrationOptions{Workers: 1}))
	require.NoError(t, err)
	job, err := e.svc.Start(ctx, e.req(model.MigrationOptions{Workers: 1}), plan.PlanHash)
	require.NoError(t, err)
	time.Sleep(60 * time.Millisecond)
	require.NoError(t, e.sessions.Close(e.sid))
	r := e.waitDone(t, job.ID)
	e.eng.Latency = 0
	require.NotEqual(t, model.MigDone, r.Job.State)
	require.NotEmpty(t, r.Job.Error, "se dice por qué se interrumpió")
}

func TestMigrationRetryVerifyAndCleanup(t *testing.T) {
	e := newMigEnv(t)
	var failing = true
	e.eng.OnCommit = func(table string, seq, attempt int) error {
		if table == "orders" && failing {
			return apperr.New(apperr.QueryFailed, "falla")
		}
		return nil
	}
	ctx := context.Background()
	req := e.req(model.MigrationOptions{Workers: 2})
	plan, err := e.svc.Plan(ctx, req)
	require.NoError(t, err)
	job, err := e.svc.Start(ctx, req, plan.PlanHash)
	require.NoError(t, err)
	r := e.waitDone(t, job.ID)
	require.Equal(t, model.MigPartial, r.Job.State)
	require.Len(t, r.Failed, 1)

	failing = false
	_, err = e.svc.Retry(ctx, job.ID)
	require.NoError(t, err)
	r = e.waitDone(t, job.ID)
	require.Equal(t, model.MigDone, r.Job.State, "%+v", r.Failed)
	require.Equal(t, 50, e.eng.RowCount("copia", "orders"))

	_, err = e.svc.Verify(ctx, job.ID, model.VerifyOptions{})
	require.Equal(t, "objects", appErr(t, err).Detail)
	v, err := e.svc.Verify(ctx, job.ID, model.VerifyOptions{RowCounts: true, ForeignKeys: true})
	require.NoError(t, err)
	require.True(t, v.Checked)
	require.Empty(t, v.RowCounts)

	require.Equal(t, "confirm", appErr(t, e.svc.Cleanup(ctx, job.ID, "otro")).Detail, "hay que escribir el nombre del schema destino")
	require.NoError(t, e.svc.Cleanup(ctx, job.ID, "copia"))
	require.Equal(t, -1, e.eng.RowCount("copia", "orders"))
}

func TestMigrationLogsNoSQLNoValues(t *testing.T) {
	// Lo que sale hacia fuera (eventos e historial) no lleva sentencias ni valores de filas.
	e := newMigEnv(t)
	plan, err := e.svc.Plan(context.Background(), e.req(model.MigrationOptions{}))
	require.NoError(t, err)
	job, err := e.svc.Start(context.Background(), e.req(model.MigrationOptions{}), plan.PlanHash)
	require.NoError(t, err)
	e.waitDone(t, job.ID)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		dump := strings.ToUpper(strings.Join([]string{ev.name, sprint(ev.data)}, " "))
		require.NotContains(t, dump, "CREATE TABLE")
		require.NotContains(t, dump, "INSERT")
	}
	for _, h := range e.hist {
		require.NotContains(t, strings.ToUpper(h.SQL), "INSERT")
		require.NotContains(t, strings.ToUpper(h.SQL), "CREATE TABLE")
	}
}

func sprint(v any) string { return fmt.Sprintf("%+v", v) }
