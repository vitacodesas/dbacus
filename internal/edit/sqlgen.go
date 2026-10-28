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

// Tipos de sentencia generada.
const (
	KindUpdate = "update"
	KindInsert = "insert"
	KindDelete = "delete"
)

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

// NewCell es el valor que el usuario dio a una celda de una fila nueva (Data del buffer).
type NewCell struct {
	Column string // columna de la tabla
	Value  Value
	Arg    any
}

// VerifyCol es un valor original que debe seguir igual en el servidor para poder borrar la fila (control optimista).
type VerifyCol struct {
	Column string
	Arg    any
	Null   bool
}

// DeleteChange es una fila marcada para borrar, con lo capturado al marcarla (Data del buffer).
type DeleteChange struct {
	Row    int
	Key    []any // valores de la clave, en el orden de Plan.KeyTableCols
	Verify []VerifyCol
}

// Statement es el UPDATE de una fila: SQL con parámetros para ejecutar y Review con los valores a la vista para que el
// usuario lo revise (nunca se ejecuta el Review).
type Statement struct {
	Kind   string // KindUpdate | KindInsert | KindDelete
	Key    []any  // clave de la fila (update y delete), para comprobar si sigue existiendo tras un conflicto
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
		out = append(out, Statement{Kind: KindUpdate, Key: cs[0].Key, Row: r, Cols: cols, SQL: sql, Args: all, Review: render(q, sql, all)})
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

// InsertCell es el valor de una columna de una fila nueva.
type InsertCell struct {
	Col    int
	Column string
	Arg    any
}

// InsertRow es una fila nueva con las celdas que el usuario rellenó (las demás valen su DEFAULT).
type InsertRow struct {
	Row   int
	Cells []InsertCell
}

// BuildInserts genera un INSERT por fila nueva, en orden, solo con las columnas rellenadas. Sin ninguna, `() VALUES ()`
// (todo a su DEFAULT).
func BuildInserts(q Quoter, db, table string, rows []InsertRow) []Statement {
	target := q.QuoteIdent(db) + "." + q.QuoteIdent(table)
	out := make([]Statement, 0, len(rows))
	for _, r := range rows {
		cols := make([]string, len(r.Cells))
		marks := make([]string, len(r.Cells))
		args := make([]any, len(r.Cells))
		idx := make([]int, len(r.Cells))
		for i, c := range r.Cells {
			cols[i], marks[i], args[i], idx[i] = q.QuoteIdent(c.Column), "?", c.Arg, c.Col
		}
		sql := "INSERT INTO " + target + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
		out = append(out, Statement{Kind: KindInsert, Row: r.Row, Cols: idx, SQL: sql, Args: args, Review: render(q, sql, args)})
	}
	return out
}

// BuildDeletes genera un DELETE por fila marcada, ordenado por fila:
//
//	DELETE FROM `db`.`t` WHERE `id` = ? AND `a` <=> ? LIMIT 1
//
// Como en el UPDATE, el WHERE lleva la clave y los valores originales verificables: si otra sesión cambió la fila, no
// coincide y se informa como conflicto en vez de borrar algo distinto de lo que se vio.
func BuildDeletes(q Quoter, db, table string, keyCols []string, ds []DeleteChange) []Statement {
	sorted := append([]DeleteChange(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Row < sorted[j].Row })
	target := q.QuoteIdent(db) + "." + q.QuoteIdent(table)
	out := make([]Statement, 0, len(sorted))
	for _, d := range sorted {
		var where []string
		var args []any
		for i, k := range keyCols {
			where = append(where, q.QuoteIdent(k)+" = ?")
			args = append(args, d.Key[i])
		}
		for _, v := range d.Verify {
			where = append(where, q.QuoteIdent(v.Column)+" <=> ?")
			if v.Null {
				args = append(args, nil)
			} else {
				args = append(args, v.Arg)
			}
		}
		sql := "DELETE FROM " + target + " WHERE " + strings.Join(where, " AND ") + " LIMIT 1"
		out = append(out, Statement{Kind: KindDelete, Key: d.Key, Row: d.Row, SQL: sql, Args: args, Review: render(q, sql, args)})
	}
	return out
}
