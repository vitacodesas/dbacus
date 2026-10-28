package edit

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Quoter escapa identificadores y literales según el motor (lo implementa driver.Driver).
type Quoter interface {
	QuoteIdent(name string) string
	QuoteString(s string) string
}

// Change es un cambio de celda pendiente, ya validado y convertido (solo vive en Go).
type Change struct {
	Row, Col int
	Column   string // columna de la tabla
	Value    Value
	Arg      any // valor nuevo como parámetro
	// OrigArg/OrigNull son el valor que tenía la celda al cargarse; Verify indica si se comprueba en el WHERE.
	OrigArg  any
	OrigNull bool
	Verify   bool
	// Key son los valores de la clave de la fila, en el orden de Plan.KeyTableCols, capturados al cargarse el resultado.
	Key []any
}

// Statement es el UPDATE de una fila: SQL con parámetros para ejecutar y Review con los valores a la vista para que el
// usuario lo revise (nunca se ejecuta el Review).
type Statement struct {
	Row    int
	Cols   []int
	SQL    string
	Args   []any
	Review string
}

// BuildUpdates agrupa los cambios por fila y genera un UPDATE por fila (spec 13):
//
//	UPDATE `db`.`t` SET `a` = ?, `b` = ? WHERE `id` = ? AND `a` <=> ? AND `b` <=> ?
//
// El WHERE lleva la clave y, para las columnas verificables, su valor original con `<=>` (igualdad que trata NULL como un
// valor): si otra sesión cambió la fila, no coincide y la edición se rechaza como conflicto en vez de pisar el cambio.
func BuildUpdates(q Quoter, db, table string, keyCols []string, changes []Change) []Statement {
	byRow := map[int][]Change{}
	for _, c := range changes {
		byRow[c.Row] = append(byRow[c.Row], c)
	}
	rows := make([]int, 0, len(byRow))
	for r := range byRow {
		rows = append(rows, r)
	}
	sort.Ints(rows)

	target := q.QuoteIdent(db) + "." + q.QuoteIdent(table)
	out := make([]Statement, 0, len(rows))
	for _, r := range rows {
		cs := byRow[r]
		sort.Slice(cs, func(i, j int) bool { return cs[i].Col < cs[j].Col })

		var set, where []string
		var args, whereArgs []any
		var cols []int
		for _, c := range cs {
			set = append(set, q.QuoteIdent(c.Column)+" = ?")
			args = append(args, c.Arg)
			cols = append(cols, c.Col)
		}
		for i, k := range keyCols {
			where = append(where, q.QuoteIdent(k)+" = ?")
			whereArgs = append(whereArgs, cs[0].Key[i])
		}
		for _, c := range cs {
			if c.Verify {
				where = append(where, q.QuoteIdent(c.Column)+" <=> ?")
				if c.OrigNull {
					whereArgs = append(whereArgs, nil)
				} else {
					whereArgs = append(whereArgs, c.OrigArg)
				}
			}
		}
		sql := "UPDATE " + target + " SET " + strings.Join(set, ", ") + " WHERE " + strings.Join(where, " AND ")
		all := append(args, whereArgs...)
		out = append(out, Statement{Row: r, Cols: cols, SQL: sql, Args: all, Review: render(q, sql, all)})
	}
	return out
}

// render sustituye cada `?` por el literal de su parámetro. Solo se usa para mostrar el SQL al usuario.
func render(q Quoter, sql string, args []any) string {
	var b strings.Builder
	i := 0
	for _, ch := range sql {
		if ch == '?' && i < len(args) {
			b.WriteString(literal(q, args[i]))
			i++
			continue
		}
		b.WriteRune(ch)
	}
	return b.String()
}

func literal(q Quoter, v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return q.QuoteString(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return q.QuoteString(fmt.Sprint(v))
}

// RowSelect genera `SELECT cols FROM db.t WHERE key = lit …` con los valores como literales (se ejecuta tras aplicar para
// releer la fila, o para comprobar si existe). La clave son valores ya tipados por OriginalArg.
func RowSelect(q Quoter, db, table string, cols, keyCols []string, key []any) string {
	sel := "1"
	if len(cols) > 0 {
		quoted := make([]string, len(cols))
		for i, c := range cols {
			quoted[i] = q.QuoteIdent(c)
		}
		sel = strings.Join(quoted, ", ")
	}
	where := make([]string, len(keyCols))
	for i, k := range keyCols {
		where[i] = q.QuoteIdent(k) + " = " + literal(q, key[i])
	}
	return "SELECT " + sel + " FROM " + q.QuoteIdent(db) + "." + q.QuoteIdent(table) + " WHERE " + strings.Join(where, " AND ") + " LIMIT 1"
}
