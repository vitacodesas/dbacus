package services

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/results"
)

// Un JOIN sobre items (30 filas) con las columnas renombradas: sin nombres repetidos, para que la tabla derivada valga.
const joinSQL = "SELECT i.id AS id, i.price AS price FROM app.items i JOIN app.other o ON 1"

func (pe *pagingEnv) pageBy(t *testing.T, qid, dir, column string) (StatementResult, error) {
	t.Helper()
	return pe.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: 0, Direction: dir, Column: column})
}

func TestApproxIsOfferedWhenAJoinHitsTheLimit(t *testing.T) {
	pe := newPagingEnv(t, 30)
	_, r := pe.exec(t, "tab1", joinSQL, 10)
	require.Equal(t, "none", r.Paging.Mode)
	require.Equal(t, "not_simple", r.Paging.Reason, "el alias de tabla corta el análisis de una sola tabla")
	require.Equal(t, []string{"id", "price"}, r.Paging.Candidates)
	require.Equal(t, 10, r.LimitApplied, "mientras no se elige columna se carga con el LIMIT de siempre")

	// Si el resultado cabe, no hay nada que paginar: no se ofrece.
	_, r = pe.exec(t, "tab2", joinSQL, 100)
	require.Empty(t, r.Paging.Candidates)
}

func TestApproxExplainsWhyItIsNotOffered(t *testing.T) {
	pe := newPagingEnv(t, 30)
	_, r := pe.exec(t, "t1", "SELECT * FROM app.items JOIN app.other ON 1", 10)
	require.NotNil(t, r.Paging)
	require.Equal(t, "duplicate_columns", r.Paging.Reason, "dos `id` no caben en una tabla derivada")
	require.Empty(t, r.Paging.Candidates)

	_, r = pe.exec(t, "t2", joinSQL+" LIMIT 20", 10)
	require.Equal(t, "user_limit", r.Paging.Reason)
	require.Empty(t, r.Paging.Candidates)
}

func TestApproxWalksTheWholeResultByAUniqueColumn(t *testing.T) {
	pe := newPagingEnv(t, 30)
	qid, _ := pe.exec(t, "tab1", joinSQL, 10)

	r, err := pe.pageBy(t, qid, "first", "id")
	require.NoError(t, err)
	require.Equal(t, "approx", r.Paging.Mode)
	require.Equal(t, "id", r.Paging.Column)
	require.Equal(t, []string{"id", "price"}, r.Paging.Candidates, "se puede cambiar de columna desde el modo aproximado")
	require.True(t, r.Paging.HasNext)
	require.False(t, r.Paging.HasPrev)
	require.Equal(t, 1, r.Paging.Page)
	require.Zero(t, r.LimitApplied)
	require.Equal(t, seq(1, 10), pe.ids(t, qid, 0))

	var all []int64
	all = append(all, pe.ids(t, qid, 0)...)
	for r.Paging.HasNext {
		r = pe.page(t, qid, 0, "next")
		all = append(all, pe.ids(t, qid, 0)...)
	}
	require.Equal(t, seq(1, 30), all, "ni se repite ni se salta ninguna fila")
	require.Equal(t, 3, r.Paging.Page)

	r = pe.page(t, qid, 0, "prev")
	require.Equal(t, seq(11, 20), pe.ids(t, qid, 0))
	require.True(t, r.Paging.HasPrev)
	r = pe.page(t, qid, 0, "last")
	require.Equal(t, seq(21, 30), pe.ids(t, qid, 0))
	require.Zero(t, r.Paging.Page, "tras «Última» el número no se conoce")
	r = pe.page(t, qid, 0, "first")
	require.Equal(t, seq(1, 10), pe.ids(t, qid, 0))
	require.False(t, r.Paging.HasPrev)
}

func TestApproxTieAtTheEdgeBlocksAdvancingInsteadOfSkippingRows(t *testing.T) {
	pe := newPagingEnv(t, 30) // price vale 0..4: muchísimos empates
	size := tieSize(t, pe)
	qid, _ := pe.exec(t, "tab1", joinSQL, size)
	r, err := pe.pageBy(t, qid, "first", "price")
	require.NoError(t, err)
	require.True(t, r.Paging.HasNext)
	require.True(t, r.Paging.TieNext, "el valor de la última fila y el de la de sondeo coinciden")
	require.Equal(t, "tie_at_edge", r.Paging.Warning)

	_, err = pe.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: 0, Direction: "next"})
	require.Equal(t, apperr.Validation, apperr.From(err).Code)
	require.Equal(t, "tie_at_edge", apperr.From(err).Detail)

	// Cambiar de columna sale del bloqueo y vuelve a la primera página.
	r, err = pe.pageBy(t, qid, "first", "id")
	require.NoError(t, err)
	require.False(t, r.Paging.TieNext)
	require.Empty(t, r.Paging.Warning)
	pe.page(t, qid, 0, "next")
	require.Equal(t, seq(int64(size)+1, int64(2*size)), pe.ids(t, qid, 0))
}

