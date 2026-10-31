package alter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/vitacodesas/dbacus/internal/model"
)

// LargeTableRows es el umbral a partir del cual la revisión avisa de que el ALTER puede tardar (D10 del spec 19).
const LargeTableRows = 1_000_000

var lenRe = regexp.MustCompile(`\(\s*(\d+)`)

// Warnings devuelve los avisos de la revisión y si el conjunto destruye datos. Los textos son fijos: no incluyen valores.
func Warnings(cs model.ChangeSet, m model.AlterModel) (w []model.AlterWarning, destructive bool) {
	byName := map[string]model.Column{}
	for _, c := range m.Columns {
		byName[c.Name] = c
	}
	danger := func(code, msg string) {
		w = append(w, model.AlterWarning{Code: code, Message: msg, Severity: "danger"})
		destructive = true
	}
	for _, op := range cs.Ops {
		switch op.Kind {
		case model.OpDropColumn:
			danger("drop_column", fmt.Sprintf("Se borrará la columna «%s» con todos sus datos.", op.Name))
		case model.OpDropIndex:
			danger("drop_index", fmt.Sprintf("Se borrará el índice «%s»: las consultas que lo usan pueden volverse lentas.", op.Name))
		case model.OpDropFK:
			danger("drop_fk", fmt.Sprintf("Se borrará la clave foránea «%s»: dejará de garantizarse la integridad referencial.", op.Name))
		case model.OpModifyColumn:
			orig, ok := byName[op.Name]
			if ok && op.Column != nil && lossy(orig, *op.Column) {
				danger("lossy_change", fmt.Sprintf("El cambio en «%s» puede truncar o convertir datos existentes.", op.Name))
			}
		}
	}
	if m.RowEstimate != nil && *m.RowEstimate > LargeTableRows && len(cs.Ops) > 0 {
		w = append(w, model.AlterWarning{Code: "large_table", Severity: "warning",
			Message: fmt.Sprintf("La tabla tiene unas %d filas (estimación): el ALTER puede tardar y bloquear escrituras.", *m.RowEstimate)})
	}
	if len(cs.Ops) > 0 {
		hasPK := false
		for _, c := range m.Columns {
			hasPK = hasPK || c.IsPrimaryKey
		}
		if !hasPK {
			w = append(w, model.AlterWarning{Code: "no_primary_key", Severity: "info", Message: "La tabla no tiene clave primaria."})
		}
	}
	return w, destructive
}

// lossy detecta cambios de tipo que pueden perder datos: otro tipo base, menor longitud, signo distinto o NULL → NOT NULL.
func lossy(orig model.Column, n model.ColumnSpec) bool {
	if orig.Nullable && !n.Nullable {
		return true
	}
	ot, nt := strings.ToLower(orig.ColumnType), strings.ToLower(strings.TrimSpace(n.Type))
	if ot == nt {
		return false
	}
	if BaseType(ot) != BaseType(nt) {
		return true
	}
	if strings.Contains(ot, "unsigned") != strings.Contains(nt, "unsigned") {
		return true
	}
	if BaseType(nt) == "enum" || BaseType(nt) == "set" {
		return true // quitar un valor deja filas con un valor ya inexistente
	}
	om, nm := lenRe.FindStringSubmatch(ot), lenRe.FindStringSubmatch(nt)
	if om != nil && nm != nil {
		a, _ := strconv.Atoi(om[1])
		b, _ := strconv.Atoi(nm[1])
		return b < a
	}
	return false
}
