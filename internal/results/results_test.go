package results

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

func TestConvertTypes(t *testing.T) {
	big := int64(9007199254740993) // 2^53 + 1
	cases := []struct {
		name string
		in   any
		dt   string
		want Cell
	}{
		{"nil", nil, "VARCHAR", Cell{}},
		{"bool", true, "TINYINT", Cell{V: true}},
		{"int seguro", int64(42), "INT", Cell{V: int64(42)}},
		{"int negativo seguro", int64(-9007199254740991), "BIGINT", Cell{V: int64(-9007199254740991)}},
		{"int fuera de rango", big, "BIGINT", Cell{V: "9007199254740993", T: TBigint}},
		{"int negativo fuera de rango", -big, "BIGINT", Cell{V: "-9007199254740993", T: TBigint}},
		{"uint64 máximo", uint64(18446744073709551615), "UNSIGNED BIGINT", Cell{V: "18446744073709551615", T: TBigint}},
		{"uint64 pequeño", uint64(7), "UNSIGNED BIGINT", Cell{V: int64(7)}},
		{"bigint sin signo como texto", []byte("18446744073709551615"), "UNSIGNED BIGINT", Cell{V: "18446744073709551615", T: TBigint}},
		{"entero pequeño como texto", []byte("1234567890123456"), "BIGINT", Cell{V: "1234567890123456"}}, // dentro de 2^53
		{"float", 1.5, "DOUBLE", Cell{V: 1.5}},
		{"decimal", []byte("12.340"), "DECIMAL", Cell{V: "12.340", T: TDecimal}},
		{"texto", []byte("héllo 😀"), "VARCHAR", Cell{V: "héllo 😀"}},
		{"json", []byte(`{"a":1}`), "JSON", Cell{V: `{"a":1}`, T: TJSON}},
		{"datetime", []byte("2024-01-02 03:04:05.123456"), "DATETIME", Cell{V: "2024-01-02 03:04:05.123456", T: TDatetime}},
		{"time.Time", time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.UTC), "DATETIME", Cell{V: "2024-01-02 03:04:05.123456", T: TDatetime}},
		{"binario", []byte{0x00, 0xFF, 0x10}, "BLOB", Cell{V: "0x00ff10", T: TBytes}},
		{"texto no UTF-8 en columna de texto", []byte{0xff, 0xfe}, "VARCHAR", Cell{V: "0xfffe", T: TBytes}},
		{"cadena vacía", []byte{}, "VARCHAR", Cell{V: ""}},
		{"tipo en minúsculas", []byte("1.5"), "decimal", Cell{V: "1.5", T: TDecimal}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, Convert(c.in, c.dt)) })
	}
}

func TestConvertSpecialFloats(t *testing.T) {
	for _, f := range []float64{1 / zero(), -1 / zero(), zero() / zero()} {
		c := Convert(f, "DOUBLE")
		_, isStr := c.V.(string)
		require.True(t, isStr, "NaN/Inf no son JSON válido: %v", c.V)
	}
}

func zero() float64 { return 0 }

func TestConvertTruncatesLongTextAtRuneBoundary(t *testing.T) {
	long := strings.Repeat("é", MaxCellBytes) // 2 bytes por carácter → 128 KiB
	c := Convert([]byte(long), "LONGTEXT")
	require.True(t, c.Trunc)
	s := c.V.(string)
	require.LessOrEqual(t, len(s), MaxCellBytes)
	require.True(t, strings.HasSuffix(s, "é"), "no parte un carácter UTF-8 por la mitad")
	require.Equal(t, long, c.Full())
}

func TestConvertTruncatesLongBinary(t *testing.T) {
	c := Convert(make([]byte, 1000), "BLOB")
	require.True(t, c.Trunc)
	require.Equal(t, TBytes, c.T)
	require.True(t, strings.HasSuffix(c.V.(string), "…"))
	require.Len(t, c.Full().(string), 2+2000)
}

