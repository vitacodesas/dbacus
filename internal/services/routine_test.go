package services

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/driver/fake"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/sqlsplit"
)

const origProc = "CREATE DEFINER=`root`@`localhost` PROCEDURE `p`()\nBEGIN\n  SELECT 1;\nEND"
const newProc = "CREATE PROCEDURE p()\nBEGIN\n  SELECT 2;\nEND"

func newRoutineEnv(t *testing.T, profileMut ...func(*model.ConnectionProfile)) (*queryEnv, *RoutineService, *[]model.HistoryEntry) {
	t.Helper()
	e := newQueryEnv(t, profileMut...)
	e.schema.RoutinesBy = map[string][]model.Routine{"app": {
		{Name: "p", Kind: model.KindProcedure, Definer: "root@localhost"},
		{Name: "f", Kind: model.KindFunction},
	}}
	e.schema.RoutineDets = map[string]model.RoutineDetail{
		"app.procedure.p": {Routine: model.Routine{Name: "p", Kind: model.KindProcedure, Definer: "root@localhost"}, Database: "app",
			CreateSQL: origProc, SQLMode: "ANSI_QUOTES", CollationConnection: "utf8mb4_general_ci"},
		"app.function.f": {Routine: model.Routine{Name: "f", Kind: model.KindFunction}, Database: "app",
			CreateSQL: "CREATE FUNCTION `f`() RETURNS int RETURN 1", SQLMode: ""},
	}
	var mu sync.Mutex
	hist := &[]model.HistoryEntry{}
	svc := NewRoutineService(RoutineDeps{Sessions: e.sessions, Schema: e.q.d.Schema, Store: e.st,
		History: func(h model.HistoryEntry) { mu.Lock(); *hist = append(*hist, h); mu.Unlock() }})
	return e, svc, hist
}

func replaceChange() model.RoutineChange {
	return model.RoutineChange{Database: "app", Kind: model.KindProcedure, Name: "p", Op: model.RoutineReplace, SQL: newProc}
}

func allExecs(e *queryEnv) []string {
	var out []string
	for _, c := range e.drv.Pools[0].Edits {
		for _, x := range c.Execs {
			out = append(out, x.Stmt)
		}
	}
	return out
}

func TestRoutineListIsCachedAndHasNoBodies(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRoutineEnv(t)
	l, err := svc.List(ctx, e.sid, "app")
	require.NoError(t, err)
	require.Len(t, l, 2)
	calls := e.schema.Calls.Load()
	_, err = svc.List(ctx, e.sid, "app")
	require.NoError(t, err)
	require.Equal(t, calls, e.schema.Calls.Load(), "la segunda lectura sale de la caché")
	empty, err := svc.List(ctx, e.sid, "otra")
	require.NoError(t, err)
	require.NotNil(t, empty)
	require.Empty(t, empty)
}

func TestRoutineGetSetsFingerprint(t *testing.T) {
	e, svc, _ := newRoutineEnv(t)
	d, err := svc.Get(context.Background(), e.sid, "app", model.KindProcedure, "p")
	require.NoError(t, err)
	require.NotEmpty(t, d.Fingerprint)
	require.Equal(t, origProc, d.CreateSQL)
	_, err = svc.Get(context.Background(), e.sid, "app", model.KindProcedure, "nada")
	require.Equal(t, apperr.NotFound, code(t, err))
	_, err = svc.Get(context.Background(), e.sid, "app", "trigger", "p")
	require.Equal(t, "kind", detail(t, err))
}

func TestRoutinePreviewReplaceDoesNotExecute(t *testing.T) {
	e, svc, _ := newRoutineEnv(t)
	p, err := svc.Preview(context.Background(), e.sid, replaceChange())
	require.NoError(t, err)
	require.Len(t, p.Statements, 5)
	require.Equal(t, "SET SESSION sql_mode = 'ANSI_QUOTES', collation_connection = 'utf8mb4_general_ci'", p.Statements[0])
	require.Contains(t, p.Statements[1], "CREATE PROCEDURE `__dbacus_tmp_")
	require.Contains(t, p.Statements[2], "DROP PROCEDURE `app`.`__dbacus_tmp_")
	require.Equal(t, "DROP PROCEDURE `app`.`p`", p.Statements[3])
	require.Equal(t, newProc, p.Statements[4])
	require.True(t, p.NoRollback)
	require.True(t, p.Destructive)
	var codes []string
	for _, w := range p.Warnings {
		codes = append(codes, w.Code)
	}
	require.Contains(t, codes, "grants_lost")
	require.Contains(t, codes, "no_rollback")
	require.Empty(t, e.drv.Pools[0].Edits, "Preview no abre ninguna conexión de escritura")

	p2, err := svc.Preview(context.Background(), e.sid, replaceChange())
	require.NoError(t, err)
	require.Equal(t, p.SQLHash, p2.SQLHash, "el nombre temporal es determinista: Preview y Apply coinciden")
}

