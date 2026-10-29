package services

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/driver/fake"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/results"
)

// pagingEnv conecta el driver falso a SQLite: el SQL de cada página se ejecuta de verdad.
type pagingEnv struct {
	*queryEnv
	db      *sql.DB
	mu      sync.Mutex
	ran     []string // sentencias que llegaron al servidor, sin los EXPLAIN
	explain string   // lo que responde EXPLAIN en la columna Extra
}

func i64(name string) model.Column { return model.Column{Name: name, DataType: "bigint"} }

func newPagingEnv(t *testing.T, rows int, profileMut ...func(*model.ConnectionProfile)) *pagingEnv {
	t.Helper()
	qe := newQueryEnv(t, profileMut...)
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1) // el ATTACH en memoria es por conexión
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		"ATTACH DATABASE ':memory:' AS app",
		"CREATE TABLE app.items (id INTEGER PRIMARY KEY, price INTEGER NOT NULL, qty INTEGER NOT NULL, note TEXT)",
		"CREATE TABLE app.other (id INTEGER PRIMARY KEY)",
	} {
		_, err := db.Exec(q)
		require.NoError(t, err)
	}
	rng := rand.New(rand.NewSource(1))
	for _, id := range rng.Perm(rows) {
		_, err := db.Exec("INSERT INTO app.items VALUES (?, ?, ?, ?)", id+1, rng.Intn(5), rng.Intn(4), fmt.Sprintf("n%d", id+1))
		require.NoError(t, err)
	}
	_, err = db.Exec("INSERT INTO app.other VALUES (1)")
	require.NoError(t, err)

	qe.schema.Details = map[string]model.TableDetail{
		"app.items": {
			Table:   model.Table{Name: "items", Type: "table"},
			Columns: []model.Column{i64("id"), i64("price"), i64("qty"), {Name: "note", DataType: "varchar"}},
			Indexes: []model.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true}},
		},
	}
	pe := &pagingEnv{queryEnv: qe, db: db}
	qe.drv.OnRun = pe.run
	return pe
}

func (pe *pagingEnv) run(_ context.Context, stmt string) (driver.RunResult, error) {
	if strings.HasPrefix(strings.ToUpper(stmt), "EXPLAIN") {
		pe.mu.Lock()
		defer pe.mu.Unlock()
		return fake.Rows([]string{"id", "Extra"}, []any{int64(1), pe.explain}), nil
	}
	pe.mu.Lock()
	pe.ran = append(pe.ran, stmt)
	pe.mu.Unlock()
	rs, err := pe.db.Query(stmt)
	if err != nil {
		return nil, apperr.Wrap(apperr.QueryFailed, err.Error(), err)
	}
	defer rs.Close()
	names, _ := rs.Columns()
	res := &fake.Result{}
	for _, n := range names {
		res.Cols = append(res.Cols, model.ColumnMeta{Name: n, DataType: "bigint"})
	}
	for rs.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		res.Rows = append(res.Rows, vals)
	}
	return res, rs.Err()
}

func (pe *pagingEnv) lastRan() string {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	return pe.ran[len(pe.ran)-1]
}

func (pe *pagingEnv) ranCount() int {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	return len(pe.ran)
}

// exec ejecuta una sentencia paginada y devuelve el id de la ejecución y su primer resultado.
func (pe *pagingEnv) exec(t *testing.T, tab, sqlText string, limit int) (string, StatementResult) {
	t.Helper()
	qid := pe.execute(t, tab, sqlText, func(r *ExecuteRequest) { r.AutoLimit = limit })
	done, failed := pe.terminal(t, qid)
	require.Nil(t, failed)
	return qid, done.Results[0]
}

// ids lee la columna `id` (la primera) de la página que hay ahora en el buffer.
func (pe *pagingEnv) ids(t *testing.T, qid string, stmt int) []int64 {
	t.Helper()
	w, err := pe.q.GetRows(qid, stmt, 0, 1000)
	require.NoError(t, err)
	out := make([]int64, len(w.Rows))
	for i, r := range w.Rows {
		out[i] = r[0].V.(int64)
	}
	return out
}

