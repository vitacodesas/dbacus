package mysql

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
)

// Pool `mig` de la migración (spec 23 ⚑D4): propio, sin consumir plazas de pestaña. Las variables de sesión van en el conector,
// así que también las lleva cada conexión que el pool abra tras una reconexión (un SET suelto se perdería).

var (
	sqlModeSafe = regexp.MustCompile(`^[A-Za-z0-9_,]*$`)
	nameSafe    = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	fkRules     = map[string]bool{"CASCADE": true, "SET NULL": true, "RESTRICT": true, "NO ACTION": true, "SET DEFAULT": true}
)

// initConnector ejecuta unas sentencias constantes en cada conexión nueva; si alguna falla, la conexión no se entrega.
// Cada Connect usa una copia de la configuración: go-sql-driver escribe en ella (con TLS «preferred» y un servidor sin TLS pone
// TLS a nil) y varias conexiones abriéndose a la vez la pisarían (carrera detectada con -race en MariaDB).
type initConnector struct {
	cfg   *gomysql.Config
	stmts []string
}

func (c *initConnector) Connect(ctx context.Context) (sqldriver.Conn, error) {
	base, err := gomysql.NewConnector(c.cfg.Clone())
	if err != nil {
		return nil, err
	}
	conn, err := base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	ex, ok := conn.(sqldriver.ExecerContext)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("el driver no permite ejecutar sentencias de inicio")
	}
	for _, s := range c.stmts {
		if _, err := ex.ExecContext(ctx, s, nil); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("sesión de migración: %w", err)
		}
	}
	return conn, nil
}

func (c *initConnector) Driver() sqldriver.Driver { return gomysql.MySQLDriver{} }

// migSQLMode es el sql_mode del origen sin los modos que rechazan fechas cero y con NO_AUTO_VALUE_ON_ZERO (conserva los ceros
// de una columna autoincremental). Se queda el modo estricto: nada se trunca en silencio.
func migSQLMode(src string) string {
	var out []string
	has := false
	for _, m := range strings.Split(src, ",") {
		m = strings.ToUpper(strings.TrimSpace(m))
		switch {
		case m == "" || m == "NO_ZERO_DATE" || m == "NO_ZERO_IN_DATE" || !nameSafe.MatchString(m):
			continue
		case m == "NO_AUTO_VALUE_ON_ZERO":
			has = true
		}
		out = append(out, m)
	}
	if !has {
		out = append(out, "NO_AUTO_VALUE_ON_ZERO")
	}
	return strings.Join(out, ",")
}

func migInitStatements(o driver.MigrationOpen) (stmts []string, mode string) {
	vars := []string{"foreign_key_checks=0"}
	if o.Role == driver.RoleServer {
		vars = append(vars, "unique_checks=0")
	}
	vars = append(vars, "time_zone='+00:00'", "lock_wait_timeout=30", "innodb_lock_wait_timeout=20")
	if o.SQLMode != "" && sqlModeSafe.MatchString(o.SQLMode) {
		mode = migSQLMode(o.SQLMode)
		vars = append(vars, "sql_mode='"+mode+"'")
	}
	stmts = append(stmts, "SET SESSION "+strings.Join(vars, ", "))
	if o.Role == driver.RoleSource {
		stmts = append(stmts, "SET SESSION TRANSACTION READ ONLY") // lo impone el servidor: error 1792 ante cualquier escritura
	}
	return stmts, mode
}

