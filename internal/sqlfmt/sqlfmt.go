// Package sqlfmt da un formato legible y estable a sentencias SQL (spec 06). Solo cambia espacios, saltos de línea y
// la capitalización de palabras reservadas: nunca el contenido de strings, identificadores ni comentarios.
package sqlfmt

import (
	"regexp"
	"strings"

	"github.com/vitacodesas/dbacus/internal/sqlsplit"
)

const indent = "  "

type tokKind int

const (
	tWord tokKind = iota
	tNumber
	tString
	tQuoted // `identificador`
	tLineComment
	tBlockComment
	tPunct
)

type token struct {
	kind tokKind
	text string
	up   string // en mayúsculas, solo para tWord
}

var delimiterCmd = regexp.MustCompile(`(?im)^\s*delimiter\s`)

// Format formatea un script completo. Los scripts con DELIMITER (procedimientos) se devuelven tal cual.
// Los comentarios que quedan entre sentencias o al final del script se conservan, cada uno en su línea.
func Format(sql string) string {
	if delimiterCmd.MatchString(sql) {
		return sql
	}
	stmts := sqlsplit.Split(sql)
	var parts []string
	prevEnd := 0
	for _, s := range stmts {
		parts = append(parts, standaloneComments(sql[prevEnd:s.ByteStart])...)
		if hasUnterminated(tokenize(s.Text)) {
			parts = append(parts, s.Text+";")
			prevEnd = s.ByteEnd
			continue
		}
		parts = append(parts, terminate(s.Text))
		prevEnd = s.ByteEnd
	}
	parts = append(parts, standaloneComments(sql[prevEnd:])...)
	if len(parts) == 0 {
		return strings.TrimSpace(sql)
	}
	return strings.Join(parts, "\n\n")
}

// standaloneComments extrae los comentarios de un hueco entre sentencias (el resto son ';' y espacios).
func standaloneComments(gap string) []string {
	var out []string
	for _, t := range tokenize(gap) {
		if t.kind == tLineComment || t.kind == tBlockComment {
			out = append(out, t.text)
		}
	}
	return out
}

// terminate formatea y añade ';'. Si la sentencia acaba en comentario de línea, el ';' va en la línea siguiente
// (en la misma quedaría comentado, y tras el ';' el comentario se perdería al volver a dividir el script).
func terminate(stmt string) string {
	toks := tokenize(stmt)
	if n := len(toks); n > 1 && toks[n-1].kind == tLineComment {
		return (&printer{toks: toks[:n-1]}).run() + " " + toks[n-1].text + "\n;"
	}
	return FormatStatement(stmt) + ";"
}

func FormatStatement(stmt string) string {
	toks := tokenize(stmt)
	if len(toks) == 0 {
		return strings.TrimSpace(stmt)
	}
	if hasUnterminated(toks) {
		return stmt // SQL inválido (string sin cerrar…): no se toca
	}
	return (&printer{toks: toks}).run()
}

// hasUnterminated detecta strings, identificadores o comentarios de bloque sin cerrar (el servidor reportará el error).
func hasUnterminated(toks []token) bool {
	for _, t := range toks {
		switch t.kind {
		case tString, tQuoted:
			if len(t.text) < 2 || t.text[len(t.text)-1] != t.text[0] {
				return true
			}
		case tBlockComment:
			if !strings.HasSuffix(t.text, "*/") || len(t.text) < 4 {
				return true
			}
		}
	}
	return false
}

