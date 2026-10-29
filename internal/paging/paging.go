// Package paging genera el SQL de cada página de una consulta paginada por cursor de clave (spec 18, ADR-005).
// No ejecuta nada y no conoce el driver: recibe las partes de la sentencia y los valores de borde y devuelve SQL.
package paging

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/vitacodesas/dbacus/internal/sqlsplit"
)

// Dir es la dirección de la página pedida respecto a la actual.
type Dir string

const (
	First Dir = "first"
	Next  Dir = "next"
	Prev  Dir = "prev"
	Last  Dir = "last"
)

// Backward: las páginas hacia atrás se leen con el orden invertido y las filas se invierten en Go antes de mostrarse.
func (d Dir) Backward() bool { return d == Prev || d == Last }

// Col es una columna del orden efectivo.
type Col struct {
	Name string
	Desc bool
}

// Plan son las partes de la sentencia original y el orden efectivo con el que se pagina.
type Plan struct {
	List, From, Where string // tal como se escribieron; Where sin la palabra WHERE
	Order             []Col  // ORDER BY del usuario + columnas de la clave que falten (desempate)
	PageSize          int
}

// EffectiveOrder es el ORDER BY del usuario más las columnas de la clave que falten para que el orden sea total y único.
// Sin ORDER BY del usuario es solo la clave, ascendente.
//
// Las columnas añadidas van en la MISMA dirección que la última columna del ORDER BY del usuario: la dirección del
// desempate es indiferente para la corrección, pero no para el rendimiento. Un índice solo sirve un ORDER BY si todas sus
// columnas van en la misma dirección (se recorre del derecho o del revés); `v DESC, id ASC` obligaría a ordenar en cada
// página (medido: ~530 ms por página con 5 M de filas, frente a ~1 ms con `v DESC, id DESC`).
func EffectiveOrder(user []sqlsplit.OrderItem, key []string) []Col {
	out := make([]Col, 0, len(user)+len(key))
	seen := map[string]bool{}
	tailDesc := false
	for _, u := range user {
		out = append(out, Col{Name: u.Column, Desc: u.Desc})
		seen[strings.ToLower(u.Column)] = true
		tailDesc = u.Desc
	}
	for _, k := range key {
		if !seen[strings.ToLower(k)] {
			out = append(out, Col{Name: k, Desc: tailDesc})
			seen[strings.ToLower(k)] = true
		}
	}
	return out
}

// Un entero o un decimal exacto, con signo opcional. Es lo único que entra en el SQL como literal: viene de las celdas
// de columnas numéricas, nunca de texto del usuario, y aun así se valida.
var numberRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// ValidLiteral indica si v se puede incrustar en el SQL como número.
func ValidLiteral(v string) bool { return numberRe.MatchString(v) }

// Build devuelve la consulta de una página. `edge` son los valores del borde de la página actual, uno por columna de
// Order y en el mismo orden: el de la última fila para Next y el de la primera para Prev (ignorado en First y Last).
// La consulta pide PageSize+1 filas: la última solo sirve para saber si hay más y no se muestra.
func (p Plan) Build(d Dir, edge []string, quote func(string) string) (string, error) {
	if len(p.Order) == 0 || p.PageSize <= 0 {
		return "", fmt.Errorf("paging: plan sin orden o sin tamaño de página")
	}
	var conds []string
	if strings.TrimSpace(p.Where) != "" {
		// El salto de línea antes del «)» evita que un comentario `-- …` al final del WHERE se coma el paréntesis.
		conds = append(conds, "("+p.Where+"\n)")
	}
	if d == Next || d == Prev {
		if len(edge) != len(p.Order) {
			return "", fmt.Errorf("paging: %d valores de borde para %d columnas de orden", len(edge), len(p.Order))
		}
		for _, v := range edge {
			if !ValidLiteral(v) {
				return "", fmt.Errorf("paging: valor de borde no numérico")
			}
		}
		conds = append(conds, "("+p.seek(edge, d == Prev, quote)+")")
	}

	var b strings.Builder
	b.WriteString("SELECT " + p.List + " FROM " + p.From)
	if len(conds) > 0 {
		b.WriteString(" WHERE " + strings.Join(conds, " AND "))
	}
	b.WriteString(" ORDER BY ")
	for i, c := range p.Order {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quote(c.Name))
		if c.Desc != d.Backward() { // invertir las direcciones al leer hacia atrás
			b.WriteString(" DESC")
		}
	}
	fmt.Fprintf(&b, " LIMIT %d", p.PageSize+1)
	return b.String(), nil
}

// seek es el predicado «estrictamente después (o antes) del borde» en la forma expandida
// (c1 ▷ v1) OR (c1 = v1 AND c2 ▷ v2) OR … que, a diferencia de la comparación de tuplas (c1,c2) > (v1,v2), admite direcciones
// mixtas.
func (p Plan) seek(edge []string, backward bool, quote func(string) string) string {
	ors := make([]string, 0, len(p.Order))
	for i, c := range p.Order {
		ands := make([]string, 0, i+1)
		for j := 0; j < i; j++ {
			ands = append(ands, quote(p.Order[j].Name)+" = "+edge[j])
		}
		op := "<"
		if c.Desc == backward { // asc hacia delante o desc hacia atrás
			op = ">"
		}
		ands = append(ands, quote(c.Name)+" "+op+" "+edge[i])
		ors = append(ors, "("+strings.Join(ands, " AND ")+")")
	}
	return strings.Join(ors, " OR ")
}

// Page recorta las filas leídas (PageSize+1 como máximo) a las de la página y dice si hay más en la dirección leída.
// Para Prev y Last las filas llegan en orden inverso: el llamador las invierte después.
func Page[T any](rows []T, pageSize int) (page []T, more bool) {
	if len(rows) > pageSize {
		return rows[:pageSize], true
	}
	return rows, false
}

// Reverse invierte las filas en el sitio (páginas leídas hacia atrás).
func Reverse[T any](rows []T) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}