func TestRoutineApplyReplace(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newRoutineEnv(t)
	ch := replaceChange()
	p, err := svc.Preview(ctx, e.sid, ch)
	require.NoError(t, err)
	res, err := svc.Apply(ctx, e.sid, ch, p.SQLHash)
	require.NoError(t, err)
	require.Nil(t, res.Failed)
	require.False(t, res.Restored)
	require.Equal(t, []string{"DROP PROCEDURE `app`.`p`", newProc}, res.Applied)
	require.Equal(t, p.Statements, allExecs(e), "se ejecuta exactamente lo que se previsualizó")
	require.Len(t, *hist, 2, "solo DROP y CREATE al historial: el SET y la rutina temporal no")
	require.NotNil(t, res.Detail)
	require.Equal(t, "app", e.drv.Pools[0].Edits[0].UsedDB.Load())
}

func TestRoutineApplyRestoresOnFailure(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newRoutineEnv(t)
	e.drv.OnExec = func(_ context.Context, stmt string, _ []any) (int64, error) {
		if stmt == newProc {
			return 0, errors.New("syntax error")
		}
		return 1, nil
	}
	ch := replaceChange()
	p, err := svc.Preview(ctx, e.sid, ch)
	require.NoError(t, err)
	res, err := svc.Apply(ctx, e.sid, ch, p.SQLHash)
	require.NoError(t, err)
	require.NotNil(t, res.Failed)
	require.Equal(t, 4, res.Failed.Index)
	require.True(t, res.Restored)
	require.Empty(t, res.BackupSQL)
	require.Equal(t, origProc, allExecs(e)[len(allExecs(e))-1], "la copia se reejecuta")
	require.True(t, (*hist)[len(*hist)-1].OK)
}

func TestRoutineRestoreFailureReturnsBackup(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newRoutineEnv(t)
	e.drv.OnExec = func(_ context.Context, stmt string, _ []any) (int64, error) {
		if stmt == newProc || stmt == origProc {
			return 0, errors.New("denegado")
		}
		return 1, nil
	}
	ch := replaceChange()
	p, _ := svc.Preview(ctx, e.sid, ch)
	res, err := svc.Apply(ctx, e.sid, ch, p.SQLHash)
	require.NoError(t, err)
	require.NotNil(t, res.Failed)
	require.False(t, res.Restored)
	require.Equal(t, origProc, res.BackupSQL)
	last := (*hist)[len(*hist)-1]
	require.Equal(t, origProc, last.SQL, "la copia queda en el historial")
	require.False(t, last.OK)
}

func TestRoutineBadSyntaxLeavesOriginalIntact(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newRoutineEnv(t)
	e.drv.OnExec = func(_ context.Context, stmt string, _ []any) (int64, error) {
		if strings.Contains(stmt, "__dbacus_tmp_") && strings.HasPrefix(stmt, "CREATE") {
			return 0, errors.New("syntax")
		}
		return 1, nil
	}
	ch := replaceChange()
	p, _ := svc.Preview(ctx, e.sid, ch)
	res, err := svc.Apply(ctx, e.sid, ch, p.SQLHash)
	require.NoError(t, err)
	require.Equal(t, 1, res.Failed.Index)
	require.False(t, res.Restored)
	for _, s := range allExecs(e) {
		require.False(t, strings.HasPrefix(s, "DROP PROCEDURE `app`.`p`"), "la original no se tocó")
	}
	require.Empty(t, res.Applied)
	require.Empty(t, *hist)
}

func TestRoutineReadOnlyRejectsBeforeSending(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newRoutineEnv(t, readOnlyProfile)
	for _, ch := range []model.RoutineChange{
		replaceChange(),
		{Database: "app", Kind: model.KindProcedure, Op: model.RoutineCreate, SQL: newProc},
		{Database: "app", Kind: model.KindProcedure, Name: "p", Op: model.RoutineDrop},
	} {
		_, err := svc.Preview(ctx, e.sid, ch)
		require.Equal(t, "readonly", detail(t, err))
		_, err = svc.Apply(ctx, e.sid, ch, "x")
		require.Equal(t, "readonly", detail(t, err))
	}
	require.Empty(t, e.drv.Pools[0].Edits)
	require.Empty(t, *hist)
	// Ver sí se puede.
	_, err := svc.List(ctx, e.sid, "app")
	require.NoError(t, err)
	_, err = svc.Get(ctx, e.sid, "app", model.KindProcedure, "p")
	require.NoError(t, err)
}

