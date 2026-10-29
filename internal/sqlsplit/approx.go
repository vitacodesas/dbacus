package sqlsplit

import "strings"

// Wrap devuelve el texto de un SELECT listo para usarlo como tabla derivada —`SELECT * FROM (<inner>) AS _p`— en la
// paginación aproximada (spec 18 §5): sin `;` final y sin comentarios al final que se coman el paréntesis de cierre
// (el llamador pone un salto de línea antes de `)`). Si no se puede envolver devuelve el código del motivo:
// ReasonNotSelect (no empieza por SELECT: SHOW, WITH…, o lleva modificadores que no valen en una tabla derivada),
// ReasonUserLimit (LIMIT propio) o ReasonIntoLock (INTO, FOR UPDATE, LOCK IN SHARE MODE).
func Wrap(stmt string) (inner, reason string) {
	toks := tokenize(stmt)
	end := len(stmt)
	for len(toks) > 0 && toks[len(toks)-1].kind == 'p' && toks[len(toks)-1].text == ";" {
		end = toks[len(toks)-1].start
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 || toks[0].kind != 'w' || toks[0].text != "SELECT" {
		return "", ReasonNotSelect
	}
	if len(toks) > 1 && toks[1].kind == 'w' && selectModifiers[toks[1].text] {
		switch toks[1].text {
		case "ALL", "DISTINCT", "DISTINCTROW": // valen dentro de una tabla derivada
		default:
			return "", ReasonNotSelect
		}
	}
	depth := 0
	for _, t := range toks[1:] {
		switch {
		case t.kind == 'p' && t.text == "(":
			depth++
		case t.kind == 'p' && t.text == ")":
			depth--
		case depth == 0 && t.kind == 'w':
			switch t.text {
			case "LIMIT":
				return "", ReasonUserLimit
			case "INTO", "FOR", "LOCK", "PROCEDURE":
				return "", ReasonIntoLock
			}
		}
	}
	return strings.TrimSpace(stmt[toks[0].start:end]), ""
}