func tokenize(s string) []token {
	var out []token
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '\'' || c == '"':
			j := scanQuoted(s, i, c, true)
			out = append(out, token{kind: tString, text: s[i:j]})
			i = j
		case c == '`':
			j := scanQuoted(s, i, c, false)
			out = append(out, token{kind: tQuoted, text: s[i:j]})
			i = j
		case c == '#' || (c == '-' && i+1 < len(s) && s[i+1] == '-' && (i+2 >= len(s) || s[i+2] == ' ' || s[i+2] == '\t' || s[i+2] == '\n' || s[i+2] == '\r' || s[i+2] < 0x20)):
			j := i
			for j < len(s) && s[j] != '\n' {
				j++
			}
			out = append(out, token{kind: tLineComment, text: strings.TrimRight(s[i:j], " \t\r")})
			i = j
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				j = len(s)
			} else {
				j = i + 2 + j + 2
			}
			out = append(out, token{kind: tBlockComment, text: s[i:j]})
			i = j
		case c >= '0' && c <= '9' || (c == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' && !prevIsOperand(out)):
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.' || s[j] == 'e' || s[j] == 'E' || s[j] == 'x' || s[j] == 'X' || (s[j] >= 'a' && s[j] <= 'f') || (s[j] >= 'A' && s[j] <= 'F')) {
				j++
			}
			out = append(out, token{kind: tNumber, text: s[i:j]})
			i = j
		case c == '_' || c == '@' || c == '$' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			j := i + 1
			for j < len(s) && (s[j] == '_' || s[j] == '@' || s[j] == '$' || s[j] >= 0x80 || (s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			out = append(out, token{kind: tWord, text: s[i:j], up: strings.ToUpper(s[i:j])})
			i = j
		default:
			j := i + 1
			for _, op := range []string{"<=>", "->>", "<=", ">=", "<>", "!=", ":=", "||", "&&", "<<", ">>", "->"} {
				if strings.HasPrefix(s[i:], op) {
					j = i + len(op)
					break
				}
			}
			out = append(out, token{kind: tPunct, text: s[i:j]})
			i = j
		}
	}
	return out
}

func prevIsOperand(out []token) bool {
	if len(out) == 0 {
		return false
	}
	p := out[len(out)-1]
	return p.kind == tWord || p.kind == tQuoted || p.kind == tNumber || p.text == ")"
}

func scanQuoted(s string, i int, q byte, backslash bool) int {
	i++
	for i < len(s) {
		switch {
		case backslash && s[i] == '\\':
			i += 2
		case s[i] == q:
			if i+1 < len(s) && s[i+1] == q {
				i += 2
				continue
			}
			return i + 1
		default:
			i++
		}
	}
	return len(s)
}

var reserved = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`SELECT FROM WHERE GROUP BY HAVING ORDER LIMIT OFFSET UNION ALL DISTINCT INTERSECT EXCEPT INSERT INTO VALUES VALUE
		UPDATE SET DELETE REPLACE JOIN INNER LEFT RIGHT OUTER CROSS NATURAL FULL STRAIGHT_JOIN ON USING AS AND OR NOT IN IS NULL LIKE RLIKE REGEXP
		BETWEEN EXISTS CASE WHEN THEN ELSE END ASC DESC CREATE ALTER DROP TABLE VIEW INDEX DATABASE TRUNCATE RENAME ADD COLUMN PRIMARY KEY FOREIGN
		REFERENCES DEFAULT UNIQUE IF EXISTS DUPLICATE WITH RECURSIVE OVER PARTITION FOR SHARE LOCK TRUE FALSE ANY SOME INTERVAL`) {
		reserved[w] = true
	}
}

// Palabras tras las que un '(' va separado por un espacio (no son llamadas a función).
var spaceBeforeParen = map[string]bool{
	"IN": true, "AND": true, "OR": true, "NOT": true, "ON": true, "AS": true, "FROM": true, "JOIN": true, "WHERE": true, "SELECT": true,
	"VALUES": true, "VALUE": true, "USING": true, "EXISTS": true, "ANY": true, "ALL": true, "SOME": true, "UNION": true, "BY": true,
	"HAVING": true, "WHEN": true, "THEN": true, "ELSE": true, "SET": true, "BETWEEN": true, "LIKE": true, "RLIKE": true, "REGEXP": true,
	"IS": true, "INTO": true, "UPDATE": true, "CASE": true, "OVER": true, "TABLE": true, "WITH": true, "DISTINCT": true,
}

type printer struct {
	toks []token
	out  strings.Builder
	i    int

	depth     int    // profundidad de paréntesis
	clause    string // cláusula de nivel superior vigente: SELECT, FROM, WHERE…
	lineStart bool   // estamos al principio de una línea
	prev      *token // último token emitido
}

func (p *printer) run() string {
	for p.i = 0; p.i < len(p.toks); p.i++ {
		p.emit(&p.toks[p.i])
	}
	return strings.TrimRight(p.out.String(), " \n")
}

func (p *printer) word(i int) string {
	if i < len(p.toks) && p.toks[i].kind == tWord {
		return p.toks[i].up
	}
	return ""
}

func (p *printer) newline(level int) {
	s := strings.TrimRight(p.out.String(), " ")
	p.out.Reset()
	p.out.WriteString(s)
	if s != "" {
		p.out.WriteString("\n")
	}
	p.out.WriteString(strings.Repeat(indent, level))
	p.lineStart = true
}

