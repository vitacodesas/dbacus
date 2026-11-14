// Package sqllex trocea SQL en palabras, identificadores, cadenas, símbolos y comentarios conservando todos los bytes. Lo usan la
// reescritura de schemas (spec 23) y el guardia de escrituras del driver: ninguno debe confundir texto de una cadena o de un
// comentario con código.
package sqllex

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind es el tipo de un token.
type Kind byte

const (
	Word  Kind = 'w' // palabra o número sin comillas
	Ident Kind = 'i' // `identificador`
	Str   Kind = 's' // 'texto' o "texto"
	Sym   Kind = 'y' // un símbolo suelto
	Other Kind = 'o' // espacios y comentarios (se conservan tal cual)
)

// Token es un trozo del SQL: Start y End son offsets en bytes del texto original.
type Token struct {
	Kind       Kind
	Start, End int
	Text       string // identificador sin backticks (Ident), palabra (Word) o contenido de la cadena (Str)
}

// Lex trocea el SQL conservando todos los bytes: la concatenación de [start,end) de los tokens es el texto original.
func Lex(src string) []Token {
	var out []Token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			j := i
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
				j++
			}
			out = append(out, Token{Kind: Other, Start: i, End: j})
			i = j
		case c == '#' || (c == '-' && i+1 < len(src) && src[i+1] == '-' && (i+2 >= len(src) || src[i+2] == ' ' || src[i+2] == '\t' || src[i+2] == '\n' || src[i+2] == '\r')):
			j := i
			for j < len(src) && src[j] != '\n' {
				j++
			}
			out = append(out, Token{Kind: Other, Start: i, End: j})
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			end := len(src)
			if j >= 0 {
				end = i + 2 + j + 2
			}
			out = append(out, Token{Kind: Other, Start: i, End: end})
			i = end
		case c == '`':
			j := i + 1
			var b strings.Builder
			for j < len(src) {
				if src[j] == '`' {
					if j+1 < len(src) && src[j+1] == '`' {
						b.WriteByte('`')
						j += 2
						continue
					}
					break
				}
				b.WriteByte(src[j])
				j++
			}
			end := min(j+1, len(src))
			out = append(out, Token{Kind: Ident, Start: i, End: end, Text: b.String()})
			i = end
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(src) {
				if src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == c {
					if j+1 < len(src) && src[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := min(j+1, len(src))
			out = append(out, Token{Kind: Str, Start: i, End: end, Text: src[i+1 : min(j, len(src))]})
			i = end
		case isWordByte(c):
			j := i
			for j < len(src) {
				if isWordByte(src[j]) {
					j++
					continue
				}
				if src[j] >= 0x80 {
					r, size := utf8.DecodeRuneInString(src[j:])
					if unicode.IsLetter(r) || unicode.IsDigit(r) {
						j += size
						continue
					}
				}
				break
			}
			out = append(out, Token{Kind: Word, Start: i, End: j, Text: src[i:j]})
			i = j
		default:
			if c >= 0x80 {
				r, size := utf8.DecodeRuneInString(src[i:])
				if unicode.IsLetter(r) {
					j := i + size
					for j < len(src) && (isWordByte(src[j]) || src[j] >= 0x80) {
						j++
					}
					out = append(out, Token{Kind: Word, Start: i, End: j, Text: src[i:j]})
					i = j
					continue
				}
			}
			out = append(out, Token{Kind: Sym, Start: i, End: i + 1, Text: src[i : i+1]})
			i++
		}
	}
	return out
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$'
}

// IsName dice si el token es un identificador (con o sin comillas).
func (t Token) IsName() bool { return t.Kind == Word || t.Kind == Ident }
