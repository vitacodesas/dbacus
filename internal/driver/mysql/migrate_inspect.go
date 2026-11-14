package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
)

var _ driver.SchemaMigrator = (*pool)(nil)

var (
	autoIncOption = regexp.MustCompile(`(?m)\bAUTO_INCREMENT=(\d+)`)
	identSafe     = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	spatialTypes  = map[string]bool{"geometry": true, "point": true, "linestring": true, "polygon": true, "multipoint": true, "multilinestring": true, "multipolygon": true, "geometrycollection": true}
)

// querier es lo que comparten *sql.DB y *sql.Conn para leer.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inspectMode quita del modo SQL los que cambian la forma del SHOW CREATE.
func inspectMode(mode string) string {
	var out []string
	for _, m := range strings.Split(mode, ",") {
		if m = strings.TrimSpace(m); m != "" && m != "ANSI_QUOTES" && m != "NO_BACKSLASH_ESCAPES" && m != "ANSI" {
			out = append(out, m)
		}
	}
	return strings.Join(out, ",")
}

type idxInfo struct {
	name       string
	unique     bool
	typ        string
	cols       []string
	nullable   []bool
	prefix     bool
	functional bool
	card       int64 // cardinalidad de la columna líder
}

// stripForeignKeys quita del SHOW CREATE TABLE las líneas de claves foráneas (se vuelven a añadir al final, spec 23 ⚑D5) y arregla
// la coma de la línea anterior si era la última. Si queda algún «FOREIGN KEY» que no se quitó, el formato no es el esperado y
// falla cerrado (UNSUPPORTED) en vez de adivinar.
func stripForeignKeys(createSQL string, fks []model.ForeignKey) (string, error) {
	if len(fks) == 0 {
		if strings.Contains(strings.ToUpper(createSQL), "FOREIGN KEY") {
			return "", apperr.New(apperr.Unsupported, "El SHOW CREATE TABLE trae una clave foránea que no figura en information_schema").WithDetail("create_format")
		}
		return createSQL, nil
	}
	names := map[string]bool{}
	for _, fk := range fks {
		names[fk.Name] = true
	}
	lines := strings.Split(createSQL, "\n")
	kept := make([]string, 0, len(lines))
	removed := 0
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "CONSTRAINT `") && strings.Contains(t, "` FOREIGN KEY ") {
			rest := t[len("CONSTRAINT `"):]
			if end := strings.Index(rest, "` FOREIGN KEY "); end >= 0 && names[strings.ReplaceAll(rest[:end], "``", "`")] {
				removed++
				continue
			}
		}
		kept = append(kept, l)
	}
	if removed != len(names) {
		return "", apperr.New(apperr.Unsupported, "No se reconocen las claves foráneas del SHOW CREATE TABLE").WithDetail("create_format")
	}
	for i := 1; i < len(kept); i++ {
		if strings.HasPrefix(strings.TrimSpace(kept[i]), ")") {
			kept[i-1] = strings.TrimSuffix(strings.TrimRight(kept[i-1], " "), ",")
		}
	}
	out := strings.Join(kept, "\n")
	if strings.Contains(strings.ToUpper(out), "FOREIGN KEY") {
		return "", apperr.New(apperr.Unsupported, "Quedan claves foráneas sin reconocer en el SHOW CREATE TABLE").WithDetail("create_format")
	}
	return out, nil
}

