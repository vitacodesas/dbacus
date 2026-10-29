package paging

import (
	"database/sql"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/vitacodesas/dbacus/internal/sqlsplit"
)

// quote imita Driver.QuoteIdent de MySQL (comillas invertidas); SQLite las acepta, así que el SQL generado se ejecuta tal cual.
func quote(n string) string { return "`" + strings.ReplaceAll(n, "`", "``") + "`" }

type row struct{ id, price, qty int64 }

func (r row) val(col string) string {
	switch col {
	case "id":
		return strconv.FormatInt(r.id, 10)
	case "price":
		return strconv.FormatInt(r.price, 10)
	}
	return strconv.FormatInt(r.qty, 10)
}

// newDB crea una tabla con muchos valores repetidos en price y qty: es donde un cursor mal hecho repite o pierde filas.
func newDB(t testing.TB, n int, seed int64) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, price INTEGER NOT NULL, qty INTEGER NOT NULL)")
	require.NoError(t, err)
	rng := rand.New(rand.NewSource(seed))
	for _, id := range rng.Perm(n) {
		_, err = db.Exec("INSERT INTO t VALUES (?, ?, ?)", id+1, rng.Intn(5), rng.Intn(4))
		require.NoError(t, err)
	}
	return db
}

func fetch(t testing.TB, db *sql.DB, q string) []row {
	t.Helper()
	rs, err := db.Query(q)
	require.NoError(t, err, q)
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		require.NoError(t, rs.Scan(&r.id, &r.price, &r.qty))
		out = append(out, r)
	}
	require.NoError(t, rs.Err())
	return out
}

func edgeOf(r row, order []Col) []string {
	e := make([]string, len(order))
	for i, c := range order {
		e[i] = r.val(c.Name)
	}
	return e
}

// oracle es el resultado completo y ordenado, sin paginar.
func oracle(t testing.TB, db *sql.DB, p Plan) []row {
	var ob []string
	for _, c := range p.Order {
		s := quote(c.Name)
		if c.Desc {
			s += " DESC"
		}
		ob = append(ob, s)
	}
	q := "SELECT " + p.List + " FROM " + p.From
	if p.Where != "" {
		q += " WHERE " + p.Where
	}
	return fetch(t, db, q+" ORDER BY "+strings.Join(ob, ", "))
}

// page ejecuta una página y la devuelve ya recortada, en orden de visualización, con el aviso de «hay más».
func page(t testing.TB, db *sql.DB, p Plan, d Dir, edge []string) (rows []row, more bool) {
	t.Helper()
	q, err := p.Build(d, edge, quote)
	require.NoError(t, err)
	got := fetch(t, db, q)
	rows, more = Page(got, p.PageSize)
	rows = append([]row(nil), rows...)
	if d.Backward() {
		Reverse(rows)
	}
	return rows, more
}

func ids(rs []row) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.id
	}
	return out
}

func walkForward(t testing.TB, db *sql.DB, p Plan) []row {
	t.Helper()
	var all []row
	cur, more := page(t, db, p, First, nil)
	all = append(all, cur...)
	for pages := 0; more; pages++ {
		require.Less(t, pages, 100_000, "el recorrido no termina")
		require.NotEmpty(t, cur)
		cur, more = page(t, db, p, Next, edgeOf(cur[len(cur)-1], p.Order))
		all = append(all, cur...)
	}
	return all
}

func walkBackward(t testing.TB, db *sql.DB, p Plan) []row {
	t.Helper()
	cur, more := page(t, db, p, Last, nil)
	all := append([]row(nil), cur...)
	for pages := 0; more; pages++ {
		require.Less(t, pages, 100_000, "el recorrido no termina")
		require.NotEmpty(t, cur)
		cur, more = page(t, db, p, Prev, edgeOf(cur[0], p.Order))
		all = append(append([]row(nil), cur...), all...)
	}
	return all
}

func plan(order []Col, where string, size int) Plan {
	return Plan{List: "id, price, qty", From: "t", Where: where, Order: order, PageSize: size}
}

func TestWalkingForwardAndBackwardReproducesTheWholeOrder(t *testing.T) {
	db := newDB(t, 200, 1)
	orders := map[string][]Col{
		"solo la clave":                        {{Name: "id"}},
		"clave descendente":                    {{Name: "id", Desc: true}},
		"precio desc y clave":                  {{Name: "price", Desc: true}, {Name: "id"}},
		"precio asc, cantidad desc y clave":    {{Name: "price"}, {Name: "qty", Desc: true}, {Name: "id"}},
		"direcciones mixtas con la clave desc": {{Name: "qty", Desc: true}, {Name: "price"}, {Name: "id", Desc: true}},
	}
	for name, order := range orders {
		for _, where := range []string{"", "price >= 1 AND qty <> 2"} {
			for _, size := range []int{1, 7, 50, 200, 500} {
				t.Run(fmt.Sprintf("%s/where=%q/n=%d", name, where, size), func(t *testing.T) {
					p := plan(order, where, size)
					want := oracle(t, db, p)
					require.Equal(t, ids(want), ids(walkForward(t, db, p)), "hacia delante")
					require.Equal(t, ids(want), ids(walkBackward(t, db, p)), "hacia atrás")
				})
			}
		}
	}
}

