package paging

import (
	"fmt"
	"strings"

	"github.com/vitacodesas/dbacus/internal/model"
)

// Modo y motivos de la paginación aproximada (spec 18 §5).
const (
	ModeApprox = "approx"

	ReasonNoNumericColumn = "no_numeric_column"
	ReasonDuplicateColumn = "duplicate_columns"
)

// Approx es el plan de una sentencia que no admite keyset: se pagina envolviéndola como tabla derivada y ordenando por
// una sola columna numérica no nula del resultado. Es aproximado: con valores repetidos en el borde de la página un
// «siguiente» se saltaría filas, y el servicio lo bloquea (ver Page de QueryService).
type Approx struct {
	Inner    string // la sentencia original, sin «;»
	Column   string // columna por la que se ordena y se avanza
	PageSize int
}

// Order es la única columna del cursor, en el mismo formato que el orden de Plan (para leer los valores de borde).
func (a Approx) Order() []Col { return []Col{{Name: a.Column}} }

// Build devuelve la consulta de una página, con la misma convención que Plan.Build: pide PageSize+1 filas, `edge` es el
// valor del borde (el de la última fila para Next, el de la primera para Prev) y las páginas hacia atrás se leen con el
// orden invertido.
func (a Approx) Build(d Dir, edge string, quote func(string) string) (string, error) {
	if a.Column == "" || a.PageSize <= 0 {
		return "", fmt.Errorf("paging: plan aproximado sin columna o sin tamaño de página")
	}
	col := "_p." + quote(a.Column)
	var b strings.Builder
	// El salto de línea antes del «)» evita que un comentario `-- …` al final de la sentencia se coma el paréntesis.
	b.WriteString("SELECT * FROM (" + a.Inner + "\n) AS _p")
	if d == Next || d == Prev {
		if !ValidLiteral(edge) {
			return "", fmt.Errorf("paging: valor de borde no numérico")
		}
		op := ">"
		if d == Prev {
			op = "<"
		}
		b.WriteString(" WHERE " + col + " " + op + " " + edge)
	}
	b.WriteString(" ORDER BY " + col)
	if d.Backward() {
		b.WriteString(" DESC")
	}
	fmt.Fprintf(&b, " LIMIT %d", a.PageSize+1)
	return b.String(), nil
}

func isExactNumericType(dataType string) bool {
	// El driver da "INT", "BIGINT", "UNSIGNED BIGINT", "DECIMAL"…
	return exactNumeric[strings.ToLower(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(dataType)), "UNSIGNED "))]
}

// Candidates son las columnas del resultado por las que se puede paginar en modo aproximado: numéricas exactas y no
// nulas. Si no hay ninguna, o si dos columnas se llaman igual (la tabla derivada no admite nombres repetidos, como en
// `SELECT * FROM a JOIN b` con dos `id`), devuelve el motivo.
func Candidates(cols []model.ColumnMeta) (names []string, reason string) {
	seen := map[string]bool{}
	for _, c := range cols {
		k := strings.ToLower(c.Name)
		if seen[k] {
			return nil, ReasonDuplicateColumn
		}
		seen[k] = true
	}
	for _, c := range cols {
		if !c.Nullable && isExactNumericType(c.DataType) {
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 {
		return nil, ReasonNoNumericColumn
	}
	return names, ""
}

// TieAtEdge dice si avanzar desde el borde `edge` (el valor de la última fila de la página, o el de la primera si se
// retrocede) se saltaría filas: la fila de sondeo —la siguiente en el orden de lectura— vale lo mismo, y la página
// siguiente es `columna > edge` (o `<` hacia atrás), que excluye todas las que empatan. Es la salvaguarda del spec 18 §5.
func TieAtEdge(edge, probe string) bool { return edge != "" && edge == probe }
