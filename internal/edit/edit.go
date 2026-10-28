// Package edit decide si un resultado se puede editar celda a celda y, más adelante, valida y genera el SQL de los cambios
// (spec 13). No conoce Wails ni drivers: recibe metadatos ya leídos.
package edit

import (
	"strings"

	"tabulariswails/internal/model"
	"tabulariswails/internal/sqlsplit"
)

// Motivos por los que un resultado (Info.Reason) o una columna (Column.Reason) no son editables. La UI los traduce.
const (
	ReasonNotRows        = "not_rows"         // la sentencia no devuelve filas
	ReasonReadOnly       = "readonly_profile" // el perfil es de solo lectura
	ReasonNoSingleTable  = "no_single_table"  // JOIN, UNION, alias de tabla, GROUP BY, subconsulta en FROM…
	ReasonTableNotFound  = "table_not_found"
	ReasonView           = "view"
	ReasonNoKey          = "no_primary_key"
	ReasonKeyNotSelected = "key_not_selected" // hay clave pero alguna de sus columnas no está en el resultado
	ReasonExpired        = "expired"

	ColKey           = "key"
	ColAutoIncrement = "auto_increment"
	ColGenerated     = "generated"
	ColBinary        = "binary"
	ColExpression    = "expression" // expresión o alias: no es una columna de la tabla
	ColUnmapped      = "unmapped"   // no se encontró en la tabla
	ColDuplicate     = "duplicate"  // la misma columna de la tabla aparece dos veces
)

// Column describe si una columna del resultado se puede editar.
type Column struct {
	Editable bool   `json:"editable"`
	Reason   string `json:"reason,omitempty"`
	DataType string `json:"dataType"`
	Nullable bool   `json:"nullable"`
}

// Info es el veredicto de edición de un resultado (spec 13).
type Info struct {
	Editable   bool     `json:"editable"`
	Reason     string   `json:"reason,omitempty"`
	Database   string   `json:"database"`
	Table      string   `json:"table"`
	KeyColumns []int    `json:"keyColumns"` // índices de columna del resultado que forman la clave
	Columns    []Column `json:"columns"`    // una por columna del resultado
	Pending    int      `json:"pending"`
}

// Plan es Info más lo que el servidor necesita internamente (no viaja a Vue).
type Plan struct {
	Info
	// TableCols[i] es el nombre de la columna de la tabla que corresponde a la columna i del resultado ("" si ninguna).
	TableCols []string
	// KeyTableCols son los nombres de las columnas de la clave, en el orden de la clave.
	KeyTableCols []string
}

// Input reúne lo necesario para decidir; Detail es nil si no se pudo leer la tabla.
type Input struct {
	SQL       string // sentencia original (sin el LIMIT automático)
	DefaultDB string // base con la que se ejecutó
	Cols      []model.ColumnMeta
	ReadOnly  bool
	// Describe se llama una sola vez, solo si hace falta, con la base y la tabla ya resueltas.
	Describe func(db, table string) (model.TableDetail, error)
}

