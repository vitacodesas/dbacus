package services

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/alter"
	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/driver/mysql"
	"github.com/vitacodesas/dbacus/internal/model"
)

const alterCreateSQL = "CREATE TABLE `t` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  `name` varchar(5) NOT NULL,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB AUTO_INCREMENT=10"

func newAlterEnv(t *testing.T, profileMut ...func(*model.ConnectionProfile)) (*queryEnv, *AlterService, *[]model.HistoryEntry) {
	t.Helper()
	e := newQueryEnv(t, profileMut...)
	e.schema.Details = map[string]model.TableDetail{"app.t": {
		Table:     model.Table{Name: "t", Schema: "app", Type: "table"},
		CreateSQL: alterCreateSQL,
		Columns: []model.Column{
			{Name: "id", DataType: "int", ColumnType: "int", IsPrimaryKey: true, IsAutoInc: true},
			{Name: "name", DataType: "varchar", ColumnType: "varchar(5)"},
		},
		Indexes: []model.Index{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
	}}
	// El generador real de MySQL sobre el driver falso: así se prueba el SQL de verdad, no un sustituto.
	e.drv.OnDDL = (&mysql.Driver{}).GenerateDDL
	var mu sync.Mutex
	hist := &[]model.HistoryEntry{}
	svc := NewAlterService(AlterDeps{Sessions: e.sessions, Schema: e.q.d.Schema, Store: e.st,
		History: func(h model.HistoryEntry) { mu.Lock(); *hist = append(*hist, h); mu.Unlock() }})
	return e, svc, hist
}

func widen() model.ChangeSet {
	return model.ChangeSet{Database: "app", Table: "t", Ops: []model.AlterOp{
		{Kind: model.OpModifyColumn, Name: "name", Column: &model.ColumnSpec{Type: "varchar(50)"}},
	}}
}

func TestAlterPreviewDoesNotExecute(t *testing.T) {
	e, svc, _ := newAlterEnv(t)
	p, err := svc.Preview(context.Background(), e.sid, widen())
	require.NoError(t, err)
	require.Equal(t, []string{"ALTER TABLE `app`.`t`\n  MODIFY COLUMN `name` varchar(50) NOT NULL"}, p.Statements)
	require.Equal(t, alter.Hash(p.Statements), p.SQLHash)
	require.True(t, p.NoRollback)
	require.False(t, p.Destructive)
	require.Empty(t, e.drv.Pools[0].Edits, "Preview no abre ninguna conexión de escritura")
}

func TestAlterPreviewEmptyChangeSet(t *testing.T) {
	e, svc, _ := newAlterEnv(t)
	p, err := svc.Preview(context.Background(), e.sid, model.ChangeSet{Database: "app", Table: "t"})
	require.NoError(t, err)
	require.Empty(t, p.Statements)
	require.False(t, p.NoRollback)
}

func TestAlterApply(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newAlterEnv(t)
	cs := widen()
	p, err := svc.Preview(ctx, e.sid, cs)
	require.NoError(t, err)

	res, err := svc.Apply(ctx, e.sid, cs, p.SQLHash)
	require.NoError(t, err)
	require.Nil(t, res.Failed)
	require.Equal(t, p.Statements, res.Applied)
	require.NotNil(t, res.Model, "devuelve la tabla releída")

	c := e.drv.Pools[0].Edits
	require.Len(t, c, 1)
	require.Len(t, c[0].Execs, 1)
	require.Equal(t, p.Statements[0], c[0].Execs[0].Stmt)
	require.Empty(t, c[0].TxLog, "el DDL no usa transacción")
	require.True(t, c[0].Released.Load())

	inv := e.next(t, "schema:invalidated").(SchemaInvalidated)
	require.Equal(t, "app", inv.Database)
	require.Len(t, *hist, 1)
	require.Equal(t, p.Statements[0], (*hist)[0].SQL)
	require.True(t, (*hist)[0].OK)
}

func TestAlterApplyRejectsStalePreview(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newAlterEnv(t)
	_, err := svc.Apply(ctx, e.sid, widen(), "otro-hash")
	require.Equal(t, apperr.Validation, code(t, err))
	require.Equal(t, "stale_preview", detail(t, err))
	require.Empty(t, e.drv.Pools[0].Edits, "no se envió nada al servidor")
}

func TestAlterApplyNothingToApply(t *testing.T) {
	e, svc, _ := newAlterEnv(t)
	_, err := svc.Apply(context.Background(), e.sid, model.ChangeSet{Database: "app", Table: "t"}, alter.Hash(nil))
	require.Equal(t, "empty", detail(t, err))
}

func TestAlterStaleModel(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newAlterEnv(t)
	m, err := svc.GetModel(ctx, e.sid, "app", "t")
	require.NoError(t, err)
	cs := widen()
	cs.Fingerprint = m.Fingerprint

	// Otra sesión cambia la estructura entre GetModel y Apply.
	d := e.schema.Details["app.t"]
	d.CreateSQL = strings.Replace(alterCreateSQL, "varchar(5)", "varchar(9)", 1)
	e.schema.Details["app.t"] = d
	_, err = svc.Preview(ctx, e.sid, cs)
	require.Equal(t, "stale_model", detail(t, err))
	_, err = svc.Apply(ctx, e.sid, cs, "x")
	require.Equal(t, "stale_model", detail(t, err))
	require.Empty(t, e.drv.Pools[0].Edits)
}

func TestAlterFingerprintIgnoresAutoIncrement(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newAlterEnv(t)
	m, err := svc.GetModel(ctx, e.sid, "app", "t")
	require.NoError(t, err)
	d := e.schema.Details["app.t"]
	d.CreateSQL = strings.Replace(alterCreateSQL, "AUTO_INCREMENT=10", "AUTO_INCREMENT=99", 1)
	e.schema.Details["app.t"] = d
	cs := widen()
	cs.Fingerprint = m.Fingerprint
	_, err = svc.Preview(ctx, e.sid, cs)
	require.NoError(t, err, "un INSERT entre medias no cambia la estructura")
}

func TestAlterValidationErrorsReachTheCaller(t *testing.T) {
	e, svc, _ := newAlterEnv(t)
	_, err := svc.Preview(context.Background(), e.sid, model.ChangeSet{Database: "app", Table: "t", Ops: []model.AlterOp{{Kind: model.OpDropColumn, Name: "id"}}})
	require.Equal(t, apperr.Validation, code(t, err))
	require.Equal(t, "primaryKey", detail(t, err))
}

func TestAlterViewIsNotEditable(t *testing.T) {
	e, svc, _ := newAlterEnv(t)
	d := e.schema.Details["app.t"]
	d.Type = "view"
	e.schema.Details["app.t"] = d
	_, err := svc.GetModel(context.Background(), e.sid, "app", "t")
	require.Equal(t, "view", detail(t, err))
}

func TestAlterPartialFailure(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newAlterEnv(t)
	e.drv.OnDDL = func(model.ChangeSet, model.AlterModel, driver.ServerInfo) ([]string, error) {
		return []string{"ALTER TABLE uno", "ALTER TABLE dos", "ALTER TABLE tres"}, nil
	}
	e.drv.OnExec = func(_ context.Context, stmt string, _ []any) (int64, error) {
		if stmt == "ALTER TABLE dos" {
			return 0, errors.New("Duplicate key name")
		}
		return 0, nil
	}
	cs := widen()
	p, err := svc.Preview(ctx, e.sid, cs)
	require.NoError(t, err)
	res, err := svc.Apply(ctx, e.sid, cs, p.SQLHash)
	require.NoError(t, err, "un fallo de la base no es un error de la llamada: el resultado dice qué pasó")
	require.Equal(t, []string{"ALTER TABLE uno"}, res.Applied)
	require.NotNil(t, res.Failed)
	require.Equal(t, 1, res.Failed.Index)
	require.NotNil(t, res.Model, "el modelo devuelto es el real tras el intento")
	require.Len(t, e.drv.Pools[0].Edits[0].Execs, 2, "se detuvo en la que falló")

	e.next(t, "schema:invalidated") // se aplicó una: la caché se invalida aunque otra fallara
	require.Len(t, *hist, 2)
	require.True(t, (*hist)[0].OK)
	require.False(t, (*hist)[1].OK)
}

func TestAlterOneApplyPerTable(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newAlterEnv(t)
	started, release := make(chan struct{}), make(chan struct{})
	e.drv.OnExec = func(context.Context, string, []any) (int64, error) {
		close(started)
		<-release
		return 0, nil
	}
	cs := widen()
	p, err := svc.Preview(ctx, e.sid, cs)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { _, err := svc.Apply(ctx, e.sid, cs, p.SQLHash); done <- err }()
	<-started
	_, err = svc.Apply(ctx, e.sid, cs, p.SQLHash)
	require.Equal(t, apperr.QueryFailed, code(t, err))
	require.Equal(t, "busy", detail(t, err))
	close(release)
	require.NoError(t, <-done)
}

func TestAlterCancelKillsTheStatement(t *testing.T) {
	e, svc, _ := newAlterEnv(t)
	started := make(chan struct{})
	e.drv.OnExec = func(ctx context.Context, _ string, _ []any) (int64, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	}
	cs := widen()
	p, err := svc.Preview(context.Background(), e.sid, cs)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan model.AlterResult, 1)
	go func() { r, _ := svc.Apply(ctx, e.sid, cs, p.SQLHash); done <- r }()
	<-started
	cancel()
	select {
	case r := <-done:
		require.NotNil(t, r.Failed)
		require.Equal(t, "Operación cancelada", r.Failed.Message)
	case <-time.After(5 * time.Second):
		t.Fatal("Apply no terminó tras cancelar")
	}
	require.Eventually(t, func() bool { return e.drv.Pools[0].Edits[0].Killed.Load() == 1 }, 2*time.Second, 10*time.Millisecond)
}

