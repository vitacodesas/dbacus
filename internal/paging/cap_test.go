package paging

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWindowAndLastStart(t *testing.T) {
	p := Plan{PageSize: 20, Cap: 100}
	for _, c := range []struct {
		start int64
		n     int
		probe bool
	}{{0, 20, true}, {60, 20, true}, {80, 20, false}, {90, 10, false}, {100, 0, false}, {120, 0, false}} {
		n, probe := p.Window(c.start)
		require.Equal(t, c.n, n, "start %d", c.start)
		require.Equal(t, c.probe, probe, "start %d", c.start)
	}
	n, probe := Plan{PageSize: 20}.Window(500)
	require.Equal(t, 20, n)
	require.True(t, probe, "sin tope siempre hay sondeo")

	require.EqualValues(t, 80, p.LastStart(1_000_000), "el tope manda sobre el total")
	require.EqualValues(t, 40, p.LastStart(41))
	require.EqualValues(t, 0, p.LastStart(20))
	require.EqualValues(t, 0, p.LastStart(0))
	require.EqualValues(t, 99980, Plan{PageSize: 20}.LastStart(100_000), "sin tope vale el total")
}

func TestCountSQL(t *testing.T) {
	require.Equal(t, "SELECT COUNT(*) FROM (SELECT 1 FROM t LIMIT 100) _c", Plan{From: "t", Cap: 100}.CountSQL())
	require.Equal(t, "SELECT COUNT(*) FROM (SELECT 1 FROM `d`.`t` WHERE (a > 1 -- c\n) LIMIT 7) _c", Plan{From: "`d`.`t`", Where: "a > 1 -- c", Cap: 7}.CountSQL())
}

func TestBuildNAddsLimitAndOffset(t *testing.T) {
	p := plan([]Col{{Name: "id"}}, "", 20)
	got, err := p.BuildN(First, nil, quote, 15, 80)
	require.NoError(t, err)
	require.Equal(t, "SELECT id, price, qty FROM t ORDER BY `id` LIMIT 15 OFFSET 80", got)
	got, err = p.BuildN(Next, []string{"40"}, quote, 5, 0)
	require.NoError(t, err)
	require.NotContains(t, got, "OFFSET")
	_, err = p.BuildN(First, nil, quote, 0, 0)
	require.Error(t, err)
}

// walkCapped hace lo que el servicio con un LIMIT propio: pide por ventanas, sin pasar nunca del tope.
func walkCapped(t *testing.T, db *sql.DB, p Plan) []row {
	t.Helper()
	var all []row
	start, edge := int64(0), []string(nil)
	for pages := 0; ; pages++ {
		require.Less(t, pages, 10_000, "el recorrido no termina")
		n, probe := p.Window(start)
		if n == 0 {
			return all
		}
		limit := n
		if probe {
			limit++
		}
		d := First
		if edge != nil {
			d = Next
		}
		q, err := p.BuildN(d, edge, quote, limit, 0)
		require.NoError(t, err)
		got := fetch(t, db, q)
		page, more := Page(got, n)
		all = append(all, page...)
		if !more || len(page) == 0 {
			return all
		}
		start += int64(len(page))
		edge = edgeOf(page[len(page)-1], p.Order)
	}
}

// Con un tope, el recorrido devuelve exactamente las primeras Cap filas del orden efectivo, ni una más ni una menos, para
// cualquier tamaño de página, tope y tamaño de tabla (incluida una tabla más corta que el tope).
func TestCappedWalkReturnsExactlyTheFirstCapRows(t *testing.T) {
	for _, tableRows := range []int{0, 1, 7, 40, 61} {
		db := newDB(t, tableRows, int64(tableRows))
		for _, size := range []int{1, 3, 10, 20} {
			for _, capN := range []int64{1, 5, 20, 21, 39, 40, 100} {
				p := plan([]Col{{Name: "price", Desc: true}, {Name: "id", Desc: true}}, "qty >= 1", size)
				p.Cap = capN
				name := fmt.Sprintf("filas=%d tamaño=%d tope=%d", tableRows, size, capN)
				want := oracle(t, db, p)
				if int64(len(want)) > capN {
					want = want[:capN]
				}
				got := walkCapped(t, db, p)
				require.Equal(t, ids(want), ids(got), name)
			}
		}
	}
}

// «Última» con tope: se lee por posición (OFFSET) la última página del resultado acotado.
func TestCappedLastPageIsReadByPosition(t *testing.T) {
	db := newDB(t, 50, 9)
	for _, size := range []int{4, 10, 25} {
		for _, capN := range []int64{9, 30, 50, 200} {
			p := plan([]Col{{Name: "id"}}, "", size)
			p.Cap = capN
			total := int64(len(oracle(t, db, p)))
			start := p.LastStart(total)
			n, _ := p.Window(start)
			q, err := p.BuildN(First, nil, quote, n, start)
			require.NoError(t, err)
			got := fetch(t, db, q)
			all := oracle(t, db, p)
			upto := min(total, capN)
			require.Equal(t, ids(all[start:upto]), ids(got), "tamaño=%d tope=%d", size, capN)
			require.LessOrEqual(t, int64(len(got)), int64(size))
		}
	}
}
