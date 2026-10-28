package edit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/model"
	"tabulariswails/internal/results"
)

func mcol(dataType, columnType string, nullable bool) model.Column {
	return model.Column{Name: "c", DataType: dataType, ColumnType: columnType, Nullable: nullable}
}

func txt(s string) Value { return Value{Text: s} }

func TestConvertOK(t *testing.T) {
	cases := []struct {
		name string
		col  model.Column
		in   Value
		want any
	}{
		{"int", mcol("int", "int", false), txt(" 42 "), int64(42)},
		{"int negativo", mcol("int", "int", false), txt("-7"), int64(-7)},
		{"int con más", mcol("int", "int", false), txt("+7"), int64(7)},
		{"tinyint unsigned máx", mcol("tinyint", "tinyint unsigned", false), txt("255"), int64(255)},
		{"bigint máx", mcol("bigint", "bigint", false), txt("9223372036854775807"), int64(9223372036854775807)},
		{"bigint unsigned máx sin pasar por float", mcol("bigint", "bigint unsigned", false), txt("18446744073709551615"), uint64(18446744073709551615)},
		{"bool true", mcol("tinyint", "tinyint(1)", false), txt("TRUE"), int64(1)},
		{"decimal", mcol("decimal", "decimal(10,2)", false), txt("12345678.5"), "12345678.5"},
		{"decimal negativo", mcol("decimal", "decimal(10,2)", false), txt("-0.5"), "-0.5"},
		{"double", mcol("double", "double", false), txt("1.5e3"), 1500.0},
		{"year", mcol("year", "year", false), txt("2024"), int64(2024)},
		{"date", mcol("date", "date", false), txt("2024-02-29"), "2024-02-29"},
		{"datetime con fracción", mcol("datetime", "datetime(6)", false), txt("2024-01-02 03:04:05.123456"), "2024-01-02 03:04:05.123456"},
		{"datetime con T", mcol("datetime", "datetime", false), txt("2024-01-02T03:04:05"), "2024-01-02 03:04:05"},
		{"datetime solo fecha", mcol("datetime", "datetime", false), txt("2024-01-02"), "2024-01-02 00:00:00"},
		{"time", mcol("time", "time", false), txt("838:59:59"), "838:59:59"},
		{"json", mcol("json", "json", false), txt(`{"a": [1, 2]}`), `{"a": [1, 2]}`},
		{"texto con emoji", mcol("varchar", "varchar(5)", false), txt("hola😀"), "hola😀"},
		{"texto vacío no es NULL", mcol("varchar", "varchar(5)", false), txt(""), ""},
		{"texto sin recortar", mcol("text", "text", false), txt("  con espacios  "), "  con espacios  "},
		{"NULL en columna nulable", mcol("int", "int", true), Value{Null: true}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Convert(c.col, c.in)
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

func TestConvertRejects(t *testing.T) {
	cases := []struct {
		name string
		col  model.Column
		in   Value
		msg  string
	}{
		{"texto en entero", mcol("int", "int", false), txt("abc"), "no es un entero"},
		{"entero vacío", mcol("int", "int", false), txt(""), "no es un entero"},
		{"decimal en entero", mcol("int", "int", false), txt("1.5"), "no es un entero"},
		{"fuera de rango", mcol("tinyint", "tinyint", false), txt("128"), "no es un entero"},
		{"negativo en unsigned", mcol("int", "int unsigned", false), txt("-1"), "no es un entero"},
		{"unsigned excedido", mcol("bigint", "bigint unsigned", false), txt("18446744073709551616"), "no es un entero"},
		{"decimal inválido", mcol("decimal", "decimal(10,2)", false), txt("1,5"), "decimal válido"},
		{"decimal con demasiadas cifras", mcol("decimal", "decimal(5,2)", false), txt("1234.5"), "demasiadas cifras"},
		{"float NaN", mcol("double", "double", false), txt("NaN"), "no es un número"},
		{"año fuera", mcol("year", "year", false), txt("1800"), "año"},
		{"fecha inválida", mcol("date", "date", false), txt("2024-02-30"), "formato"},
		{"fecha y hora inválidas", mcol("datetime", "datetime", false), txt("ayer"), "formato"},
		{"hora inválida", mcol("time", "time", false), txt("25:61:00"), "formato"},
		{"json inválido", mcol("json", "json", false), txt("{a:1}"), "JSON"},
		{"json con comillas tipográficas dice dónde", mcol("json", "json", false), txt("{“host\": 1}"), "posición 2"},
		{"NULL en NOT NULL", mcol("int", "int", false), Value{Null: true}, "no admite NULL"},
		{"varchar demasiado largo", mcol("varchar", "varchar(3)", false), txt("abcd"), "admite 3"},
		{"UTF-8 inválido", mcol("text", "text", false), txt("a\xffb"), "UTF-8"},
		{"binario no soportado", mcol("blob", "blob", true), txt("x"), "no se puede editar"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Convert(c.col, c.in)
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), c.msg), "mensaje %q no contiene %q", err.Error(), c.msg)
		})
	}
}