func (p *pool) OpenMigration(ctx context.Context, o driver.MigrationOpen) (driver.MigrationSession, error) {
	switch o.Role {
	case driver.RoleSource, driver.RoleTarget, driver.RoleServer:
	default:
		return nil, apperr.New(apperr.Internal, "Rol de migración desconocido")
	}
	if o.Size < 1 {
		o.Size = 1
	}
	if p.cfg == nil {
		return nil, apperr.New(apperr.Internal, "El pool no guardó su configuración")
	}
	cfg := p.cfg.Clone()
	cfg.DBName = o.Database
	if _, err := gomysql.NewConnector(cfg); err != nil { // valida la configuración
		return nil, mapError(err)
	}
	if o.SQLMode != "" && sqlModeSafe.MatchString(o.SQLMode) {
		o.SQLMode = p.acceptedModes(ctx, cfg, o.SQLMode)
	}
	stmts, mode := migInitStatements(o)
	db := sql.OpenDB(&initConnector{cfg: cfg, stmts: stmts})
	db.SetMaxOpenConns(o.Size)
	db.SetMaxIdleConns(o.Size)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return &migSession{p: p, db: db, o: o, mode: mode}, nil
}

// acceptedModes quita del sql_mode los modos que ESTE servidor no conoce (error 1231): un origen 5.7 trae NO_AUTO_CREATE_USER, que un 8.x
// rechaza. Se prueba en una conexión desechable para no tocar las de la migración; si no se puede conectar, deja la lista intacta y el
// error real sale al abrir la conexión de trabajo.
func (p *pool) acceptedModes(ctx context.Context, cfg *gomysql.Config, list string) string {
	tmp := sql.OpenDB(&initConnector{cfg: cfg})
	defer tmp.Close()
	tmp.SetMaxOpenConns(1)
	conn, err := tmp.Conn(ctx)
	if err != nil {
		return list
	}
	defer conn.Close()
	return compatibleModes(ctx, func(ctx context.Context, stmt string) error {
		_, err := conn.ExecContext(ctx, stmt)
		return err
	}, list)
}

// compatibleModes devuelve los modos de `list` que acepta el servidor al que apunta exec. Solo el error 1231 (valor no válido para la
// variable) descarta un modo; cualquier otro fallo devuelve la lista tal cual para no ocultar un problema distinto.
func compatibleModes(ctx context.Context, exec func(ctx context.Context, stmt string) error, list string) string {
	err := exec(ctx, "SET SESSION sql_mode='"+list+"'")
	if err == nil || !isBadVariableValue(err) {
		return list
	}
	var out []string
	for _, m := range strings.Split(list, ",") {
		if m = strings.TrimSpace(m); m == "" {
			continue
		}
		if err := exec(ctx, "SET SESSION sql_mode='"+m+"'"); err != nil && isBadVariableValue(err) {
			continue
		}
		out = append(out, m)
	}
	return strings.Join(out, ",")
}

func isBadVariableValue(err error) bool {
	var me *gomysql.MySQLError
	return errors.As(err, &me) && me.Number == 1231
}

type migSession struct {
	p    *pool
	db   *sql.DB
	o    driver.MigrationOpen
	mode string // sql_mode con el que nacen las conexiones ("" = el del servidor)
}

func (s *migSession) Close() error { return s.db.Close() }

func (s *migSession) Acquire(ctx context.Context) (driver.MigrationConn, error) {
	c, err := s.db.Conn(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	var id uint64
	if err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		_ = c.Close()
		return nil, mapError(err)
	}
	g := newWriteGuard(s.o.Role, s.o.Source, s.o.Database)
	if s.o.Role == driver.RoleSource {
		g = newWriteGuard(s.o.Role, "", "")
	}
	return &migConn{s: s, c: c, id: id, guard: g}, nil
}

type migConn struct {
	s     *migSession
	c     *sql.Conn
	id    uint64
	guard *writeGuard
	tx    *sql.Tx
	stmts map[string]*sql.Stmt
}

func (c *migConn) ThreadID() uint64 { return c.id }

// Kill aborta la sentencia en curso de ESTE hilo por el pool auxiliar (spec 23 ⚑D19.5: solo hilos propios).
func (c *migConn) Kill(ctx context.Context) error {
	_, err := c.s.p.aux.ExecContext(ctx, "KILL QUERY "+strconv.FormatUint(c.id, 10))
	var me *gomysql.MySQLError
	if errors.As(err, &me) && me.Number == 1094 {
		return nil // el hilo ya terminó
	}
	return mapError(err)
}

