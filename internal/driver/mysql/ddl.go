package mysql

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/vitacodesas/dbacus/internal/alter"
	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
)

var (
	_            driver.DDLGenerator = (*Driver)(nil)
	identWordRe                      = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	nowDefaultRe                     = regexp.MustCompile(`(?i)^(CURRENT_TIMESTAMP|NOW|LOCALTIME|LOCALTIMESTAMP)(\(\d*\))?$`)
	versionRe                        = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)
)

// GenerateDDL traduce el ChangeSet (ya validado por alter.Validate) a sentencias ALTER TABLE (spec 19).
// Un solo ALTER por tabla, salvo cuando MySQL no admite mezclar operaciones: borrar una clave foránea junto a borrar un
// índice o una columna, o junto a crear otra con el mismo nombre. Entonces las claves foráneas se borran primero, aparte.
func (d *Driver) GenerateDDL(cs model.ChangeSet, m model.AlterModel, info driver.ServerInfo) ([]string, error) {
	if len(cs.Ops) == 0 {
		return nil, nil
	}
	q := QuoteIdent
	orig := map[string]*model.Column{}
	for i := range m.Columns {
		orig[m.Columns[i].Name] = &m.Columns[i]
	}

	var fkDrops, idxDrops, cols, idxRenames, idxAdds, fkAdds []string
	droppedFK := map[string]bool{}
	hasDropCol := false
	for _, op := range cs.Ops {
		switch op.Kind {
		case model.OpDropFK:
			fkDrops = append(fkDrops, "DROP FOREIGN KEY "+q(op.Name))
			droppedFK[op.Name] = true
		case model.OpDropIndex:
			idxDrops = append(idxDrops, "DROP INDEX "+q(op.Name))
		case model.OpDropColumn:
			cols = append(cols, "DROP COLUMN "+q(op.Name))
			hasDropCol = true
		case model.OpAddColumn, model.OpModifyColumn:
			def, err := d.columnDef(alter.Resolve(op, orig[op.Name], m.TableCollation))
			if err != nil {
				return nil, err
			}
			var clause string
			switch {
			case op.Kind == model.OpAddColumn:
				clause = "ADD COLUMN " + def
			case op.NewName != "" && op.NewName != op.Name:
				// CHANGE vale en todas las versiones (RENAME COLUMN exige MySQL 8.0 / MariaDB 10.5.2) y lleva la definición.
				clause = "CHANGE COLUMN " + q(op.Name) + " " + def
			default:
				clause = "MODIFY COLUMN " + def
			}
			switch {
			case op.First:
				clause += " FIRST"
			case op.After != "":
				clause += " AFTER " + q(op.After)
			}
			cols = append(cols, clause)
		case model.OpRenameIndex:
			if !supportsRenameIndex(info) {
				return nil, apperr.New(apperr.Unsupported, "Este servidor no puede renombrar índices (MySQL 5.7+ o MariaDB 10.5.2+)").WithDetail("renameIndex")
			}
			idxRenames = append(idxRenames, "RENAME INDEX "+q(op.Name)+" TO "+q(op.NewName))
		case model.OpAddIndex:
			idxAdds = append(idxAdds, addIndex(op))
		case model.OpAddFK:
			fkAdds = append(fkAdds, d.addFK(cs.Database, op))
		}
	}

	sameNameFK := false
	for _, op := range cs.Ops {
		sameNameFK = sameNameFK || (op.Kind == model.OpAddFK && droppedFK[op.Name])
	}
	rest := append(append(append(append(append([]string{}, idxDrops...), cols...), idxRenames...), idxAdds...), fkAdds...)
	target := "ALTER TABLE " + q(cs.Database) + "." + q(cs.Table)
	build := func(clauses []string) string { return target + "\n  " + strings.Join(clauses, ",\n  ") }

	if len(fkDrops) > 0 && len(rest) > 0 && (len(idxDrops) > 0 || hasDropCol || sameNameFK) {
		return []string{build(fkDrops), build(rest)}, nil
	}
	return []string{build(append(append([]string{}, fkDrops...), rest...))}, nil
}