func (pe *pagingEnv) page(t *testing.T, qid string, stmt int, dir string) StatementResult {
	t.Helper()
	r, err := pe.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: stmt, Direction: dir})
	require.NoError(t, err)
	return r
}

func seq(from, to int64) []int64 {
	var out []int64
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func TestFirstPageIsKeysetPagedAndReportsMore(t *testing.T) {
	pe := newPagingEnv(t, 95)
	qid, r := pe.exec(t, "tab1", "SELECT * FROM items", 10)

	require.NotNil(t, r.Paging)
	require.Equal(t, "keyset", r.Paging.Mode)
	require.Equal(t, 10, r.Paging.PageSize)
	require.True(t, r.Paging.HasNext)
	require.False(t, r.Paging.HasPrev)
	require.Equal(t, 1, r.Paging.Page)
	require.Equal(t, []string{"id"}, r.Paging.KeyColumns)
	require.EqualValues(t, 10, r.RowCount)
	require.False(t, r.Truncated)
	require.Zero(t, r.LimitApplied, "con paginación por cursor no se inyecta LIMIT")
	require.Equal(t, "SELECT * FROM items", r.SQL, "se informa la sentencia original")
	require.Equal(t, seq(1, 10), pe.ids(t, qid, 0))
	require.Equal(t, "SELECT * FROM `app`.`items` ORDER BY `id` LIMIT 11", pe.lastRan(), "tabla calificada con su base y una fila de sondeo")
}

func TestWalkingForwardThenBackwardVisitsEveryRowOnce(t *testing.T) {
	pe := newPagingEnv(t, 95)
	qid, r := pe.exec(t, "tab1", "SELECT * FROM items", 10)

	var all []int64
	all = append(all, pe.ids(t, qid, 0)...)
	pages := 1
	for r.Paging.HasNext {
		r = pe.page(t, qid, 0, "next")
		pages++
		require.Equal(t, pages, r.Paging.Page)
		require.True(t, r.Paging.HasPrev)
		all = append(all, pe.ids(t, qid, 0)...)
	}
	require.Equal(t, seq(1, 95), all, "ni repetidas ni saltadas")
	require.Equal(t, 10, pages)
	require.EqualValues(t, 5, r.RowCount, "la última página trae el resto")
	require.False(t, r.Paging.HasNext)

	// De vuelta hasta el principio.
	var back []int64
	back = append(back, pe.ids(t, qid, 0)...)
	for r.Paging.HasPrev {
		r = pe.page(t, qid, 0, "prev")
		back = append(append([]int64(nil), pe.ids(t, qid, 0)...), back...)
		require.True(t, r.Paging.HasNext)
	}
	require.Equal(t, seq(1, 95), back)
	require.Equal(t, 1, r.Paging.Page)
	require.Equal(t, seq(1, 10), pe.ids(t, qid, 0))
}

func TestLastAndFirstJumpDirectly(t *testing.T) {
	pe := newPagingEnv(t, 95)
	qid, _ := pe.exec(t, "tab1", "SELECT * FROM items", 10)

	r := pe.page(t, qid, 0, "last")
	require.Equal(t, seq(86, 95), pe.ids(t, qid, 0), "las últimas 10 en orden ascendente")
	require.False(t, r.Paging.HasNext)
	require.True(t, r.Paging.HasPrev)
	require.Zero(t, r.Paging.Page, "tras «Última» el número de página no se conoce")
	require.Contains(t, pe.lastRan(), "ORDER BY `id` DESC LIMIT 11")

	r = pe.page(t, qid, 0, "prev")
	require.Equal(t, seq(76, 85), pe.ids(t, qid, 0))
	require.Zero(t, r.Paging.Page, "sigue siendo desconocida")

	r = pe.page(t, qid, 0, "first")
	require.Equal(t, seq(1, 10), pe.ids(t, qid, 0))
	require.Equal(t, 1, r.Paging.Page)
	require.False(t, r.Paging.HasPrev)
}

func TestLastPageOfATableThatFitsInOnePage(t *testing.T) {
	pe := newPagingEnv(t, 7)
	qid, r := pe.exec(t, "tab1", "SELECT * FROM items", 10)
	require.False(t, r.Paging.HasNext, "caben todas")
	r = pe.page(t, qid, 0, "last")
	require.Equal(t, seq(1, 7), pe.ids(t, qid, 0))
	require.False(t, r.Paging.HasPrev)
	require.Equal(t, 1, r.Paging.Page, "si no hay nada antes, es la primera")
}

func TestUserOrderByWithTiesIsRespectedAndKeyBreaksTies(t *testing.T) {
	pe := newPagingEnv(t, 80)
	qid, r := pe.exec(t, "tab1", "SELECT * FROM items WHERE qty >= 1 ORDER BY price DESC", 7)
	require.Equal(t, []string{"price", "id"}, r.Paging.KeyColumns)

	var got []int64
	got = append(got, pe.ids(t, qid, 0)...)
	for r.Paging.HasNext {
		r = pe.page(t, qid, 0, "next")
		got = append(got, pe.ids(t, qid, 0)...)
	}
	var want []int64
	rs, err := pe.db.Query("SELECT id FROM app.items WHERE qty >= 1 ORDER BY price DESC, id DESC")
	require.NoError(t, err)
	for rs.Next() {
		var id int64
		require.NoError(t, rs.Scan(&id))
		want = append(want, id)
	}
	require.NoError(t, rs.Close())
	require.Equal(t, want, got, "el recorrido paginado reproduce el orden completo, empates incluidos")
}

func TestPagesAreNeverRecordedInTheHistory(t *testing.T) {
	pe := newPagingEnv(t, 40)
	qid, _ := pe.exec(t, "tab1", "SELECT * FROM items", 10)
	h := <-pe.history
	require.Equal(t, "SELECT * FROM items", h.SQL, "el historial guarda lo que escribió el usuario")
	require.EqualValues(t, 10, h.RowCount, "con las filas de la primera página")
	require.True(t, h.OK)

	pe.page(t, qid, 0, "next")
	pe.page(t, qid, 0, "last")
	pe.page(t, qid, 0, "prev")
	select {
	case extra := <-pe.history:
		t.Fatalf("una página entró en el historial: %q", extra.SQL)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestMemoryStaysBoundedToOnePage(t *testing.T) {
	pe := newPagingEnv(t, 200)
	qid, _ := pe.exec(t, "tab1", "SELECT * FROM items", 10)
	one := pe.q.d.Results.Total()
	require.Positive(t, one)
	for _, dir := range []string{"next", "next", "last", "prev", "first", "next", "next", "next"} {
		pe.page(t, qid, 0, dir)
		// El texto de `note` mide distinto en cada página; lo que no puede pasar es acumular páginas.
		total := pe.q.d.Results.Total()
		require.InDelta(t, float64(one), float64(total), float64(one)*0.1, "tras %q la memoria es la de una página, no la suma de varias", dir)
	}
	pe.q.ReleaseResult(qid)
	require.Zero(t, pe.q.d.Results.Total())
}

func TestNextBeyondTheEndKeepsTheCurrentPage(t *testing.T) {
	pe := newPagingEnv(t, 25)
	qid, _ := pe.exec(t, "tab1", "SELECT * FROM items", 10)
	pe.page(t, qid, 0, "last")
	before := pe.ranCount()
	r := pe.page(t, qid, 0, "next")
	require.Equal(t, seq(16, 25), pe.ids(t, qid, 0), "se queda donde estaba")
	require.False(t, r.Paging.HasNext)
	require.Equal(t, before, pe.ranCount(), "ni siquiera consulta al servidor")
}

func TestRowsDeletedAfterwardsMakeNextStayPut(t *testing.T) {
	pe := newPagingEnv(t, 25)
	qid, r := pe.exec(t, "tab1", "SELECT * FROM items", 10)
	require.True(t, r.Paging.HasNext)
	_, err := pe.db.Exec("DELETE FROM app.items WHERE id > 10") // desaparece todo lo que venía detrás
	require.NoError(t, err)

	r = pe.page(t, qid, 0, "next")
	require.Equal(t, seq(1, 10), pe.ids(t, qid, 0), "no hay más filas: se queda la página actual")
	require.False(t, r.Paging.HasNext, "y ya no se ofrece «siguiente»")
}

func TestRowsInsertedAheadAppearAndNothingIsRepeated(t *testing.T) {
	pe := newPagingEnv(t, 25)
	qid, _ := pe.exec(t, "tab1", "SELECT * FROM items", 10)
	pe.page(t, qid, 0, "next") // 11..20
	_, err := pe.db.Exec("INSERT INTO app.items VALUES (1000, 1, 1, 'x'), (0, 1, 1, 'y')")
	require.NoError(t, err)
	pe.page(t, qid, 0, "next")
	got := pe.ids(t, qid, 0)
	require.Equal(t, []int64{21, 22, 23, 24, 25, 1000}, got, "lo nuevo por delante aparece; lo de detrás (id 0) no se repite ni salta nada")
}

func TestPendingEditsBlockPaging(t *testing.T) {
	pe := newPagingEnv(t, 40)
	qid, _ := pe.exec(t, "tab1", "SELECT * FROM items", 10)
	run, err := pe.q.d.Results.Get(qid)
	require.NoError(t, err)
	buf, err := run.Buffer(0)
	require.NoError(t, err)
	require.NoError(t, buf.Stage(0, 1, results.Cell{V: int64(99)}, nil))

	_, err = pe.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: 0, Direction: "next"})
	require.Equal(t, apperr.Validation, apperr.From(err).Code)
	require.Equal(t, "pending", apperr.From(err).Detail)
	require.Equal(t, seq(1, 10), pe.ids(t, qid, 0), "no se perdió nada")

	buf.ClearPending()
	pe.page(t, qid, 0, "next") // la interfaz descarta y entonces sí se pasa de página
}

func TestPageRejectsBadRequests(t *testing.T) {
	pe := newPagingEnv(t, 40)
	qid, _ := pe.exec(t, "tab1", "SELECT * FROM items", 10)

	_, err := pe.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: 0, Direction: "sideways"})
	require.Equal(t, "direction", apperr.From(err).Detail)
	_, err = pe.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: 7, Direction: "next"})
	require.Equal(t, apperr.NotFound, apperr.From(err).Code)
	_, err = pe.q.Page(context.Background(), PageRequest{QueryID: "no-existe", StmtIndex: 0, Direction: "next"})
	require.Equal(t, apperr.ResultExpired, apperr.From(err).Code)

	pe.q.ReleaseResult(qid)
	_, err = pe.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: 0, Direction: "next"})
	require.Equal(t, apperr.ResultExpired, apperr.From(err).Code, "si el buffer se liberó hay que volver a ejecutar")
}

