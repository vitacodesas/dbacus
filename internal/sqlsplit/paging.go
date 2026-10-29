package sqlsplit

import "strings"

// Motivos por los que un SELECT no se puede paginar por cursor (spec 18 §6). Son códigos estables: los muestra la interfaz.
const (
	ReasonNotSelect        = "not_select"
	ReasonUserLimit        = "user_limit"
	ReasonMultiTable       = "multi_table"
	ReasonGrouped          = "grouped"
	ReasonCompound         = "compound"
	ReasonIntoLock         = "into_lock"
	ReasonOrderUnsupported = "order_by_unsupported"
	ReasonNotSimple        = "not_simple"
)

// OrderItem es una columna del ORDER BY del usuario.
type OrderItem struct {
	Column string
	Desc   bool
}

// SelectParts son los tramos de un SELECT de una tabla que hacen falta para reescribirlo página a página (spec 18 §4).
// Los textos van tal como se escribieron (con sus comentarios) para poder recomponer la consulta sin alterarla.
type SelectParts struct {
	SelectSource
	List  string      // lista de columnas, entre SELECT y FROM
	From  string      // referencia a la tabla: `t` o `db`.`t`
	Where string      // expresión del WHERE sin la palabra WHERE; "" si no hay
	Order []OrderItem // ORDER BY del usuario; vacío si no hay
}

