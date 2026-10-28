package sqlsplit

import "strconv"

type word struct {
	text       string // en mayúsculas
	start, end int
}

// scanCode recorre una sentencia ignorando strings, identificadores con backticks y comentarios.
// Devuelve las palabras de nivel superior (fuera de paréntesis) y dónde termina el último carácter de código.
func scanCode(text string) (words []word, lastCodeEnd int) {
	depth := 0
	i := 0
	for i < len(text) {
		c := text[i]
		switch {
		case isSpace(c):
			i++
		case c == '\'' || c == '"':
			i = skipQuoted(text, i, c, true)
			lastCodeEnd = i
		case c == '`':
			i = skipQuoted(text, i, c, false)
			lastCodeEnd = i
		case c == '#' || (c == '-' && isDashComment(text, i)):
			i = skipLine(text, i)
		case c == '/' && i+1 < len(text) && text[i+1] == '*':
			i = skipBlockComment(text, i)
		case c == '(':
			depth++
			i++
			lastCodeEnd = i
		case c == ')':
			if depth > 0 {
				depth--
			}
			i++
			lastCodeEnd = i
		case isWordStart(c):
			j := i
			for j < len(text) && isWordChar(text[j]) {
				j++
			}
			if depth == 0 {
				words = append(words, word{upper(text[i:j]), i, j})
			}
			i = j
			lastCodeEnd = j
		default:
			i++
			lastCodeEnd = i
		}
	}
	return words, lastCodeEnd
}

func isWordStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
func isWordChar(c byte) bool { return isWordStart(c) || c >= '0' && c <= '9' || c == '$' }

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// Palabras de nivel superior que impiden añadir un LIMIT de forma segura.
var noLimit = map[string]bool{"LIMIT": true, "UNION": true, "INTERSECT": true, "EXCEPT": true, "INTO": true, "FOR": true, "LOCK": true, "PROCEDURE": true}

// InjectLimit añade `LIMIT n` a un SELECT simple que no lo tiene (spec 06). Devuelve false si la sentencia no es un SELECT
// simple (WITH, UNION, SELECT ... INTO, FOR UPDATE, ya con LIMIT, etc.): en ese caso se ejecuta tal cual.
// El LIMIT se coloca tras el último carácter de código para no quedar dentro de un comentario final.
func InjectLimit(stmt string, n int) (string, bool) {
	if n <= 0 {
		return stmt, false
	}
	words, end := scanCode(stmt)
	if len(words) == 0 || words[0].text != "SELECT" {
		return stmt, false
	}
	for _, w := range words[1:] {
		if noLimit[w.text] {
			return stmt, false
		}
	}
	return stmt[:end] + " LIMIT " + strconv.Itoa(n) + stmt[end:], true
}
