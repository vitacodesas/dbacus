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
	"tabulariswails/internal/results"
)

func newEditEnv(t *testing.T) (*queryEnv, *EditService) {
	t.Helper()
	e := newQueryEnv(t)
	e.schema.Details = map[string]model.TableDetail{"app.t": {
		Table:   model.Table{Name: "t", Schema: "app", Type: "table"},
		Columns: []model.Column{{Name: "id", DataType: "int", ColumnType: "int"}, {Name: "name", DataType: "varchar", ColumnType: "varchar(5)"}, {Name: "note", DataType: "text", ColumnType: "text", Nullable: true}},
		Indexes: []model.Index{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
	}}
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"id", "name", "note"}, []any{int64(1), "a", nil}, []any{int64(2), "b", "nota"}), nil
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

func TestStageAndPreview(t *testing.T) {
	ctx := context.Background()
	e, svc := newEditEnv(t)
	qid := e.execute(t, "t1", "SELECT * FROM t")
	e.terminal(t, qid)
	cell := func(row, col int) results.Cell {
		w, err := e.q.GetRows(qid, 0, 0, 10)
		require.NoError(t, err)
		return w.Rows[row][col]
	}

	// Un cambio se ve en el grid con Edited y el valor del servidor sigue intacto en Go.
	info, err := svc.Stage(ctx, qid, 0, 0, 1, edit.Value{Text: "x"})
	require.NoError(t, err)
	require.Equal(t, 1, info.Pending)
	require.Equal(t, results.Cell{V: "x", Edited: true}, cell(0, 1))
	require.Equal(t, results.Cell{V: "b"}, cell(1, 1))

	// NULL en una columna nulable y el segundo cambio de otra fila.
	_, err = svc.Stage(ctx, qid, 0, 1, 2, edit.Value{Null: true})
	require.NoError(t, err)
	info, err = svc.Stage(ctx, qid, 0, 0, 2, edit.Value{Text: "o'brien"})
	require.NoError(t, err)
	require.Equal(t, 3, info.Pending)

	pv, err := svc.Preview(ctx, qid, 0)
	require.NoError(t, err)
	require.Len(t, pv, 2)
	require.Equal(t, 0, pv[0].Row)
	require.Equal(t, []int{1, 2}, pv[0].Cols)
	require.Equal(t, "UPDATE `app`.`t` SET `name` = 'x', `note` = 'o\\'brien' WHERE `id` = 1 AND `name` <=> 'a' AND `note` <=> NULL", pv[0].SQL)
	require.Equal(t, "UPDATE `app`.`t` SET `note` = NULL WHERE `id` = 2 AND `note` <=> 'nota'", pv[1].SQL)

	// Escribir otra vez el valor original retira el cambio.
	info, err = svc.Stage(ctx, qid, 0, 0, 1, edit.Value{Text: "a"})
	require.NoError(t, err)
	require.Equal(t, 2, info.Pending)
	require.Equal(t, results.Cell{V: "a"}, cell(0, 1))

	info, err = svc.Unstage(ctx, qid, 0, 1, 2)
	require.NoError(t, err)
	require.Equal(t, 1, info.Pending)

	info, err = svc.Discard(ctx, qid, 0)
	require.NoError(t, err)
	require.Equal(t, 0, info.Pending)
	require.Equal(t, results.Cell{}, cell(0, 2), "tras descartar vuelve el NULL original")
	pv, _ = svc.Preview(ctx, qid, 0)
	require.Empty(t, pv)
}

func TestStageRejectsWhatCannotBeEdited(t *testing.T) {
	ctx := context.Background()
	e, svc := newEditEnv(t)
	qid := e.execute(t, "t1", "SELECT * FROM t")
	e.terminal(t, qid)

	cases := []struct {
		name     string
		row, col int
		v        edit.Value
		detail   string
	}{
		{"columna clave", 0, 0, edit.Value{Text: "9"}, edit.ColKey},
		{"columna fuera de rango", 0, 9, edit.Value{Text: "x"}, "col"},
		{"fila fuera de rango", 9, 1, edit.Value{Text: "x"}, "row"},
		{"NULL en NOT NULL", 0, 1, edit.Value{Null: true}, "cell"},
		{"demasiado largo para varchar(5)", 0, 1, edit.Value{Text: "abcdef"}, "cell"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.Stage(ctx, qid, 0, c.row, c.col, c.v)
			require.Equal(t, apperr.Validation, code(t, err))
			require.Equal(t, c.detail, detail(t, err))
		})
	}
	info, err := svc.GetEditInfo(ctx, qid, 0)
	require.NoError(t, err)
	require.Equal(t, 0, info.Pending, "los rechazos no dejan nada pendiente")

	t.Run("perfil de solo lectura", func(t *testing.T) {
		profiles, err := e.svc.List(ctx)
		require.NoError(t, err)
		p := profiles[0]
		p.ReadOnly = true
		_, err = e.svc.Save(ctx, p, ConnectionSecrets{})
		require.NoError(t, err)
		_, err = svc.Stage(ctx, qid, 0, 0, 1, edit.Value{Text: "x"})
		require.Equal(t, edit.ReasonReadOnly, detail(t, err))
	})
}

func TestPendingDiesWithTheResult(t *testing.T) {
	ctx := context.Background()
	e, svc := newEditEnv(t)
	qid := e.execute(t, "t1", "SELECT * FROM t")
	e.terminal(t, qid)
	_, err := svc.Stage(ctx, qid, 0, 0, 1, edit.Value{Text: "x"})
	require.NoError(t, err)

	// Volver a ejecutar en la pestaña libera el buffer y, con él, los cambios pendientes.
	qid2 := e.execute(t, "t1", "SELECT * FROM t")
	e.terminal(t, qid2)
	_, err = svc.GetEditInfo(ctx, qid, 0)
	require.Equal(t, apperr.ResultExpired, code(t, err))
	info, err := svc.GetEditInfo(ctx, qid2, 0)
	require.NoError(t, err)
	require.Equal(t, 0, info.Pending)
}