func (c *migConn) Release() error {
	if c.tx != nil {
		_ = c.tx.Rollback()
		c.tx = nil
	}
	c.stmts = nil
	return c.c.Close()
}

func (c *migConn) internal(msg string) error { return apperr.New(apperr.Internal, msg) }

func (c *migConn) needWrite() error {
	if c.s.o.Role == driver.RoleSource {
		return c.internal("Las conexiones del origen no escriben")
	}
	return nil
}

func (c *migConn) needRead() error {
	if c.s.o.Role == driver.RoleTarget {
		return c.internal("Las conexiones de escritura del destino no leen el origen")
	}
	return nil
}

// readSchema es el schema del que se lee la tabla: el origen en RoleSource y RoleServer.
func (c *migConn) readSchema() string {
	if c.s.o.Role == driver.RoleServer {
		return c.s.o.Source
	}
	return c.s.o.Database
}

func (c *migConn) exec(ctx context.Context, stmt string, args ...any) (sql.Result, error) {
	if err := c.guard.check(stmt); err != nil {
		return nil, err
	}
	var (
		res sql.Result
		err error
	)
	if c.tx != nil {
		res, err = c.tx.ExecContext(ctx, stmt, args...)
	} else {
		res, err = c.c.ExecContext(ctx, stmt, args...)
	}
	return res, mapError(err)
}

func (c *migConn) query(ctx context.Context, stmt string, args ...any) (*sql.Rows, error) {
	if err := c.guard.check(stmt); err != nil {
		return nil, err
	}
	var (
		rows *sql.Rows
		err  error
	)
	if c.tx != nil {
		rows, err = c.tx.QueryContext(ctx, stmt, args...)
	} else {
		rows, err = c.c.QueryContext(ctx, stmt, args...)
	}
	return rows, mapError(err)
}

func (c *migConn) Exec(ctx context.Context, stmt string) error {
	if err := c.needWrite(); err != nil {
		return err
	}
	_, err := c.exec(ctx, stmt)
	return err
}

func (c *migConn) Begin(ctx context.Context) error {
	if c.tx != nil {
		return c.internal("Ya hay una transacción abierta en esta conexión")
	}
	// READ COMMITTED: INSERT … SELECT lee el origen sin bloquear (con REPEATABLE READ pone bloqueos compartidos en sus filas).
	tx, err := c.c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: c.s.o.Role == driver.RoleSource})
	if err != nil {
		return mapError(err)
	}
	c.tx, c.stmts = tx, nil
	return nil
}

func (c *migConn) Commit(context.Context) error {
	tx := c.tx
	if tx == nil {
		return c.internal("No hay transacción abierta")
	}
	c.tx, c.stmts = nil, nil
	return mapError(tx.Commit())
}

func (c *migConn) Rollback(context.Context) error {
	tx := c.tx
	if tx == nil {
		return nil
	}
	c.tx, c.stmts = nil, nil
	return mapError(tx.Rollback())
}

// ---- consultas generadas ----

func qcols(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = QuoteIdent(c)
	}
	return strings.Join(q, ", ")
}