func cols(names ...string) []model.ColumnMeta {
	out := make([]model.ColumnMeta, len(names))
	for i, n := range names {
		out[i] = model.ColumnMeta{Name: n, DataType: "INT"}
	}
	return out
}

func fill(t *testing.T, b *Buffer, n int) {
	t.Helper()
	for i := range n {
		require.True(t, b.Append([]Cell{{V: int64(i)}}))
	}
}

func TestBufferWindows(t *testing.T) {
	m := NewManager(1 << 30)
	r := m.NewRun("q1", "tab1")
	b := r.NewBuffer(cols("n"))
	fill(t, b, 10)

	rows, total, complete, err := b.Window(2, 3)
	require.NoError(t, err)
	require.Equal(t, 10, total)
	require.False(t, complete, "aún se está leyendo")
	require.Equal(t, int64(2), rows[0][0].V)
	require.Len(t, rows, 3)

	b.Finish(false)
	rows, _, complete, _ = b.Window(8, 100)
	require.True(t, complete)
	require.Len(t, rows, 2, "el límite se recorta al final")
	rows, _, _, _ = b.Window(50, 10)
	require.Empty(t, rows)
	rows, _, _, _ = b.Window(-5, 2)
	require.Len(t, rows, 2)

	c, err := b.Cell(9, 0)
	require.NoError(t, err)
	require.Equal(t, int64(9), c.V)
	_, err = b.Cell(10, 0)
	require.Error(t, err)
}

func TestWindowIsStableWhileAppending(t *testing.T) {
	m := NewManager(1 << 30)
	b := m.NewRun("q", "t").NewBuffer(cols("n"))
	fill(t, b, 5)
	rows, _, _, _ := b.Window(0, 5)
	fill(t, b, 5000) // fuerza realocaciones
	require.Len(t, rows, 5)
	require.Equal(t, int64(4), rows[4][0].V)
}

func TestGetAndRelease(t *testing.T) {
	m := NewManager(1 << 30)
	m.NewRun("q1", "tab1").NewBuffer(cols("n"))
	r, err := m.Get("q1")
	require.NoError(t, err)
	_, err = r.Buffer(0)
	require.NoError(t, err)
	_, err = r.Buffer(3)
	require.Error(t, err)

	m.Release("q1")
	_, err = m.Get("q1")
	require.ErrorIs(t, err, ErrExpired)
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, apperr.ResultExpired, ae.Code)
	m.Release("q1") // idempotente
	require.EqualValues(t, 0, m.Total())
}

func TestReleaseTab(t *testing.T) {
	m := NewManager(1 << 30)
	for i := range 3 {
		b := m.NewRun(fmt.Sprint("q", i), "tabA").NewBuffer(cols("n"))
		fill(t, b, 3)
	}
	other := m.NewRun("qB", "tabB").NewBuffer(cols("n"))
	fill(t, other, 3)

	m.ReleaseTab("tabA")
	_, err := m.Get("q1")
	require.ErrorIs(t, err, ErrExpired)
	_, err = m.Get("qB")
	require.NoError(t, err)
}

func TestEvictsOldestDoneRunsWhenOverLimit(t *testing.T) {
	// Cada fila de un entero cuesta 24 + 48 = 72 bytes.
	m := NewManager(72 * 25)
	mk := func(id string, n int) *Run {
		r := m.NewRun(id, id)
		fill(t, r.NewBuffer(cols("n")), n)
		r.MarkDone()
		return r
	}
	mk("old", 10)
	mk("mid", 10)
	_, _ = m.Get("old") // "old" pasa a ser más reciente que "mid"

	cur := m.NewRun("cur", "cur") // en curso: no expulsable
	b := cur.NewBuffer(cols("n"))
	fill(t, b, 10)

	_, err := m.Get("mid")
	require.ErrorIs(t, err, ErrExpired, "se expulsó la menos usada")
	_, err = m.Get("old")
	require.NoError(t, err)
	_, err = m.Get("cur")
	require.NoError(t, err)
	require.LessOrEqual(t, m.Total(), int64(72*25))
}

