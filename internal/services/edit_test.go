package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver"
	"tabulariswails/internal/driver/fake"
	"tabulariswails/internal/edit"
	"tabulariswails/internal/model"
)

func newEditEnv(t *testing.T) (*queryEnv, *EditService) {
	t.Helper()
	e := newQueryEnv(t)
	e.schema.Details = map[string]model.TableDetail{"app.t": {
		Table:   model.Table{Name: "t", Schema: "app", Type: "table"},
		Columns: []model.Column{{Name: "id", DataType: "int"}, {Name: "name", DataType: "varchar"}},
		Indexes: []model.Index{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
	}}
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"id", "name"}, []any{int64(1), "a"}), nil
	}
	return e, NewEditService(EditDeps{Sessions: e.sessions, Results: e.q.d.Results, Schema: e.q.d.Schema, Store: e.st})
}

func TestGetEditInfo(t *testing.T) {
	ctx := context.Background()

	t.Run("SELECT de una tabla con clave es editable y usa la base de la ejecución", func(t *testing.T) {
		e, svc := newEditEnv(t)
		qid := e.execute(t, "t1", "SELECT * FROM t")
		e.terminal(t, qid)
		info, err := svc.GetEditInfo(ctx, qid, 0)
		require.NoError(t, err)
		require.True(t, info.Editable, info.Reason)
		require.Equal(t, "app", info.Database)
		require.Equal(t, []int{0}, info.KeyColumns)
		require.False(t, info.Columns[0].Editable)
		require.True(t, info.Columns[1].Editable)
	})

	t.Run("el LIMIT automático no afecta: se analiza la sentencia original", func(t *testing.T) {
		e, svc := newEditEnv(t)
		qid := e.execute(t, "t1", "SELECT * FROM t", func(r *ExecuteRequest) { r.AutoLimit = 100 })
		e.terminal(t, qid)
		info, err := svc.GetEditInfo(ctx, qid, 0)
		require.NoError(t, err)
		require.True(t, info.Editable, info.Reason)
	})

	t.Run("un JOIN no es editable", func(t *testing.T) {
		e, svc := newEditEnv(t)
		qid := e.execute(t, "t1", "SELECT * FROM t JOIN u ON 1")
		e.terminal(t, qid)
		info, err := svc.GetEditInfo(ctx, qid, 0)
		require.NoError(t, err)
		require.False(t, info.Editable)
		require.Equal(t, edit.ReasonNoSingleTable, info.Reason)
	})

	t.Run("una sentencia sin filas no es editable", func(t *testing.T) {
		e, svc := newEditEnv(t)
		e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) { return &fake.Result{}, nil }
		qid := e.execute(t, "t1", "UPDATE t SET name = 'x'")
		e.terminal(t, qid)
		info, err := svc.GetEditInfo(ctx, qid, 0)
		require.NoError(t, err)
		require.Equal(t, edit.ReasonNotRows, info.Reason)
	})

	t.Run("marcar solo lectura en el perfil lo desactiva al instante", func(t *testing.T) {
		e, svc := newEditEnv(t)
		qid := e.execute(t, "t1", "SELECT * FROM t")
		e.terminal(t, qid)
		profiles, err := e.svc.List(ctx)
		require.NoError(t, err)
		p := profiles[0]
		p.ReadOnly = true
		_, err = e.svc.Save(ctx, p, ConnectionSecrets{})
		require.NoError(t, err)
		info, err := svc.GetEditInfo(ctx, qid, 0)
		require.NoError(t, err)
		require.Equal(t, edit.ReasonReadOnly, info.Reason)
	})

	t.Run("resultado liberado → RESULT_EXPIRED; sentencia inexistente → NOT_FOUND", func(t *testing.T) {
		e, svc := newEditEnv(t)
		qid := e.execute(t, "t1", "SELECT * FROM t")
		e.terminal(t, qid)
		_, err := svc.GetEditInfo(ctx, qid, 7)
		require.Equal(t, apperr.NotFound, code(t, err))
		e.q.d.Results.Release(qid)
		_, err = svc.GetEditInfo(ctx, qid, 0)
		require.Equal(t, apperr.ResultExpired, code(t, err))
	})
}