func TestRandomOrdersWithManyTiesNeverRepeatOrSkip(t *testing.T) {
	db := newDB(t, 150, 2)
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 200; i++ {
		var user []sqlsplit.OrderItem
		for _, c := range rng.Perm(2) {
			if rng.Intn(3) > 0 {
				user = append(user, sqlsplit.OrderItem{Column: []string{"price", "qty"}[c], Desc: rng.Intn(2) == 0})
			}
		}
		p := plan(EffectiveOrder(user, []string{"id"}), "", 1+rng.Intn(40))
		want := ids(oracle(t, db, p))
		require.Equal(t, want, ids(walkForward(t, db, p)), "iteración %d, orden %+v", i, p.Order)
		require.Equal(t, want, ids(walkBackward(t, db, p)), "iteración %d, orden %+v", i, p.Order)
	}
}

func TestLastPageHoldsTheLastRowsAndFirstPageTheFirst(t *testing.T) {
	db := newDB(t, 100, 4)
	p := plan([]Col{{Name: "price", Desc: true}, {Name: "id"}}, "", 8)
	all := oracle(t, db, p)

	last, more := page(t, db, p, Last, nil)
	require.True(t, more, "antes de la última hay más")
	require.Equal(t, ids(all[len(all)-8:]), ids(last))
	first, more := page(t, db, p, First, nil)
	require.True(t, more)
	require.Equal(t, ids(all[:8]), ids(first))
}

func TestGoingBackAfterGoingForwardReturnsTheSamePage(t *testing.T) {
	db := newDB(t, 120, 5)
	p := plan([]Col{{Name: "qty"}, {Name: "price", Desc: true}, {Name: "id"}}, "", 10)
	p1, _ := page(t, db, p, First, nil)
	p2, _ := page(t, db, p, Next, edgeOf(p1[len(p1)-1], p.Order))
	p3, _ := page(t, db, p, Next, edgeOf(p2[len(p2)-1], p.Order))
	back2, more := page(t, db, p, Prev, edgeOf(p3[0], p.Order))
	require.True(t, more)
	require.Equal(t, ids(p2), ids(back2))
	back1, more := page(t, db, p, Prev, edgeOf(back2[0], p.Order))
	require.False(t, more, "no hay nada antes de la primera página")
	require.Equal(t, ids(p1), ids(back1))
}

func TestExactPageSizeHasNoNextAndEmptyResultIsEmpty(t *testing.T) {
	db := newDB(t, 20, 6)
	exact := plan([]Col{{Name: "id"}}, "", 20)
	rows, more := page(t, db, exact, First, nil)
	require.Len(t, rows, 20)
	require.False(t, more, "exactamente N filas: no hay página siguiente")

	none := plan([]Col{{Name: "id"}}, "price > 100", 5)
	rows, more = page(t, db, none, First, nil)
	require.Empty(t, rows)
	require.False(t, more)
	rows, more = page(t, db, none, Last, nil)
	require.Empty(t, rows)
	require.False(t, more)
}

func TestWhereEndingInALineCommentDoesNotSwallowTheClosingParenthesis(t *testing.T) {
	db := newDB(t, 50, 7)
	p := plan([]Col{{Name: "id"}}, "price >= 1 -- solo con precio", 5)
	first, more := page(t, db, p, First, nil)
	require.True(t, more)
	next, _ := page(t, db, p, Next, edgeOf(first[len(first)-1], p.Order))
	require.NotEmpty(t, next)
	require.Greater(t, next[0].id, first[len(first)-1].id)
}

