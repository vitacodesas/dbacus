package sqlsplit

import (
	"reflect"
	"testing"
)

func TestSimpleSelectTable(t *testing.T) {
	plain := func(cols ...string) []SelectItem {
		out := make([]SelectItem, len(cols))
		for i, c := range cols {
			out[i] = SelectItem{Column: c, Plain: true}
		}
		return out
	}
	cases := []struct {
		name string
		sql  string
		want SelectSource
		ok   bool
	}{
		{"star", "SELECT * FROM t", SelectSource{Table: "t", Star: true}, true},
		{"con base", "select * from `db`.`t`;", SelectSource{Database: "db", Table: "t", Star: true}, true},
		{"identificador con punto y backtick", "SELECT * FROM `a.b`.`c``d`", SelectSource{Database: "a.b", Table: "c`d", Star: true}, true},
		{"tabla.*", "SELECT t.* FROM t", SelectSource{Table: "t", Star: true}, true},
		{"columnas", "SELECT id, `name` FROM users", SelectSource{Table: "users", Items: plain("id", "name")}, true},
		{"columnas calificadas", "SELECT users.id, db.users.name FROM users", SelectSource{Table: "users", Items: plain("id", "name")}, true},
		{"WHERE ORDER LIMIT", "SELECT * FROM t WHERE a = 1 AND b IN (SELECT x FROM y JOIN z) ORDER BY a DESC, b LIMIT 10 OFFSET 5", SelectSource{Table: "t", Star: true}, true},
		{"comentarios", "/* c */ SELECT * FROM t -- fin\n WHERE a = 'FROM x JOIN y'", SelectSource{Table: "t", Star: true}, true},
		{"expresión no plana", "SELECT id, UPPER(name), price * 2 FROM t", SelectSource{Table: "t", Items: []SelectItem{{Column: "id", Plain: true}, {}, {}}}, true},
		{"alias de columna no es plana", "SELECT id AS x FROM t", SelectSource{Table: "t", Items: []SelectItem{{}}}, true},
		{"coma dentro de función", "SELECT CONCAT(a, b), c FROM t", SelectSource{Table: "t", Items: []SelectItem{{}, {Column: "c", Plain: true}}}, true},

		{"JOIN", "SELECT * FROM a JOIN b ON a.id = b.id", SelectSource{}, false},
		{"LEFT JOIN", "SELECT * FROM a LEFT JOIN b USING (id)", SelectSource{}, false},
		{"varias tablas con coma", "SELECT * FROM a, b", SelectSource{}, false},
		{"alias de tabla", "SELECT * FROM a x", SelectSource{}, false},
		{"alias de tabla con AS", "SELECT * FROM a AS x", SelectSource{}, false},
		{"UNION", "SELECT * FROM a UNION SELECT * FROM b", SelectSource{}, false},
		{"GROUP BY", "SELECT a FROM t GROUP BY a", SelectSource{}, false},
		{"HAVING", "SELECT a FROM t WHERE 1 HAVING a > 1", SelectSource{}, false},
		{"DISTINCT", "SELECT DISTINCT a FROM t", SelectSource{}, false},
		{"subconsulta en FROM", "SELECT * FROM (SELECT * FROM t) s", SelectSource{}, false},
		{"INTO", "SELECT * FROM t INTO OUTFILE '/tmp/x'", SelectSource{}, false},
		{"FOR UPDATE", "SELECT * FROM t FOR UPDATE", SelectSource{}, false},
		{"estrella mezclada", "SELECT *, 1 FROM t", SelectSource{}, false},
		{"sin FROM", "SELECT 1", SelectSource{}, false},
		{"no es SELECT", "UPDATE t SET a = 1", SelectSource{}, false},
		{"WITH", "WITH c AS (SELECT 1) SELECT * FROM c", SelectSource{}, false},
		{"vacío", "", SelectSource{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := SimpleSelectTable(c.sql)
			if ok != c.ok {
				t.Fatalf("SimpleSelectTable(%q) ok = %v, quiero %v (devolvió %+v)", c.sql, ok, c.ok, got)
			}
			if ok && !reflect.DeepEqual(got, c.want) {
				t.Fatalf("SimpleSelectTable(%q) = %+v, quiero %+v", c.sql, got, c.want)
			}
		})
	}
}
