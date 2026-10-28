package mysql

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"sync"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver"
	"tabulariswails/internal/model"
)

// Bases internas de MySQL/MariaDB: se marcan IsSystem y la UI las oculta por defecto.
var systemSchemas = map[string]bool{"mysql": true, "sys": true, "performance_schema": true, "information_schema": true}

// schemaReader lee information_schema con consultas parametrizadas; usa el pool general (ADR-004).
type schemaReader struct {
	db *sql.DB

	engineOnce sync.Once
	mariaDB    bool
}

func newSchemaReader(db *sql.DB) driver.SchemaReader { return &schemaReader{db: db} }

// isMariaDB detecta el motor la primera vez que hace falta (los DEFAULT difieren entre motores). Si falla, asume MySQL.
func (r *schemaReader) isMariaDB(ctx context.Context) bool {
	r.engineOnce.Do(func() {
		var v string
		if err := r.db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v); err == nil {
			r.mariaDB = strings.Contains(strings.ToLower(v), "mariadb")
		}
	})
	return r.mariaDB
}

// mariaDefault convierte un DEFAULT de MariaDB (literal SQL: 'anon', 'it”s') en su valor, como lo devuelve MySQL.
func mariaDefault(d string) string {
	if len(d) >= 2 && d[0] == '\'' && d[len(d)-1] == '\'' {
		return strings.ReplaceAll(d[1:len(d)-1], "''", "'")
	}
	return d
}