// chooseKey elige la clave de troceo de una tabla (spec 23 ⚑D7): PK, única NOT NULL, particiones, primer índice no único.
func chooseKey(idx []idxInfo, parts []string) (spec model.ChunkKeySpec, hasPK bool, card int64) {
	var pk *idxInfo
	for i := range idx {
		if idx[i].name == "PRIMARY" {
			pk = &idx[i]
		}
	}
	if pk != nil {
		return model.ChunkKeySpec{Kind: model.ChunkPK, Columns: pk.cols}, true, 0
	}
	var best *idxInfo
	for i := range idx {
		x := &idx[i]
		if !x.unique || x.prefix || x.functional || x.typ != "BTREE" && x.typ != "HASH" {
			continue
		}
		notNull := true
		for _, n := range x.nullable {
			notNull = notNull && !n
		}
		if notNull && (best == nil || len(x.cols) < len(best.cols)) {
			best = x
		}
	}
	if best != nil {
		return model.ChunkKeySpec{Kind: model.ChunkUnique, Columns: best.cols}, false, 0
	}
	if len(parts) > 0 {
		return model.ChunkKeySpec{Kind: model.ChunkPartition, Partitions: parts}, false, 0
	}
	best = nil
	for i := range idx {
		x := &idx[i]
		if x.typ == "FULLTEXT" || x.typ == "SPATIAL" || x.functional || len(x.cols) == 0 || x.prefix {
			continue
		}
		if best == nil || x.card > best.card {
			best = x
		}
	}
	if best != nil {
		return model.ChunkKeySpec{Kind: model.ChunkIndex, Columns: best.cols[:1]}, false, best.card
	}
	return model.ChunkKeySpec{Kind: model.ChunkNone}, false, 0
}

