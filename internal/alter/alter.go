// Package alter reúne lo independiente del motor del editor de estructura (spec 19): huella de la tabla, validación del
// ChangeSet, avisos y resolución de columnas. La sintaxis concreta de cada motor vive en su driver (driver.DDLGenerator).
package alter

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/vitacodesas/dbacus/internal/model"
)

var autoIncRe = regexp.MustCompile(`(?i)\s+AUTO_INCREMENT=\d+`)

// Fingerprint identifica la estructura de una tabla. Quita el contador AUTO_INCREMENT=N del CREATE TABLE: cambia con
// cada INSERT y haría saltar `stale_model` sin que nadie tocara la estructura.
func Fingerprint(createSQL string) string {
	sum := sha256.Sum256([]byte(autoIncRe.ReplaceAllString(createSQL, "")))
	return hex.EncodeToString(sum[:])
}

// Hash identifica el SQL que se muestra y se ejecuta (D6 del spec 19).
func Hash(stmts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(stmts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// Column es una columna ya resuelta: lo que edita el usuario más lo que se conserva de la original.
type Column struct {
	Name        string
	Type        string
	Nullable    bool
	Default     *string
	DefaultExpr bool
	AutoInc     bool
	Comment     string
	Charset     string
	Collation   string
	OnUpdate    string
}

// Resolve da el estado final de la columna de una operación addColumn/modifyColumn. orig es la columna actual (nil en
// addColumn). Charset, collation y ON UPDATE se conservan de la original si el tipo nuevo los admite. Una collation igual
// a la de la tabla no se escribe: la columna la hereda y el DDL queda como el que dio el servidor.
func Resolve(op model.AlterOp, orig *model.Column, tableCollation string) Column {
	name := op.Name
	if op.NewName != "" {
		name = op.NewName
	}
	c := Column{Name: name}
	switch {
	case op.Column != nil:
		c.Type, c.Nullable, c.Default, c.DefaultExpr = strings.TrimSpace(op.Column.Type), op.Column.Nullable, op.Column.Default, op.Column.DefaultExpr
		c.AutoInc, c.Comment = op.Column.AutoInc, op.Column.Comment
	case orig != nil:
		c.Type, c.Nullable, c.Default, c.DefaultExpr = orig.ColumnType, orig.Nullable, orig.Default, orig.DefaultExpr
		c.AutoInc, c.Comment = orig.IsAutoInc, orig.Comment
	}
	if orig != nil {
		if IsTextType(c.Type) && (orig.Collation == "" || orig.Collation != tableCollation) {
			c.Charset, c.Collation = orig.Charset, orig.Collation
		}
		if IsTimeType(c.Type) {
			c.OnUpdate = orig.OnUpdate
		}
	}
	return c
}

// BaseType devuelve el tipo sin argumentos ni modificadores, en minúsculas: «varchar(10)» → «varchar».
func BaseType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if i := strings.IndexAny(t, "( "); i >= 0 {
		t = t[:i]
	}
	return t
}

func IsTextType(t string) bool {
	switch BaseType(t) {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext", "enum", "set":
		return true
	}
	return false
}

func IsTimeType(t string) bool {
	switch BaseType(t) {
	case "timestamp", "datetime":
		return true
	}
	return false
}

func isIntType(t string) bool {
	switch BaseType(t) {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint":
		return true
	}
	return false
}