func (r *schemaReader) Databases(ctx context.Context) ([]model.Database, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT SCHEMA_NAME AS name FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME`)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := []model.Database{}
	for rows.Next() {
		var d model.Database
		if err := rows.Scan(&d.Name); err != nil {
			return nil, mapError(err)
		}
		d.IsSystem = systemSchemas[strings.ToLower(d.Name)]
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return nameLess(out[i].Name, out[j].Name) })
	return out, mapError(rows.Err())
}

// nameLess da el mismo orden en MySQL y MariaDB (el ORDER BY de information_schema depende de la collation del servidor).
func nameLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

func (r *schemaReader) Tables(ctx context.Context, db string) ([]model.Table, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT TABLE_NAME AS name, TABLE_TYPE AS type, TABLE_ROWS AS row_estimate, TABLE_COMMENT AS comment
		FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, db)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := []model.Table{}
	for rows.Next() {
		var (
			t        model.Table
			typ      string
			estimate sql.NullInt64
		)
		if err := rows.Scan(&t.Name, &typ, &estimate, &t.Comment); err != nil {
			return nil, mapError(err)
		}
		t.Schema = db
		t.Type = "table"
		if strings.Contains(strings.ToUpper(typ), "VIEW") {
			t.Type = "view"
			t.Comment = "" // MySQL rellena el comentario de las vistas con la palabra "VIEW"
		} else if estimate.Valid {
			t.RowEstimate = &estimate.Int64
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool { return nameLess(out[i].Name, out[j].Name) })
	return out, mapError(rows.Err())
}

func (r *schemaReader) Describe(ctx context.Context, db, table string) (model.TableDetail, error) {
	detail := model.TableDetail{}

	// Metadatos de la tabla (también confirma que existe).
	var typ string
	var estimate sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT TABLE_TYPE, TABLE_ROWS, TABLE_COMMENT FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, db, table).
		Scan(&typ, &estimate, &detail.Comment)
	if errors.Is(err, sql.ErrNoRows) {
		return detail, apperr.Newf(apperr.NotFound, "La tabla «%s.%s» no existe", db, table)
	}
	if err != nil {
		return detail, mapError(err)
	}
	detail.Name, detail.Schema, detail.Type = table, db, "table"
	if strings.Contains(strings.ToUpper(typ), "VIEW") {
		detail.Type, detail.Comment = "view", ""
	} else if estimate.Valid {
		detail.RowEstimate = &estimate.Int64
	}

	var errs []error
	detail.Columns, err = r.columns(ctx, db, table)
	errs = append(errs, err)
	detail.Indexes, err = r.indexes(ctx, db, table)
	errs = append(errs, err)
	detail.ForeignKeys, err = r.foreignKeys(ctx, db, table)
	errs = append(errs, err)
	detail.IncomingFKs, err = r.incomingFKs(ctx, db, table)
	errs = append(errs, err)
	detail.CreateSQL, err = r.createSQL(ctx, db, table)
	errs = append(errs, err)
	if e := errors.Join(errs...); e != nil {
		return detail, e
	}
	return detail, nil
}

func (r *schemaReader) columns(ctx context.Context, db, table string) ([]model.Column, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_KEY, EXTRA, COLUMN_COMMENT, ORDINAL_POSITION, GENERATION_EXPRESSION
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, db, table)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	maria := r.isMariaDB(ctx)
	out := []model.Column{}
	for rows.Next() {
		var (
			c             model.Column
			nullable, key string
			extra         string
			def, genExpr  sql.NullString
		)
		if err := rows.Scan(&c.Name, &c.DataType, &c.ColumnType, &nullable, &def, &key, &extra, &c.Comment, &c.Ordinal, &genExpr); err != nil {
			return nil, mapError(err)
		}
		c.Nullable = strings.EqualFold(nullable, "YES")
		c.IsPrimaryKey = key == "PRI"
		c.IsAutoInc = strings.Contains(strings.ToLower(extra), "auto_increment")
		// EXTRA = DEFAULT_GENERATED es un valor por defecto con expresión, no una columna generada: se mira la expresión.
		c.IsGenerated = genExpr.Valid && genExpr.String != ""
		if def.Valid {
			d := def.String
			// MariaDB devuelve la cadena "NULL" para "sin valor por defecto" en columnas nulables.
			if !(c.Nullable && d == "NULL") {
				if maria {
					d = mariaDefault(d)
				}
				c.Default = &d
			}
		}
		out = append(out, c)
	}
	return out, mapError(rows.Err())
}

func (r *schemaReader) indexes(ctx context.Context, db, table string) ([]model.Index, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT INDEX_NAME, NON_UNIQUE, COLUMN_NAME, INDEX_TYPE, SUB_PART
		FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY INDEX_NAME, SEQ_IN_INDEX`, db, table)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := []model.Index{}
	pos := map[string]int{}
	for rows.Next() {
		var (
			name, typ string
			nonUnique int
			col       sql.NullString // NULL en índices funcionales de MySQL 8
			subPart   sql.NullInt64  // longitud del prefijo indexado; NULL si se indexa la columna entera
		)
		if err := rows.Scan(&name, &nonUnique, &col, &typ, &subPart); err != nil {
			return nil, mapError(err)
		}
		i, ok := pos[name]
		if !ok {
			i = len(out)
			pos[name] = i
			out = append(out, model.Index{Name: name, Unique: nonUnique == 0, Type: typ, Columns: []string{}})
		}
		if subPart.Valid {
			out[i].Prefix = true
		}
		if col.Valid {
			out[i].Columns = append(out[i].Columns, col.String)
		} else {
			out[i].Columns = append(out[i].Columns, "(expresión)")
		}
	}
	return out, mapError(rows.Err())
}

func (r *schemaReader) foreignKeys(ctx context.Context, db, table string) ([]model.ForeignKey, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
		       r.UPDATE_RULE, r.DELETE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k
		JOIN information_schema.REFERENTIAL_CONSTRAINTS r
		  ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.TABLE_NAME = k.TABLE_NAME
		WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, db, table)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := []model.ForeignKey{}
	pos := map[string]int{}
	for rows.Next() {
		var name, col, refSchema, refTable, refCol, onUpdate, onDelete string
		if err := rows.Scan(&name, &col, &refSchema, &refTable, &refCol, &onUpdate, &onDelete); err != nil {
			return nil, mapError(err)
		}
		i, ok := pos[name]
		if !ok {
			i = len(out)
			pos[name] = i
			out = append(out, model.ForeignKey{
				Name: name, RefSchema: refSchema, RefTable: refTable, OnUpdate: onUpdate, OnDelete: onDelete,
				Columns: []string{}, RefColumns: []string{},
			})
		}
		out[i].Columns = append(out[i].Columns, col)
		out[i].RefColumns = append(out[i].RefColumns, refCol)
	}
	return out, mapError(rows.Err())
}