// MigrationInspect lee el schema origen (solo lectura): tablas con su DDL sin claves foráneas, columnas copiables, clave de
// troceo y claves foráneas; vistas, rutinas y triggers con su CREATE.
func (p *pool) MigrationInspect(ctx context.Context, db string) (model.MigrationSource, error) {
	src := model.MigrationSource{Database: db, Tables: []model.MigrationTable{}, Views: []model.MigrationCode{}, Routines: []model.MigrationCode{}, Triggers: []model.MigrationCode{}}
	// El modo SQL es el global del servidor: una conexión del pool general puede arrastrar el SET de otro uso y no es fiable.
	if err := p.db.QueryRowContext(ctx, `SELECT VERSION(), @@GLOBAL.sql_mode`).Scan(&src.ServerVersion, &src.SQLMode); err != nil {
		return src, mapError(err)
	}
	// Todas las lecturas van por una sesión propia READ ONLY y con un modo estable (sin ANSI_QUOTES ni NO_BACKSLASH_ESCAPES, que
	// cambian cómo SHOW CREATE escribe identificadores y cadenas).
	sess, err := p.OpenMigration(ctx, driver.MigrationOpen{Role: driver.RoleSource, Database: db, Size: 8, SQLMode: inspectMode(src.SQLMode)})
	if err != nil {
		return src, err
	}
	defer sess.Close()
	q := sess.(*migSession).db
	err = q.QueryRowContext(ctx, `SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?`, db).Scan(&src.Charset, &src.Collation)
	if err == sql.ErrNoRows {
		return src, apperr.New(apperr.NotFound, "La base de datos de origen no existe")
	}
	if err != nil {
		return src, mapError(err)
	}
	src.MariaDB = strings.Contains(strings.ToLower(src.ServerVersion), "mariadb")

	tables, err := migTables(ctx, q, db)
	if err != nil {
		return src, err
	}
	cols, err := migColumns(ctx, q, db)
	if err != nil {
		return src, err
	}
	idx, err := migIndexes(ctx, q, db)
	if err != nil {
		return src, err
	}
	parts, err := migPartitions(ctx, q, db)
	if err != nil {
		return src, err
	}
	fks, err := migForeignKeys(ctx, q, db)
	if err != nil {
		return src, err
	}
	for i := range tables {
		t := &tables[i]
		for _, c := range cols[t.Name] {
			if c.generated {
				t.SkippedColumns = append(t.SkippedColumns, c.name)
				continue
			}
			t.Columns = append(t.Columns, c.name)
			if c.spatial {
				t.Spatial = append(t.Spatial, c.name)
			}
			if c.autoInc {
				t.AutoIncColumn = c.name
			}
		}
		seen := map[string]bool{}
		for _, c := range t.Charsets {
			seen[c] = true
		}
		for _, c := range cols[t.Name] {
			if c.collation != "" {
				seen[c.collation] = true
			}
		}
		t.Charsets = t.Charsets[:0]
		for c := range seen {
			t.Charsets = append(t.Charsets, c)
		}
		sort.Strings(t.Charsets)
		t.ForeignKeys = fks[t.Name]
		t.Partitioned = len(parts[t.Name]) > 0
		t.Key, t.HasPrimaryKey, t.KeyCardinality = chooseKey(idx[t.Name], parts[t.Name])
		if t.Spatial == nil {
			t.Spatial = []string{}
		}
		if t.SkippedColumns == nil {
			t.SkippedColumns = []string{}
		}
	}

	// SHOW CREATE TABLE de cada tabla, con un poco de paralelismo (son lecturas sobre el pool general).
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i := range tables {
		i := i
		g.Go(func() error {
			create, err := showCreate(gctx, q, "TABLE", db, tables[i].Name, 1)
			if err != nil {
				return err
			}
			stripped, err := stripForeignKeys(create, tables[i].ForeignKeys)
			if err != nil {
				return fmt.Errorf("%s: %w", tables[i].Name, err)
			}
			tables[i].CreateSQL = stripped
			if m := autoIncOption.FindStringSubmatch(create); m != nil {
				tables[i].AutoIncrement, _ = strconv.ParseInt(m[1], 10, 64)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return src, mapError(err)
	}
	src.Tables = tables

	if src.Views, err = migViews(ctx, q, db); err != nil {
		return src, err
	}
	if src.Routines, err = migRoutines(ctx, q, db); err != nil {
		return src, err
	}
	if src.Triggers, err = migTriggers(ctx, q, db); err != nil {
		return src, err
	}
	return src, nil
}

type colInfo struct {
	name      string
	generated bool
	spatial   bool
	autoInc   bool
	collation string
}

func migTables(ctx context.Context, q querier, db string) ([]model.MigrationTable, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT TABLE_NAME, COALESCE(TABLE_ROWS, 0), COALESCE(DATA_LENGTH + INDEX_LENGTH, 0), COALESCE(AVG_ROW_LENGTH, 0), COALESCE(TABLE_COLLATION, '')
		FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME`, db)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var out []model.MigrationTable
	for rows.Next() {
		var t model.MigrationTable
		var coll string
		if err := rows.Scan(&t.Name, &t.EstRows, &t.EstBytes, &t.AvgRowLen, &coll); err != nil {
			return nil, mapError(err)
		}
		t.Columns, t.SkippedColumns, t.Charsets, t.ForeignKeys = []string{}, []string{}, []string{}, []model.ForeignKey{}
		if coll != "" {
			t.Charsets = append(t.Charsets, coll)
		}
		out = append(out, t)
	}
	return out, mapError(rows.Err())
}

func migColumns(ctx context.Context, q querier, db string) (map[string][]colInfo, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT TABLE_NAME, COLUMN_NAME, EXTRA, DATA_TYPE, COLLATION_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION`, db)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := map[string][]colInfo{}
	for rows.Next() {
		var tbl, col, typ string
		var extra, coll sql.NullString
		if err := rows.Scan(&tbl, &col, &extra, &typ, &coll); err != nil {
			return nil, mapError(err)
		}
		ex := strings.ToUpper(extra.String)
		out[tbl] = append(out[tbl], colInfo{
			name: col, generated: strings.Contains(ex, "GENERATED"), spatial: spatialTypes[strings.ToLower(typ)],
			autoInc: strings.Contains(ex, "AUTO_INCREMENT"), collation: coll.String,
		})
	}
	return out, mapError(rows.Err())
}

func migIndexes(ctx context.Context, q querier, db string) (map[string][]idxInfo, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE, COLUMN_NAME, COALESCE(CARDINALITY, 0), SUB_PART, NULLABLE, INDEX_TYPE
		FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, db)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := map[string][]idxInfo{}
	for rows.Next() {
		var tbl, name, typ string
		var nonUnique int
		var col, nullable sql.NullString
		var card int64
		var sub sql.NullInt64
		if err := rows.Scan(&tbl, &name, &nonUnique, &col, &card, &sub, &nullable, &typ); err != nil {
			return nil, mapError(err)
		}
		list := out[tbl]
		if n := len(list); n == 0 || list[n-1].name != name {
			list = append(list, idxInfo{name: name, unique: nonUnique == 0, typ: strings.ToUpper(typ), card: card})
		}
		x := &list[len(list)-1]
		if !col.Valid { // índice funcional: la parte es una expresión
			x.functional = true
		}
		x.cols = append(x.cols, col.String)
		x.nullable = append(x.nullable, strings.EqualFold(nullable.String, "YES"))
		x.prefix = x.prefix || sub.Valid
		out[tbl] = list
	}
	return out, mapError(rows.Err())
}

func migPartitions(ctx context.Context, q querier, db string) (map[string][]string, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT TABLE_NAME, PARTITION_NAME, SUBPARTITION_NAME FROM information_schema.PARTITIONS
		WHERE TABLE_SCHEMA = ? AND PARTITION_NAME IS NOT NULL
		ORDER BY TABLE_NAME, PARTITION_ORDINAL_POSITION, SUBPARTITION_ORDINAL_POSITION`, db)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var tbl, part string
		var sub sql.NullString
		if err := rows.Scan(&tbl, &part, &sub); err != nil {
			return nil, mapError(err)
		}
		if sub.Valid {
			out[tbl] = append(out[tbl], sub.String)
		} else {
			out[tbl] = append(out[tbl], part)
		}
	}
	return out, mapError(rows.Err())
}

func migForeignKeys(ctx context.Context, q querier, db string) (map[string][]model.ForeignKey, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT k.TABLE_NAME, k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME, r.UPDATE_RULE, r.DELETE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k
		JOIN information_schema.REFERENTIAL_CONSTRAINTS r ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.TABLE_NAME = k.TABLE_NAME
		WHERE k.TABLE_SCHEMA = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, db)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	out := map[string][]model.ForeignKey{}
	for rows.Next() {
		var tbl, name, col, refSchema, refTable, refCol, upd, del string
		if err := rows.Scan(&tbl, &name, &col, &refSchema, &refTable, &refCol, &upd, &del); err != nil {
			return nil, mapError(err)
		}
		list := out[tbl]
		if n := len(list); n == 0 || list[n-1].Name != name {
			list = append(list, model.ForeignKey{Name: name, RefSchema: refSchema, RefTable: refTable, OnUpdate: upd, OnDelete: del})
		}
		fk := &list[len(list)-1]
		fk.Columns = append(fk.Columns, col)
		fk.RefColumns = append(fk.RefColumns, refCol)
		out[tbl] = list
	}
	return out, mapError(rows.Err())
}

// showCreate devuelve la columna `col` (1 = la segunda) de SHOW CREATE <kind> db.name.
func showCreate(ctx context.Context, q querier, kind, db, name string, col int) (string, error) {
	rows, err := q.QueryContext(ctx, "SHOW CREATE "+kind+" "+QuoteIdent(db)+"."+QuoteIdent(name))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	if !rows.Next() {
		return "", rows.Err()
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", err
	}
	return vals[col].String, nil
}

func migViews(ctx context.Context, q querier, db string) ([]model.MigrationCode, error) {
	rows, err := q.QueryContext(ctx, `SELECT TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, db)
	if err != nil {
		return nil, mapError(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return nil, mapError(err)
		}
		names = append(names, n)
	}
	if err := rows.Close(); err != nil {
		return nil, mapError(err)
	}
	out := []model.MigrationCode{}
	for _, n := range names {
		c, err := showCreateFull(ctx, q, "VIEW", db, n)
		if err != nil {
			return nil, mapError(err)
		}
		c.Name, c.Kind = n, "view"
		out = append(out, c)
	}
	return out, nil
}

