package services

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver"
	"tabulariswails/internal/driver/fake"
	"tabulariswails/internal/edit"
	"tabulariswails/internal/model"
	"tabulariswails/internal/results"
)

// newRowsEnv: tabla t(id AUTO_INCREMENT PK, name NOT NULL sin DEFAULT, note NULL) con dos filas cargadas.
func newRowsEnv(t *testing.T, mut ...func(*model.TableDetail)) (*queryEnv, *EditService, *[]model.HistoryEntry) {
	t.Helper()
	e, _ := newEditEnv(t)
	d := model.TableDetail{
		Table: model.Table{Name: "t", Schema: "app", Type: "table"},
		Columns: []model.Column{
			{Name: "id", DataType: "int", ColumnType: "int", IsAutoInc: true},
			{Name: "name", DataType: "varchar", ColumnType: "varchar(5)"},
			{Name: "note", DataType: "text", ColumnType: "text", Nullable: true},
		},
		Indexes: []model.Index{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
	}
	for _, m := range mut {
		m(&d)
	}
	e.schema.Details["app.t"] = d
	var mu sync.Mutex
	hist := &[]model.HistoryEntry{}
	svc := NewEditService(EditDeps{Sessions: e.sessions, Results: e.q.d.Results, Schema: e.q.d.Schema, Store: e.st, Emit: e.q.d.Emit,
		History: func(h model.HistoryEntry) { mu.Lock(); *hist = append(*hist, h); mu.Unlock() }})
	e.drv.OnRun = func(_ context.Context, stmt string) (driver.RunResult, error) {
		if strings.Contains(stmt, "WHERE") { // relectura de la fila insertada
			return fake.Rows([]string{"id", "name", "note"}, []any{int64(7), "xyz", "servidor"}), nil
		}
		return fake.Rows([]string{"id", "name", "note"}, []any{int64(1), "a", nil}, []any{int64(2), "b", "nota"}), nil
	}
	return e, svc, hist
}

func load(t *testing.T, e *queryEnv) string {
	t.Helper()
	qid := e.execute(t, "t1", "SELECT * FROM t")
	e.terminal(t, qid)
	return qid
}

func view(t *testing.T, e *queryEnv, qid string) RowWindow {
	t.Helper()
	w, err := e.q.GetRows(qid, 0, 0, 100)
	require.NoError(t, err)
	return w
}

func TestAddRowFillPreviewAndApply(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newRowsEnv(t)
	e.drv.LastID = 7
	qid := load(t, e)

	info, err := svc.GetEditInfo(ctx, qid, 0)
	require.NoError(t, err)
	require.True(t, info.CanInsert)
	require.False(t, info.Columns[0].Insertable, "AUTO_INCREMENT: lo pone el servidor")
	require.True(t, info.Columns[1].Required)
	require.False(t, info.Columns[2].Required)

	add, err := svc.AddRow(ctx, qid, 0)
	require.NoError(t, err)
	require.Equal(t, []int{2}, add.Rows)
	require.Equal(t, 1, add.Info.PendingInserts)
	require.Equal(t, 1, add.Info.Pending)

	w := view(t, e, qid)
	require.EqualValues(t, 3, w.Total, "el total incluye la fila nueva")
	require.Equal(t, 2, w.NewFrom)
	require.Equal(t, results.Cell{T: results.TDefault, Edited: true}, w.Rows[2][1], "sin valor se ve como DEFAULT")

	// Falta la columna obligatoria: Apply lo dice antes de abrir ninguna conexión.
	_, err = svc.Apply(ctx, qid, 0)
	require.Equal(t, apperr.Validation, code(t, err))
	require.Equal(t, "required", detail(t, err))
	require.Contains(t, err.Error(), "Fila nueva 1")
	require.Contains(t, err.Error(), "name")
	require.Empty(t, e.drv.Pools[0].Edits)

	// La clave autoincremental no se escribe.
	_, err = svc.Stage(ctx, qid, 0, 2, 0, edit.Value{Text: "9"})
	require.Equal(t, edit.ReasonNotInsertable, detail(t, err))
	_, err = svc.Stage(ctx, qid, 0, 2, 1, edit.Value{Text: "demasiado largo"})
	require.Equal(t, "cell", detail(t, err), "las mismas validaciones que al editar")

	_, err = svc.Stage(ctx, qid, 0, 2, 1, edit.Value{Text: "xyz"})
	require.NoError(t, err)
	pv, err := svc.Preview(ctx, qid, 0)
	require.NoError(t, err)
	require.Equal(t, []edit.Preview{{Kind: "insert", Row: 2, Cols: []int{1}, SQL: "INSERT INTO `app`.`t` (`name`) VALUES ('xyz')"}}, pv)

	res, err := svc.Apply(ctx, qid, 0)
	require.NoError(t, err)
	require.Equal(t, 1, res.Inserted)
	c := editConn(t, e)
	require.Equal(t, []string{"begin", "commit"}, c.TxLog)
	require.Equal(t, "INSERT INTO `app`.`t` (`name`) VALUES (?)", c.Execs[1].Stmt)
	require.Equal(t, []any{"xyz"}, c.Execs[1].Args)

	// La fila nueva pasa a ser real, releída por el ID que dio el servidor.
	w = view(t, e, qid)
	require.EqualValues(t, 3, w.Total)
	require.Equal(t, -1, w.NewFrom)
	require.Equal(t, results.Cell{V: "xyz"}, w.Rows[2][1])
	require.Equal(t, results.Cell{V: "servidor"}, w.Rows[2][2], "valores que dejó el servidor, no solo lo escrito")
	require.Len(t, *hist, 1)
	require.Contains(t, (*hist)[0].SQL, "INSERT INTO")
	info, _ = svc.GetEditInfo(ctx, qid, 0)
	require.Equal(t, 0, info.Pending)
}

func TestInsertIsNotOfferedWhenARequiredColumnIsMissing(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRowsEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"id", "note"}, []any{int64(1), nil}), nil
	}
	qid := e.execute(t, "t1", "SELECT id, note FROM t")
	e.terminal(t, qid)
	info, err := svc.GetEditInfo(ctx, qid, 0)
	require.NoError(t, err)
	require.True(t, info.Editable)
	require.False(t, info.CanInsert)
	require.Equal(t, []string{"name"}, info.InsertMissing)

	_, err = svc.AddRow(ctx, qid, 0)
	require.Equal(t, edit.InsertMissingRequired, detail(t, err))
	_, err = svc.DuplicateRows(ctx, qid, 0, []int{0})
	require.Equal(t, edit.InsertMissingRequired, detail(t, err))
}

