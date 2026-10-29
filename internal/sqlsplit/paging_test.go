package sqlsplit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitSelectParts(t *testing.T) {
	cases := []struct {
		name  string
		sql   string
		want  SelectParts
		order []OrderItem
	}{
		{"estrella sin nada", "SELECT * FROM actor", SelectParts{SelectSource: SelectSource{Table: "actor", Star: true}, List: "*", From: "actor"}, nil},
		{"punto y coma final", "SELECT * FROM actor;", SelectParts{SelectSource: SelectSource{Table: "actor", Star: true}, List: "*", From: "actor"}, nil},
		{"base y tabla con comillas", "select id,name from `sakila`.`actor` where id>1",
			SelectParts{SelectSource: SelectSource{Database: "sakila", Table: "actor", Items: []SelectItem{{Column: "id", Plain: true}, {Column: "name", Plain: true}}},
				List: "id,name", From: "`sakila`.`actor`", Where: "id>1"}, nil},
		{"WHERE y ORDER BY con direcciones", "SELECT * FROM t WHERE a = 1 AND b < 5 ORDER BY a DESC, b asc, c",
			SelectParts{SelectSource: SelectSource{Table: "t", Star: true}, List: "*", From: "t", Where: "a = 1 AND b < 5"},
			[]OrderItem{{"a", true}, {"b", false}, {"c", false}}},
		{"ORDER BY calificado", "SELECT * FROM t ORDER BY t.price DESC", SelectParts{SelectSource: SelectSource{Table: "t", Star: true}, List: "*", From: "t"}, []OrderItem{{"price", true}}},
		{"ORDER BY con base", "SELECT * FROM db.t ORDER BY db.t.price", SelectParts{SelectSource: SelectSource{Database: "db", Table: "t", Star: true}, List: "*", From: "db.t"}, []OrderItem{{"price", false}}},
		{"columna ordenada con comillas y palabra reservada", "SELECT * FROM t ORDER BY `desc` DESC", SelectParts{SelectSource: SelectSource{Table: "t", Star: true}, List: "*", From: "t"}, []OrderItem{{"desc", true}}},
		{"lista con expresión y alias sigue siendo paginable", "SELECT id, UPPER(name) AS n FROM t",
			SelectParts{SelectSource: SelectSource{Table: "t", Items: []SelectItem{{Column: "id", Plain: true}, {}}}, List: "id, UPPER(name) AS n", From: "t"}, nil},
		{"palabras clave dentro de cadenas y comillas no cuentan", "SELECT * FROM t WHERE note = 'ORDER BY x LIMIT 5 UNION' AND `group` = 1",
			SelectParts{SelectSource: SelectSource{Table: "t", Star: true}, List: "*", From: "t", Where: "note = 'ORDER BY x LIMIT 5 UNION' AND `group` = 1"}, nil},
		{"subconsulta con ORDER BY y LIMIT dentro del WHERE", "SELECT * FROM t WHERE id IN (SELECT id FROM u ORDER BY id LIMIT 3) ORDER BY id",
			SelectParts{SelectSource: SelectSource{Table: "t", Star: true}, List: "*", From: "t", Where: "id IN (SELECT id FROM u ORDER BY id LIMIT 3)"}, []OrderItem{{"id", false}}},
		{"LEFT y RIGHT como funciones no son JOIN", "SELECT * FROM t WHERE LEFT(name, 1) = 'a' AND RIGHT(name, 1) = 'z'",
			SelectParts{SelectSource: SelectSource{Table: "t", Star: true}, List: "*", From: "t", Where: "LEFT(name, 1) = 'a' AND RIGHT(name, 1) = 'z'"}, nil},
		{"comentario de línea al final del WHERE se conserva", "SELECT * FROM t WHERE a = 1 -- solo activos\nORDER BY a",
			SelectParts{SelectSource: SelectSource{Table: "t", Star: true}, List: "*", From: "t", Where: "a = 1 -- solo activos"}, []OrderItem{{"a", false}}},
		{"comentario de bloque en medio", "SELECT * FROM t WHERE /* filtro */ a = 1 ORDER BY a",
			SelectParts{SelectSource: SelectSource{Table: "t", Star: true}, List: "*", From: "t", Where: "/* filtro */ a = 1"}, []OrderItem{{"a", false}}},
		{"saltos de línea y tabuladores", "SELECT\n  *\nFROM\n  t\nWHERE\n  a = 1\nORDER\tBY\n  a", SelectParts{SelectSource: SelectSource{Table: "t", Star: true}, List: "*", From: "t", Where: "a = 1"}, []OrderItem{{"a", false}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := SplitSelect(c.sql)
			require.Empty(t, reason)
			c.want.Order = c.order
			require.Equal(t, c.want, got)
		})
	}
}

