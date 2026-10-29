package results

import (
	"strconv"
	"testing"

	"github.com/vitacodesas/dbacus/internal/model"
)

func benchRows(n int) [][]Cell {
	rows := make([][]Cell, n)
	for i := range rows {
		rows[i] = []Cell{{V: int64(i)}, {V: "fila-" + strconv.Itoa(i)}, {V: "2026-01-01 00:00:00", T: "datetime"}, {V: nil}}
	}
	return rows
}

var benchCols = []model.ColumnMeta{{Name: "id", DataType: "bigint"}, {Name: "txt", DataType: "varchar"}, {Name: "at", DataType: "datetime"}, {Name: "n", DataType: "int", Nullable: true}}

// BenchmarkBufferAppend: coste de acumular filas en el buffer (el camino caliente de la lectura del cursor).
func BenchmarkBufferAppend(b *testing.B) {
	rows := benchRows(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := NewManager(1<<40).NewRun("r", "t").NewBuffer(benchCols)
		for _, r := range rows {
			buf.Append(r)
		}
	}
	b.ReportMetric(float64(b.N*len(rows))/b.Elapsed().Seconds(), "rows/s")
}

// BenchmarkBufferWindow: servir una ventana de 200 filas a mitad de un resultado de 100 000.
func BenchmarkBufferWindow(b *testing.B) {
	buf := NewManager(1<<40).NewRun("r", "t").NewBuffer(benchCols)
	for _, r := range benchRows(100_000) {
		buf.Append(r)
	}
	buf.Finish(false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := buf.Window(50_000, 200); err != nil {
			b.Fatal(err)
		}
	}
}