func TestDuplicateRowsCopiesWhatYouSee(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRowsEnv(t)
	qid := load(t, e)

	// Una edición pendiente de la fila origen se copia (se copia lo que se ve).
	_, err := svc.Stage(ctx, qid, 0, 0, 1, edit.Value{Text: "z"})
	require.NoError(t, err)

	got, err := svc.DuplicateRows(ctx, qid, 0, []int{0, 1})
	require.NoError(t, err)
	require.Equal(t, []int{2, 3}, got.Rows)
	require.Equal(t, 2, got.Info.PendingInserts)

	pv, err := svc.Preview(ctx, qid, 0)
	require.NoError(t, err)
	var inserts []string
	for _, p := range pv {
		if p.Kind == "insert" {
			inserts = append(inserts, p.SQL)
		}
	}
	require.Equal(t, []string{
		"INSERT INTO `app`.`t` (`name`, `note`) VALUES ('z', NULL)", // fila 0: edición pendiente y NULL explícito; sin `id` autoincremental
		"INSERT INTO `app`.`t` (`name`, `note`) VALUES ('b', 'nota')",
	}, inserts)

	// Se puede copiar también una fila nueva, y se puede cambiar lo que se quiera de la copia.
	again, err := svc.DuplicateRows(ctx, qid, 0, []int{3})
	require.NoError(t, err)
	require.Equal(t, []int{4}, again.Rows)
	_, err = svc.Stage(ctx, qid, 0, 4, 2, edit.Value{Text: "cambiada"})
	require.NoError(t, err)
	w := view(t, e, qid)
	require.Equal(t, results.Cell{V: "cambiada", Edited: true}, w.Rows[4][2])
	require.Equal(t, results.Cell{V: "b", Edited: true}, w.Rows[4][1])
	require.Equal(t, results.Cell{T: results.TDefault, Edited: true}, w.Rows[4][0], "el autoincremental queda para el servidor")
}

