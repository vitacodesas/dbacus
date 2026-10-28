package sqlsplit

import (
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/require"
)

func texts(src string) []string {
	var out []string
	for _, s := range Split(src) {
		out = append(out, s.Text)
	}
	return out
}

func TestSplitCases(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"vacío", "", nil},
		{"solo espacios y ;", "  ;;  ; ", nil},
		{"una sin ;", "SELECT 1", []string{"SELECT 1"}},
		{"dos", "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"salto de línea CRLF", "SELECT 1;\r\nSELECT 2;\r\n", []string{"SELECT 1", "SELECT 2"}},
		{"; en string simple", "SELECT ';'; SELECT 2", []string{"SELECT ';'", "SELECT 2"}},
		{"; en string doble", `SELECT ";" , 1; SELECT 2`, []string{`SELECT ";" , 1`, "SELECT 2"}},
		{"comilla doblada", "SELECT 'it''s;ok'; SELECT 2", []string{"SELECT 'it''s;ok'", "SELECT 2"}},
		{"escape con backslash", `SELECT 'a\';b'; SELECT 2`, []string{`SELECT 'a\';b'`, "SELECT 2"}},
		{"backslash antes del cierre real", `SELECT 'a\\'; SELECT 2`, []string{`SELECT 'a\\'`, "SELECT 2"}},
		{"backticks", "SELECT `a;b` FROM `t``x;`; SELECT 2", []string{"SELECT `a;b` FROM `t``x;`", "SELECT 2"}},
		{"backtick no admite backslash", "SELECT `a\\`; SELECT 2", []string{"SELECT `a\\`", "SELECT 2"}},
		{"comentario -- con ;", "SELECT 1 -- hola; adios\n; SELECT 2", []string{"SELECT 1 -- hola; adios", "SELECT 2"}},
		{"-- sin espacio no es comentario", "SELECT 1--1; SELECT 2", []string{"SELECT 1--1", "SELECT 2"}},
		{"-- al final del texto", "SELECT 1; --", []string{"SELECT 1"}},
		{"comentario #", "SELECT 1 # a;b\n; SELECT 2", []string{"SELECT 1 # a;b", "SELECT 2"}},
		{"comentario de bloque", "SELECT /* a; b */ 1; SELECT 2", []string{"SELECT /* a; b */ 1", "SELECT 2"}},
		{"comentario condicional", "/*!40101 SET x=1; */ SELECT 1;", []string{"/*!40101 SET x=1; */ SELECT 1"}},
		{"solo comentarios se descartan", "-- nada\n/* nada */; # nada\n", nil},
		{"comentario inicial se conserva", "-- cabecera\nSELECT 1;", []string{"-- cabecera\nSELECT 1"}},
		{"string sin cerrar", "SELECT 'abc; SELECT 2", []string{"SELECT 'abc; SELECT 2"}},
		{"bloque sin cerrar", "SELECT 1; /* abc; SELECT 2", []string{"SELECT 1"}},
		{"DELIMITER", "DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END$$\nDELIMITER ;\nSELECT 3;",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END", "SELECT 3"}},
		{"delimiter en minúsculas y //", "delimiter //\nSELECT 1; SELECT 2//\nSELECT 3//", []string{"SELECT 1; SELECT 2", "SELECT 3"}},
		{"delimiter no al inicio es código", "SELECT delimiter FROM t;", []string{"SELECT delimiter FROM t"}},
		{"identificador que empieza como delimiter", "delimiters_table;", []string{"delimiters_table"}},
		{"unicode", "SELECT 'ñ😀;'; SELECT 2", []string{"SELECT 'ñ😀;'", "SELECT 2"}},
		{"espacios finales recortados", "SELECT 1   \n  ;", []string{"SELECT 1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, texts(c.src)) })
	}
}

func TestOffsetsUTF16(t *testing.T) {
	src := "SELECT '😀'; -- ñ\nSELECT 2"
	stmts := Split(src)
	require.Len(t, stmts, 2)

	u16 := utf16.Encode([]rune(src))
	for _, s := range stmts {
		got := string(utf16.Decode(u16[s.Start:s.End]))
		require.Equal(t, s.Text, got)
		require.Equal(t, s.Text, src[s.ByteStart:s.ByteEnd])
	}
	// La primera sentencia contiene un emoji (2 unidades UTF-16): "SELECT '😀'" = 11 unidades.
	require.Equal(t, 11, stmts[0].End)
}

func TestAt(t *testing.T) {
	src := "SELECT 1;\n\nSELECT 2;"
	stmts := Split(src)
	at := func(off int) string { s, _ := At(stmts, off); return s.Text }

	require.Equal(t, "SELECT 1", at(0))
	require.Equal(t, "SELECT 1", at(8))
	require.Equal(t, "SELECT 1", at(9), "justo tras el ; sigue siendo la anterior")
	require.Equal(t, "SELECT 1", at(10), "hueco en blanco")
	require.Equal(t, "SELECT 2", at(11))
	require.Equal(t, "SELECT 2", at(999))
	_, ok := At(nil, 0)
	require.False(t, ok)
}

func TestFirstKeyword(t *testing.T) {
	cases := map[string]string{
		"select 1":                     "SELECT",
		"  \n -- c\n /* x */ Insert x": "INSERT",
		"(SELECT 1) UNION (SELECT 2)":  "SELECT",
		"# c\nshow tables":             "SHOW",
		"":                             "",
		"-- solo":                      "",
	}
	for in, want := range cases {
		require.Equal(t, want, FirstKeyword(in), in)
	}
	require.True(t, ReturnsRows("SELECT"))
	require.True(t, ReturnsRows("CALL"))
	require.False(t, ReturnsRows("INSERT"))
	require.True(t, IsDDL("ALTER"))
	require.False(t, IsDDL("SELECT"))
}

func FuzzSplit(f *testing.F) {
	for _, s := range []string{"", "SELECT 1;", "'a;b'", "/* x", "-- y\n;", "DELIMITER $$\nA$$", "`a``b`;`", "😀;ñ", `'\`, "delimiter ", "--"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		stmts := Split(src)
		prevEnd := 0
		for _, s := range stmts {
			require.GreaterOrEqual(t, s.ByteStart, prevEnd)
			require.Less(t, s.ByteStart, s.ByteEnd)
			require.LessOrEqual(t, s.ByteEnd, len(src))
			require.Equal(t, src[s.ByteStart:s.ByteEnd], s.Text)
			require.Equal(t, strings.Trim(s.Text, " \t\r\n\f\v"), s.Text) // solo espacio ASCII, como MySQL
			require.Less(t, s.Start, s.End)
			prevEnd = s.ByteEnd
		}
		// Idempotencia: cada sentencia, separada de nuevo, es ella misma si no usa DELIMITER.
		if !strings.Contains(strings.ToLower(src), "delimiter") {
			for _, s := range stmts {
				// "--" pegado al delimitador es código, pero al final del texto es comentario: puede desaparecer.
				again := Split(s.Text)
				require.LessOrEqual(t, len(again), 1, "%q", s.Text)
				if len(again) == 1 {
					require.Equal(t, s.Text, again[0].Text)
				}
			}
		}
	})
}