func TestSplitSelectReasons(t *testing.T) {
	cases := []struct{ sql, reason string }{
		{"INSERT INTO t VALUES (1)", ReasonNotSelect},
		{"SHOW TABLES", ReasonNotSelect},
		{"WITH c AS (SELECT 1) SELECT * FROM c", ReasonNotSelect},
		{"(SELECT * FROM t)", ReasonNotSelect},
		{"", ReasonNotSelect},
		{"SELECT 1", ReasonNotSimple},
		{"SELECT DISTINCT a FROM t", ReasonGrouped},
		{"SELECT SQL_CALC_FOUND_ROWS * FROM t", ReasonNotSimple},
		{"SELECT * FROM t LIMIT 10", ReasonUserLimit},
		{"SELECT * FROM t ORDER BY a LIMIT 10 OFFSET 5", ReasonUserLimit},
		{"SELECT * FROM t JOIN u ON t.id = u.id", ReasonMultiTable},
		{"SELECT * FROM t LEFT JOIN u ON t.id = u.id", ReasonMultiTable},
		{"SELECT * FROM t LEFT OUTER JOIN u ON t.id = u.id", ReasonMultiTable},
		{"SELECT * FROM t INNER JOIN u ON 1", ReasonMultiTable},
		{"SELECT * FROM t NATURAL JOIN u", ReasonMultiTable},
		{"SELECT * FROM t, u", ReasonMultiTable},
		{"SELECT * FROM (SELECT * FROM t) x", ReasonMultiTable},
		{"SELECT * FROM t x", ReasonNotSimple},
		{"SELECT * FROM t AS x", ReasonNotSimple},
		{"SELECT * FROM t UNION SELECT * FROM u", ReasonCompound},
		{"SELECT * FROM t WHERE a = 1 UNION ALL SELECT * FROM u", ReasonCompound},
		{"SELECT a FROM t GROUP BY a", ReasonGrouped},
		{"SELECT a FROM t GROUP BY a HAVING COUNT(*) > 1", ReasonGrouped},
		{"SELECT * FROM t INTO OUTFILE '/tmp/x'", ReasonIntoLock},
		{"SELECT * FROM t WHERE id = 1 FOR UPDATE", ReasonIntoLock},
		{"SELECT * FROM t LOCK IN SHARE MODE", ReasonIntoLock},
		{"SELECT * FROM t ORDER BY 2", ReasonOrderUnsupported},
		{"SELECT * FROM t ORDER BY a + 1", ReasonOrderUnsupported},
		{"SELECT * FROM t ORDER BY RAND()", ReasonOrderUnsupported},
		{"SELECT * FROM t ORDER BY name COLLATE utf8mb4_bin", ReasonOrderUnsupported},
		{"SELECT * FROM t ORDER BY u.a", ReasonOrderUnsupported},
		{"SELECT * FROM t ORDER BY a,", ReasonOrderUnsupported},
		{"SELECT * FROM t ORDER BY", ReasonOrderUnsupported},
		{"SELECT * FROM t WHERE", ReasonNotSimple},
		{"SELECT * FROM t ORDER", ReasonNotSimple},
		{"SELECT * FROM", ReasonNotSimple},
		{"SELECT * FROM t WHERE (a = 1", ReasonNotSimple},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			got, reason := SplitSelect(c.sql)
			require.Equal(t, c.reason, reason)
			require.Equal(t, SelectParts{}, got, "sin motivo no hay partes")
		})
	}
}

// El texto recompuesto debe volver a ser la misma consulta: es lo que usa la reescritura página a página.
func TestSplitSelectPartsRecomposeTheOriginal(t *testing.T) {
	sql := "SELECT id, name FROM `db`.`t` WHERE a = 1 AND b IN (1,2) ORDER BY id DESC"
	p, reason := SplitSelect(sql)
	require.Empty(t, reason)
	require.Equal(t, "SELECT "+p.List+" FROM "+p.From+" WHERE "+p.Where+" ORDER BY id DESC", sql)
}