func TestRoutineReadOnlyAppliesToOpenSession(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRoutineEnv(t)
	ch := replaceChange()
	p, err := svc.Preview(ctx, e.sid, ch)
	require.NoError(t, err)
	profiles, _ := e.svc.List(ctx)
	prof := profiles[0]
	prof.ReadOnly = true
	_, err = e.svc.Save(ctx, prof, ConnectionSecrets{})
	require.NoError(t, err)
	_, err = svc.Apply(ctx, e.sid, ch, p.SQLHash)
	require.Equal(t, "readonly", detail(t, err))
	require.Empty(t, e.drv.Pools[0].Edits)
}

func TestRoutineDDLIsNeverReadOnlySafe(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRoutineEnv(t)
	for _, ch := range []model.RoutineChange{
		replaceChange(), {Database: "app", Kind: model.KindProcedure, Op: model.RoutineCreate, SQL: newProc},
		{Database: "app", Kind: model.KindProcedure, Name: "p", Op: model.RoutineDrop},
	} {
		p, err := svc.Preview(ctx, e.sid, ch)
		require.NoError(t, err)
		for _, s := range p.Statements {
			require.False(t, sqlsplit.IsReadOnlySafe(s), s)
		}
	}
}

func TestRoutineValidation(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRoutineEnv(t)
	with := func(mut func(*model.RoutineChange)) error {
		ch := replaceChange()
		mut(&ch)
		_, err := svc.Preview(ctx, e.sid, ch)
		return err
	}
	require.Equal(t, "empty", detail(t, with(func(c *model.RoutineChange) { c.SQL = "  " })))
	require.Equal(t, "head", detail(t, with(func(c *model.RoutineChange) { c.SQL = "SELECT 1" })))
	require.Equal(t, "name_changed", detail(t, with(func(c *model.RoutineChange) { c.SQL = "CREATE PROCEDURE otro() SELECT 1" })))
	require.Equal(t, "name_changed", detail(t, with(func(c *model.RoutineChange) { c.SQL = "CREATE FUNCTION p() RETURNS INT RETURN 1" })))
	require.Equal(t, "database", detail(t, with(func(c *model.RoutineChange) { c.SQL = "CREATE PROCEDURE otra.p() SELECT 1" })))
	require.Equal(t, "delimiter", detail(t, with(func(c *model.RoutineChange) { c.SQL = "DELIMITER $$\n" + newProc + "$$" })))
	require.Equal(t, "too_large", detail(t, with(func(c *model.RoutineChange) { c.SQL = newProc + strings.Repeat(" ", routineMaxSQL) })))
	require.Equal(t, "system", detail(t, with(func(c *model.RoutineChange) { c.Database = "mysql" })))
	require.Equal(t, "op", detail(t, with(func(c *model.RoutineChange) { c.Op = "x" })))
	require.Equal(t, "name", detail(t, with(func(c *model.RoutineChange) {
		c.Op, c.SQL = model.RoutineCreate, "CREATE PROCEDURE __dbacus_tmp_x() SELECT 1"
	})))
}

func TestRoutineStalePreviewAndModel(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRoutineEnv(t)
	ch := replaceChange()
	_, err := svc.Apply(ctx, e.sid, ch, "otro-hash")
	require.Equal(t, "stale_preview", detail(t, err))

	d, _ := svc.Get(ctx, e.sid, "app", model.KindProcedure, "p")
	ch.Fingerprint = d.Fingerprint
	_, err = svc.Preview(ctx, e.sid, ch)
	require.NoError(t, err)
	det := e.schema.RoutineDets["app.procedure.p"]
	det.CreateSQL += " -- otro"
	e.schema.RoutineDets["app.procedure.p"] = det
	_, err = svc.Preview(ctx, e.sid, ch)
	require.Equal(t, "stale_model", detail(t, err))
}