func TestUnpagedStatementsExplainWhyAndKeepTheAutomaticLimit(t *testing.T) {
	pe := newPagingEnv(t, 30)
	cases := []struct{ sql, reason string }{
		{"SELECT * FROM app.items JOIN app.other ON items.id = other.id", "multi_table"},
		{"SELECT price FROM items GROUP BY price", "grouped"},
		{"SELECT * FROM items WHERE id < 5 UNION SELECT * FROM items WHERE id > 25", "compound"},
		{"SELECT * FROM items ORDER BY note", "key_type"},
		{"SELECT note FROM items", "key_not_selected"},
		{"SELECT * FROM items LIMIT 3 OFFSET 1", "user_limit"},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			qid, r := pe.exec(t, "tab-"+c.reason, c.sql, 10)
			require.NotNil(t, r.Paging)
			require.Equal(t, "none", r.Paging.Mode)
			require.Equal(t, c.reason, r.Paging.Reason)
			require.Equal(t, 10, r.Paging.PageSize)
			_, err := pe.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: 0, Direction: "next"})
			// Sin columna elegida solo sirve en las paginadas; si se ofrece el modo aproximado, pide la columna.
			require.Contains(t, []string{"statement", "column"}, apperr.From(err).Detail)
		})
	}

	// Los SELECT simples sin clave siguen recibiendo el LIMIT automático de siempre.
	_, r := pe.exec(t, "tab-limit", "SELECT note FROM items", 10)
	require.Equal(t, 10, r.LimitApplied)
	require.Equal(t, "SELECT note FROM items LIMIT 10", pe.lastRan())
}

