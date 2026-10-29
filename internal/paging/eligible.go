package paging

import (
	"strings"

	"github.com/vitacodesas/dbacus/internal/edit"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/sqlsplit"
)

// Modos de paginación (spec 18 §3).
const (
	ModeKeyset = "keyset"
	ModeNone   = "none"
)

// Motivos propios de la decisión sobre la tabla (los del analizador de SQL están en sqlsplit.Reason*).
const (
	ReasonView           = "view"
	ReasonNoKey          = "no_key"
	ReasonKeyType        = "key_type"
	ReasonKeyNotSelected = "key_not_selected"
	ReasonNullableOrder  = "nullable_order"
)

// Decision es el resultado de decidir cómo paginar una sentencia.
type Decision struct {
	Mode   string // ModeKeyset | ModeNone
	Reason string // código si Mode es ModeNone
	Plan   Plan   // solo con ModeKeyset
}

// KeyColumns son los nombres de las columnas del cursor, en orden.
func (p Plan) KeyColumns() []string {
	out := make([]string, len(p.Order))
	for i, c := range p.Order {
		out[i] = c.Name
	}
	return out
}

// Numéricos exactos (spec 18 §4): FLOAT/DOUBLE no lo son y BIT/YEAR no se tratan.
var exactNumeric = map[string]bool{
	"tinyint": true, "smallint": true, "mediumint": true, "int": true, "integer": true, "bigint": true,
	"decimal": true, "numeric": true,
}

func isExactNumeric(c model.Column) bool { return exactNumeric[strings.ToLower(c.DataType)] }

// Decide dice si una sentencia ya analizada (`parts`) se puede paginar por cursor sobre la tabla descrita por `detail`
// y, si es así, devuelve el plan. Las condiciones y los códigos de motivo son los del spec 18 §4 y §6.
func Decide(parts sqlsplit.SelectParts, detail model.TableDetail, pageSize int) Decision {
	none := func(reason string) Decision { return Decision{Mode: ModeNone, Reason: reason} }
	if strings.EqualFold(detail.Type, "view") {
		return none(ReasonView)
	}

	cols := map[string]model.Column{} // por nombre en minúsculas
	for _, c := range detail.Columns {
		cols[strings.ToLower(c.Name)] = c
	}

	// ORDER BY del usuario: columnas de la tabla, numéricas exactas y no nulas.
	user := make([]sqlsplit.OrderItem, 0, len(parts.Order))
	for _, o := range parts.Order {
		c, ok := cols[strings.ToLower(o.Column)]
		if !ok {
			return none(sqlsplit.ReasonOrderUnsupported)
		}
		if !isExactNumeric(c) {
			return none(ReasonKeyType)
		}
		if c.Nullable {
			return none(ReasonNullableOrder)
		}
		user = append(user, sqlsplit.OrderItem{Column: c.Name, Desc: o.Desc}) // nombre canónico de la tabla
	}

	// Clave única numérica.
	all := edit.KeyCandidates(detail)
	if len(all) == 0 {
		return none(ReasonNoKey)
	}
	var numeric [][]string
	for _, cand := range all {
		ok := true
		for _, name := range cand {
			if c, known := cols[strings.ToLower(name)]; !known || !isExactNumeric(c) || c.Nullable {
				ok = false
				break
			}
		}
		if ok {
			numeric = append(numeric, cand)
		}
	}
	if len(numeric) == 0 {
		return none(ReasonKeyType)
	}

	// Columnas disponibles en el resultado: con `*` todas; si no, las que se listan tal cual (sin alias ni expresión).
	selected := func(name string) bool {
		if parts.Star {
			return true
		}
		for _, it := range parts.Items {
			if it.Plain && strings.EqualFold(it.Column, name) {
				return true
			}
		}
		return false
	}

	// La primera clave (la primaria, si sirve) cuyo orden efectivo está entero en el resultado.
	for _, cand := range numeric {
		canon := make([]string, len(cand))
		for i, name := range cand {
			canon[i] = cols[strings.ToLower(name)].Name
		}
		order := EffectiveOrder(user, canon)
		all := true
		for _, c := range order {
			if !selected(c.Name) {
				all = false
				break
			}
		}
		if all {
			return Decision{Mode: ModeKeyset, Plan: Plan{List: parts.List, From: parts.From, Where: parts.Where, Order: order, PageSize: pageSize}}
		}
	}
	return none(ReasonKeyNotSelected)
}
