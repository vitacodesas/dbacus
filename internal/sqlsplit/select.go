package sqlsplit

import "strings"

// SelectItem es un elemento de la lista de columnas de un SELECT. Plain: columna simple (`c`, `t.c`, `db.t.c`) sin alias
// ni expresión; solo esas se pueden mapear a una columna de la tabla.
type SelectItem struct {
	Column string
	Plain  bool
}

// SelectSource describe un SELECT de una sola tabla (spec 13): de qué tabla lee y qué columnas devuelve.
type SelectSource struct {
	Database string // vacío si la sentencia no la nombra (se usa la base de la ejecución)
	Table    string
	Star     bool         // la lista es `*` o `t.*`
	Items    []SelectItem // vacío si Star
}

type selTok struct {
	kind byte // 'w' palabra (text en mayúsculas), 'i' identificador entre backticks, 's' cadena, 'n' número, 'p' puntuación
	text string
	raw  string // para 'w': tal como se escribió
}

// tokenize parte una sentencia en tokens ignorando comentarios. No valida SQL: solo lo necesario para SimpleSelectTable.
func tokenize(src string) []selTok {
	var out []selTok
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case isSpace(c):
			i++
		case c == '#' || (c == '-' && isDashComment(src, i)):
			i = skipLine(src, i)
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = skipBlockComment(src, i)
		case c == '\'' || c == '"':
			j := skipQuoted(src, i, c, true)
			out = append(out, selTok{kind: 's', text: src[i:j]})
			i = j
		case c == '`':
			j := skipQuoted(src, i, c, false)
			name := src[i+1 : j]
			if strings.HasSuffix(name, "`") {
				name = name[:len(name)-1]
			}
			out = append(out, selTok{kind: 'i', text: strings.ReplaceAll(name, "``", "`")})
			i = j
		case isWordStart(c):
			j := i
			for j < len(src) && isWordChar(src[j]) {
				j++
			}
			out = append(out, selTok{kind: 'w', text: upper(src[i:j]), raw: src[i:j]})
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < len(src) && (isWordChar(src[j]) || src[j] == '.') {
				j++
			}
			out = append(out, selTok{kind: 'n', text: src[i:j]})
			i = j
		default:
			out = append(out, selTok{kind: 'p', text: string(c)})
			i++
		}
	}
	return out
}

var selectModifiers = map[string]bool{
	"ALL": true, "DISTINCT": true, "DISTINCTROW": true, "HIGH_PRIORITY": true, "STRAIGHT_JOIN": true, "SQL_SMALL_RESULT": true,
	"SQL_BIG_RESULT": true, "SQL_BUFFER_RESULT": true, "SQL_NO_CACHE": true, "SQL_CACHE": true, "SQL_CALC_FOUND_ROWS": true,
}

// Palabras de nivel superior tras la tabla que la convierten en algo que no es «una fila = una fila de la tabla».
var notSingleTable = map[string]bool{
	"JOIN": true, "INNER": true, "CROSS": true, "LEFT": true, "RIGHT": true, "NATURAL": true, "STRAIGHT_JOIN": true, "UNION": true,
	"INTERSECT": true, "EXCEPT": true, "GROUP": true, "HAVING": true, "INTO": true, "FOR": true, "LOCK": true, "WINDOW": true,
	"PROCEDURE": true,
}

func isName(t selTok) bool { return t.kind == 'w' || t.kind == 'i' }

func nameOf(t selTok) string {
	if t.kind == 'w' {
		return t.raw
	}
	return t.text
}

// SimpleSelectTable reconoce un SELECT de **una sola tabla** y devuelve de cuál lee (spec 13). Devuelve false si hay JOIN,
// UNION, subconsulta en FROM, alias de tabla, DISTINCT, GROUP BY/HAVING, INTO, FOR UPDATE, etc.: en esos casos las filas del
// resultado no se corresponden una a una con filas de una tabla y no se pueden editar.
func SimpleSelectTable(stmt string) (SelectSource, bool) {
	toks := tokenize(stmt)
	for len(toks) > 0 && toks[len(toks)-1].kind == 'p' && toks[len(toks)-1].text == ";" {
		toks = toks[:len(toks)-1]
	}
	if len(toks) < 4 || toks[0].kind != 'w' || toks[0].text != "SELECT" {
		return SelectSource{}, false
	}
	if toks[1].kind == 'w' && selectModifiers[toks[1].text] {
		return SelectSource{}, false
	}

	// Lista de columnas hasta el FROM de nivel superior.
	var (
		items    [][]selTok
		cur      []selTok
		depth    int
		fromAt   = -1
		sawStart bool
	)
	for i := 1; i < len(toks) && fromAt < 0; i++ {
		t := toks[i]
		switch {
		case t.kind == 'p' && t.text == "(":
			depth++
			cur = append(cur, t)
		case t.kind == 'p' && t.text == ")":
			depth--
			cur = append(cur, t)
		case depth == 0 && t.kind == 'p' && t.text == ",":
			items, cur = append(items, cur), nil
		case depth == 0 && t.kind == 'w' && t.text == "FROM":
			fromAt = i
		default:
			cur = append(cur, t)
		}
		sawStart = true
	}
	if fromAt < 0 || !sawStart || depth != 0 || len(cur) == 0 {
		return SelectSource{}, false
	}
	items = append(items, cur)

	var src SelectSource
	for _, it := range items {
		switch {
		case len(it) == 1 && it[0].kind == 'p' && it[0].text == "*",
			len(it) == 3 && isName(it[0]) && it[1].text == "." && it[2].kind == 'p' && it[2].text == "*":
			src.Star = true
		case plainColumn(it):
			src.Items = append(src.Items, SelectItem{Column: nameOf(it[len(it)-1]), Plain: true})
		default:
			src.Items = append(src.Items, SelectItem{})
		}
	}
	if src.Star && len(items) > 1 {
		return SelectSource{}, false
	}
	if src.Star {
		src.Items = nil
	}

	// Tabla: `t` o `db`.`t`, y a continuación solo fin, WHERE, ORDER o LIMIT (nada de alias, JOIN ni comas).
	rest := toks[fromAt+1:]
	switch {
	case len(rest) >= 3 && isName(rest[0]) && rest[1].kind == 'p' && rest[1].text == "." && isName(rest[2]):
		src.Database, src.Table = nameOf(rest[0]), nameOf(rest[2])
		rest = rest[3:]
	case len(rest) >= 1 && isName(rest[0]):
		src.Table = nameOf(rest[0])
		rest = rest[1:]
	default:
		return SelectSource{}, false
	}
	if len(rest) > 0 {
		next := rest[0]
		if next.kind != 'w' || (next.text != "WHERE" && next.text != "ORDER" && next.text != "LIMIT") {
			return SelectSource{}, false
		}
	}
	depth = 0
	for _, t := range rest {
		switch {
		case t.kind == 'p' && t.text == "(":
			depth++
		case t.kind == 'p' && t.text == ")":
			depth--
		case depth == 0 && t.kind == 'w' && notSingleTable[t.text]:
			return SelectSource{}, false
		}
	}
	return src, depth == 0
}

// plainColumn: `c`, `t.c` o `db.t.c` (cada parte un nombre) y nada más: sin alias ni expresiones.
func plainColumn(it []selTok) bool {
	if len(it) == 0 || len(it)%2 == 0 || len(it) > 5 {
		return false
	}
	for i, t := range it {
		if i%2 == 0 {
			if !isName(t) {
				return false
			}
		} else if t.kind != 'p' || t.text != "." {
			return false
		}
	}
	return true
}