func TestNoPagingWithoutALimitAndNothingForNonSelects(t *testing.T) {
	pe := newPagingEnv(t, 30)
	_, r := pe.exec(t, "tab1", "SELECT * FROM items", 0)
	require.Nil(t, r.Paging, "«Sin límite» no pagina")
	require.Zero(t, r.LimitApplied)

	qid := pe.execute(t, "tab2", "INSERT INTO app.other VALUES (5)", func(r *ExecuteRequest) { r.AutoLimit = 10 })
	done, _ := pe.terminal(t, qid)
	require.Equal(t, "exec", done.Results[0].Kind)
	require.Nil(t, done.Results[0].Paging)
}

func TestPageSizeIsCappedByTheMaxRowsSetting(t *testing.T) {
	pe := newPagingEnv(t, 60)
	qid := pe.execute(t, "tab1", "SELECT * FROM items", func(r *ExecuteRequest) { r.AutoLimit = 50; r.MaxRows = 20 })
	done, _ := pe.terminal(t, qid)
	require.Equal(t, 20, done.Results[0].Paging.PageSize)
	require.EqualValues(t, 20, done.Results[0].RowCount)
}

func TestFilesortWarningComesFromExplainAndSurvivesPaging(t *testing.T) {
	pe := newPagingEnv(t, 40)
	_, r := pe.exec(t, "tab1", "SELECT * FROM items", 10)
	require.Empty(t, r.Paging.Warning)

	pe.explain = "Using where; Using filesort"
	qid, r := pe.exec(t, "tab2", "SELECT * FROM items", 10)
	require.Equal(t, "no_index", r.Paging.Warning)
	r = pe.page(t, qid, 0, "next")
	require.Equal(t, "no_index", r.Paging.Warning, "el aviso acompaña a todas las páginas")
}