// showCreateFull lee el CREATE y las variables de sesión con las que se creó (sql_mode, charset y collation de conexión).
func showCreateFull(ctx context.Context, q querier, kind, db, name string) (model.MigrationCode, error) {
	var c model.MigrationCode
	rows, err := q.QueryContext(ctx, "SHOW CREATE "+kind+" "+QuoteIdent(db)+"."+QuoteIdent(name))
	if err != nil {
		return c, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return c, err
	}
	if !rows.Next() {
		return c, rows.Err()
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return c, err
	}
	for i, col := range cols {
		switch strings.ToLower(col) {
		case "sql_mode":
			c.SQLMode = vals[i].String
		case "character_set_client":
			c.Charset = vals[i].String
		case "collation_connection":
			c.Collation = vals[i].String
		case "create view", "create procedure", "create function", "sql original statement":
			c.CreateSQL = vals[i].String
		}
	}
	return c, nil
}

func migRoutines(ctx context.Context, q querier, db string) ([]model.MigrationCode, error) {
	rows, err := q.QueryContext(ctx, `SELECT ROUTINE_NAME, ROUTINE_TYPE, DEFINER FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ? ORDER BY ROUTINE_TYPE, ROUTINE_NAME`, db)
	if err != nil {
		return nil, mapError(err)
	}
	type r struct{ name, typ, definer string }
	var list []r
	for rows.Next() {
		var x r
		var def sql.NullString
		if err := rows.Scan(&x.name, &x.typ, &def); err != nil {
			_ = rows.Close()
			return nil, mapError(err)
		}
		x.definer = def.String
		list = append(list, x)
	}
	if err := rows.Close(); err != nil {
		return nil, mapError(err)
	}
	out := []model.MigrationCode{}
	for _, x := range list {
		kind := model.KindProcedure
		if strings.EqualFold(x.typ, "FUNCTION") {
			kind = model.KindFunction
		}
		c, err := showCreateFull(ctx, q, strings.ToUpper(kind), db, x.name)
		if err != nil {
			return nil, mapError(err)
		}
		c.Name, c.Kind, c.Definer = x.name, kind, x.definer
		out = append(out, c)
	}
	return out, nil
}