// tieSize es un tamaño de página para el que la fila N y la N+1 ordenadas por price valen lo mismo.
func tieSize(t *testing.T, pe *pagingEnv) int {
	t.Helper()
	rows, err := pe.db.Query("SELECT price FROM app.items ORDER BY price")
	require.NoError(t, err)
	defer rows.Close()
	var prices []int64
	for rows.Next() {
		var p int64
		require.NoError(t, rows.Scan(&p))
		prices = append(prices, p)
	}
	for n := 2; n < len(prices)-1; n++ {
		if prices[n-1] == prices[n] {
			return n
		}
	}
	t.Fatal("los datos no tienen empates")
	return 0
}

// Por una columna con empates, o se llega al final sin perder nada, o se bloquea y todo lo anterior al valor del bloqueo se vio.
func TestApproxNeverLosesRowsSilentlyThroughTheService(t *testing.T) {
	for _, size := range []int{1, 2, 3, 5, 7, 12, 29, 30} {
		t.Run(fmt.Sprintf("tamaño_%d", size), func(t *testing.T) { neverLoses(t, size) })
	}
}

func neverLoses(t *testing.T, size int) {
	{
		pe := newPagingEnv(t, 30)
		qid, _ := pe.exec(t, "tab1", joinSQL, size)
		r, err := pe.pageBy(t, qid, "first", "price")
		require.NoError(t, err)
		seen := append([]int64(nil), pe.ids(t, qid, 0)...)
		for guard := 0; r.Paging.HasNext && !r.Paging.TieNext; guard++ {
			require.Less(t, guard, 1000)
			r = pe.page(t, qid, 0, "next")
			seen = append(seen, pe.ids(t, qid, 0)...)
		}
		sort.Slice(seen, func(i, j int) bool { return seen[i] < seen[j] })
		for i := 1; i < len(seen); i++ {
			require.NotEqual(t, seen[i-1], seen[i], "fila repetida (tamaño %d)", size)
		}
		if !r.Paging.HasNext {
			require.Len(t, seen, 30, "llegó al final y faltan filas (tamaño %d)", size)
			return
		}
		// Bloqueado: toda fila con price estrictamente menor que el del borde ya se vio.
		w, err := pe.q.GetRows(qid, 0, 0, 1000)
		require.NoError(t, err)
		edge := w.Rows[len(w.Rows)-1][1].V.(int64)
		got := map[int64]bool{}
		for _, id := range seen {
			got[id] = true
		}
		rows, err := pe.db.Query("SELECT id FROM app.items WHERE price < ?", edge)
		require.NoError(t, err)
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			require.True(t, got[id], "se omitió en silencio la fila %d (tamaño %d)", id, size)
		}
		require.NoError(t, rows.Close())
	}
}

func TestApproxRejectsUnknownColumnsAndNeedsOneToStart(t *testing.T) {
	pe := newPagingEnv(t, 30)
	qid, _ := pe.exec(t, "tab1", joinSQL, 10)
	for _, bad := range []string{"nope", "id; DROP TABLE x", "o.id", "`id`"} {
		_, err := pe.pageBy(t, qid, "first", bad)
		require.Equal(t, apperr.Validation, apperr.From(err).Code, bad)
		require.Equal(t, "column", apperr.From(err).Detail, bad)
	}
	_, err := pe.pageBy(t, qid, "next", "")
	require.Equal(t, "column", apperr.From(err).Detail, "sin columna elegida no hay por dónde avanzar")

	// Una sentencia paginada por clave no admite elegir columna.
	k, _ := pe.exec(t, "tab2", "SELECT * FROM items", 10)
	_, err = pe.pageBy(t, k, "first", "price")
	require.Equal(t, "column", apperr.From(err).Detail)
}

func TestApproxPagesUseTheOriginalStatementAndAreNotHistory(t *testing.T) {
	pe := newPagingEnv(t, 30)
	qid, _ := pe.exec(t, "tab1", joinSQL+" -- nota", 10)
	_, err := pe.pageBy(t, qid, "first", "id")
	require.NoError(t, err)
	require.Contains(t, pe.lastRan(), "SELECT * FROM (SELECT i.id AS id, i.price AS price FROM app.items i JOIN app.other o ON 1 -- nota\n) AS _p ORDER BY _p.`id` LIMIT 11")
	pe.page(t, qid, 0, "next")
	require.Contains(t, pe.lastRan(), "WHERE _p.`id` > 10 ORDER BY _p.`id` LIMIT 11")

	run, err := pe.q.d.Results.Get(qid)
	require.NoError(t, err)
	buf, err := run.Buffer(0)
	require.NoError(t, err)
	src, _ := buf.Source()
	require.Equal(t, joinSQL+" -- nota", src, "el export vuelve a ejecutar la sentencia original")
}

func TestApproxRefusesWithPendingEdits(t *testing.T) {
	pe := newPagingEnv(t, 30)
	qid, _ := pe.exec(t, "tab1", joinSQL, 10)
	_, err := pe.pageBy(t, qid, "first", "id")
	require.NoError(t, err)
	run, err := pe.q.d.Results.Get(qid)
	require.NoError(t, err)
	buf, err := run.Buffer(0)
	require.NoError(t, err)
	require.NoError(t, buf.Stage(0, 1, results.Cell{V: int64(99)}, nil))
	_, err = pe.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: 0, Direction: "next"})
	require.Equal(t, "pending", apperr.From(err).Detail)
}
