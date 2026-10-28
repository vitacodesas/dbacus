package sqlsplit

import "strings"

// FirstKeyword devuelve la primera palabra de la sentencia en mayúsculas, saltando espacios, comentarios y '('.
func FirstKeyword(text string) string {
	i := 0
	for i < len(text) {
		c := text[i]
		switch {
		case isSpace(c) || c == '(':
			i++
		case c == '#' || (c == '-' && isDashComment(text, i)):
			i = skipLine(text, i)
		case c == '/' && i+1 < len(text) && text[i+1] == '*':
			i = skipBlockComment(text, i)
		default:
			j := i
			for j < len(text) && (text[j] >= 'a' && text[j] <= 'z' || text[j] >= 'A' && text[j] <= 'Z') {
				j++
			}
			return strings.ToUpper(text[i:j])
		}
	}
	return ""
}

var rowReturning = map[string]bool{
	"SELECT": true, "SHOW": true, "DESCRIBE": true, "DESC": true, "EXPLAIN": true, "WITH": true,
	"VALUES": true, "TABLE": true, "CALL": true, "HELP": true, "CHECK": true, "ANALYZE": true,
	"OPTIMIZE": true, "REPAIR": true, "CHECKSUM": true,
}

var ddl = map[string]bool{"CREATE": true, "ALTER": true, "DROP": true, "RENAME": true, "TRUNCATE": true}

// ReturnsRows indica si la sentencia (por su primera palabra) normalmente produce un conjunto de filas.
func ReturnsRows(keyword string) bool { return rowReturning[keyword] }

// IsDDL indica si la sentencia modifica el esquema (invalida la caché de esquema, spec 05).
func IsDDL(keyword string) bool { return ddl[keyword] }

var readOnlyStart = map[string]bool{
	"SELECT": true, "SHOW": true, "DESCRIBE": true, "DESC": true, "EXPLAIN": true, "USE": true, "WITH": true,
	"VALUES": true, "TABLE": true, "HELP": true,
}

// IsReadOnlySafe dice si la sentencia, por su forma, solo lee. Es defensa en profundidad antes de enviarla (spec 10): la
// garantía real es `SET SESSION TRANSACTION READ ONLY` en el servidor. Se rechaza todo lo que no empiece por una palabra
// de lectura, y también un `INTO` de nivel superior (`SELECT … INTO OUTFILE/DUMPFILE` escribe en disco sin ser DML).
// `SET`, `CALL` y `START TRANSACTION` quedan fuera: podrían volver a abrir una transacción de lectura-escritura.
func IsReadOnlySafe(stmt string) bool {
	if !readOnlyStart[FirstKeyword(stmt)] {
		return false
	}
	words, _ := scanCode(stmt)
	for _, w := range words {
		if w.text == "INTO" {
			return false
		}
	}
	return true
}