func TestRunningRunIsNeverEvictedAndTruncatesAtLimit(t *testing.T) {
	m := NewManager(72 * 5)
	cur := m.NewRun("cur", "t")
	b := cur.NewBuffer(cols("n"))
	for range 5 {
		require.True(t, b.Append([]Cell{{V: int64(1)}}))
	}
	require.False(t, b.Append([]Cell{{V: int64(1)}}), "sin más memoria y sin nada que expulsar")
	require.True(t, b.Truncated())
	require.Equal(t, 5, b.Len())
	_, err := m.Get("cur")
	require.NoError(t, err)
}

func TestEvictedRunReportsExpiredOnWindow(t *testing.T) {
	m := NewManager(72 * 12)
	r1 := m.NewRun("a", "a")
	b1 := r1.NewBuffer(cols("n"))
	fill(t, b1, 10)
	r1.MarkDone()
	b2 := m.NewRun("b", "b").NewBuffer(cols("n"))
	fill(t, b2, 10)

	_, _, _, err := b1.Window(0, 5)
	require.ErrorIs(t, err, ErrExpired)
	_, err = b1.Cell(0, 0)
	require.ErrorIs(t, err, ErrExpired)
	require.False(t, b1.Append([]Cell{{V: 1}}))
}

func TestConcurrentAppendAndRead(t *testing.T) {
	m := NewManager(1 << 30)
	b := m.NewRun("q", "t").NewBuffer(cols("n"))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 2000 {
			b.Append([]Cell{{V: int64(i)}})
		}
		b.Finish(false)
	}()
	go func() {
		defer wg.Done()
		for range 2000 {
			_, _, _, _ = b.Window(0, 100)
			_ = b.Len()
		}
	}()
	wg.Wait()
	require.Equal(t, 2000, b.Len())
}

func TestSetLimitAffectsNextAppend(t *testing.T) {
	m := NewManager(1 << 30)
	b := m.NewRun("q", "t").NewBuffer(cols("n"))
	fill(t, b, 2)
	m.SetLimit(10)
	require.False(t, b.Append([]Cell{{V: int64(1)}}))
}

func TestPendingOverlayLeavesTheBufferIntact(t *testing.T) {
	m := NewManager(1 << 30)
	buf := m.NewRun("r", "t").NewBuffer(nil)
	for i := 0; i < 5; i++ {
		buf.Append([]Cell{{V: int64(i)}, {V: "orig"}})
	}
	buf.Finish(false)

	require.NoError(t, buf.Stage(1, 1, Cell{V: "nuevo"}, "dato"))
	require.NoError(t, buf.Stage(3, 1, Cell{}, nil)) // NULL pendiente
	require.Error(t, buf.Stage(9, 0, Cell{}, nil), "fuera de rango")
	require.Error(t, buf.Stage(0, 5, Cell{}, nil), "fuera de rango")
	require.Equal(t, 2, buf.PendingCount())

	v, err := buf.WindowView(0, 5)
	require.NoError(t, err)
	rows := v.Rows
	require.Equal(t, Cell{V: "nuevo", Edited: true}, rows[1][1])
	require.Equal(t, Cell{V: nil, Edited: true}, rows[3][1])
	require.Equal(t, Cell{V: "orig"}, rows[0][1], "las filas sin cambios no se tocan")

	orig, err := buf.Cell(1, 1)
	require.NoError(t, err)
	require.Equal(t, Cell{V: "orig"}, orig, "el original sigue intacto para comprobar conflictos")

	sv, _ := buf.WindowView(2, 2) // ventana que solo incluye la fila 3
	sub := sv.Rows
	require.Equal(t, Cell{V: nil, Edited: true}, sub[1][1])
	require.Equal(t, Cell{V: "orig"}, sub[0][1])

	raw, _, _, _ := buf.Window(0, 5)
	require.Equal(t, Cell{V: "orig"}, raw[1][1], "Window (exportar, INSERT) nunca lleva cambios pendientes")

	list := buf.PendingList()
	require.Equal(t, []int{1, 3}, []int{list[0].Row, list[1].Row})
	require.Equal(t, "dato", list[0].Data)

	buf.Unstage(1, 1)
	require.Equal(t, 1, buf.PendingCount())
	buf.ClearPending()
	v, _ = buf.WindowView(0, 5)
	require.Equal(t, Cell{V: "orig"}, v.Rows[3][1])
}