func TestOriginalArgAndSame(t *testing.T) {
	big := mcol("bigint", "bigint unsigned", false)
	arg, null, err := OriginalArg(big, results.Cell{V: "18446744073709551615", T: results.TBigint})
	require.NoError(t, err)
	require.False(t, null)
	require.Equal(t, uint64(18446744073709551615), arg)

	arg, _, _ = OriginalArg(mcol("int", "int", false), results.Cell{V: "42"})
	require.Equal(t, int64(42), arg)
	arg, _, _ = OriginalArg(mcol("int", "int", false), results.Cell{V: int64(42)})
	require.Equal(t, int64(42), arg, "también si el driver entrega un entero")
	_, null, _ = OriginalArg(mcol("int", "int", true), results.Cell{})
	require.True(t, null)
	arg, _, _ = OriginalArg(mcol("varchar", "varchar(5)", false), results.Cell{V: "a b"})
	require.Equal(t, "a b", arg)

	require.True(t, Same(mcol("decimal", "decimal(10,2)", false), txt("1.5"), results.Cell{V: "1.50"}))
	require.False(t, Same(mcol("decimal", "decimal(10,2)", false), txt("1.51"), results.Cell{V: "1.50"}))
	require.True(t, Same(mcol("int", "int", false), txt("007"), results.Cell{V: "7"}))
	require.True(t, Same(mcol("datetime", "datetime", false), txt("2024-01-02T03:04:05"), results.Cell{V: "2024-01-02 03:04:05"}))
	require.True(t, Same(mcol("varchar", "varchar(5)", true), Value{Null: true}, results.Cell{}))
	require.False(t, Same(mcol("varchar", "varchar(5)", true), txt(""), results.Cell{}), "cadena vacía ≠ NULL")
	require.False(t, Same(mcol("varchar", "varchar(5)", true), Value{Null: true}, results.Cell{V: ""}))
	require.False(t, Same(mcol("varchar", "varchar(5)", false), txt("A"), results.Cell{V: "a"}), "el texto se compara tal cual")
}

func TestVerifiable(t *testing.T) {
	for _, dt := range []string{"int", "BIGINT", "decimal", "varchar", "text", "enum", "date", "datetime", "time", "year"} {
		require.True(t, Verifiable(dt), dt)
	}
	for _, dt := range []string{"float", "double", "json", "timestamp", "blob", "bit"} {
		require.False(t, Verifiable(dt), dt)
	}
}

type q struct{}

func (q) QuoteIdent(n string) string { return "`" + strings.ReplaceAll(n, "`", "``") + "`" }
func (q) QuoteString(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", "''") + "'"
}

func TestBuildUpdates(t *testing.T) {
	changes := []Change{
		{Row: 3, Col: 2, Column: "note", Value: txt("o'brien"), Arg: "o'brien", OrigNull: true, Verify: true, Key: []any{int64(7), "x"}},
		{Row: 3, Col: 1, Column: "name", Value: txt("Ana"), Arg: "Ana", OrigArg: "Eva", Verify: true, Key: []any{int64(7), "x"}},
		{Row: 3, Col: 4, Column: "ratio", Value: txt("1.5"), Arg: 1.5, OrigArg: "1.4", Verify: false, Key: []any{int64(7), "x"}},
		{Row: 1, Col: 1, Column: "name", Value: Value{Null: true}, Arg: nil, OrigArg: "b", Verify: true, Key: []any{int64(2), "y"}},
	}
	got := BuildUpdates(q{}, "my`db", "t", []string{"id", "code"}, changes)
	require.Len(t, got, 2)

	require.Equal(t, 1, got[0].Row, "ordenado por fila")
	require.Equal(t, "UPDATE `my``db`.`t` SET `name` = ? WHERE `id` = ? AND `code` = ? AND `name` <=> ?", got[0].SQL)
	require.Equal(t, []any{nil, int64(2), "y", "b"}, got[0].Args)
	require.Equal(t, "UPDATE `my``db`.`t` SET `name` = NULL WHERE `id` = 2 AND `code` = 'y' AND `name` <=> 'b'", got[0].Review)

	require.Equal(t, 3, got[1].Row)
	require.Equal(t, []int{1, 2, 4}, got[1].Cols, "columnas en orden")
	require.Equal(t, "UPDATE `my``db`.`t` SET `name` = ?, `note` = ?, `ratio` = ? WHERE `id` = ? AND `code` = ? AND `name` <=> ? AND `note` <=> ?", got[1].SQL,
		"la columna no verificable (ratio) no entra en el WHERE")
	require.Equal(t, []any{"Ana", "o'brien", 1.5, int64(7), "x", "Eva", nil}, got[1].Args)
	require.Contains(t, got[1].Review, "`note` = 'o''brien'")
	require.Contains(t, got[1].Review, "`note` <=> NULL")
	require.Contains(t, got[1].Review, "`ratio` = 1.5")
}