func TestBuildProducesTheExpectedSQL(t *testing.T) {
	p := Plan{List: "*", From: "`db`.`t`", Where: "a = 1", PageSize: 20, Order: []Col{{Name: "price", Desc: true}, {Name: "id"}}}

	first, err := p.Build(First, nil, quote)
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `db`.`t` WHERE (a = 1\n) ORDER BY `price` DESC, `id` LIMIT 21", first)

	next, err := p.Build(Next, []string{"9.50", "12"}, quote)
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `db`.`t` WHERE (a = 1\n) AND ((`price` < 9.50) OR (`price` = 9.50 AND `id` > 12)) ORDER BY `price` DESC, `id` LIMIT 21", next)

	prev, err := p.Build(Prev, []string{"9.50", "12"}, quote)
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `db`.`t` WHERE (a = 1\n) AND ((`price` > 9.50) OR (`price` = 9.50 AND `id` < 12)) ORDER BY `price`, `id` DESC LIMIT 21", prev)

	last, err := p.Build(Last, nil, quote)
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `db`.`t` WHERE (a = 1\n) ORDER BY `price`, `id` DESC LIMIT 21", last)

	noWhere := Plan{List: "id", From: "t", PageSize: 5, Order: []Col{{Name: "id"}}}
	q, err := noWhere.Build(Next, []string{"100"}, quote)
	require.NoError(t, err)
	require.Equal(t, "SELECT id FROM t WHERE ((`id` > 100)) ORDER BY `id` LIMIT 6", q)
}

func TestBuildKeepsHugeUnsignedAndDecimalLiteralsExact(t *testing.T) {
	p := Plan{List: "*", From: "t", PageSize: 3, Order: []Col{{Name: "amount", Desc: true}, {Name: "id"}}}
	// BIGINT UNSIGNED por encima de 2^63 y DECIMAL con muchos dígitos: nunca pasan por float.
	q, err := p.Build(Next, []string{"123456789012345.678901234", "18446744073709551615"}, quote)
	require.NoError(t, err)
	require.Contains(t, q, "`amount` < 123456789012345.678901234")
	require.Contains(t, q, "`id` > 18446744073709551615")
	q, err = p.Build(Next, []string{"-0.01", "-9223372036854775808"}, quote)
	require.NoError(t, err)
	require.Contains(t, q, "-9223372036854775808")
}

func TestBuildRejectsAnythingThatIsNotANumber(t *testing.T) {
	p := Plan{List: "*", From: "t", PageSize: 3, Order: []Col{{Name: "id"}}}
	for _, bad := range []string{"", "1; DROP TABLE t", "1 OR 1=1", "0x10", "1e5", "'1'", "NaN", "1.", ".5", " 1", "1 "} {
		_, err := p.Build(Next, []string{bad}, quote)
		require.Error(t, err, "valor %q", bad)
	}
	_, err := p.Build(Next, []string{"1", "2"}, quote)
	require.Error(t, err, "debe haber un valor por columna de orden")
	_, err = Plan{List: "*", From: "t", PageSize: 3}.Build(First, nil, quote)
	require.Error(t, err, "sin orden no se pagina")
	_, err = Plan{List: "*", From: "t", Order: []Col{{Name: "id"}}}.Build(First, nil, quote)
	require.Error(t, err, "sin tamaño de página")
}

func TestEffectiveOrderAddsTheKeyAsATieBreaker(t *testing.T) {
	require.Equal(t, []Col{{Name: "id"}}, EffectiveOrder(nil, []string{"id"}))
	require.Equal(t, []Col{{Name: "price", Desc: true}, {Name: "id"}}, EffectiveOrder([]sqlsplit.OrderItem{{Column: "price", Desc: true}}, []string{"id"}))
	// La clave ya está en el orden del usuario (sin distinguir mayúsculas): no se repite ni se cambia su dirección.
	require.Equal(t, []Col{{Name: "ID", Desc: true}}, EffectiveOrder([]sqlsplit.OrderItem{{Column: "ID", Desc: true}}, []string{"id"}))
	// Clave compuesta: solo se añade lo que falta, en el orden de la clave.
	require.Equal(t, []Col{{Name: "b"}, {Name: "a"}, {Name: "c"}}, EffectiveOrder([]sqlsplit.OrderItem{{Column: "b"}}, []string{"a", "b", "c"}))
}

func TestPageAndReverse(t *testing.T) {
	got, more := Page([]int{1, 2, 3, 4}, 3)
	require.Equal(t, []int{1, 2, 3}, got)
	require.True(t, more)
	got, more = Page([]int{1, 2, 3}, 3)
	require.Equal(t, []int{1, 2, 3}, got)
	require.False(t, more)
	r := []int{1, 2, 3, 4}
	Reverse(r)
	require.Equal(t, []int{4, 3, 2, 1}, r)
}

func BenchmarkBuildNext(b *testing.B) {
	p := Plan{List: "*", From: "t", Where: "a = 1", PageSize: 1000, Order: []Col{{Name: "a", Desc: true}, {Name: "b"}, {Name: "id"}}}
	edge := []string{"12345", "678", "9"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := p.Build(Next, edge, quote); err != nil {
			b.Fatal(err)
		}
	}
}