func TestNewRowsAndDeletesInTheView(t *testing.T) {
	m := NewManager(1 << 30)
	run := m.NewRun("r", "t")
	buf := run.NewBuffer(nil)
	for i := 0; i < 4; i++ {
		buf.Append([]Cell{{V: int64(i)}, {V: "orig"}})
	}
	_, err := buf.AddNewRow(2)
	require.Error(t, err, "mientras el resultado aún se lee no se puede añadir una fila nueva")
	buf.Finish(false)

	r1, err := buf.AddNewRow(2)
	require.NoError(t, err)
	require.Equal(t, 4, r1)
	r2, _ := buf.AddNewRow(2)
	require.Equal(t, 5, r2)
	require.NoError(t, buf.SetNewCell(r1, 1, Cell{V: "nueva"}, "d"))

	require.NoError(t, buf.MarkDelete(2, "borrar"))
	require.NoError(t, buf.Stage(2, 1, Cell{V: "x"}, nil))
	require.NoError(t, buf.MarkDelete(2, "borrar"), "marcar dos veces es inocuo")
	require.Equal(t, 0, buf.PendingCount(), "borrar una fila descarta sus ediciones de celda")
	require.Error(t, buf.MarkDelete(5, nil), "una fila nueva no se «marca»: se descarta")

	v, err := buf.WindowView(0, 100)
	require.NoError(t, err)
	require.Equal(t, 6, v.Total, "el total incluye las filas nuevas")
	require.Equal(t, 4, v.NewFrom)
	require.Equal(t, []int{2}, v.Deleted)
	require.Equal(t, Cell{V: "nueva", Edited: true}, v.Rows[4][1])
	require.Equal(t, Cell{T: TDefault, Edited: true}, v.Rows[4][0], "sin valor se muestra como DEFAULT")
	require.Equal(t, Cell{T: TDefault, Edited: true}, v.Rows[5][1])

	w, _ := buf.WindowView(3, 2) // ventana a caballo entre filas reales y nuevas
	require.Len(t, w.Rows, 2)
	require.Equal(t, 4, w.NewFrom)
	require.Empty(t, w.Deleted, "la fila 2 queda fuera de esta ventana")

	raw, total, _, _ := buf.Window(0, 100)
	require.Equal(t, 4, total, "Window (exportar) no ve filas nuevas")
	require.Len(t, raw, 4)

	require.Equal(t, 2, buf.InsertCount())
	require.Equal(t, 1, buf.DeleteCount())
	buf.UnsetNewCell(r1, 1)
	require.Equal(t, TDefault, buf.NewRows()[0].Cells[1].T)
	buf.RemoveNewRow(r1)
	require.Equal(t, 1, buf.InsertCount())
	require.Equal(t, 4, buf.NewRows()[0].Row, "al descartar una, la siguiente sube de índice")
	buf.UnmarkDelete(2)
	require.Equal(t, 0, buf.DeleteCount())
}