// columnDef arma «`nombre` tipo [charset] [NULL] [DEFAULT] [AUTO_INCREMENT] [ON UPDATE] [COMMENT]».
func (d *Driver) columnDef(c alter.Column) (string, error) {
	var b strings.Builder
	b.WriteString(QuoteIdent(c.Name) + " " + c.Type)
	if c.Charset != "" || c.Collation != "" {
		for _, w := range []string{c.Charset, c.Collation} {
			if w != "" && !identWordRe.MatchString(w) {
				return "", apperr.New(apperr.Internal, "La columna «"+c.Name+"» tiene un charset o collation no reconocido")
			}
		}
		if c.Charset != "" {
			b.WriteString(" CHARACTER SET " + c.Charset)
		}
		if c.Collation != "" {
			b.WriteString(" COLLATE " + c.Collation)
		}
	}
	if c.Nullable {
		b.WriteString(" NULL")
	} else {
		b.WriteString(" NOT NULL")
	}
	if c.Default != nil {
		switch {
		case !c.DefaultExpr:
			b.WriteString(" DEFAULT " + d.QuoteString(*c.Default))
		case nowDefaultRe.MatchString(strings.TrimSpace(*c.Default)):
			b.WriteString(" DEFAULT " + strings.TrimSpace(*c.Default))
		default:
			b.WriteString(" DEFAULT (" + strings.TrimSpace(*c.Default) + ")")
		}
	}
	if c.AutoInc {
		b.WriteString(" AUTO_INCREMENT")
	}
	if c.OnUpdate != "" {
		b.WriteString(" ON UPDATE " + c.OnUpdate)
	}
	if c.Comment != "" {
		b.WriteString(" COMMENT " + d.QuoteString(c.Comment))
	}
	return b.String(), nil
}

func addIndex(op model.AlterOp) string {
	var parts []string
	for _, c := range op.Index.Columns {
		p := QuoteIdent(c.Name)
		if c.Prefix > 0 {
			p += "(" + strconv.Itoa(c.Prefix) + ")"
		}
		if c.Desc {
			p += " DESC"
		}
		parts = append(parts, p)
	}
	kind := ""
	switch op.Index.Kind {
	case "unique":
		kind = "UNIQUE "
	case "fulltext":
		kind = "FULLTEXT "
	}
	return fmt.Sprintf("ADD %sINDEX %s (%s)", kind, QuoteIdent(op.Name), strings.Join(parts, ", "))
}

func (d *Driver) addFK(db string, op model.AlterOp) string {
	fk := op.FK
	quote := func(names []string) string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = QuoteIdent(n)
		}
		return strings.Join(out, ", ")
	}
	schema := fk.RefSchema
	if schema == "" {
		schema = db
	}
	s := fmt.Sprintf("ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s.%s (%s)",
		QuoteIdent(op.Name), quote(fk.Columns), QuoteIdent(schema), QuoteIdent(fk.RefTable), quote(fk.RefColumns))
	if fk.OnUpdate != "" {
		s += " ON UPDATE " + strings.ToUpper(fk.OnUpdate)
	}
	if fk.OnDelete != "" {
		s += " ON DELETE " + strings.ToUpper(fk.OnDelete)
	}
	return s
}

// supportsRenameIndex: RENAME INDEX existe desde MySQL 5.7 y MariaDB 10.5.2. Si no se entiende la versión, se intenta.
func supportsRenameIndex(info driver.ServerInfo) bool {
	m := versionRe.FindAllStringSubmatch(info.Version, -1)
	if len(m) == 0 {
		return true
	}
	v := m[len(m)-1] // un servidor MariaDB antiguo antepone «5.5.5-»: la versión real es la última
	major, _ := strconv.Atoi(v[1])
	minor, _ := strconv.Atoi(v[2])
	patch, _ := strconv.Atoi(v[3])
	if info.MariaDB {
		return major > 10 || (major == 10 && (minor > 5 || (minor == 5 && patch >= 2)))
	}
	return major > 5 || (major == 5 && minor >= 7)
}