func migTriggers(ctx context.Context, q querier, db string) ([]model.MigrationCode, error) {
	rows, err := q.QueryContext(ctx, `SELECT TRIGGER_NAME, EVENT_OBJECT_TABLE, DEFINER FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ? ORDER BY TRIGGER_NAME`, db)
	if err != nil {
		return nil, mapError(err)
	}
	type tr struct{ name, table, definer string }
	var list []tr
	for rows.Next() {
		var x tr
		var def sql.NullString
		if err := rows.Scan(&x.name, &x.table, &def); err != nil {
			_ = rows.Close()
			return nil, mapError(err)
		}
		x.definer = def.String
		list = append(list, x)
	}
	if err := rows.Close(); err != nil {
		return nil, mapError(err)
	}
	out := []model.MigrationCode{}
	for _, x := range list {
		c, err := showCreateFull(ctx, q, "TRIGGER", db, x.name)
		if err != nil {
			return nil, mapError(err)
		}
		c.Name, c.Kind, c.Table, c.Definer = x.name, "trigger", x.table, x.definer
		out = append(out, c)
	}
	return out, nil
}

// ---- Preflight ----

// MigrationPreflight comprueba el destino sin crear nada (spec 23 ⚑D13, ⚑D18, ⚑D19).
func (p *pool) MigrationPreflight(ctx context.Context, in model.MigrationPreflightInput) (model.MigrationPreflight, error) {
	var out model.MigrationPreflight
	var lctn int
	if err := p.db.QueryRowContext(ctx, `SELECT VERSION(), @@lower_case_table_names, @@max_connections, @@max_allowed_packet, CURRENT_USER()`).
		Scan(&out.ServerVersion, &lctn, &out.MaxConnections, &out.MaxPacket, &out.CurrentUser); err != nil {
		return out, mapError(err)
	}
	out.MariaDB = strings.Contains(strings.ToLower(out.ServerVersion), "mariadb")

	// Nombre canónico en el servidor: con lower_case_table_names=0 la comparación es exacta; con 1 o 2, la del servidor.
	canon := func(name string) (string, error) {
		q := `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?`
		if lctn == 0 {
			q = `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = BINARY ?`
		}
		var got string
		err := p.db.QueryRowContext(ctx, q, name).Scan(&got)
		if err == sql.ErrNoRows {
			return "", nil
		}
		return got, mapError(err)
	}
	var err error
	if out.CanonicalName, err = canon(in.Database); err != nil {
		return out, err
	}
	out.Exists = out.CanonicalName != ""
	if in.SameServer && out.Exists {
		srcCanon, err := canon(in.SourceName)
		if err != nil {
			return out, err
		}
		out.SameAsSource = srcCanon != "" && srcCanon == out.CanonicalName
	}
	if out.Exists {
		rows, err := p.db.QueryContext(ctx, `SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, out.CanonicalName)
		if err != nil {
			return out, mapError(err)
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return out, mapError(err)
			}
			out.ExistingTables = append(out.ExistingTables, n)
		}
		if err := rows.Close(); err != nil {
			return out, mapError(err)
		}
		var routines, triggers int
		if err := p.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?), (SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?)`, out.CanonicalName, out.CanonicalName).Scan(&routines, &triggers); err != nil {
			return out, mapError(err)
		}
		out.ExistingObjects = len(out.ExistingTables) + routines + triggers
		if len(in.Objects) > 0 {
			if out.ExistingCode, err = p.MigrationObjectCode(ctx, out.CanonicalName, in.Objects); err != nil {
				return out, err
			}
		}
	}
	var threads sql.NullString
	var name string
	if err := p.db.QueryRowContext(ctx, `SHOW GLOBAL STATUS LIKE 'Threads_connected'`).Scan(&name, &threads); err == nil {
		n, _ := strconv.Atoi(threads.String)
		out.FreeConnections = max(out.MaxConnections-n, 0)
	} else {
		out.FreeConnections = out.MaxConnections / 2
	}
	// Lo que valdrá en las conexiones de la migración (nacen del valor global más los parámetros del perfil), no en una conexión vieja del pool.
	if fresh, err := p.OpenMigration(ctx, driver.MigrationOpen{Role: driver.RoleTarget, Size: 1}); err == nil {
		var rpk int
		if c, err := fresh.(*migSession).db.Conn(ctx); err == nil {
			if err := c.QueryRowContext(ctx, `SELECT @@SESSION.sql_require_primary_key`).Scan(&rpk); err == nil {
				out.RequirePK = rpk != 0
			} // MariaDB y MySQL < 8.0.13 no la tienen: no es un error
			_ = c.Close()
		}
		_ = fresh.Close()
	}

	if len(in.Collations) > 0 {
		have := map[string]bool{}
		rows, err := p.db.QueryContext(ctx, `SELECT COLLATION_NAME FROM information_schema.COLLATIONS`)
		if err != nil {
			return out, mapError(err)
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return out, mapError(err)
			}
			have[n] = true
		}
		if err := rows.Close(); err != nil {
			return out, mapError(err)
		}
		for _, c := range in.Collations {
			if !have[c] {
				out.MissingCollation = append(out.MissingCollation, c)
			}
		}
	}
	out.Missing = p.missingPrivileges(ctx, in, out)
	return out, nil
}

