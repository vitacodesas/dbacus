package sqlsplit

import "strings"

// RoutineHead es la cabecera de un CREATE PROCEDURE/FUNCTION (spec 21). Solo se interpreta la cabecera: el cuerpo
// (BEGIN…END con `;` dentro) nunca pasa por Split.
type RoutineHead struct {
	Kind        string // "procedure" | "function"
	Name        string // sin comillas
	Database    string // "" si el nombre no va calificado
	Definer     string // texto tras «DEFINER =»; "" si no hay cláusula
	IfNotExists bool
	// NameStart/NameEnd delimitan el identificador del nombre (con sus backticks, sin la base): se puede sustituir.
	NameStart, NameEnd int
	// DefinerStart/DefinerEnd delimitan la cláusula «DEFINER = cuenta» completa; -1 si no hay.
	DefinerStart, DefinerEnd int
	OK                       bool
}

func skipBlank(s string, i int) int {
	for i < len(s) {
		c := s[i]
		switch {
		case isSpace(c):
			i++
		case c == '#' || (c == '-' && isDashComment(s, i)):
			i = skipLine(s, i)
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i = skipBlockComment(s, i)
		default:
			return i
		}
	}
	return i
}

// wordAt lee una palabra de letras (para palabras clave) y la devuelve en mayúsculas.
func wordAt(s string, i int) (string, int) {
	j := i
	for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] == '_') {
		j++
	}
	return strings.ToUpper(s[i:j]), j
}

// identAt lee un identificador (con backticks o sin ellos) y devuelve su valor sin comillas y el final del token.
func identAt(s string, i int) (name string, end int, ok bool) {
	if i >= len(s) {
		return "", i, false
	}
	if s[i] == '`' {
		var b strings.Builder
		j := i + 1
		for j < len(s) {
			if s[j] == '`' {
				if j+1 < len(s) && s[j+1] == '`' {
					b.WriteByte('`')
					j += 2
					continue
				}
				return b.String(), j + 1, b.Len() > 0
			}
			b.WriteByte(s[j])
			j++
		}
		return "", len(s), false
	}
	j := i
	for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9' || s[j] == '_' || s[j] == '$' || s[j] >= 0x80) {
		j++
	}
	return s[i:j], j, j > i
}

// ParseRoutineHead interpreta «CREATE [DEFINER = cuenta] PROCEDURE|FUNCTION [IF NOT EXISTS] [base.]nombre». Rechaza
// (OK=false) cualquier otra cosa, incluida CREATE OR REPLACE y las funciones agregadas.
func ParseRoutineHead(s string) RoutineHead {
	h := RoutineHead{DefinerStart: -1, DefinerEnd: -1}
	i := skipBlank(s, 0)
	w, e := wordAt(s, i)
	if w != "CREATE" {
		return h
	}
	i = skipBlank(s, e)
	w, e = wordAt(s, i)
	if w == "DEFINER" {
		start := i
		i = skipBlank(s, e)
		if i >= len(s) || s[i] != '=' {
			return h
		}
		i = skipBlank(s, i+1)
		j := i
		for j < len(s) && !isSpace(s[j]) {
			switch s[j] {
			case '\'', '"':
				j = skipQuoted(s, j, s[j], true)
			case '`':
				j = skipQuoted(s, j, '`', false)
			default:
				j++
			}
		}
		if j == i {
			return h
		}
		h.Definer, h.DefinerStart, h.DefinerEnd = s[i:j], start, j
		i = skipBlank(s, j)
		w, e = wordAt(s, i)
	}
	switch w {
	case "PROCEDURE":
		h.Kind = "procedure"
	case "FUNCTION":
		h.Kind = "function"
	default:
		return h
	}
	i = skipBlank(s, e)
	if w, e = wordAt(s, i); w == "IF" {
		if w2, e2 := wordAt(s, skipBlank(s, e)); w2 == "NOT" {
			if w3, e3 := wordAt(s, skipBlank(s, e2)); w3 == "EXISTS" {
				h.IfNotExists = true
				i = skipBlank(s, e3)
			} else {
				return h
			}
		} else {
			return h
		}
	}
	name, end, ok := identAt(s, i)
	if !ok {
		return h
	}
	start := i
	if end < len(s) && s[end] == '.' {
		n2, e2, ok2 := identAt(s, end+1)
		if !ok2 {
			return h
		}
		h.Database, name, start, end = name, n2, end+1, e2
	}
	h.Name, h.NameStart, h.NameEnd, h.OK = name, start, end, true
	return h
}

// IsRoutineDDL dice si la sentencia crea, modifica o borra un procedimiento o una función (spec 21): el editor de
// consultas la usa para refrescar solo el grupo de rutinas del árbol y no las tablas.
func IsRoutineDDL(stmt string) bool {
	switch FirstKeyword(stmt) {
	case "CREATE":
		return ParseRoutineHead(stmt).OK
	case "ALTER", "DROP":
		i := skipBlank(stmt, 0)
		_, e := wordAt(stmt, i)
		w, _ := wordAt(stmt, skipBlank(stmt, e))
		return w == "PROCEDURE" || w == "FUNCTION"
	}
	return false
}

// HasDelimiterLine dice si el texto contiene una línea «DELIMITER …»: es una orden del cliente `mysql`, no SQL.
func HasDelimiterLine(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if matchDelimiterCommand(strings.TrimSpace(line), 0) {
			return true
		}
	}
	return false
}