// ---- Solo lectura (spec 10 y 19): nunca un ALTER ----

func readOnlyProfile(p *model.ConnectionProfile) { p.ReadOnly = true }

func TestAlterReadOnlyRejectsBeforeSending(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newAlterEnv(t, readOnlyProfile)
	calls := e.schema.Calls.Load()

	check := func(err error) {
		t.Helper()
		require.Equal(t, apperr.QueryFailed, code(t, err))
		require.Equal(t, "readonly", detail(t, err))
	}
	_, err := svc.GetModel(ctx, e.sid, "app", "t")
	check(err)
	_, err = svc.Preview(ctx, e.sid, widen())
	check(err)
	_, err = svc.Apply(ctx, e.sid, widen(), "cualquiera")
	check(err)

	require.Equal(t, calls, e.schema.Calls.Load(), "ni siquiera se leyó la tabla")
	require.Empty(t, e.drv.Pools[0].Edits, "no se adquirió ninguna conexión de escritura")
	for _, c := range e.drv.Pools[0].Conns {
		require.Empty(t, c.Execs)
	}
	require.Empty(t, *hist)
}

func TestAlterReadOnlyAppliesToOpenSession(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newAlterEnv(t)
	cs := widen()
	p, err := svc.Preview(ctx, e.sid, cs)
	require.NoError(t, err)

	// El perfil se marca de solo lectura DESPUÉS de abrir la sesión y de previsualizar.
	profiles, err := e.svc.List(ctx)
	require.NoError(t, err)
	prof := profiles[0]
	prof.ReadOnly = true
	_, err = e.svc.Save(ctx, prof, ConnectionSecrets{})
	require.NoError(t, err)

	_, err = svc.Apply(ctx, e.sid, cs, p.SQLHash)
	require.Equal(t, "readonly", detail(t, err))
	require.Empty(t, e.drv.Pools[0].Edits)
}

// El SQL con literales (un DEFAULT) no debe acabar en los logs (regla 5 de AGENTS.md): ni al aplicar ni al fallar.
func TestAlterLogsNoLiterals(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	ctx := context.Background()
	e, svc, _ := newAlterEnv(t)
	e.drv.OnExec = func(context.Context, string, []any) (int64, error) { return 0, errors.New("boom") }
	cs := model.ChangeSet{Database: "app", Table: "t", Ops: []model.AlterOp{
		{Kind: model.OpModifyColumn, Name: "name", Column: &model.ColumnSpec{Type: "varchar(5)", Default: sp("valor-secreto-123")}},
	}}
	p, err := svc.Preview(ctx, e.sid, cs)
	require.NoError(t, err)
	require.Contains(t, p.Statements[0], "valor-secreto-123", "el usuario sí lo ve en la revisión")
	_, err = svc.Apply(ctx, e.sid, cs, p.SQLHash)
	require.NoError(t, err)
	require.NotContains(t, buf.String(), "valor-secreto-123")
}
