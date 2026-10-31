package alter

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

var knownTypes = map[string]bool{
	"tinyint": true, "smallint": true, "mediumint": true, "int": true, "integer": true, "bigint": true,
	"decimal": true, "numeric": true, "dec": true, "fixed": true, "float": true, "double": true, "real": true, "bit": true,
	"bool": true, "boolean": true, "char": true, "varchar": true, "binary": true, "varbinary": true,
	"tinytext": true, "text": true, "mediumtext": true, "longtext": true,
	"tinyblob": true, "blob": true, "mediumblob": true, "longblob": true, "enum": true, "set": true,
	"date": true, "datetime": true, "timestamp": true, "time": true, "year": true, "json": true,
	"geometry": true, "point": true, "linestring": true, "polygon": true, "multipoint": true,
	"multilinestring": true, "multipolygon": true, "geometrycollection": true,
}

// «tipo», «tipo(10)», «tipo(10,2)», «enum('a','b')» y los modificadores unsigned/zerofill. Sin barras invertidas ni
// comillas sueltas dentro de los literales: lo que pasa este patrón no puede cerrar el tipo e inyectar SQL.
var typeRe = regexp.MustCompile(`(?i)^([a-z]+)(\(\s*(?:\d+(?:\s*,\s*\d+)?|'(?:[^'\\]|'')*'(?:\s*,\s*'(?:[^'\\]|'')*')*)\s*\))?((?:\s+(?:unsigned|signed|zerofill))*)$`)

// ValidType comprueba que t es un tipo de columna conocido y bien formado.
func ValidType(t string) bool {
	m := typeRe.FindStringSubmatch(strings.TrimSpace(t))
	return m != nil && knownTypes[strings.ToLower(m[1])]
}

var fkActions = map[string]bool{"": true, "RESTRICT": true, "CASCADE": true, "SET NULL": true, "NO ACTION": true}

func invalid(field, format string, a ...any) error {
	return apperr.Newf(apperr.Validation, format, a...).WithDetail(field)
}

func validName(field, n string) error {
	switch {
	case strings.TrimSpace(n) == "":
		return invalid(field, "Falta un nombre")
	case utf8.RuneCountInString(n) > 64:
		return invalid(field, "«%s» supera los 64 caracteres", n)
	case strings.TrimRight(n, " ") != n:
		return invalid(field, "«%s» no puede terminar en espacio", n)
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return invalid(field, "El nombre contiene caracteres de control")
		}
	}
	return nil
}

type set map[string]bool

func newSet(items []string) set {
	s := set{}
	for _, i := range items {
		s[i] = true
	}
	return s
}