// SplitSelect descompone un SELECT de una sola tabla (sin alias, JOIN, UNION, GROUP BY, DISTINCT, LIMIT, INTO, FOR/LOCK)
// en sus tramos. Si no es paginable devuelve el código del motivo (Reason*) y partes vacías.
func SplitSelect(stmt string) (SelectParts, string) {
	toks := tokenize(stmt)
	end := len(stmt)
	for len(toks) > 0 && toks[len(toks)-1].kind == 'p' && toks[len(toks)-1].text == ";" {
		end = toks[len(toks)-1].start
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 || toks[0].kind != 'w' || toks[0].text != "SELECT" {
		return SelectParts{}, ReasonNotSelect
	}
	if len(toks) > 1 && toks[1].kind == 'w' && selectModifiers[toks[1].text] {
		if toks[1].text == "DISTINCT" || toks[1].text == "DISTINCTROW" {
			return SelectParts{}, ReasonGrouped
		}
		return SelectParts{}, ReasonNotSimple
	}

	// Lista de columnas hasta el FROM de nivel superior.
	var (
		items  [][]selTok
		cur    []selTok
		depth  int
		fromAt = -1
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
	}
	if fromAt < 0 || depth != 0 || len(cur) == 0 {
		return SelectParts{}, ReasonNotSimple
	}
	items = append(items, cur)

	var parts SelectParts
	for _, it := range items {
		switch {
		case len(it) == 1 && it[0].kind == 'p' && it[0].text == "*",
			len(it) == 3 && isName(it[0]) && it[1].text == "." && it[2].kind == 'p' && it[2].text == "*":
			parts.Star = true
		case plainColumn(it):
			parts.Items = append(parts.Items, SelectItem{Column: nameOf(it[len(it)-1]), Plain: true})
		default:
			parts.Items = append(parts.Items, SelectItem{})
		}
	}
	if parts.Star && len(items) > 1 {
		return SelectParts{}, ReasonNotSimple
	}
	if parts.Star {
		parts.Items = nil
	}
	parts.List = strings.TrimSpace(stmt[toks[0].end:toks[fromAt].start])

	// Tabla: `t` o `db`.`t`.
	rest := toks[fromAt+1:]
	var tblFirst, tblLast selTok
	switch {
	case len(rest) >= 1 && rest[0].kind == 'p' && rest[0].text == "(":
		return SelectParts{}, ReasonMultiTable // subconsulta en FROM
	case len(rest) >= 3 && isName(rest[0]) && rest[1].kind == 'p' && rest[1].text == "." && isName(rest[2]):
		parts.Database, parts.Table = nameOf(rest[0]), nameOf(rest[2])
		tblFirst, tblLast = rest[0], rest[2]
		rest = rest[3:]
	case len(rest) >= 1 && isName(rest[0]):
		parts.Table = nameOf(rest[0])
		tblFirst, tblLast = rest[0], rest[0]
		rest = rest[1:]
	default:
		return SelectParts{}, ReasonNotSimple
	}
	parts.From = stmt[tblFirst.start:tblLast.end]

	// Recorrido de nivel superior tras la tabla: descalificadores y posición de cada cláusula.
	var (
		whereAt, orderAt = -1, -1
		d                int
	)
	for i, t := range rest {
		switch {
		case t.kind == 'p' && t.text == "(":
			d++
			continue
		case t.kind == 'p' && t.text == ")":
			d--
			continue
		case d != 0:
			continue
		case t.kind == 'p' && t.text == ",":
			if whereAt < 0 && orderAt < 0 { // FROM a, b; las comas del ORDER BY son legítimas
				return SelectParts{}, ReasonMultiTable
			}
			continue
		case t.kind != 'w':
			if i == 0 {
				return SelectParts{}, ReasonNotSimple // alias entre comillas
			}
			continue
		}
		next := ""
		if i+1 < len(rest) && rest[i+1].kind == 'w' {
			next = rest[i+1].text
		}
		switch t.text {
		case "WHERE":
			if whereAt < 0 {
				whereAt = i
			}
		case "ORDER":
			if next == "BY" && orderAt < 0 {
				orderAt = i
			}
		case "LIMIT":
			return SelectParts{}, ReasonUserLimit
		case "JOIN", "STRAIGHT_JOIN":
			return SelectParts{}, ReasonMultiTable
		case "INNER", "CROSS", "NATURAL":
			if next == "JOIN" || next == "LEFT" || next == "RIGHT" {
				return SelectParts{}, ReasonMultiTable
			}
		case "LEFT", "RIGHT": // también son funciones: `LEFT(nombre, 1)`
			if next == "JOIN" || next == "OUTER" {
				return SelectParts{}, ReasonMultiTable
			}
		case "UNION", "INTERSECT", "EXCEPT":
			return SelectParts{}, ReasonCompound
		case "GROUP", "HAVING", "WINDOW":
			return SelectParts{}, ReasonGrouped
		case "INTO", "FOR", "LOCK", "PROCEDURE":
			return SelectParts{}, ReasonIntoLock
		default:
			if i == 0 {
				return SelectParts{}, ReasonNotSimple // alias de tabla u otra palabra inesperada
			}
		}
	}
	if d != 0 {
		return SelectParts{}, ReasonNotSimple
	}
	if len(rest) > 0 && whereAt != 0 && orderAt != 0 {
		return SelectParts{}, ReasonNotSimple
	}

	if whereAt >= 0 {
		stop := end
		if orderAt >= 0 {
			stop = rest[orderAt].start
		}
		parts.Where = strings.TrimSpace(stmt[rest[whereAt].end:stop])
		if parts.Where == "" {
			return SelectParts{}, ReasonNotSimple
		}
	}
	if orderAt >= 0 {
		order, ok := parseOrderBy(rest[orderAt+2:], parts.SelectSource)
		if !ok {
			return SelectParts{}, ReasonOrderUnsupported
		}
		parts.Order = order
	}
	return parts, ""
}

// parseOrderBy lee `col [ASC|DESC], …` con columnas desnudas (`c`, `t.c`, `db.t.c`). Cualquier otra cosa —posiciones,
// expresiones, funciones, COLLATE— devuelve false.
func parseOrderBy(toks []selTok, src SelectSource) ([]OrderItem, bool) {
	var (
		out   []OrderItem
		cur   []selTok
		depth int
	)
	flush := func() bool {
		item, ok := orderItem(cur, src)
		cur = nil
		if !ok {
			return false
		}
		out = append(out, item)
		return true
	}
	for _, t := range toks {
		switch {
		case t.kind == 'p' && t.text == "(":
			depth++
			cur = append(cur, t)
		case t.kind == 'p' && t.text == ")":
			depth--
			cur = append(cur, t)
		case depth == 0 && t.kind == 'p' && t.text == ",":
			if !flush() {
				return nil, false
			}
		default:
			cur = append(cur, t)
		}
	}
	if depth != 0 || !flush() {
		return nil, false
	}
	return out, true
}

func orderItem(it []selTok, src SelectSource) (OrderItem, bool) {
	var item OrderItem
	if n := len(it); n >= 2 && it[n-1].kind == 'w' && (it[n-1].text == "ASC" || it[n-1].text == "DESC") && isName(it[n-2]) {
		item.Desc = it[n-1].text == "DESC"
		it = it[:n-1]
	}
	if !plainColumn(it) {
		return OrderItem{}, false
	}
	// `t.c` o `db.t.c`: el calificador debe ser la propia tabla.
	switch len(it) {
	case 3:
		if !strings.EqualFold(nameOf(it[0]), src.Table) {
			return OrderItem{}, false
		}
	case 5:
		if !strings.EqualFold(nameOf(it[2]), src.Table) || (src.Database != "" && !strings.EqualFold(nameOf(it[0]), src.Database)) {
			return OrderItem{}, false
		}
	}
	item.Column = nameOf(it[len(it)-1])
	return item, true
}