func TestFormatJSON(t *testing.T) {
	out, err := FormatJSON(`{"a":[1,2],"b":{"c":null}}`)
	require.NoError(t, err)
	require.Equal(t, "{\n  \"a\": [\n    1,\n    2\n  ],\n  \"b\": {\n    \"c\": null\n  }\n}", out)
	_, err = FormatJSON(`{"a": }`)
	require.ErrorContains(t, err, "no es JSON válido")
}

func TestSameJSONIgnoresFormatting(t *testing.T) {
	col := mcol("json", "json", true)
	orig := results.Cell{V: `{"b": [1, 2], "a": {"x": null}}`}
	pretty, _ := FormatJSON(`{"a":{"x":null},"b":[1,2]}`)
	require.True(t, Same(col, txt(pretty), orig), "dar formato y reordenar claves no es un cambio")
	require.True(t, Same(col, txt(`{"a":{"x":null},"b":[1,2]}`), orig), "tampoco quitar espacios")
	require.False(t, Same(col, txt(`{"a":{"x":null},"b":[1,3]}`), orig), "un valor distinto sí lo es")
	require.False(t, Same(col, txt(`{"a":{"x":null},"b":[1,2],"c":1}`), orig), "una clave nueva sí lo es")
	require.False(t, Same(col, txt(`{"b": [1.0, 2], "a": {"x": null}}`), orig), "1 y 1.0 son valores distintos: ante la duda es un cambio")
	require.False(t, Same(col, txt("{no es json"), orig), "un JSON inválido no es «igual»: Go lo rechazará")
	require.False(t, Same(col, Value{Null: true}, orig))
}

func TestBuildInsertsAndDeletes(t *testing.T) {
	ins := BuildInserts(q{}, "db", "t", []InsertRow{
		{Row: 5, Cells: []InsertCell{{Col: 1, Column: "name", Arg: "o'brien"}, {Col: 2, Column: "note", Arg: nil}, {Col: 3, Column: "n", Arg: int64(7)}}},
		{Row: 6}, // sin celdas: todo a su DEFAULT
	})
	require.Len(t, ins, 2)
	require.Equal(t, KindInsert, ins[0].Kind)
	require.Equal(t, "INSERT INTO `db`.`t` (`name`, `note`, `n`) VALUES (?, ?, ?)", ins[0].SQL)
	require.Equal(t, []any{"o'brien", nil, int64(7)}, ins[0].Args)
	require.Equal(t, []int{1, 2, 3}, ins[0].Cols)
	require.Equal(t, "INSERT INTO `db`.`t` (`name`, `note`, `n`) VALUES ('o''brien', NULL, 7)", ins[0].Review)
	require.Equal(t, "INSERT INTO `db`.`t` () VALUES ()", ins[1].SQL)
	require.Empty(t, ins[1].Args)

	del := BuildDeletes(q{}, "db", "t", []string{"id", "code"}, []DeleteChange{
		{Row: 9, Key: []any{int64(2), "y"}, Verify: []VerifyCol{{Column: "name", Arg: "b"}}},
		{Row: 3, Key: []any{int64(1), "x"}, Verify: []VerifyCol{{Column: "note", Null: true}, {Column: "name", Arg: "a"}}},
	})
	require.Len(t, del, 2)
	require.Equal(t, 3, del[0].Row, "ordenado por fila")
	require.Equal(t, KindDelete, del[0].Kind)
	require.Equal(t, "DELETE FROM `db`.`t` WHERE `id` = ? AND `code` = ? AND `note` <=> ? AND `name` <=> ? LIMIT 1", del[0].SQL)
	require.Equal(t, []any{int64(1), "x", nil, "a"}, del[0].Args)
	require.Equal(t, "DELETE FROM `db`.`t` WHERE `id` = 1 AND `code` = 'x' AND `note` <=> NULL AND `name` <=> 'a' LIMIT 1", del[0].Review)
	require.Equal(t, []any{int64(1), "x"}, del[0].Key)
}