// Validate comprueba el ChangeSet contra la tabla actual, aplicando las operaciones en orden sobre copias de los
// conjuntos de nombres. Devuelve VALIDATION con Detail = campo.
func Validate(cs model.ChangeSet, m model.AlterModel) error {
	if err := validName("table", cs.Table); err != nil {
		return err
	}
	if err := validName("database", cs.Database); err != nil {
		return err
	}
	byName := map[string]*model.Column{}
	var colNames, idxNames, fkNames []string
	for i := range m.Columns {
		byName[m.Columns[i].Name] = &m.Columns[i]
		colNames = append(colNames, m.Columns[i].Name)
	}
	for _, ix := range m.Indexes {
		idxNames = append(idxNames, ix.Name)
	}
	for _, fk := range m.ForeignKeys {
		fkNames = append(fkNames, fk.Name)
	}
	cols, idxs, fks := newSet(colNames), newSet(idxNames), newSet(fkNames)
	droppedCols, droppedIdx, droppedFK := set{}, set{}, set{}
	// Columnas de índices nuevos y qué columnas son AUTO_INCREMENT al final, para comprobar que tengan índice.
	var added []model.IndexSpec
	autoInc := map[string]bool{}
	for _, c := range m.Columns {
		if c.IsAutoInc {
			autoInc[c.Name] = true
		}
	}
	renamed := map[string]string{} // original → nuevo

	for n, op := range cs.Ops {
		f := fmt.Sprintf("ops[%d]", n)
		switch op.Kind {
		case model.OpAddColumn, model.OpModifyColumn:
			add := op.Kind == model.OpAddColumn
			if err := validName(f+".name", op.Name); err != nil {
				return err
			}
			final := op.Name
			if add {
				if cols[op.Name] {
					return invalid(f+".name", "Ya existe una columna «%s»", op.Name)
				}
				if op.Column == nil {
					return invalid(f+".column", "Falta la definición de la columna «%s»", op.Name)
				}
			} else {
				orig, ok := byName[op.Name]
				if !ok || !cols[op.Name] {
					return invalid(f+".name", "La columna «%s» no existe", op.Name)
				}
				if orig.IsGenerated {
					return invalid("generated", "La columna «%s» es generada y no se puede modificar desde aquí", op.Name)
				}
				if op.NewName != "" && op.NewName != op.Name {
					if err := validName(f+".newName", op.NewName); err != nil {
						return err
					}
					if cols[op.NewName] {
						return invalid(f+".newName", "Ya existe una columna «%s»", op.NewName)
					}
					delete(cols, op.Name)
					cols[op.NewName] = true
					renamed[op.Name] = op.NewName
					final = op.NewName
					if autoInc[op.Name] {
						delete(autoInc, op.Name)
						autoInc[final] = true
					}
				}
			}
			if add {
				cols[op.Name] = true
			}
			if op.Column != nil {
				if !ValidType(op.Column.Type) {
					return invalid(f+".column.type", "El tipo «%s» no es válido", op.Column.Type)
				}
				if op.Column.AutoInc {
					if !isIntType(op.Column.Type) {
						return invalid(f+".column.autoInc", "AUTO_INCREMENT solo se admite en columnas enteras")
					}
					autoInc[final] = true
				} else {
					delete(autoInc, final)
				}
				if op.Column.Default != nil && op.Column.DefaultExpr && !safeExpr(*op.Column.Default) {
					return invalid(f+".column.default", "La expresión del valor por defecto está vacía o no es válida")
				}
			}
			if op.First && op.After != "" {
				return invalid(f+".after", "Una columna no puede ir a la vez al principio y tras otra")
			}
			if op.After != "" && (!cols[op.After] || op.After == final) {
				return invalid(f+".after", "La columna «%s» no existe", op.After)
			}
		case model.OpDropColumn:
			orig, ok := byName[op.Name]
			if !ok || !cols[op.Name] {
				return invalid(f+".name", "La columna «%s» no existe", op.Name)
			}
			if orig.IsPrimaryKey {
				return invalid("primaryKey", "«%s» es parte de la clave primaria; cambiar la clave primaria no está soportado", op.Name)
			}
			delete(cols, op.Name)
			delete(autoInc, op.Name)
			droppedCols[op.Name] = true
		case model.OpAddIndex:
			if err := validName(f+".name", op.Name); err != nil {
				return err
			}
			if strings.EqualFold(op.Name, "PRIMARY") {
				return invalid(f+".name", "«PRIMARY» está reservado: cambiar la clave primaria no está soportado")
			}
			if idxs[op.Name] {
				return invalid(f+".name", "Ya existe un índice «%s»", op.Name)
			}
			ix := op.Index
			if ix == nil || len(ix.Columns) == 0 {
				return invalid(f+".index", "El índice «%s» necesita al menos una columna", op.Name)
			}
			switch ix.Kind {
			case "index", "unique", "fulltext":
			default:
				return invalid(f+".index.kind", "Tipo de índice desconocido «%s»", ix.Kind)
			}
			seen := set{}
			for _, c := range ix.Columns {
				if !cols[c.Name] {
					return invalid(f+".index.columns", "La columna «%s» no existe", c.Name)
				}
				if seen[c.Name] {
					return invalid(f+".index.columns", "La columna «%s» está repetida", c.Name)
				}
				seen[c.Name] = true
				if c.Prefix < 0 {
					return invalid(f+".index.columns", "El prefijo de «%s» no es válido", c.Name)
				}
			}
			idxs[op.Name] = true
			added = append(added, *ix)
		case model.OpDropIndex, model.OpRenameIndex:
			if strings.EqualFold(op.Name, "PRIMARY") {
				return invalid(f+".name", "La clave primaria no se puede modificar desde aquí")
			}
			if !idxs[op.Name] {
				return invalid(f+".name", "El índice «%s» no existe", op.Name)
			}
			delete(idxs, op.Name)
			if op.Kind == model.OpDropIndex {
				droppedIdx[op.Name] = true
				break
			}
			if err := validName(f+".newName", op.NewName); err != nil {
				return err
			}
			if strings.EqualFold(op.NewName, "PRIMARY") || idxs[op.NewName] {
				return invalid(f+".newName", "Ya existe un índice «%s»", op.NewName)
			}
			idxs[op.NewName] = true
		case model.OpAddFK:
			if err := validName(f+".name", op.Name); err != nil {
				return err
			}
			if fks[op.Name] {
				return invalid(f+".name", "Ya existe una clave foránea «%s»", op.Name)
			}
			fk := op.FK
			if fk == nil || len(fk.Columns) == 0 || len(fk.Columns) != len(fk.RefColumns) {
				return invalid(f+".fk", "La clave foránea «%s» debe tener el mismo número de columnas y de columnas referenciadas", op.Name)
			}
			if err := validName(f+".fk.refTable", fk.RefTable); err != nil {
				return err
			}
			for _, c := range fk.Columns {
				if !cols[c] {
					return invalid(f+".fk.columns", "La columna «%s» no existe", c)
				}
			}
			if !fkActions[strings.ToUpper(fk.OnUpdate)] || !fkActions[strings.ToUpper(fk.OnDelete)] {
				return invalid(f+".fk", "Acción referencial no válida")
			}
			fks[op.Name] = true
		case model.OpDropFK:
			if !fks[op.Name] {
				return invalid(f+".name", "La clave foránea «%s» no existe", op.Name)
			}
			delete(fks, op.Name)
			droppedFK[op.Name] = true
		default:
			return invalid(f+".kind", "Operación desconocida «%s»", op.Kind)
		}
	}

	if len(cols) == 0 {
		return invalid("columns", "La tabla no puede quedar sin columnas")
	}
	// Una columna borrada no puede seguir en un índice o clave foránea que se conserva (propia o de otra tabla).
	for c := range droppedCols {
		for _, ix := range m.Indexes {
			if !droppedIdx[ix.Name] && contains(ix.Columns, c) {
				return invalid("dependency", "La columna «%s» se usa en el índice «%s»; bórralo antes", c, ix.Name)
			}
		}
		for _, fk := range m.ForeignKeys {
			if !droppedFK[fk.Name] && contains(fk.Columns, c) {
				return invalid("dependency", "La columna «%s» se usa en la clave foránea «%s»; bórrala antes", c, fk.Name)
			}
		}
		for _, in := range m.IncomingFKs {
			if contains(in.RefColumns, c) {
				return invalid("dependency", "La columna «%s» la referencia «%s» de la tabla %s", c, in.Name, in.RefTable)
			}
		}
	}
	if len(autoInc) > 1 {
		return invalid("autoInc", "Solo puede haber una columna AUTO_INCREMENT")
	}
	// AUTO_INCREMENT exige que la columna encabece algún índice (o sea la clave primaria).
	for c := range autoInc {
		if !leadsIndex(c, m, droppedIdx, renamed, added) {
			return invalid("autoInc", "La columna AUTO_INCREMENT «%s» debe ser la primera de algún índice", c)
		}
	}
	return nil
}