func (p *printer) write(text string, spaceBefore bool) {
	if spaceBefore && !p.lineStart && p.out.Len() > 0 {
		p.out.WriteString(" ")
	}
	p.out.WriteString(text)
	p.lineStart = false
}

// emit imprime un token aplicando las reglas de saltos de línea y espacios.
func (p *printer) emit(t *token) {
	defer func() { p.prev = t }()

	switch t.kind {
	case tLineComment:
		p.write(t.text, true)
		p.newline(p.currentIndent())
		return
	case tBlockComment, tString, tQuoted, tNumber:
		p.write(t.text, p.needsSpace(t))
		return
	case tPunct:
		p.punct(t)
		return
	}

	// Palabra.
	text := t.text
	if reserved[t.up] {
		text = t.up
	}
	if p.depth == 0 {
		if p.startsClause(t) {
			return
		}
		switch t.up {
		case "AND", "OR":
			if p.clause == "WHERE" || p.clause == "HAVING" || p.clause == "ON" {
				p.newline(1)
				p.write(text, false)
				return
			}
		}
	}
	p.write(text, p.needsSpace(t))
}

func (p *printer) currentIndent() int {
	if p.depth == 0 && (p.clause == "SELECT" || p.clause == "GROUP BY" || p.clause == "ORDER BY" || p.clause == "SET" || p.clause == "FROM") {
		return 1
	}
	return 0
}

// startsClause detecta cláusulas de nivel superior, las imprime en su propia línea y devuelve true si consumió el token.
func (p *printer) startsClause(t *token) bool {
	i := p.i
	first := t.up
	var text string
	var clause string
	consume := 0 // tokens extra que forman parte de la cláusula
	sameLine := false

	switch first {
	case "SELECT":
		clause, text = "SELECT", "SELECT"
	case "FROM":
		clause, text = "FROM", "FROM"
		sameLine = p.prevWord() == "DELETE"
	case "WHERE", "HAVING", "LIMIT", "OFFSET", "WINDOW":
		clause, text = first, first
	case "VALUES", "VALUE":
		// Solo es cláusula en INSERT/REPLACE; en ON DUPLICATE KEY UPDATE a=VALUES(a) es una función.
		if p.clause == "INSERT" || p.clause == "REPLACE" {
			clause, text = first, first
		}
	case "GROUP", "ORDER":
		if p.word(i+1) == "BY" {
			clause, text, consume = first+" BY", first+" BY", 1
		}
	case "UNION", "INTERSECT", "EXCEPT":
		clause, text = first, first
		if n := p.word(i + 1); n == "ALL" || n == "DISTINCT" {
			text, consume = first+" "+n, 1
		}
	case "INSERT", "REPLACE":
		clause, text = first, first
		if p.word(i+1) == "INTO" {
			text, consume = first+" INTO", 1
		}
	case "UPDATE":
		if p.prev == nil || p.clause == "" || p.clause == "UNION" {
			clause, text = "UPDATE", "UPDATE"
		}
	case "DELETE":
		clause, text = "DELETE", "DELETE"
	case "SET":
		if p.clause == "UPDATE" || p.clause == "ON DUPLICATE KEY UPDATE" {
			clause, text = "SET", "SET"
		}
	case "ON":
		if p.word(i+1) == "DUPLICATE" {
			clause, text, consume = "ON DUPLICATE KEY UPDATE", "ON DUPLICATE KEY UPDATE", 3
		}
	case "JOIN", "INNER", "LEFT", "RIGHT", "CROSS", "NATURAL", "FULL", "STRAIGHT_JOIN":
		// Una cadena de modificadores terminada en JOIN: LEFT OUTER JOIN, NATURAL LEFT JOIN…
		j := i
		parts := []string{}
		for j < len(p.toks) && p.toks[j].kind == tWord {
			w := p.toks[j].up
			if w == "JOIN" || w == "STRAIGHT_JOIN" {
				parts = append(parts, w)
				break
			}
			if w != "INNER" && w != "LEFT" && w != "RIGHT" && w != "CROSS" && w != "NATURAL" && w != "FULL" && w != "OUTER" {
				parts = nil
				break
			}
			parts = append(parts, w)
			j++
		}
		if len(parts) > 0 {
			clause, text, consume = "JOIN", strings.Join(parts, " "), len(parts)-1
		}
	default:
		return false
	}
	if clause == "" {
		return false
	}

	// SELECT tras UNION/INTERSECT/EXCEPT va en la misma línea que el operador: no es cláusula nueva si ya estamos en una línea vacía.
	if !sameLine && p.out.Len() > 0 {
		p.newline(0)
	}
	p.clause = clause
	p.write(text, sameLine)
	p.i += consume
	if clause == "SELECT" || clause == "GROUP BY" || clause == "ORDER BY" || clause == "SET" {
		// Los elementos de la lista van en líneas propias con sangría; DISTINCT & co. se quedan con SELECT.
		for _, mod := range []string{"DISTINCT", "ALL", "DISTINCTROW", "SQL_CALC_FOUND_ROWS", "SQL_NO_CACHE", "HIGH_PRIORITY", "STRAIGHT_JOIN"} {
			if clause == "SELECT" && p.word(p.i+1) == mod {
				p.i++
				p.write(mod, true)
			}
		}
		p.newline(1)
	}
	if clause == "FROM" || clause == "UPDATE" || clause == "DELETE" {
		p.lineStart = false
	}
	p.prev = &p.toks[p.i]
	return true
}