func TestDuplicateLeavesANaturalKeyEmptyAndRequired(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRowsEnv(t, func(d *model.TableDetail) {
		d.Columns[0] = model.Column{Name: "id", DataType: "int", ColumnType: "int"} // clave natural: no autoincremental
	})
	qid := load(t, e)

	got, err := svc.DuplicateRows(ctx, qid, 0, []int{1})
	require.NoError(t, err)
	w := view(t, e, qid)
	require.Equal(t, results.Cell{T: results.TDefault, Edited: true}, w.Rows[got.Rows[0]][0], "copiar la clave tal cual chocaría siempre")
	require.Equal(t, results.Cell{V: "b", Edited: true}, w.Rows[got.Rows[0]][1])

	info, _ := svc.GetEditInfo(ctx, qid, 0)
	require.True(t, info.Columns[0].Required)
	_, err = svc.Apply(ctx, qid, 0)
	require.Equal(t, "required", detail(t, err))
	require.Contains(t, err.Error(), "id")
}

func TestMarkDeleteUnmarkAndRestrictions(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRowsEnv(t)
	qid := load(t, e)
	_, err := svc.Stage(ctx, qid, 0, 0, 1, edit.Value{Text: "z"})
	require.NoError(t, err)

	info, err := svc.MarkDelete(ctx, qid, 0, []int{0})
	require.NoError(t, err)
	require.Equal(t, 1, info.PendingDeletes)
	require.Equal(t, 1, info.Pending, "borrar una fila descarta sus ediciones de celda")
	w := view(t, e, qid)
	require.Equal(t, []int{0}, w.Deleted)
	require.Equal(t, results.Cell{V: "a"}, w.Rows[0][1], "se ve el valor original, no la edición descartada")

	_, err = svc.Stage(ctx, qid, 0, 0, 1, edit.Value{Text: "y"})
	require.Equal(t, edit.ReasonRowDeleted, detail(t, err), "no se edita una fila marcada para borrar")

	pv, err := svc.Preview(ctx, qid, 0)
	require.NoError(t, err)
	require.Equal(t, "DELETE FROM `app`.`t` WHERE `id` = 1 AND `name` <=> 'a' AND `note` <=> NULL LIMIT 1", pv[0].SQL)
	require.Equal(t, "delete", pv[0].Kind)

	info, err = svc.UnmarkDelete(ctx, qid, 0, []int{0})
	require.NoError(t, err)
	require.Equal(t, 0, info.PendingDeletes)
	require.Empty(t, view(t, e, qid).Deleted)

	// «Borrar» una fila nueva la descarta.
	add, _ := svc.AddRow(ctx, qid, 0)
	info, err = svc.MarkDelete(ctx, qid, 0, add.Rows)
	require.NoError(t, err)
	require.Equal(t, 0, info.PendingInserts)
	require.EqualValues(t, 2, view(t, e, qid).Total)
}

func TestApplyDeleteRemovesTheRowFromTheGrid(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newRowsEnv(t)
	qid := load(t, e)
	_, err := svc.MarkDelete(ctx, qid, 0, []int{0})
	require.NoError(t, err)

	res, err := svc.Apply(ctx, qid, 0)
	require.NoError(t, err)
	require.Equal(t, 1, res.Deleted)
	c := editConn(t, e)
	require.Equal(t, []string{"begin", "commit"}, c.TxLog)
	require.Equal(t, "DELETE FROM `app`.`t` WHERE `id` = ? AND `name` <=> ? AND `note` <=> ? LIMIT 1", c.Execs[1].Stmt)
	require.Equal(t, []any{int64(1), "a", nil}, c.Execs[1].Args)

	w := view(t, e, qid)
	require.EqualValues(t, 1, w.Total, "el total baja")
	require.Equal(t, results.Cell{V: "b"}, w.Rows[0][1], "las filas siguientes suben de índice")
	require.Empty(t, w.Deleted)
	require.Len(t, *hist, 1)
	require.Contains(t, (*hist)[0].SQL, "DELETE FROM")
}