func leadsIndex(col string, m model.AlterModel, dropped set, renamed map[string]string, added []model.IndexSpec) bool {
	for _, ix := range m.Indexes {
		if dropped[ix.Name] || len(ix.Columns) == 0 {
			continue
		}
		first := ix.Columns[0]
		if r, ok := renamed[first]; ok {
			first = r
		}
		if first == col {
			return true
		}
	}
	for _, ix := range added {
		if ix.Kind != "fulltext" && len(ix.Columns) > 0 && ix.Columns[0].Name == col {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// safeExpr acepta una expresión de DEFAULT solo si no puede cerrar la sentencia ni abrir un comentario: sin `;`, sin
// `--`, `#` ni `/*` fuera de cadenas, y con comillas y paréntesis balanceados.
func safeExpr(e string) bool {
	e = strings.TrimSpace(e)
	if e == "" {
		return false
	}
	depth := 0
	var quote rune
	rs := []rune(e)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if quote != 0 {
			switch {
			case r == '\\':
				i++
			case r == quote && i+1 < len(rs) && rs[i+1] == quote:
				i++
			case r == quote:
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '`', ';', '#':
			return false
		case '(':
			depth++
		case ')':
			if depth--; depth < 0 {
				return false
			}
		case '-', '/':
			if i+1 < len(rs) && ((r == '-' && rs[i+1] == '-') || (r == '/' && rs[i+1] == '*')) {
				return false
			}
		}
	}
	return quote == 0 && depth == 0
}
