package export

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/model"
	"tabulariswails/internal/results"
)

var cols = []model.ColumnMeta{{Name: "id", DataType: "BIGINT"}, {Name: "name", DataType: "VARCHAR"}, {Name: "at", DataType: "DATETIME"}}

func render(t *testing.T, o Options, cols []model.ColumnMeta, rows [][]results.Cell) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := New(&buf, o)
	require.NoError(t, err)
	require.NoError(t, w.Begin(cols))
	for _, r := range rows {
		require.NoError(t, w.Row(r))
	}
	require.NoError(t, w.End())
	return buf.String()
}

func TestCSVQuotingNullsAndUnicode(t *testing.T) {
	rows := [][]results.Cell{
		{results.Convert(int64(1), "BIGINT"), results.Convert([]byte(`a,"b"`), "VARCHAR"), results.Convert(nil, "DATETIME")},
		{results.Convert(uint64(18446744073709551615), "BIGINT UNSIGNED"), results.Convert([]byte("línea1\nñ 😀"), "VARCHAR"), results.Convert([]byte("2024-01-02 03:04:05"), "DATETIME")},
	}
	got := render(t, Options{Format: FormatCSV, Header: true, BOM: true, NullAs: "NULL"}, cols, rows)
	want := "\xEF\xBB\xBFid,name,at\n1,\"a,\"\"b\"\"\",NULL\n18446744073709551615,\"línea1\nñ 😀\",2024-01-02 03:04:05\n"
	require.Equal(t, want, got)
}

func TestCSVDelimiterCRLFAndQuoteAll(t *testing.T) {
	rows := [][]results.Cell{{results.Convert(int64(1), "INT"), results.Convert([]byte("x;y"), "VARCHAR"), results.Convert(nil, "DATETIME")}}
	require.Equal(t, "id;name;at\r\n1;\"x;y\";\r\n", render(t, Options{Format: FormatCSV, Header: true, Delimiter: ";", CRLF: true}, cols, rows))
	require.Equal(t, "\"1\",\"x;y\",\"\"\n", render(t, Options{Format: FormatCSV, QuoteAll: true}, cols, rows))
}

func TestCSVEscapeFormulasOnlyTouchesText(t *testing.T) {
	rows := [][]results.Cell{{results.Convert(int64(-5), "INT"), results.Convert([]byte("=SUM(A1)"), "VARCHAR"), results.Convert([]byte("-1"), "DECIMAL")}}
	got := render(t, Options{Format: FormatCSV, EscapeFormulas: true}, cols, rows)
	require.Equal(t, "-5,'=SUM(A1),-1\n", got, "números y decimales no se tocan")
	require.Equal(t, "-5,=SUM(A1),-1\n", render(t, Options{Format: FormatCSV}, cols, rows))
}

func TestCSVUsesFullValueOfTruncatedCells(t *testing.T) {
	big := strings.Repeat("é", results.MaxCellBytes) // 128 KiB
	c := results.Convert([]byte(big), "TEXT")
	require.True(t, c.Trunc)
	got := render(t, Options{Format: FormatCSV}, cols[:1], [][]results.Cell{{c}})
	require.Equal(t, big+"\n", got)
}

func TestJSONTypes(t *testing.T) {
	cs := []model.ColumnMeta{{Name: "n", DataType: "BIGINT"}, {Name: "d", DataType: "DECIMAL"}, {Name: "b", DataType: "BLOB"}, {Name: "j", DataType: "JSON"}, {Name: "t", DataType: "DATETIME"}, {Name: "s", DataType: "VARCHAR"}, {Name: "z", DataType: "INT"}}
	row := []results.Cell{
		results.Convert(uint64(18446744073709551615), "BIGINT UNSIGNED"), results.Convert([]byte("12.50"), "DECIMAL"), results.Convert([]byte{0, 1, 0xff}, "BLOB"),
		results.Convert([]byte(`{"a":[1,2]}`), "JSON"), results.Convert([]byte("2024-01-02 03:04:05.5"), "DATETIME"), results.Convert([]byte("hola \"x\" 😀"), "VARCHAR"), results.Convert(nil, "INT"),
	}
	got := render(t, Options{Format: FormatJSON}, cs, [][]results.Cell{row})
	var decoded []map[string]any
	require.NoError(t, json.Unmarshal([]byte(got), &decoded), got)
	m := decoded[0]
	require.Equal(t, "18446744073709551615", m["n"])
	require.Equal(t, "12.50", m["d"])
	require.Equal(t, "AAH/", m["b"])
	require.Equal(t, map[string]any{"a": []any{1.0, 2.0}}, m["j"])
	require.Equal(t, "2024-01-02T03:04:05.5", m["t"])
	require.Equal(t, "hola \"x\" 😀", m["s"])
	require.Nil(t, m["z"])
}

func TestJSONBinaryExportsFullContent(t *testing.T) {
	raw := bytes.Repeat([]byte{0xAB}, results.MaxBinaryBytes*3)
	c := results.Convert(raw, "LONGBLOB")
	require.True(t, c.Trunc)
	got := render(t, Options{Format: FormatJSON}, []model.ColumnMeta{{Name: "b", DataType: "LONGBLOB"}}, [][]results.Cell{{c}})
	var decoded []map[string][]byte
	require.NoError(t, json.Unmarshal([]byte(got), &decoded))
	require.Equal(t, raw, decoded[0]["b"])
}

func TestJSONDuplicateColumnNamesAndNDJSONAndEmpty(t *testing.T) {
	cs := []model.ColumnMeta{{Name: "id", DataType: "INT"}, {Name: "id", DataType: "INT"}}
	rows := [][]results.Cell{{results.Convert(int64(1), "INT"), results.Convert(int64(2), "INT")}, {results.Convert(int64(3), "INT"), results.Convert(int64(4), "INT")}}
	require.Equal(t, "{\"id\":1,\"id_2\":2}\n{\"id\":3,\"id_2\":4}\n", render(t, Options{Format: FormatJSON, NDJSON: true}, cs, rows))
	require.Equal(t, "[]", render(t, Options{Format: FormatJSON}, cs, nil))
	require.Equal(t, "", render(t, Options{Format: FormatJSON, NDJSON: true}, cs, nil))
}

func TestInvalidJSONColumnFallsBackToString(t *testing.T) {
	got := render(t, Options{Format: FormatJSON}, []model.ColumnMeta{{Name: "j", DataType: "JSON"}}, [][]results.Cell{{results.Convert([]byte("{no"), "JSON")}})
	require.JSONEq(t, `[{"j":"{no"}]`, got)
}

func TestValidate(t *testing.T) {
	require.Error(t, Options{Format: "xml"}.Validate())
	require.Error(t, Options{Format: FormatCSV, Delimiter: "|"}.Validate())
	require.Error(t, Options{Format: FormatCSV, NullAs: "a\nb"}.Validate())
	require.NoError(t, Options{Format: FormatCSV, Delimiter: "\t"}.Validate())
	require.Equal(t, "ndjson", Extension(Options{Format: FormatJSON, NDJSON: true}))
	require.Equal(t, "csv", Extension(Options{Format: FormatCSV}))
}