func (p *printer) prevWord() string {
	if p.prev != nil && p.prev.kind == tWord {
		return p.prev.up
	}
	return ""
}

func (p *printer) punct(t *token) {
	switch t.text {
	case "(":
		sp := p.needsSpace(t)
		p.write("(", sp)
		p.depth++
	case ")":
		if p.depth > 0 {
			p.depth--
		}
		p.write(")", false)
	case ",":
		p.write(",", false)
		if p.depth == 0 && (p.clause == "SELECT" || p.clause == "GROUP BY" || p.clause == "ORDER BY" || p.clause == "SET" || p.clause == "FROM") {
			p.newline(1)
		}
	case ".":
		p.write(".", false)
	case ";":
		p.write(";", false)
	default:
		if isUnary(t.text, p.prev) {
			p.write(t.text, p.needsSpace(t))
			p.lineStart = false
			return
		}
		p.write(t.text, p.needsSpace(t))
	}
}

func isUnary(op string, prev *token) bool {
	if op != "-" && op != "+" && op != "~" && op != "!" {
		return false
	}
	if op == "~" || op == "!" {
		return true
	}
	if prev == nil {
		return true
	}
	switch prev.kind {
	case tWord:
		return reserved[prev.up] && prev.up != "NULL" && prev.up != "TRUE" && prev.up != "FALSE" && prev.up != "END"
	case tNumber, tString, tQuoted:
		return false
	case tPunct:
		return prev.text != ")"
	}
	return false
}

// needsSpace decide si hace falta un espacio antes de t según el token anterior.
func (p *printer) needsSpace(t *token) bool {
	pr := p.prev
	if pr == nil || p.lineStart {
		return false
	}
	if pr.kind == tPunct {
		switch pr.text {
		case "(", ".":
			return false
		case "-", "+", "~", "!":
			// operador unario: pegado a su operando
			// Un operador unario va pegado a su operando; si lo que sigue es otro operador se separa para no fusionarlos ("! =" ≠ "!=").
			if isUnary(pr.text, p.tokBefore(pr)) && !(t.kind == tPunct && t.text != "(") {
				return false
			}
		}
	}
	if t.kind == tPunct && t.text == "(" {
		// INSERT INTO tabla (cols): aquí el paréntesis no es una llamada a función.
		if p.depth == 0 && (p.clause == "INSERT" || p.clause == "REPLACE") && (pr.kind == tWord || pr.kind == tQuoted) {
			return true
		}
		// llamada a función: NOMBRE( sin espacio; tras una palabra reservada de sintaxis, con espacio
		if pr.kind == tWord {
			if (pr.up == "VALUES" || pr.up == "VALUE") && p.clause == "ON DUPLICATE KEY UPDATE" {
				return false // VALUES(col) en ON DUPLICATE KEY UPDATE es una función
			}
			return spaceBeforeParen[pr.up]
		}
		if pr.kind == tQuoted {
			return false
		}
		return pr.kind != tPunct || pr.text != "("
	}
	if pr.kind == tPunct && pr.text == "." {
		return false
	}
	return true
}

func (p *printer) tokBefore(t *token) *token {
	for i := range p.toks {
		if &p.toks[i] == t && i > 0 {
			return &p.toks[i-1]
		}
	}
	return nil
}
