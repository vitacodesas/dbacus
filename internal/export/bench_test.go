package export

import (
	"io"
	"strconv"
	"testing"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/results"
)

var benchCols = []model.ColumnMeta{{Name: "id", DataType: "bigint"}, {Name: "txt", DataType: "varchar"}, {Name: "at", DataType: "datetime"}, {Name: "n", DataType: "int", Nullable: true}}

func benchRow(i int) []results.Cell {
	return []results.Cell{{V: int64(i)}, {V: "fila, con \"comillas\" " + strconv.Itoa(i)}, {V: "2026-01-01 00:00:00", T: "datetime"}, {V: nil}}
}

func benchWriter(b *testing.B, o Options) {
	w, err := New(io.Discard, o)
	if err != nil {
		b.Fatal(err)
	}
	if err := w.Begin(benchCols); err != nil {
		b.Fatal(err)
	}
	rows := make([][]results.Cell, 1000)
	for i := range rows {
		rows[i] = benchRow(i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, r := range rows {
			if err := w.Row(r); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.StopTimer()
	_ = w.End()
	b.ReportMetric(float64(b.N*len(rows))/b.Elapsed().Seconds(), "rows/s")
}

func BenchmarkCSV(b *testing.B) {
	benchWriter(b, Options{Format: FormatCSV, Header: true, Delimiter: ","})
}
func BenchmarkJSON(b *testing.B) { benchWriter(b, Options{Format: FormatJSON}) }
func BenchmarkNDJSON(b *testing.B) {
	benchWriter(b, Options{Format: FormatJSON, NDJSON: true})
}