// geClause: clave >= vals; gtClause: clave > vals; ltClause: clave < vals. Con varias columnas se expande en disyunciones
// (no se usa la comparación de filas: el optimizador no siempre la convierte en un rango de índice).
func keyClause(cols []string, vals []any, strict, lower bool) (string, []any) {
	op := ">"
	if !lower {
		op = "<"
	}
	if len(cols) == 1 {
		o := op
		if !strict && lower {
			o = ">="
		}
		return QuoteIdent(cols[0]) + " " + o + " ?", []any{vals[0]}
	}
	var parts []string
	var args []any
	for i := range cols {
		var eq []string
		for j := 0; j < i; j++ {
			eq = append(eq, QuoteIdent(cols[j])+" = ?")
			args = append(args, vals[j])
		}
		eq = append(eq, QuoteIdent(cols[i])+" "+op+" ?")
		args = append(args, vals[i])
		parts = append(parts, "("+strings.Join(eq, " AND ")+")")
	}
	if !strict && lower { // todas iguales
		var eq []string
		for j := range cols {
			eq = append(eq, QuoteIdent(cols[j])+" = ?")
			args = append(args, vals[j])
		}
		parts = append(parts, "("+strings.Join(eq, " AND ")+")")
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// chunkWhere arma la parte de un trozo: PARTITION(…) y WHERE. Un trozo de clave de índice no único excluye los NULL (van en su propio trozo).
func chunkWhere(t model.MigrationTable, ch driver.MigrationChunk) (partition, where string, args []any) {
	if ch.Partition != "" {
		partition = " PARTITION (" + QuoteIdent(ch.Partition) + ")"
	}
	if ch.Whole {
		return partition, "", nil
	}
	var conds []string
	cols := t.Key.Columns
	if ch.Null {
		return partition, " WHERE " + QuoteIdent(cols[0]) + " IS NULL", nil
	}
	if ch.Lo != nil {
		c, a := keyClause(cols, ch.Lo, false, true)
		conds, args = append(conds, c), append(args, a...)
	}
	if ch.Hi != nil {
		c, a := keyClause(cols, ch.Hi, true, false)
		conds, args = append(conds, c), append(args, a...)
	}
	if t.Key.Kind == model.ChunkIndex {
		conds = append(conds, QuoteIdent(cols[0])+" IS NOT NULL")
	}
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	return partition, where, args
}

func scanKey(rows *sql.Rows, n int) ([]any, bool, error) {
	if !rows.Next() {
		return nil, false, rows.Err()
	}
	vals := make([]any, n)
	ptrs := make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, false, err
	}
	return vals, true, nil
}

// Chunks recorre el índice de la clave de troceo y emite los trozos a medida que los calcula (spec 23 ⚑D7). Los límites se piden
// con OFFSET sobre el índice, así que cada tabla se lee una sola vez de punta a punta, sin tocar los datos.
func (c *migConn) Chunks(ctx context.Context, t model.MigrationTable, chunkRows int, emit func(driver.MigrationChunk) error) error {
	if err := c.needRead(); err != nil {
		return err
	}
	cols := t.Key.Columns
	if len(cols) == 0 {
		return c.internal("La tabla no tiene clave de troceo")
	}
	tbl := QuoteIdent(c.readSchema()) + "." + QuoteIdent(t.Name)
	qc := qcols(cols)
	seq := 0
	if t.Key.Kind == model.ChunkIndex { // las filas con NULL no caen en ningún rango: trozo propio, solo si las hay
		rows, err := c.query(ctx, "SELECT 1 FROM "+tbl+" WHERE "+QuoteIdent(cols[0])+" IS NULL LIMIT 1")
		if err != nil {
			return err
		}
		_, found, serr := scanKey(rows, 1)
		_ = rows.Close()
		if serr != nil {
			return mapError(serr)
		}
		if found {
			if err := emit(driver.MigrationChunk{Seq: seq, Null: true}); err != nil {
				return err
			}
			seq++
		}
	}
	step := int64(chunkRows)
	group := ""
	if t.Key.Kind == model.ChunkIndex { // índice no único: los límites cuentan valores distintos, para avanzar siempre
		group = " GROUP BY " + qc
		if t.EstRows > 0 && t.KeyCardinality > 0 {
			step = max(1, int64(chunkRows)*t.KeyCardinality/t.EstRows)
		} else {
			step = 1
		}
	}
	var lo []any
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var conds []string
		var args []any
		if lo != nil {
			cl, a := keyClause(cols, lo, false, true)
			conds, args = append(conds, cl), a
		}
		if t.Key.Kind == model.ChunkIndex {
			conds = append(conds, QuoteIdent(cols[0])+" IS NOT NULL")
		}
		where := ""
		if len(conds) > 0 {
			where = " WHERE " + strings.Join(conds, " AND ")
		}
		rows, err := c.query(ctx, fmt.Sprintf("SELECT %s FROM %s%s%s ORDER BY %s LIMIT 1 OFFSET %d", qc, tbl, where, group, qc, step), args...)
		if err != nil {
			return err
		}
		hi, ok, serr := scanKey(rows, len(cols))
		_ = rows.Close()
		if serr != nil {
			return mapError(serr)
		}
		if !ok { // el último trozo llega hasta el final
			return emit(driver.MigrationChunk{Seq: seq, Lo: lo})
		}
		if err := emit(driver.MigrationChunk{Seq: seq, Lo: lo, Hi: hi}); err != nil {
			return err
		}
		seq++
		lo = hi
	}
}