func TestRemoveRowsAndAppendRowAdjustMemory(t *testing.T) {
	m := NewManager(1 << 30)
	buf := m.NewRun("r", "t").NewBuffer(nil)
	for i := 0; i < 3; i++ {
		buf.Append([]Cell{{V: int64(i)}, {V: "x"}})
	}
	buf.Finish(false)
	before := m.Total()
	buf.RemoveRows([]int{1})
	require.Less(t, m.Total(), before)
	rows, total, _, _ := buf.Window(0, 10)
	require.Equal(t, 2, total)
	require.Equal(t, int64(2), rows[1][0].V, "las filas siguientes suben de índice")
	buf.AppendRow([]Cell{{V: int64(9)}, {V: "nueva"}})
	_, total, _, _ = buf.Window(0, 10)
	require.Equal(t, 3, total)
	buf.ClearPending()
	require.Equal(t, 0, buf.InsertCount()+buf.DeleteCount()+buf.PendingCount())
}

func TestReplaceBufferFreesTheOldMemoryAndKeepsTheOtherStatements(t *testing.T) {
	m := NewManager(1 << 30)
	r := m.NewRun("q1", "tab1")
	first := r.NewBuffer(cols("n"))
	other := r.NewBuffer(cols("n"))
	fill(t, first, 100)
	fill(t, other, 10)
	before := m.Total()
	require.Positive(t, before)

	// Cada fila pesa lo mismo, así que cambiar 100 filas por 3 deja exactamente el peso de 13.
	nb, err := r.ReplaceBuffer(0, cols("n"))
	require.NoError(t, err)
	require.EqualValues(t, rowSize([]Cell{{V: int64(0)}})*10, m.Total(), "al sustituir se descuenta lo del buffer anterior")
	fill(t, nb, 3)
	require.EqualValues(t, rowSize([]Cell{{V: int64(0)}})*13, m.Total())

	got, err := r.Buffer(0)
	require.NoError(t, err)
	require.Same(t, nb, got)
	require.Equal(t, 3, got.Len())
	o, _ := r.Buffer(1)
	require.Same(t, other, o)
	require.Equal(t, 10, o.Len(), "las demás sentencias no se tocan")

	// El buffer viejo sigue siendo legible para quien aún lo tuviera, pero ya no cuenta.
	require.Equal(t, 100, first.Len())
	m.Release("q1")
	require.EqualValues(t, 0, m.Total(), "liberar la ejecución deja la memoria en cero (sin fugas ni negativos)")
}

func TestReplaceBufferErrors(t *testing.T) {
	m := NewManager(1 << 30)
	r := m.NewRun("q1", "tab1")
	r.NewBuffer(cols("n"))
	_, err := r.ReplaceBuffer(5, cols("n"))
	require.Error(t, err)
	_, err = r.ReplaceBuffer(-1, cols("n"))
	require.Error(t, err)
	m.Release("q1")
	_, err = r.ReplaceBuffer(0, cols("n"))
	require.ErrorIs(t, err, ErrExpired)
}

func TestReplaceBufferDropsPendingEditsAndExt(t *testing.T) {
	m := NewManager(1 << 30)
	r := m.NewRun("q1", "tab1")
	b := r.NewBuffer(cols("n"))
	fill(t, b, 3)
	b.SetExt("estado de la página 1")
	require.NoError(t, b.Stage(0, 0, Cell{V: int64(9)}, nil))
	require.Equal(t, 1, b.PendingCount())
	require.Equal(t, "estado de la página 1", b.Ext())

	nb, err := r.ReplaceBuffer(0, cols("n"))
	require.NoError(t, err)
	require.Zero(t, nb.PendingCount())
	require.Nil(t, nb.Ext())
}

func TestExtIsPerBuffer(t *testing.T) {
	m := NewManager(1 << 30)
	r := m.NewRun("q1", "tab1")
	a, b := r.NewBuffer(cols("n")), r.NewBuffer(cols("n"))
	a.SetExt(1)
	b.SetExt(2)
	require.Equal(t, 1, a.Ext())
	require.Equal(t, 2, b.Ext())
}
