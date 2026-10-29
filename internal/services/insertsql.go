package services

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/results"
)

var numberRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
var hexRe = regexp.MustCompile(`^0x[0-9a-fA-F]*$`)

// RowsAsInsert genera un INSERT con las filas [offset, offset+limit) de un resultado (máx. 1000), con identificadores y
// literales escapados por el driver de la sesión. El front nunca arma SQL.
func (s *QueryService) RowsAsInsert(queryID string, stmtIndex, offset, limit int, table string) (string, error) {
	run, err := s.d.Results.Get(queryID)
	if err != nil {
		return "", toAppErr(err)
	}
	buf, err := run.Buffer(stmtIndex)
	if err != nil {
		return "", toAppErr(err)
	}
	sess, err := s.d.Sessions.Get(run.SessionID)
	if err != nil {
		return "", toAppErr(err)
	}
	if limit <= 0 || limit > maxWindow {
		limit = maxWindow
	}
	rows, _, _, err := buf.Window(offset, limit)
	if err != nil {
		return "", toAppErr(err)
	}
	if len(rows) == 0 {
		return "", apperr.New(apperr.Validation, "No hay filas que copiar").WithDetail("rows")
	}
	if strings.TrimSpace(table) == "" {
		table = "tabla"
	}
	return buildInsert(sess.Driver, table, buf.Columns(), rows), nil
}

func buildInsert(d driver.Driver, table string, cols []model.ColumnMeta, rows [][]results.Cell) string {
	var b strings.Builder
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = d.QuoteIdent(c.Name)
	}
	fmt.Fprintf(&b, "INSERT INTO %s (%s) VALUES\n", d.QuoteIdent(table), strings.Join(names, ", "))
	for i, row := range rows {
		vals := make([]string, len(row))
		for j, c := range row {
			vals[j] = sqlLiteral(d, c)
		}
		b.WriteString("  (" + strings.Join(vals, ", ") + ")")
		if i < len(rows)-1 {
			b.WriteString(",\n")
		} else {
			b.WriteString(";")
		}
	}
	return b.String()
}

// sqlLiteral convierte una celda en un literal SQL. Usa el valor completo (no el recortado para mostrar).
func sqlLiteral(d driver.Driver, c results.Cell) string {
	v := c.Full()
	switch x := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if x {
			return "1"
		}
		return "0"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		switch c.T {
		case results.TBigint, results.TDecimal:
			if numberRe.MatchString(x) {
				return x
			}
		case results.TBytes:
			if hexRe.MatchString(x) && len(x) > 2 {
				return x // literal hexadecimal de MySQL
			}
			if x == "0x" {
				return "''"
			}
		}
		return d.QuoteString(x)
	}
	return d.QuoteString(fmt.Sprint(v))
}
