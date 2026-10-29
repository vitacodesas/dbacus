package paging

import (
	"database/sql"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/model"
)

func TestApproxBuild(t *testing.T) {
	a := Approx{Inner: "SELECT a.id, b.n FROM a JOIN b ON 1 -- fin", Column: "id", PageSize: 20}
	cases := []struct {
		d    Dir
		edge string
		want string
	}{
		{First, "", "SELECT * FROM (SELECT a.id, b.n FROM a JOIN b ON 1 -- fin\n) AS _p ORDER BY _p.`id` LIMIT 21"},
		{Next, "40", "SELECT * FROM (SELECT a.id, b.n FROM a JOIN b ON 1 -- fin\n) AS _p WHERE _p.`id` > 40 ORDER BY _p.`id` LIMIT 21"},
		{Prev, "-3", "SELECT * FROM (SELECT a.id, b.n FROM a JOIN b ON 1 -- fin\n) AS _p WHERE _p.`id` < -3 ORDER BY _p.`id` DESC LIMIT 21"},
		{Last, "", "SELECT * FROM (SELECT a.id, b.n FROM a JOIN b ON 1 -- fin\n) AS _p ORDER BY _p.`id` DESC LIMIT 21"},
	}
	for _, c := range cases {
		got, err := a.Build(c.d, c.edge, quote)
		require.NoError(t, err)
		require.Equal(t, c.want, got)
	}
	for _, bad := range []string{"1 OR 1=1", "1; DROP TABLE t", "", "0x10", "1e3"} {
		_, err := a.Build(Next, bad, quote)
		require.Error(t, err, bad)
	}
	_, err := Approx{Inner: "SELECT 1", PageSize: 5}.Build(First, "", quote)
	require.Error(t, err)
}

func TestCandidates(t *testing.T) {
	col := func(n, dt string, nullable bool) model.ColumnMeta {
		return model.ColumnMeta{Name: n, DataType: dt, Nullable: nullable}
	}
	got, reason := Candidates([]model.ColumnMeta{col("id", "BIGINT", false), col("n", "UNSIGNED INT", false), col("p", "DECIMAL", false), col("x", "INT", true), col("f", "DOUBLE", false), col("s", "VARCHAR", false)})
	require.Empty(t, reason)
	require.Equal(t, []string{"id", "n", "p"}, got)

	_, reason = Candidates([]model.ColumnMeta{col("x", "INT", true), col("s", "VARCHAR", false)})
	require.Equal(t, ReasonNoNumericColumn, reason)
	_, reason = Candidates([]model.ColumnMeta{col("id", "INT", false), col("ID", "INT", false)})
	require.Equal(t, ReasonDuplicateColumn, reason, "una tabla derivada no admite nombres repetidos")
}

// approxWalk recorre el resultado paginando por `column` con la regla de empate del servicio. Devuelve los ids vistos en
// orden de visita y el valor en el que se bloqueó (blocked=false si llegó al final).
func approxWalk(t *testing.T, db *sql.DB, a Approx, backward bool) (seen []int64, blockedAt string, blocked bool) {
	t.Helper()
	run := func(d Dir, edge string) (rows []row, probe string, more bool) {
		q, err := a.Build(d, edge, quote)
		require.NoError(t, err)
		got := fetch(t, db, q)
		page, more := Page(got, a.PageSize)
		if more {
			probe = got[a.PageSize].val(a.Column)
		}
		page = append([]row(nil), page...)
		if d.Backward() {
			Reverse(page)
		}
		return page, probe, more
	}
	last := First
	edgeAt := func(rows []row) string { return rows[len(rows)-1].val(a.Column) }
	if backward {
		last = Last
		edgeAt = func(rows []row) string { return rows[0].val(a.Column) }
	}
	cur, probe, more := run(last, "")
	for pages := 0; ; pages++ {
		require.Less(t, pages, 100_000, "el recorrido no termina")
		seen = append(seen, ids(cur)...)
		if !more {
			return seen, "", false
		}
		edge := edgeAt(cur)
		if TieAtEdge(edge, probe) {
			return seen, edge, true
		}
		d := Next
		if backward {
			d = Prev
		}
		cur, probe, more = run(d, edge)
		require.NotEmpty(t, cur)
	}
}

// Con la regla de empate nunca se omite una fila EN SILENCIO: o se recorre todo sin repetir ni saltar, o se bloquea y lo
// visto hasta entonces contiene todas las filas estrictamente anteriores al valor del bloqueo.
func TestApproxNeverSkipsSilently(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	completed, blockedRuns := 0, 0
	for iter := 0; iter < 300; iter++ {
		n := 1 + rng.Intn(60)
		db := newDB(t, n, int64(iter))
		a := Approx{Inner: "SELECT id, price, qty FROM t", Column: []string{"id", "price", "qty"}[rng.Intn(3)], PageSize: 1 + rng.Intn(12)}
		all := fetch(t, db, "SELECT id, price, qty FROM t ORDER BY "+a.Column+", id")
		for _, backward := range []bool{false, true} {
			seen, at, blocked := approxWalk(t, db, a, backward)
			seenSet := map[int64]bool{}
			for _, id := range seen {
				require.False(t, seenSet[id], "fila repetida %d (%s, tamaño %d)", id, a.Column, a.PageSize)
				seenSet[id] = true
			}
			if !blocked {
				completed++
				require.Len(t, seen, len(all), "no se bloqueó pero faltan filas (%s, tamaño %d, atrás=%v)", a.Column, a.PageSize, backward)
				continue
			}
			blockedRuns++
			for _, r := range all {
				v, _ := strconv.Atoi(r.val(a.Column))
				edge, _ := strconv.Atoi(at)
				// Hacia delante, lo anterior al valor del bloqueo; hacia atrás, lo posterior.
				if (!backward && v < edge) || (backward && v > edge) {
					require.True(t, seenSet[r.id], "se omitió en silencio la fila %d con %s=%d antes del bloqueo en %s", r.id, a.Column, v, at)
				}
			}
		}
	}
	require.Greater(t, completed, 20, "el test debe ejercitar recorridos completos")
	require.Greater(t, blockedRuns, 20, "el test debe ejercitar bloqueos por empate")
}

// Sin la salvaguarda, avanzar con empate en el borde SÍ omite filas: el test anterior depende de ella.
func TestApproxWithoutGuardWouldSkip(t *testing.T) {
	db := newDB(t, 40, 3)
	a := Approx{Inner: "SELECT id, price, qty FROM t", Column: "price", PageSize: 3}
	var all []int64
	for _, r := range fetch(t, db, "SELECT id, price, qty FROM t") {
		all = append(all, r.id)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	var seen []int64
	q, _ := a.Build(First, "", quote)
	got := fetch(t, db, q)
	page, more := Page(got, a.PageSize)
	for pages := 0; more && pages < 1000; pages++ {
		seen = append(seen, ids(page)...)
		q, _ = a.Build(Next, page[len(page)-1].val("price"), quote)
		got = fetch(t, db, q)
		page, more = Page(got, a.PageSize)
	}
	seen = append(seen, ids(page)...)
	require.Less(t, len(seen), len(all), fmt.Sprintf("con %d valores distintos y páginas de 3 sin guarda debían faltar filas", 5))
}