func TestRoutineBusy(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRoutineEnv(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.drv.OnExec = func(context.Context, string, []any) (int64, error) {
		once.Do(func() { close(started) })
		<-release
		return 1, nil
	}
	ch := replaceChange()
	p, _ := svc.Preview(ctx, e.sid, ch)
	done := make(chan error, 1)
	go func() { _, err := svc.Apply(ctx, e.sid, ch, p.SQLHash); done <- err }()
	<-started
	_, err := svc.Apply(ctx, e.sid, ch, p.SQLHash)
	require.Equal(t, "busy", detail(t, err))
	close(release)
	require.NoError(t, <-done)
}

func TestRoutineCreateAndDrop(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newRoutineEnv(t)
	create := model.RoutineChange{Database: "app", Kind: model.KindProcedure, Op: model.RoutineCreate, SQL: "CREATE PROCEDURE nueva() SELECT 1"}
	p, err := svc.Preview(ctx, e.sid, create)
	require.NoError(t, err)
	require.Equal(t, []string{create.SQL}, p.Statements)
	res, err := svc.Apply(ctx, e.sid, create, p.SQLHash)
	require.NoError(t, err)
	require.Nil(t, res.Failed)

	drop := model.RoutineChange{Database: "app", Kind: model.KindProcedure, Name: "p", Op: model.RoutineDrop}
	p, err = svc.Preview(ctx, e.sid, drop)
	require.NoError(t, err)
	require.Equal(t, []string{"DROP PROCEDURE `app`.`p`"}, p.Statements)
	require.True(t, p.Destructive)
	res, err = svc.Apply(ctx, e.sid, drop, p.SQLHash)
	require.NoError(t, err)
	require.Equal(t, []string{"DROP PROCEDURE `app`.`p`"}, res.Applied)
	require.Len(t, *hist, 2)
}

func TestRoutineRecreateDefinerRewritesHeader(t *testing.T) {
	e, svc, _ := newRoutineEnv(t)
	ch := replaceChange()
	ch.SQL = "CREATE DEFINER = `otro`@`h` PROCEDURE `p`()\nSELECT 1"
	ch.RecreateDefiner = true
	p, err := svc.Preview(context.Background(), e.sid, ch)
	require.NoError(t, err)
	require.Equal(t, "CREATE PROCEDURE `p`()\nSELECT 1", p.Statements[4])
	require.NotContains(t, p.Statements[1], "DEFINER")
}

func TestRoutineForeignDefinerWarns(t *testing.T) {
	e, svc, _ := newRoutineEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"CURRENT_USER()"}, []any{"alguien@%"}), nil
	}
	p, err := svc.Preview(context.Background(), e.sid, replaceChange())
	require.NoError(t, err)
	var found bool
	for _, w := range p.Warnings {
		found = found || w.Code == "definer_foreign"
	}
	require.True(t, found)
}

func TestRoutineInvalidationScope(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRoutineEnv(t)
	_, err := svc.List(ctx, e.sid, "app")
	require.NoError(t, err)
	ch := replaceChange()
	p, _ := svc.Preview(ctx, e.sid, ch)
	_, err = svc.Apply(ctx, e.sid, ch, p.SQLHash)
	require.NoError(t, err)
	ev := e.next(t, "schema:invalidated").(SchemaInvalidated)
	require.Equal(t, "routines", ev.Scope)
	require.Equal(t, "app", ev.Database)
}

func TestRoutineLogsNoSQL(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	ctx := context.Background()
	e, svc, _ := newRoutineEnv(t)
	e.drv.OnExec = func(context.Context, string, []any) (int64, error) { return 0, errors.New("boom") }
	ch := replaceChange()
	ch.SQL = "CREATE PROCEDURE p() SELECT 'valor-secreto-123'"
	p, err := svc.Preview(ctx, e.sid, ch)
	require.NoError(t, err)
	_, err = svc.Apply(ctx, e.sid, ch, p.SQLHash)
	require.NoError(t, err)
	require.NotContains(t, buf.String(), "valor-secreto-123")
}

func TestRoutineUnsupportedWithoutReader(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.SchemaReader = &plainReader{}
	svc := NewRoutineService(RoutineDeps{Sessions: e.sessions, Store: e.st})
	_, err := svc.List(context.Background(), e.sid, "app")
	require.Equal(t, apperr.Unsupported, code(t, err))
}

func TestRoutineTemplate(t *testing.T) {
	_, svc, _ := newRoutineEnv(t)
	for _, k := range []string{model.KindProcedure, model.KindFunction} {
		s, err := svc.Template(k)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(s, "CREATE "))
	}
	_, err := svc.Template("x")
	require.Equal(t, "kind", detail(t, err))
}

// plainReader es un SchemaReader sin rutinas: el motor no las ofrece.
type plainReader struct{ fake.Schema }

func (*plainReader) Routines() {}

// Un CREATE/DROP de rutina lanzado desde el editor de consultas refresca solo las rutinas, no el árbol de tablas (spec 21).
func TestRoutineDDLFromQueryEditorInvalidatesOnlyRoutines(t *testing.T) {
	e := newQueryEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) { return &fake.Result{}, nil }
	// El evento llega antes del fin de la consulta: se lee primero (terminal descarta los eventos que no son suyos).
	qid := e.execute(t, "tab1", "CREATE PROCEDURE p() SELECT 1")
	ev := e.next(t, "schema:invalidated").(SchemaInvalidated)
	require.Equal(t, "routines", ev.Scope)
	e.terminal(t, qid)

	qid = e.execute(t, "tab1", "ALTER TABLE t ADD COLUMN c INT")
	ev = e.next(t, "schema:invalidated").(SchemaInvalidated)
	require.Equal(t, "", ev.Scope, "un DDL de tablas sigue invalidando todo")
	e.terminal(t, qid)
}