func TestEstimatedRowsOnlyWithoutWhere(t *testing.T) {
	pe := newPagingEnv(t, 40)
	est := int64(52340)
	d := pe.schema.Details["app.items"]
	d.RowEstimate = &est
	pe.schema.Details["app.items"] = d

	_, r := pe.exec(t, "tab1", "SELECT * FROM items", 10)
	require.EqualValues(t, 52340, r.Paging.Estimated)
	_, r = pe.exec(t, "tab2", "SELECT * FROM items WHERE qty > 1", 10)
	require.Zero(t, r.Paging.Estimated, "con WHERE la estimación de la tabla no vale")
}

func TestEachStatementOfAScriptIsPagedIndependently(t *testing.T) {
	pe := newPagingEnv(t, 50)
	qid := pe.execute(t, "tab1", "SELECT * FROM items; SELECT * FROM items WHERE id > 30 ORDER BY id DESC", func(r *ExecuteRequest) { r.AutoLimit = 10 })
	done, _ := pe.terminal(t, qid)
	require.Equal(t, "keyset", done.Results[0].Paging.Mode)
	require.Equal(t, "keyset", done.Results[1].Paging.Mode)
	require.Equal(t, seq(1, 10), pe.ids(t, qid, 0))
	require.Equal(t, []int64{50, 49, 48, 47, 46, 45, 44, 43, 42, 41}, pe.ids(t, qid, 1))

	pe.page(t, qid, 1, "next")
	require.Equal(t, []int64{40, 39, 38, 37, 36, 35, 34, 33, 32, 31}, pe.ids(t, qid, 1))
	require.Equal(t, seq(1, 10), pe.ids(t, qid, 0), "la otra sentencia no se toca")
}

