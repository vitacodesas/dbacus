package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/driver/fake"
	"github.com/vitacodesas/dbacus/internal/edit"
	"github.com/vitacodesas/dbacus/internal/logging"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/results"
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
	return e, NewEditService(EditDeps{Sessions: e.sessions, Results: e.q.d.Results, Schema: e.q.d.Schema, Store: e.st, Emit: e.q.d.Emit})
}

// newApplyEnv es newEditEnv con un historial capturado y las filas releídas simuladas.
func newApplyEnv(t *testing.T) (*queryEnv, *EditService, *[]model.HistoryEntry) {
	t.Helper()
	e, _ := newEditEnv(t)
	var mu sync.Mutex
	hist := &[]model.HistoryEntry{}
	svc := NewEditService(EditDeps{Sessions: e.sessions, Results: e.q.d.Results, Schema: e.q.d.Schema, Store: e.st, Emit: e.q.d.Emit,
		History: func(h model.HistoryEntry) { mu.Lock(); *hist = append(*hist, h); mu.Unlock() }})
	e.drv.OnRun = func(_ context.Context, stmt string) (driver.RunResult, error) {
		if strings.Contains(stmt, "WHERE") { // relectura de una fila tras aplicar
			return fake.Rows([]string{"id", "name", "note"}, []any{int64(1), "servidor", "trigger"}), nil
		}
		return fake.Rows([]string{"id", "name", "note"}, []any{int64(1), "a", nil}, []any{int64(2), "b", "nota"}), nil
	}
	return e, svc, hist
}

func editConn(t *testing.T, e *queryEnv) *fake.Conn {
	t.Helper()
	require.Len(t, e.drv.Pools[0].Edits, 1, "se usó una conexión de edición")
	return e.drv.Pools[0].Edits[0]
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

func stageTwo(t *testing.T, svc *EditService, qid string) {
	t.Helper()
	ctx := context.Background()
	_, err := svc.Stage(ctx, qid, 0, 0, 1, edit.Value{Text: "x"})
	require.NoError(t, err)
	_, err = svc.Stage(ctx, qid, 0, 1, 2, edit.Value{Text: "y"})
	require.NoError(t, err)
}

func TestApplyCommitsEverythingInOneTransaction(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newApplyEnv(t)
	qid := e.execute(t, "t1", "SELECT * FROM t")
	e.terminal(t, qid)
	stageTwo(t, svc, qid)

	res, err := svc.Apply(ctx, qid, 0)
	require.NoError(t, err)
	require.Equal(t, 2, res.Applied)
	require.Nil(t, res.Conflict)

	c := editConn(t, e)
	require.Equal(t, []string{"begin", "commit"}, c.TxLog)
	require.Len(t, c.Execs, 3)
	require.Contains(t, c.Execs[0].Stmt, "STRICT_ALL_TABLES", "el modo estricto evita recortes silenciosos")
	require.Equal(t, "UPDATE `app`.`t` SET `name` = ? WHERE `id` = ? AND `name` <=> ?", c.Execs[1].Stmt)
	require.Equal(t, []any{"x", int64(1), "a"}, c.Execs[1].Args)
	require.Equal(t, []any{"y", int64(2), "nota"}, c.Execs[2].Args)
	require.Empty(t, e.drv.Pools[0].Conns[0].Execs, "la conexión de la pestaña no se toca")
	require.True(t, c.Released.Load())

	// El grid ve lo que quedó en el servidor (relectura: triggers y valores por defecto), sin marcas de pendiente.
	w, err := e.q.GetRows(qid, 0, 0, 10)
	require.NoError(t, err)
	require.Equal(t, results.Cell{V: "servidor"}, w.Rows[0][1])
	require.Equal(t, results.Cell{V: "trigger"}, w.Rows[0][2])
	info, err := svc.GetEditInfo(ctx, qid, 0)
	require.NoError(t, err)
	require.Equal(t, 0, info.Pending)
	e.next(t, "edit:applied")

	require.Len(t, *hist, 2)
	require.True(t, (*hist)[0].OK)
	require.Contains(t, (*hist)[0].SQL, "UPDATE `app`.`t` SET `name` = 'x'")
	require.Equal(t, "app", (*hist)[0].Database)

	// Aplicar otra vez no hace nada.
	res, err = svc.Apply(ctx, qid, 0)
	require.NoError(t, err)
	require.Zero(t, res.Applied)
}

func TestApplyConflictRollsEverythingBack(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		exists bool
		reason string
	}{{"la fila cambió", true, edit.ConflictChanged}, {"la fila ya no existe", false, edit.ConflictMissing}} {
		t.Run(tc.name, func(t *testing.T) {
			e, svc, hist := newApplyEnv(t)
			qid := e.execute(t, "t1", "SELECT * FROM t")
			e.terminal(t, qid)
			stageTwo(t, svc, qid)
			updates := 0
			e.drv.OnExec = func(_ context.Context, stmt string, _ []any) (int64, error) {
				if strings.HasPrefix(stmt, "UPDATE") {
					updates++
					if updates == 2 {
						return 0, nil // la segunda fila no coincide con lo que se vio
					}
				}
				return 1, nil
			}
			prev := e.drv.OnRun
			e.drv.OnRun = func(ctx context.Context, stmt string) (driver.RunResult, error) {
				if strings.HasPrefix(stmt, "SELECT 1 FROM") && !tc.exists {
					return fake.Rows([]string{"1"}), nil
				}
				if strings.HasPrefix(stmt, "SELECT 1 FROM") {
					return fake.Rows([]string{"1"}, []any{int64(1)}), nil
				}
				return prev(ctx, stmt)
			}

			res, err := svc.Apply(ctx, qid, 0)
			require.NoError(t, err)
			require.Zero(t, res.Applied)
			require.NotNil(t, res.Conflict)
			require.Equal(t, 1, res.Conflict.Row)
			require.Equal(t, tc.reason, res.Conflict.Reason)

			require.Equal(t, []string{"begin", "rollback"}, editConn(t, e).TxLog)
			info, _ := svc.GetEditInfo(ctx, qid, 0)
			require.Equal(t, 2, info.Pending, "los cambios siguen pendientes para decidir qué hacer")
			require.Empty(t, *hist, "lo no aplicado no entra en el historial")
		})
	}
}