// createSQL usa SHOW CREATE TABLE (también válido para vistas; la segunda columna trae el DDL).
func (r *schemaReader) createSQL(ctx context.Context, db, table string) (string, error) {
	rows, err := r.db.QueryContext(ctx, "SHOW CREATE TABLE "+QuoteIdent(db)+"."+QuoteIdent(table))
	if err != nil {
		return "", mapError(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", mapError(err)
	}
	if !rows.Next() {
		return "", mapError(rows.Err())
	}
	vals := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", mapError(err)
	}
	if len(vals) < 2 {
		return "", nil
	}
	return string(vals[1]), nil
}

func (r *schemaReader) Catalog(ctx context.Context, db string) (model.CompletionCatalog, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT TABLE_NAME, COLUMN_NAME, DATA_TYPE FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION`, db)
	if err != nil {
		return model.CompletionCatalog{}, mapError(err)
	}
	defer rows.Close()
	cat := model.CompletionCatalog{Tables: []model.CatalogTable{}}
	pos := map[string]int{}
	for rows.Next() {
		var table string
		var c model.CatalogColumn
		if err := rows.Scan(&table, &c.Name, &c.DataType); err != nil {
			return cat, mapError(err)
		}
		i, ok := pos[table]
		if !ok {
			i = len(cat.Tables)
			pos[table] = i
			cat.Tables = append(cat.Tables, model.CatalogTable{Name: table, Columns: []model.CatalogColumn{}})
		}
		cat.Tables[i].Columns = append(cat.Tables[i].Columns, c)
	}
	return cat, mapError(rows.Err())
}

// incomingFKs devuelve las claves foráneas de otras tablas que apuntan a esta (spec 14). En cada una, RefSchema/RefTable
// son la tabla que referencia, Columns sus columnas y RefColumns las de esta tabla a las que apuntan.
func (r *schemaReader) incomingFKs(ctx context.Context, db, table string) ([]model.ForeignKey, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT k.CONSTRAINT_NAME, k.TABLE_SCHEMA, k.TABLE_NAME, k.COLUMN_NAME, k.REFERENCED_COLUMN_NAME, r.UPDATE_RULE, r.DELETE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k
		JOIN information_schema.REFERENTIAL_CONSTRAINTS r
		  ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.TABLE_NAME = k.TABLE_NAME
		WHERE k.REFERENCED_TABLE_SCHEMA = ? AND k.REFERENCED_TABLE_NAME = ?
		ORDER BY k.TABLE_SCHEMA, k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, db, table)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := []model.ForeignKey{}
	pos := map[string]int{}
	for rows.Next() {
		var name, schema, tbl, col, refCol, onUpdate, onDelete string
		if err := rows.Scan(&name, &schema, &tbl, &col, &refCol, &onUpdate, &onDelete); err != nil {
			return nil, mapError(err)
		}
		key := schema + "\x00" + tbl + "\x00" + name
		i, ok := pos[key]
		if !ok {
			i = len(out)
			pos[key] = i
			out = append(out, model.ForeignKey{
				Name: name, RefSchema: schema, RefTable: tbl, OnUpdate: onUpdate, OnDelete: onDelete,
				Columns: []string{}, RefColumns: []string{},
			})
		}
		out[i].Columns = append(out[i].Columns, col)
		out[i].RefColumns = append(out[i].RefColumns, refCol)
	}
	return out, mapError(rows.Err())
}