// Analyze aplica las reglas del spec 13 («Cuándo es editable un resultado»).
func Analyze(in Input) Plan {
	plan := Plan{Info: Info{KeyColumns: []int{}, Columns: make([]Column, len(in.Cols))}, TableCols: make([]string, len(in.Cols))}
	deny := func(reason string) Plan {
		plan.Reason = reason
		return plan
	}
	if len(in.Cols) == 0 {
		return deny(ReasonNotRows)
	}
	if in.ReadOnly {
		return deny(ReasonReadOnly)
	}
	src, ok := sqlsplit.SimpleSelectTable(in.SQL)
	if !ok {
		return deny(ReasonNoSingleTable)
	}
	plan.Database, plan.Table = src.Database, src.Table
	if plan.Database == "" {
		plan.Database = in.DefaultDB
	}
	if plan.Database == "" {
		return deny(ReasonNoSingleTable) // sin base no hay forma de saber a qué tabla se refiere
	}
	detail, err := in.Describe(plan.Database, plan.Table)
	if err != nil {
		return deny(ReasonTableNotFound)
	}
	if detail.Type == "view" {
		return deny(ReasonView)
	}

	byName := map[string]*model.Column{}
	for i := range detail.Columns {
		byName[strings.ToLower(detail.Columns[i].Name)] = &detail.Columns[i]
	}
	used := map[string]bool{}
	// reasons[i] != "" marca columnas que no se pueden mapear o repiten otra.
	reasons := make([]string, len(in.Cols))
	for i, c := range in.Cols {
		name := c.Name
		switch {
		case src.Star:
		case i < len(src.Items) && src.Items[i].Plain:
			name = src.Items[i].Column
		default:
			reasons[i] = ColExpression
			continue
		}
		col, found := byName[strings.ToLower(name)]
		switch {
		case !found:
			reasons[i] = ColUnmapped
		case used[strings.ToLower(col.Name)]:
			reasons[i] = ColDuplicate
		default:
			used[strings.ToLower(col.Name)] = true
			plan.TableCols[i] = col.Name
		}
	}

	key, status := chooseKey(detail, plan.TableCols)
	if status != "" {
		return deny(status)
	}
	plan.KeyTableCols = key
	isKey := map[string]bool{}
	for _, k := range key {
		isKey[strings.ToLower(k)] = true
		for i, tc := range plan.TableCols {
			if strings.EqualFold(tc, k) {
				plan.KeyColumns = append(plan.KeyColumns, i)
			}
		}
	}

	for i := range in.Cols {
		cc := Column{}
		if tc := plan.TableCols[i]; tc != "" {
			col := byName[strings.ToLower(tc)]
			cc.DataType, cc.Nullable = col.DataType, col.Nullable
			switch {
			case isKey[strings.ToLower(tc)]:
				cc.Reason = ColKey
			case col.IsAutoInc:
				cc.Reason = ColAutoIncrement
			case col.IsGenerated:
				cc.Reason = ColGenerated
			case isBinaryType(col.DataType):
				cc.Reason = ColBinary
			default:
				cc.Editable = true
			}
		} else {
			cc.Reason = reasons[i]
			cc.DataType, cc.Nullable = in.Cols[i].DataType, in.Cols[i].Nullable
		}
		plan.Columns[i] = cc
	}
	plan.Editable = true
	return plan
}

// chooseKey elige la clave con la que se identifica una fila: la primaria y, si no hay o no está entera en el resultado,
// un índice único completo (sin prefijos) sobre columnas NOT NULL. Devuelve un motivo si no hay ninguna utilizable.
func chooseKey(d model.TableDetail, mapped []string) ([]string, string) {
	selected := map[string]bool{}
	for _, c := range mapped {
		if c != "" {
			selected[strings.ToLower(c)] = true
		}
	}
	nullable := map[string]bool{}
	for _, c := range d.Columns {
		nullable[strings.ToLower(c.Name)] = c.Nullable
	}

	var candidates [][]string
	var pk []string
	for _, ix := range d.Indexes {
		if ix.Name == "PRIMARY" {
			pk = ix.Columns
		}
	}
	if len(pk) == 0 { // sin índice PRIMARY visible: se usan las marcadas como clave, en orden de columna
		for _, c := range d.Columns {
			if c.IsPrimaryKey {
				pk = append(pk, c.Name)
			}
		}
	}
	if len(pk) > 0 {
		candidates = append(candidates, pk)
	}
	for _, ix := range d.Indexes {
		if ix.Name == "PRIMARY" || !ix.Unique || ix.Prefix || len(ix.Columns) == 0 {
			continue
		}
		ok := true
		for _, c := range ix.Columns {
			if n, known := nullable[strings.ToLower(c)]; !known || n { // "(expresión)" o columna que admite NULL
				ok = false
				break
			}
		}
		if ok {
			candidates = append(candidates, ix.Columns)
		}
	}
	if len(candidates) == 0 {
		return nil, ReasonNoKey
	}
	for _, cand := range candidates {
		all := true
		for _, c := range cand {
			if !selected[strings.ToLower(c)] {
				all = false
				break
			}
		}
		if all {
			return cand, ""
		}
	}
	return nil, ReasonKeyNotSelected
}

var binaryTypes = map[string]bool{
	"binary": true, "varbinary": true, "tinyblob": true, "blob": true, "mediumblob": true, "longblob": true, "bit": true,
	"geometry": true, "point": true, "linestring": true, "polygon": true, "multipoint": true, "multilinestring": true,
	"multipolygon": true, "geometrycollection": true,
}

func isBinaryType(dataType string) bool { return binaryTypes[strings.ToLower(dataType)] }