// valueBytes estima lo que pesa un valor para acotar los lotes.
func valueBytes(v any) int {
	switch x := v.(type) {
	case nil:
		return 1
	case []byte:
		return len(x)
	case string:
		return len(x)
	default:
		return 8
	}
}

// Read transmite un trozo en lotes. Va por una sentencia preparada (protocolo binario) para que flotantes y enteros viajen exactos.
func (c *migConn) Read(ctx context.Context, t model.MigrationTable, ch driver.MigrationChunk, batchBytes int, emit func(driver.RowBatch) error) error {
	if c.s.o.Role != driver.RoleSource {
		return c.internal("Read solo se usa con las conexiones del origen")
	}
	partition, where, args := chunkWhere(t, ch)
	q := "SELECT " + qcols(t.Columns) + " FROM " + QuoteIdent(c.s.o.Database) + "." + QuoteIdent(t.Name) + partition + where
	if err := c.guard.check(q); err != nil {
		return err
	}
	tx, err := c.c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if err != nil {
		return mapError(err)
	}
	defer func() { _ = tx.Rollback() }()
	st, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return mapError(err)
	}
	defer st.Close()
	rows, err := st.QueryContext(ctx, args...)
	if err != nil {
		return mapError(err)
	}
	defer rows.Close()
	n := len(t.Columns)
	var b driver.RowBatch
	for rows.Next() {
		vals := make([]any, n)
		ptrs := make([]any, n)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return mapError(err)
		}
		for _, v := range vals {
			b.Bytes += valueBytes(v)
		}
		b.Rows = append(b.Rows, vals)
		if b.Bytes >= batchBytes {
			out := b
			b = driver.RowBatch{}
			if err := emit(out); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return mapError(err)
	}
	if len(b.Rows) > 0 {
		return emit(b)
	}
	return nil
}

func (c *migConn) CountRows(ctx context.Context, table string) (int64, error) {
	schema := c.s.o.Database // RoleSource cuenta el origen; RoleTarget y RoleServer, el destino
	rows, err := c.query(ctx, "SELECT COUNT(*) FROM "+QuoteIdent(schema)+"."+QuoteIdent(table))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int64
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, mapError(err)
		}
	}
	return n, mapError(rows.Err())
}