func TestApplyServerErrorRollsBackAndNamesTheRow(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newApplyEnv(t)
	qid := e.execute(t, "t1", "SELECT * FROM t")
	e.terminal(t, qid)
	stageTwo(t, svc, qid)
	updates := 0
	e.drv.OnExec = func(_ context.Context, stmt string, _ []any) (int64, error) {
		if strings.HasPrefix(stmt, "UPDATE") {
			updates++
			if updates == 2 {
				return 0, apperr.New(apperr.QueryFailed, "Data too long for column 'name'")
			}
		}
		return 1, nil
	}
	_, err := svc.Apply(ctx, qid, 0)
	require.Error(t, err)
	require.Contains(t, apperr.From(err).Message, "Fila 2: Data too long")
	require.Equal(t, apperr.QueryFailed, code(t, err))
	require.Equal(t, []string{"begin", "rollback"}, editConn(t, e).TxLog)
	require.Len(t, *hist, 1)
	require.False(t, (*hist)[0].OK, "el fallo queda en el historial")
	info, _ := svc.GetEditInfo(ctx, qid, 0)
	require.Equal(t, 2, info.Pending)
}

func TestApplyIsRejectedOnReadOnlyProfile(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newApplyEnv(t)
	qid := e.execute(t, "t1", "SELECT * FROM t")
	e.terminal(t, qid)
	stageTwo(t, svc, qid)
	profiles, err := e.svc.List(ctx)
	require.NoError(t, err)
	p := profiles[0]
	p.ReadOnly = true
	_, err = e.svc.Save(ctx, p, ConnectionSecrets{})
	require.NoError(t, err)

	_, err = svc.Apply(ctx, qid, 0)
	require.Equal(t, apperr.QueryFailed, code(t, err))
	require.Equal(t, "readonly", detail(t, err))
	require.Empty(t, e.drv.Pools[0].Edits, "ni siquiera se abre una conexión de escritura")
}