func TestCancelAbortsAPageAndKeepsTheCurrentOne(t *testing.T) {
	pe := newPagingEnv(t, 60)
	qid, _ := pe.exec(t, "tab1", "SELECT * FROM items", 10)

	release := make(chan struct{})
	started := make(chan struct{})
	pe.drv.OnKill = func(string) { close(release) }
	pe.drv.OnRun = func(ctx context.Context, stmt string) (driver.RunResult, error) {
		if strings.HasPrefix(stmt, "EXPLAIN") {
			return fake.Rows([]string{"Extra"}), nil
		}
		close(started)
		select {
		case <-release:
			return nil, context.Canceled
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	errc := make(chan error, 1)
	go func() {
		_, err := pe.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: 0, Direction: "next"})
		errc <- err
	}()
	<-started

	// Mientras la página corre, la pestaña está ocupada.
	_, err := pe.q.Execute(context.Background(), ExecuteRequest{SessionID: pe.sid, TabID: "tab1", SQL: "SELECT 1"})
	require.Equal(t, "busy", apperr.From(err).Detail)
	_, err = pe.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: 0, Direction: "last"})
	require.Equal(t, "busy", apperr.From(err).Detail)

	require.NoError(t, pe.q.Cancel(qid))
	select {
	case err := <-errc:
		require.Equal(t, apperr.QueryCanceled, apperr.From(err).Code)
	case <-time.After(5 * time.Second):
		t.Fatal("la página no se canceló")
	}
	require.Equal(t, seq(1, 10), pe.ids(t, qid, 0), "la página anterior sigue intacta")

	// Terminada la cancelación la pestaña vuelve a quedar libre.
	pe.drv.OnRun = pe.run
	r := pe.page(t, qid, 0, "next")
	require.Equal(t, seq(11, 20), pe.ids(t, qid, 0))
	require.True(t, r.Paging.HasPrev)
}

func TestSourceStaysTheOriginalStatementSoEditingSeesIt(t *testing.T) {
	pe := newPagingEnv(t, 40)
	qid, _ := pe.exec(t, "tab1", "SELECT * FROM items WHERE qty > 0", 10)
	pe.page(t, qid, 0, "next")
	run, _ := pe.q.d.Results.Get(qid)
	buf, _ := run.Buffer(0)
	src, db := buf.Source()
	require.Equal(t, "SELECT * FROM items WHERE qty > 0", src, "edit.Analyze no debe ver el SQL reescrito de la página")
	require.Equal(t, "app", db)
}

func TestCellLiteralKeepsHugeAndDecimalValuesExact(t *testing.T) {
	v, ok := cellLiteral(results.Cell{V: int64(42)})
	require.True(t, ok)
	require.Equal(t, "42", v)
	v, ok = cellLiteral(results.Cell{V: "18446744073709551615", T: results.TBigint})
	require.True(t, ok)
	require.Equal(t, "18446744073709551615", v)
	v, ok = cellLiteral(results.Cell{V: "12345.678900", T: results.TDecimal})
	require.True(t, ok)
	require.Equal(t, "12345.678900", v)
	for _, bad := range []results.Cell{{}, {V: 1.5}, {V: "abc"}, {V: "1; DROP TABLE t"}, {V: true}} {
		_, ok := cellLiteral(bad)
		require.False(t, ok, "%v", bad.V)
	}
}