// MigrationObjectCode dice cuáles de los objetos pedidos existen en `db` y con qué CREATE. Si no se puede leer el código (sin SHOW_ROUTINE o
// SHOW VIEW) el objeto sale igualmente, con el texto vacío: existe, pero no se podrá comparar.
func (p *pool) MigrationObjectCode(ctx context.Context, db string, want []model.ObjectRef) ([]model.MigrationCode, error) {
	have := map[string]string{} // «tipo:nombre en minúsculas» → nombre real
	collect := func(query, kind string, typed bool) error {
		rows, err := p.db.QueryContext(ctx, query, db)
		if err != nil {
			return mapError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var name, typ string
			if typed {
				if err := rows.Scan(&name, &typ); err != nil {
					return mapError(err)
				}
				kind = strings.ToLower(typ)
			} else if err := rows.Scan(&name); err != nil {
				return mapError(err)
			}
			have[kind+":"+strings.ToLower(name)] = name
		}
		return mapError(rows.Err())
	}
	if err := collect(`SELECT ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?`, "", true); err != nil {
		return nil, err
	}
	if err := collect(`SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'VIEW'`, "view", false); err != nil {
		return nil, err
	}
	if err := collect(`SELECT TRIGGER_NAME FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?`, "trigger", false); err != nil {
		return nil, err
	}
	// SHOW CREATE omite el nombre del schema cuando es la base por defecto de la conexión: se lee con una cuyo valor por defecto es `db`,
	// igual que el Inspect del origen, o el mismo objeto saldría calificado de un lado y sin calificar del otro.
	sess, err := p.OpenMigration(ctx, driver.MigrationOpen{Role: driver.RoleSource, Database: db, Size: 1})
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	q := sess.(*migSession).db
	var out []model.MigrationCode
	for _, w := range want {
		real, ok := have[w.Kind+":"+strings.ToLower(w.Name)]
		if !ok {
			continue
		}
		c, err := showCreateFull(ctx, q, strings.ToUpper(w.Kind), db, real)
		if err != nil {
			c = model.MigrationCode{} // existe pero no se puede leer: la comparación dirá «unknown»
		}
		out = append(out, model.MigrationCode{Name: w.Name, Kind: w.Kind, CreateSQL: c.CreateSQL})
	}
	return out, nil
}