func TestApplyFailsIfTheEditConnectionFails(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newApplyEnv(t)
	qid := e.execute(t, "t1", "SELECT * FROM t")
	e.terminal(t, qid)
	stageTwo(t, svc, qid)
	e.drv.AcquireFn = func(context.Context) error { return errors.New("sin conexiones") }
	_, err := svc.Apply(ctx, qid, 0)
	require.Error(t, err)
	info, _ := svc.GetEditInfo(ctx, qid, 0)
	require.Equal(t, 2, info.Pending)
}

// Criterio del spec 13: lo que el usuario escribe en una celda no aparece en logs ni en eventos.
func TestEditedValuesNeverReachLogsOrEvents(t *testing.T) {
	const secret = "VALOR-EDITADO-9f2c"
	ctx := context.Background()
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(logging.NewHandler(&logs, slog.LevelDebug)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	e, svc, _ := newApplyEnv(t)
	e.drv.OnExec = func(_ context.Context, stmt string, _ []any) (int64, error) {
		if strings.HasPrefix(stmt, "UPDATE") {
			return 0, errors.New("fallo del servidor") // también el camino de error
		}
		return 1, nil
	}
	qid := e.execute(t, "t1", "SELECT * FROM t")
	e.terminal(t, qid)
	_, err := svc.Stage(ctx, qid, 0, 0, 2, edit.Value{Text: secret})
	require.NoError(t, err)
	_, _ = svc.Apply(ctx, qid, 0) // falla a propósito

	e.drv.OnExec = nil
	_, err = svc.Stage(ctx, qid, 0, 1, 2, edit.Value{Text: secret})
	require.NoError(t, err)
	_, err = svc.Discard(ctx, qid, 0)
	require.NoError(t, err)
	_, err = svc.Stage(ctx, qid, 0, 1, 2, edit.Value{Text: secret})
	require.NoError(t, err)
	_, err = svc.Apply(ctx, qid, 0)
	require.NoError(t, err)

	var wire []any
	for len(e.events) > 0 {
		ev := <-e.events
		wire = append(wire, ev.name, ev.data)
	}
	body, err := json.Marshal(wire)
	require.NoError(t, err)
	require.NotContains(t, string(body), secret, "eventos")
	require.NotContains(t, logs.String(), secret, "logs")
}

func TestFormattingJSONIsNotAChange(t *testing.T) {
	ctx := context.Background()
	e, svc := newEditEnv(t)
	e.schema.Details["app.t"] = model.TableDetail{
		Table:   model.Table{Name: "t", Schema: "app", Type: "table"},
		Columns: []model.Column{{Name: "id", DataType: "int", ColumnType: "int"}, {Name: "doc", DataType: "json", ColumnType: "json", Nullable: true}},
		Indexes: []model.Index{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
	}
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"id", "doc"}, []any{int64(1), `{"b": 1, "a": [1, 2]}`}), nil
	}
	qid := e.execute(t, "t1", "SELECT * FROM t")
	e.terminal(t, qid)

	info, err := svc.Stage(ctx, qid, 0, 0, 1, edit.Value{Text: "{\n  \"a\": [\n    1,\n    2\n  ],\n  \"b\": 1\n}"})
	require.NoError(t, err)
	require.Equal(t, 0, info.Pending, "solo dar formato no deja un cambio pendiente")

	info, err = svc.Stage(ctx, qid, 0, 0, 1, edit.Value{Text: `{"b": 2, "a": [1, 2]}`})
	require.NoError(t, err)
	require.Equal(t, 1, info.Pending)
}