func TestMixedBatchRunsUpdateDeleteInsertInOrderAndIsAtomic(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newRowsEnv(t)
	qid := load(t, e)
	_, err := svc.Stage(ctx, qid, 0, 0, 2, edit.Value{Text: "editada"})
	require.NoError(t, err)
	_, err = svc.MarkDelete(ctx, qid, 0, []int{1})
	require.NoError(t, err)
	add, err := svc.AddRow(ctx, qid, 0)
	require.NoError(t, err)
	_, err = svc.Stage(ctx, qid, 0, add.Rows[0], 1, edit.Value{Text: "nueva"})
	require.NoError(t, err)

	info, _ := svc.GetEditInfo(ctx, qid, 0)
	require.Equal(t, 3, info.Pending)

	t.Run("el orden es UPDATE, DELETE, INSERT", func(t *testing.T) {
		pv, err := svc.Preview(ctx, qid, 0)
		require.NoError(t, err)
		var kinds []string
		for _, p := range pv {
			kinds = append(kinds, p.Kind)
		}
		require.Equal(t, []string{"update", "delete", "insert"}, kinds)
	})

	t.Run("si el DELETE no coincide con la fila no se aplica nada, tampoco el INSERT", func(t *testing.T) {
		e.drv.OnExec = func(_ context.Context, stmt string, _ []any) (int64, error) {
			if strings.HasPrefix(stmt, "DELETE") {
				return 0, nil
			}
			return 1, nil
		}
		res, err := svc.Apply(ctx, qid, 0)
		require.NoError(t, err)
		require.NotNil(t, res.Conflict)
		require.Equal(t, 1, res.Conflict.Row)
		require.Equal(t, []string{"begin", "rollback"}, editConn(t, e).TxLog)
		require.Empty(t, *hist, "nada se aplicó")
		info, _ := svc.GetEditInfo(ctx, qid, 0)
		require.Equal(t, 3, info.Pending, "los tres cambios siguen pendientes")
	})
}

func TestInsertServerErrorNamesTheNewRow(t *testing.T) {
	ctx := context.Background()
	e, svc, hist := newRowsEnv(t)
	qid := load(t, e)
	add, _ := svc.AddRow(ctx, qid, 0)
	_, err := svc.Stage(ctx, qid, 0, add.Rows[0], 1, edit.Value{Text: "dup"})
	require.NoError(t, err)
	e.drv.OnExec = func(_ context.Context, stmt string, _ []any) (int64, error) {
		if strings.HasPrefix(stmt, "INSERT") {
			return 0, apperr.New(apperr.QueryFailed, "Duplicate entry 'dup' for key 'uq_name'")
		}
		return 1, nil
	}
	_, err = svc.Apply(ctx, qid, 0)
	require.Error(t, err)
	require.Contains(t, apperr.From(err).Message, "Fila nueva 1: Duplicate entry")
	require.Equal(t, []string{"begin", "rollback"}, editConn(t, e).TxLog)
	require.Len(t, *hist, 1)
	require.False(t, (*hist)[0].OK)
	info, _ := svc.GetEditInfo(ctx, qid, 0)
	require.Equal(t, 1, info.PendingInserts, "la fila nueva sigue ahí para corregirla")
}

func TestRowsOperationsAreRejectedOnAReadOnlyProfile(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRowsEnv(t)
	qid := load(t, e)
	profiles, err := e.svc.List(ctx)
	require.NoError(t, err)
	p := profiles[0]
	p.ReadOnly = true
	_, err = e.svc.Save(ctx, p, ConnectionSecrets{})
	require.NoError(t, err)

	_, err = svc.AddRow(ctx, qid, 0)
	require.Equal(t, edit.ReasonReadOnly, detail(t, err))
	_, err = svc.DuplicateRows(ctx, qid, 0, []int{0})
	require.Equal(t, edit.ReasonReadOnly, detail(t, err))
	_, err = svc.MarkDelete(ctx, qid, 0, []int{0})
	require.Equal(t, edit.ReasonReadOnly, detail(t, err))
}

func TestMaxPendingCountsRowsToo(t *testing.T) {
	ctx := context.Background()
	e, svc, _ := newRowsEnv(t)
	qid := load(t, e)
	rows := make([]int, MaxPending+1)
	_, err := svc.DuplicateRows(ctx, qid, 0, rows)
	require.Equal(t, "pending", detail(t, err))
}