// missingPrivileges mira SHOW GRANTS. Falla abierto: con roles, o si no se puede leer, no se afirma nada (el servidor dirá el
// error real en el objeto afectado); solo se avisa cuando está claro que falta algo.
func (p *pool) missingPrivileges(ctx context.Context, in model.MigrationPreflightInput, pre model.MigrationPreflight) []string {
	rows, err := p.db.QueryContext(ctx, `SHOW GRANTS FOR CURRENT_USER()`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var grants []string
	for rows.Next() {
		var g string
		if rows.Scan(&g) != nil {
			return nil
		}
		grants = append(grants, g)
	}
	have := map[string]bool{}
	dstName := pre.CanonicalName
	if dstName == "" {
		dstName = in.Database
	}
	for _, g := range grants {
		up := strings.ToUpper(g)
		if !strings.HasPrefix(up, "GRANT ") {
			continue
		}
		on := strings.Index(up, " ON ")
		to := strings.LastIndex(up, " TO ")
		if on < 0 || to < on {
			return nil
		}
		privs, scope := up[len("GRANT "):on], strings.TrimSpace(g[on+4:to])
		if !strings.Contains(scope, ".") { // GRANT `rol` TO usuario: hay roles, no se puede asegurar nada
			return nil
		}
		if !grantScopeCovers(scope, dstName) {
			continue
		}
		for _, x := range strings.Split(privs, ",") {
			x = strings.TrimSpace(x)
			if x == "ALL PRIVILEGES" || x == "ALL" {
				return nil
			}
			have[x] = true
		}
	}
	need := []string{"CREATE", "ALTER", "INSERT", "DELETE"}
	if in.NeedsView {
		need = append(need, "CREATE VIEW")
	}
	if in.NeedsRoutine {
		need = append(need, "CREATE ROUTINE")
	}
	if in.NeedsTrigger {
		need = append(need, "TRIGGER")
	}
	var missing []string
	for _, n := range need {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%s: GRANT %s ON %s.* TO %s;", strings.Join(missing, ", "), strings.Join(missing, ", "), QuoteIdent(in.Database), grantTarget(pre.CurrentUser))}
}

// grantScopeCovers dice si el ámbito de un GRANT (`*.*`, `base`.*, `ba%`.*) cubre el schema dado.
func grantScopeCovers(scope, db string) bool {
	i := strings.LastIndex(scope, ".")
	if i < 0 {
		return false
	}
	obj, schema := strings.TrimSpace(scope[i+1:]), strings.Trim(strings.TrimSpace(scope[:i]), "`")
	if obj != "*" {
		return false // privilegios de tabla o de columna: no cubren la creación de objetos del schema
	}
	if schema == "*" {
		return true
	}
	re := regexp.QuoteMeta(strings.ReplaceAll(schema, "\\_", "_"))
	re = strings.ReplaceAll(strings.ReplaceAll(re, "%", ".*"), "_", ".")
	ok, _ := regexp.MatchString("^(?i)"+re+"$", db)
	return ok
}

// MigrationEnsureDatabase crea el schema destino si no existe.
func (p *pool) MigrationEnsureDatabase(ctx context.Context, name, charset, collation string) (bool, error) {
	if name == "" || !identSafe.MatchString(charset) || !identSafe.MatchString(collation) {
		return false, apperr.New(apperr.Validation, "Nombre, charset o collation no válidos").WithDetail("database")
	}
	var one int
	err := p.db.QueryRowContext(ctx, `SELECT 1 FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = BINARY ?`, name).Scan(&one)
	if err == nil {
		return false, nil
	}
	if err != sql.ErrNoRows {
		return false, mapError(err)
	}
	stmt := "CREATE DATABASE " + QuoteIdent(name) + " CHARACTER SET " + charset + " COLLATE " + collation
	if _, err := p.db.ExecContext(ctx, stmt); err != nil {
		return false, mapError(err)
	}
	return true, nil
}

// MigrationDrop borra, en el orden recibido, lo que creó la migración (spec 23 ⚑D12). El schema solo se borra si queda vacío.
func (p *pool) MigrationDrop(ctx context.Context, db string, objs []model.MigrationCreated) error {
	s, err := p.OpenMigration(ctx, driver.MigrationOpen{Role: driver.RoleTarget, Database: "", Size: 1})
	if err != nil {
		return err
	}
	defer s.Close()
	conn, err := s.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	mc := conn.(*migConn)
	mc.guard = newWriteGuard(driver.RoleTarget, "", db)
	qdb := QuoteIdent(db)
	for _, o := range objs {
		var stmt string
		switch o.Kind {
		case "table":
			stmt = "DROP TABLE IF EXISTS " + qdb + "." + QuoteIdent(o.Name)
		case "view":
			stmt = "DROP VIEW IF EXISTS " + qdb + "." + QuoteIdent(o.Name)
		case model.KindProcedure:
			stmt = "DROP PROCEDURE IF EXISTS " + qdb + "." + QuoteIdent(o.Name)
		case model.KindFunction:
			stmt = "DROP FUNCTION IF EXISTS " + qdb + "." + QuoteIdent(o.Name)
		case "trigger":
			stmt = "DROP TRIGGER IF EXISTS " + qdb + "." + QuoteIdent(o.Name)
		case "database":
			var n int
			if err := p.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?) + (SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?) + (SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?) + (SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ?)`, db, db, db, db).Scan(&n); err != nil {
				return mapError(err)
			}
			if n > 0 {
				continue // alguien más dejó cosas en el schema: no se toca
			}
			if _, err := p.db.ExecContext(ctx, "DROP DATABASE "+qdb); err != nil {
				return mapError(err)
			}
			continue
		default:
			continue
		}
		if err := mc.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
