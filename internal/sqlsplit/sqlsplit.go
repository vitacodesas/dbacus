// Package sqlsplit divide scripts SQL en sentencias respetando strings, comentarios, backticks y DELIMITER (spec 03/06).
// Los offsets Start/End están en unidades UTF-16, que es lo que usa Monaco (model.getOffsetAt).
package sqlsplit

import (
	"strings"
	"unicode/utf8"
)

type Statement struct {
	Text      string `json:"text"`
	Start     int    `json:"start"` // UTF-16, inclusivo
	End       int    `json:"end"`   // UTF-16, exclusivo; no incluye el delimitador
	ByteStart int    `json:"-"`
	ByteEnd   int    `json:"-"`
}

const defaultDelimiter = ";"

// Split devuelve las sentencias con código real; los fragmentos solo con comentarios o espacios se descartan.
// Un string o comentario sin cerrar se extiende hasta el final: el servidor informará el error.
func Split(src string) []Statement {
	var (
		out       []Statement
		delim     = defaultDelimiter
		stmtStart = -1
		hasCode   bool
		i         int
		pos       = newU16Pos(src)
	)
	emit := func(end int) {
		if hasCode {
			text := strings.TrimRight(src[stmtStart:end], " \t\r\n\f\v")
			be := stmtStart + len(text)
			out = append(out, Statement{
				Text: text, Start: pos.at(stmtStart), End: pos.at(be),
				ByteStart: stmtStart, ByteEnd: be,
			})
		}
		stmtStart, hasCode = -1, false
	}
	begin := func() {
		if stmtStart < 0 {
			stmtStart = i
		}
	}

	for i < len(src) {
		c := src[i]
		switch {
		case isSpace(c):
			i++
		case !hasCode && matchDelimiterCommand(src, i):
			var arg string
			arg, i = parseDelimiterCommand(src, i)
			if arg != "" {
				delim = arg
			}
			stmtStart = -1
		case strings.HasPrefix(src[i:], delim):
			emit(i)
			i += len(delim)
		case c == '\'' || c == '"':
			begin()
			hasCode = true
			i = skipQuoted(src, i, c, true)
		case c == '`':
			begin()
			hasCode = true
			i = skipQuoted(src, i, c, false)
		case c == '#' || (c == '-' && isDashComment(src, i)):
			begin()
			i = skipLine(src, i)
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			begin()
			i = skipBlockComment(src, i)
		default:
			begin()
			hasCode = true
			i++
		}
	}
	emit(len(src))
	return out
}

// At devuelve la sentencia "actual" para un cursor en offset (UTF-16): la última que empieza en o antes del cursor
// (o la primera si el cursor está antes de todas).
func At(stmts []Statement, offset int) (Statement, bool) {
	if len(stmts) == 0 {
		return Statement{}, false
	}
	cur := stmts[0]
	for _, s := range stmts {
		if s.Start > offset {
			break
		}
		cur = s
	}
	return cur, true
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// MySQL exige espacio (o control) tras "--" para que sea comentario.
func isDashComment(src string, i int) bool {
	if i+1 >= len(src) || src[i+1] != '-' {
		return false
	}
	return i+2 >= len(src) || isSpace(src[i+2]) || src[i+2] < 0x20
}

func skipLine(src string, i int) int {
	if n := strings.IndexByte(src[i:], '\n'); n >= 0 {
		return i + n + 1
	}
	return len(src)
}

func skipBlockComment(src string, i int) int {
	if n := strings.Index(src[i+2:], "*/"); n >= 0 {
		return i + 2 + n + 2
	}
	return len(src)
}

// skipQuoted avanza más allá del cierre de la comilla q; admite comilla doblada y, si backslash, escapes con '\'.
func skipQuoted(src string, i int, q byte, backslash bool) int {
	i++
	for i < len(src) {
		switch {
		case backslash && src[i] == '\\':
			i += 2
		case src[i] == q:
			if i+1 < len(src) && src[i+1] == q {
				i += 2
				continue
			}
			return i + 1
		default:
			i++
		}
	}
	return len(src)
}

func matchDelimiterCommand(src string, i int) bool {
	const kw = "delimiter"
	if i+len(kw) >= len(src) || !strings.EqualFold(src[i:i+len(kw)], kw) {
		return false
	}
	c := src[i+len(kw)]
	return c == ' ' || c == '\t'
}

func parseDelimiterCommand(src string, i int) (arg string, next int) {
	end := len(src)
	if n := strings.IndexByte(src[i:], '\n'); n >= 0 {
		end = i + n
	}
	fields := strings.Fields(src[i+len("delimiter") : end])
	if len(fields) > 0 {
		arg = fields[0]
	}
	if end < len(src) {
		end++
	}
	return arg, end
}

// u16Pos convierte offsets de byte monótonos crecientes a unidades UTF-16 en O(n) total.
type u16Pos struct {
	src       string
	lastByte  int
	lastUnits int
}

func newU16Pos(src string) *u16Pos { return &u16Pos{src: src} }

func (p *u16Pos) at(b int) int {
	if b < p.lastByte { // no debería ocurrir; recalcula desde cero por seguridad
		p.lastByte, p.lastUnits = 0, 0
	}
	for p.lastByte < b {
		r, size := utf8.DecodeRuneInString(p.src[p.lastByte:])
		if r >= 0x10000 {
			p.lastUnits += 2
		} else {
			p.lastUnits++
		}
		p.lastByte += size
	}
	return p.lastUnits
}