// Insert escribe un lote con INSERT multifila preparado dentro de la transacción abierta. Se prepara una vez por forma de lote y
// transacción; el tope de filas respeta los 65 535 marcadores por sentencia.
func (c *migConn) Insert(ctx context.Context, t model.MigrationTable, _ driver.MigrationChunk, b driver.RowBatch) (int64, error) {
	if c.s.o.Role != driver.RoleTarget {
		return 0, c.internal("Insert solo se usa con las conexiones de escritura del destino")
	}
	if c.tx == nil {
		return 0, c.internal("Insert exige una transacción abierta")
	}
	ncols := len(t.Columns)
	per := max(1, min(1000, 65535/max(ncols, 1)))
	var total int64
	for off := 0; off < len(b.Rows); off += per {
		part := b.Rows[off:min(off+per, len(b.Rows))]
		key := t.Name + "#" + strconv.Itoa(len(part))
		st := c.stmts[key]
		if st == nil {
			q := insertSQL(c.s.o.Database, t, len(part))
			if err := c.guard.check(q); err != nil {
				return total, err
			}
			var err error
			if st, err = c.tx.PrepareContext(ctx, q); err != nil {
				return total, mapError(err)
			}
			if c.stmts == nil {
				c.stmts = map[string]*sql.Stmt{}
			}
			c.stmts[key] = st
		}
		args := make([]any, 0, len(part)*ncols)
		for _, r := range part {
			args = append(args, r...)
		}
		res, err := st.ExecContext(ctx, args...)
		if err != nil {
			return total, mapError(err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

func insertSQL(db string, t model.MigrationTable, rows int) string {
	one := "(" + strings.TrimSuffix(strings.Repeat("?,", len(t.Columns)), ",") + ")"
	var b strings.Builder
	b.WriteString("INSERT INTO " + QuoteIdent(db) + "." + QuoteIdent(t.Name) + " (" + qcols(t.Columns) + ") VALUES ")
	for i := 0; i < rows; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(one)
	}
	return b.String()
}

// CopyChunk copia un trozo dentro del servidor (INSERT … SELECT). El origen solo aparece detrás de FROM: lo comprueba el guardia.
func (c *migConn) CopyChunk(ctx context.Context, t model.MigrationTable, ch driver.MigrationChunk) (int64, error) {
	if c.s.o.Role != driver.RoleServer {
		return 0, c.internal("CopyChunk solo se usa con las conexiones de la ruta dentro del servidor")
	}
	partition, where, args := chunkWhere(t, ch)
	cols := qcols(t.Columns)
	q := "INSERT INTO " + QuoteIdent(c.s.o.Database) + "." + QuoteIdent(t.Name) + " (" + cols + ") SELECT " + cols +
		" FROM " + QuoteIdent(c.s.o.Source) + "." + QuoteIdent(t.Name) + partition + where
	res, err := c.exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ClearRange borra del destino el rango de un trozo antes de reintentarlo. Un trozo «entero» de toda la tabla la vacía con
// TRUNCATE (la tabla la creó esta migración).
func (c *migConn) ClearRange(ctx context.Context, t model.MigrationTable, ch driver.MigrationChunk) error {
	if err := c.needWrite(); err != nil {
		return err
	}
	tbl := QuoteIdent(c.s.o.Database) + "." + QuoteIdent(t.Name)
	if ch.Whole && ch.Partition == "" {
		_, err := c.exec(ctx, "TRUNCATE TABLE "+tbl)
		return err
	}
	partition, where, args := chunkWhere(t, ch)
	_, err := c.exec(ctx, "DELETE FROM "+tbl+partition+where, args...)
	return err
}

func (c *migConn) AddForeignKey(ctx context.Context, table string, fk model.ForeignKey) error {
	if err := c.needWrite(); err != nil {
		return err
	}
	rule := func(r string) string {
		r = strings.ToUpper(strings.TrimSpace(r))
		if fkRules[r] {
			return r
		}
		return "RESTRICT"
	}
	q := "ALTER TABLE " + QuoteIdent(c.s.o.Database) + "." + QuoteIdent(table) + " ADD CONSTRAINT " + QuoteIdent(fk.Name) +
		" FOREIGN KEY (" + qcols(fk.Columns) + ") REFERENCES " + QuoteIdent(fk.RefSchema) + "." + QuoteIdent(fk.RefTable) +
		" (" + qcols(fk.RefColumns) + ") ON DELETE " + rule(fk.OnDelete) + " ON UPDATE " + rule(fk.OnUpdate)
	_, err := c.exec(ctx, q)
	return err
}

func (c *migConn) FixAutoIncrement(ctx context.Context, table, column string, source int64) error {
	if err := c.needWrite(); err != nil {
		return err
	}
	tbl := QuoteIdent(c.s.o.Database) + "." + QuoteIdent(table)
	rows, err := c.query(ctx, "SELECT COALESCE(MAX("+QuoteIdent(column)+"), 0) + 1 FROM "+tbl)
	if err != nil {
		return err
	}
	var next int64
	if rows.Next() {
		_ = rows.Scan(&next)
	}
	_ = rows.Close()
	next = max(next, source)
	if next < 1 {
		return nil
	}
	_, err = c.exec(ctx, "ALTER TABLE "+tbl+" AUTO_INCREMENT = "+strconv.FormatInt(next, 10))
	return err
}

// CreateObject crea una vista, rutina o trigger con el sql_mode y los charsets con los que se creó en el origen, y los restaura después.
func (c *migConn) CreateObject(ctx context.Context, obj model.MigrationCode, createSQL string) error {
	if err := c.needWrite(); err != nil {
		return err
	}
	if strings.TrimSpace(createSQL) == "" {
		return c.internal("El objeto «" + obj.Name + "» no tiene código que crear: el origen no dejó leerlo")
	}
	var set []string
	if sqlModeSafe.MatchString(obj.SQLMode) && obj.SQLMode != "" {
		mode := compatibleModes(ctx, func(ctx context.Context, stmt string) error {
			_, err := c.c.ExecContext(ctx, stmt)
			return err
		}, obj.SQLMode)
		if mode != "" {
			set = append(set, "sql_mode='"+mode+"'")
		}
	}
	if nameSafe.MatchString(obj.Charset) {
		set = append(set, "character_set_client="+obj.Charset)
	}
	if nameSafe.MatchString(obj.Collation) {
		set = append(set, "collation_connection="+obj.Collation)
	}
	if len(set) > 0 {
		if _, err := c.c.ExecContext(ctx, "SET SESSION "+strings.Join(set, ", ")); err != nil {
			return mapError(err)
		}
		defer func() {
			restore := "SET SESSION character_set_client=utf8mb4, collation_connection=utf8mb4_general_ci"
			if c.s.mode != "" {
				restore += ", sql_mode='" + c.s.mode + "'"
			}
			_, _ = c.c.ExecContext(context.Background(), restore)
		}()
	}
	_, err := c.exec(ctx, createSQL)
	return err
}

// Orphans cuenta, hasta limit, las filas cuya clave foránea no tiene fila padre (MATCH SIMPLE: una columna NULL no se comprueba).
func (c *migConn) Orphans(ctx context.Context, table string, fk model.ForeignKey, limit int) (int64, error) {
	if len(fk.Columns) == 0 || len(fk.Columns) != len(fk.RefColumns) {
		return 0, c.internal("Clave foránea mal formada")
	}
	var on, notNull []string
	for i, col := range fk.Columns {
		on = append(on, "c."+QuoteIdent(col)+" = p."+QuoteIdent(fk.RefColumns[i]))
		notNull = append(notNull, "c."+QuoteIdent(col)+" IS NOT NULL")
	}
	q := fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 FROM %s.%s c LEFT JOIN %s.%s p ON %s WHERE %s AND p.%s IS NULL LIMIT %d) x",
		QuoteIdent(c.s.o.Database), QuoteIdent(table), QuoteIdent(fk.RefSchema), QuoteIdent(fk.RefTable),
		strings.Join(on, " AND "), strings.Join(notNull, " AND "), QuoteIdent(fk.RefColumns[0]), max(limit, 1))
	rows, err := c.query(ctx, q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int64
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, mapError(err)
		}
	}
	return n, mapError(rows.Err())
}
