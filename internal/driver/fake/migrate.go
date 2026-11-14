package fake

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
)

// MigEngine es un «servidor» en memoria para probar la migración (spec 23) sin base de datos. Varios pools pueden compartirlo
// (mismo servidor) o tener cada uno el suyo (servidores distintos). Los trozos son rangos de índices de fila: el fake los interpreta él mismo.

type MigRow struct {
	Vals []any
	Tag  int // seq del trozo que la escribió (para ClearRange)
}

type MigTable struct {
	model.MigrationTable
	Rows []MigRow
}

type MigDB struct {
	Name      string
	Charset   string
	Collation string
	Tables    map[string]*MigTable
	Order     []string
	Views     []model.MigrationCode
	Routines  []model.MigrationCode
	Triggers  []model.MigrationCode
	FKs       map[string][]model.ForeignKey // tabla → FKs añadidas
	Created   bool
}

type MigEngine struct {
	mu  sync.Mutex
	DBs map[string]*MigDB
	// Log registra, en orden, lo que se hizo: «insert:db.tabla», «exec:db:SQL», «fk:db.tabla.nombre», «object:db:kind:nombre»…
	log []string

	// Hooks de prueba.
	OnCommit      func(table string, seq, attempt int) error // se llama al confirmar un trozo; un error lo revierte
	LoseAck       func(table string, seq, attempt int) bool  // true: el trozo SÍ se confirma pero la llamada devuelve un error reintentable
	Latency       time.Duration                              // espera por lote/trozo (respeta ctx y KILL)
	MaxConns      int
	UsedConns     int
	RequirePK     bool
	Collations    map[string]bool // nil = todas existen
	Version       string
	ServerWriteOK bool // permite CopyChunk
	OrphanCount   func(table, fk string) int64

	attempts map[string]int
	threadID atomic.Uint64
	killed   sync.Map // threadID → chan struct{}
	Killed   []uint64
}

func NewMigEngine() *MigEngine {
	e := &MigEngine{DBs: map[string]*MigDB{}, MaxConns: 100, UsedConns: 5, Version: "fake-8.0", attempts: map[string]int{}, ServerWriteOK: true}
	e.threadID.Store(1000)
	return e
}

// AddDB crea (o devuelve) un schema.
func (e *MigEngine) AddDB(name string) *MigDB {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.addDB(name)
}

func (e *MigEngine) addDB(name string) *MigDB {
	if d, ok := e.DBs[name]; ok {
		return d
	}
	d := &MigDB{Name: name, Charset: "utf8mb4", Collation: "utf8mb4_0900_ai_ci", Tables: map[string]*MigTable{}, FKs: map[string][]model.ForeignKey{}}
	e.DBs[name] = d
	return d
}

// AddTable añade una tabla con filas de enteros (columna 0 = clave).
func (e *MigEngine) AddTable(db string, t model.MigrationTable, rows int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.addDB(db)
	mt := &MigTable{MigrationTable: t}
	if len(mt.Columns) == 0 {
		mt.Columns = []string{"id", "v"}
	}
	for i := 0; i < rows; i++ {
		v := make([]any, len(mt.Columns))
		for c := range v {
			v[c] = int64(i*10 + c)
		}
		mt.Rows = append(mt.Rows, MigRow{Vals: v})
	}
	if mt.EstRows == 0 {
		mt.EstRows = int64(rows)
	}
	if mt.Key.Kind == "" {
		mt.Key = model.ChunkKeySpec{Kind: model.ChunkPK, Columns: []string{mt.Columns[0]}}
	}
	d.Tables[t.Name] = mt
	d.Order = append(d.Order, t.Name)
}

func (e *MigEngine) Log() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

func (e *MigEngine) logf(format string, a ...any) {
	e.mu.Lock()
	e.log = append(e.log, fmt.Sprintf(format, a...))
	e.mu.Unlock()
}

// RowCount devuelve las filas de una tabla (-1 si no existe).
func (e *MigEngine) RowCount(db, table string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d, ok := e.DBs[db]; ok {
		if t, ok := d.Tables[table]; ok {
			return len(t.Rows)
		}
	}
	return -1
}

// MaxInflight es el mayor número de lotes leídos y aún no escritos a la vez (para probar la contrapresión).
func (e *MigEngine) MaxInflight() int64 { return maxInfl.Load() }

// ResetInflight pone a cero el contador de lotes en vuelo (se comparte entre motores: lector y escritor pueden ser distintos).
func ResetInflight() { inflight.Store(0); maxInfl.Store(0) }

func (e *MigEngine) Attempts(table string, seq int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.attempts[fmt.Sprintf("%s#%d", table, seq)]
}

var inflight, maxInfl atomic.Int64

// ---- driver.SchemaMigrator sobre el Pool ----

var _ driver.SchemaMigrator = (*Pool)(nil)

func (p *Pool) mig() (*MigEngine, error) {
	if p.d.Mig == nil {
		return nil, apperr.New(apperr.Unsupported, "El driver falso no tiene motor de migración")
	}
	return p.d.Mig, nil
}

func (p *Pool) MigrationInspect(_ context.Context, db string) (model.MigrationSource, error) {
	e, err := p.mig()
	if err != nil {
		return model.MigrationSource{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.DBs[db]
	if !ok {
		return model.MigrationSource{}, apperr.New(apperr.NotFound, "La base de datos no existe")
	}
	src := model.MigrationSource{Database: db, Charset: d.Charset, Collation: d.Collation, ServerVersion: e.Version, Views: d.Views, Routines: d.Routines, Triggers: d.Triggers}
	for _, n := range d.Order {
		src.Tables = append(src.Tables, d.Tables[n].MigrationTable)
	}
	return src, nil
}

func (p *Pool) MigrationPreflight(_ context.Context, in model.MigrationPreflightInput) (model.MigrationPreflight, error) {
	e, err := p.mig()
	if err != nil {
		return model.MigrationPreflight{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := model.MigrationPreflight{MaxConnections: e.MaxConns, FreeConnections: e.MaxConns - e.UsedConns, RequirePK: e.RequirePK, MaxPacket: 16 << 20, ServerVersion: e.Version}
	for name, d := range e.DBs {
		if strings.EqualFold(name, in.Database) {
			out.Exists, out.CanonicalName = true, name
			for _, tn := range d.Order {
				out.ExistingTables = append(out.ExistingTables, tn)
			}
			out.ExistingObjects = len(d.Tables) + len(d.Views) + len(d.Routines) + len(d.Triggers)
			out.ExistingCode = objectCode(d, in.Objects)
		}
	}
	out.SameAsSource = in.SameServer && strings.EqualFold(in.Database, in.SourceName)
	if e.Collations != nil {
		for _, c := range in.Collations {
			if !e.Collations[c] {
				out.MissingCollation = append(out.MissingCollation, c)
			}
		}
	}
	return out, nil
}

func objectCode(d *MigDB, want []model.ObjectRef) []model.MigrationCode {
	var out []model.MigrationCode
	for _, w := range want {
		for _, c := range append(append(append([]model.MigrationCode{}, d.Views...), d.Routines...), d.Triggers...) {
			if c.Kind == w.Kind && strings.EqualFold(c.Name, w.Name) {
				out = append(out, model.MigrationCode{Name: w.Name, Kind: w.Kind, CreateSQL: c.CreateSQL})
			}
		}
	}
	return out
}

func (p *Pool) MigrationObjectCode(_ context.Context, db string, refs []model.ObjectRef) ([]model.MigrationCode, error) {
	e, err := p.mig()
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.DBs[db]
	if !ok {
		return nil, nil
	}
	return objectCode(d, refs), nil
}

func (p *Pool) MigrationEnsureDatabase(_ context.Context, name, charset, collation string) (bool, error) {
	e, err := p.mig()
	if err != nil {
		return false, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.DBs[name]; ok {
		return false, nil
	}
	d := e.addDB(name)
	d.Charset, d.Collation, d.Created = charset, collation, true
	e.log = append(e.log, "createdb:"+name)
	return true, nil
}

func (p *Pool) MigrationDrop(_ context.Context, db string, objs []model.MigrationCreated) error {
	e, err := p.mig()
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.DBs[db]
	if !ok {
		return nil
	}
	for _, o := range objs {
		switch o.Kind {
		case "table":
			delete(d.Tables, o.Name)
			for i, n := range d.Order {
				if n == o.Name {
					d.Order = append(d.Order[:i], d.Order[i+1:]...)
					break
				}
			}
		case "database":
			delete(e.DBs, db)
		}
		e.log = append(e.log, "drop:"+o.Kind+":"+o.Name)
	}
	return nil
}

func (p *Pool) OpenMigration(_ context.Context, o driver.MigrationOpen) (driver.MigrationSession, error) {
	e, err := p.mig()
	if err != nil {
		return nil, err
	}
	if o.Size < 1 {
		o.Size = 1
	}
	e.mu.Lock()
	e.UsedConns += o.Size
	e.mu.Unlock()
	return &migSession{e: e, o: o, sem: make(chan struct{}, o.Size)}, nil
}

type migSession struct {
	e   *MigEngine
	o   driver.MigrationOpen
	sem chan struct{}
}

func (s *migSession) Acquire(ctx context.Context) (driver.MigrationConn, error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	id := s.e.threadID.Add(1)
	ch := make(chan struct{})
	s.e.killed.Store(id, ch)
	return &migConn{s: s, id: id, kill: ch}, nil
}

func (s *migSession) Close() error {
	s.e.mu.Lock()
	s.e.UsedConns -= s.o.Size
	s.e.mu.Unlock()
	return nil
}

type migConn struct {
	s    *migSession
	id   uint64
	kill chan struct{}
	once sync.Once
	tx   []pending
	open bool
}

type pending struct {
	table string
	rows  []MigRow
	seq   int
}

func (c *migConn) role() string     { return c.s.o.Role }
func (c *migConn) ThreadID() uint64 { return c.id }
func (c *migConn) Kill(context.Context) error {
	c.s.e.mu.Lock()
	c.s.e.Killed = append(c.s.e.Killed, c.id)
	c.s.e.mu.Unlock()
	c.once.Do(func() { close(c.kill) })
	return nil
}
func (c *migConn) Release() error {
	c.s.e.killed.Delete(c.id)
	<-c.s.sem
	return nil
}

func (c *migConn) wait(ctx context.Context) error {
	if d := c.s.e.Latency; d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		case <-c.kill:
			return apperr.New(apperr.QueryCanceled, "KILL QUERY")
		}
	}
	select {
	case <-c.kill:
		return apperr.New(apperr.QueryCanceled, "KILL QUERY")
	default:
	}
	return ctx.Err()
}

func (c *migConn) db(name string) (*MigDB, *MigEngine) {
	return c.s.e.DBs[name], c.s.e
}

func (c *migConn) needWrite() error {
	if c.role() == driver.RoleSource {
		return apperr.New(apperr.QueryFailed, "source_write_blocked").WithDetail("source_write_blocked")
	}
	return nil
}

// ---- RoleSource ----

func (c *migConn) Chunks(ctx context.Context, t model.MigrationTable, chunkRows int, emit func(driver.MigrationChunk) error) error {
	e := c.s.e
	e.mu.Lock()
	d := e.DBs[c.s.o.Database]
	if c.role() == driver.RoleServer {
		d = e.DBs[c.s.o.Source]
	}
	var n int
	if d != nil && d.Tables[t.Name] != nil {
		n = len(d.Tables[t.Name].Rows)
	}
	e.mu.Unlock()
	if t.Key.Kind == model.ChunkNone || n == 0 || chunkRows <= 0 || n <= chunkRows {
		return emit(driver.MigrationChunk{Seq: 0, Whole: true})
	}
	seq := 0
	for i := 0; i < n; i += chunkRows {
		if err := ctx.Err(); err != nil {
			return err
		}
		hi := min(i+chunkRows, n)
		if err := emit(driver.MigrationChunk{Seq: seq, Lo: []any{i}, Hi: []any{hi}}); err != nil {
			return err
		}
		seq++
	}
	return nil
}

func (c *migConn) srcRows(t model.MigrationTable, ch driver.MigrationChunk, db string) []MigRow {
	e := c.s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.DBs[db]
	if d == nil || d.Tables[t.Name] == nil {
		return nil
	}
	rows := d.Tables[t.Name].Rows
	if ch.Whole || ch.Lo == nil {
		return append([]MigRow(nil), rows...)
	}
	lo, hi := ch.Lo[0].(int), ch.Hi[0].(int)
	return append([]MigRow(nil), rows[lo:min(hi, len(rows))]...)
}

func (c *migConn) Read(ctx context.Context, t model.MigrationTable, ch driver.MigrationChunk, batchBytes int, emit func(driver.RowBatch) error) error {
	if c.role() != driver.RoleSource {
		return apperr.New(apperr.Internal, "Read solo en RoleSource")
	}
	rows := c.srcRows(t, ch, c.s.o.Database)
	var b driver.RowBatch
	flush := func() error {
		if len(b.Rows) == 0 {
			return nil
		}
		if err := c.wait(ctx); err != nil {
			return err
		}
		out := b
		b = driver.RowBatch{}
		inflight.Add(1)
		for {
			m := maxInfl.Load()
			cur := inflight.Load()
			if cur <= m || maxInfl.CompareAndSwap(m, cur) {
				break
			}
		}
		return emit(out)
	}
	for _, r := range rows {
		b.Rows = append(b.Rows, r.Vals)
		b.Bytes += 8 * len(r.Vals)
		if b.Bytes >= batchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func (c *migConn) CountRows(_ context.Context, table string) (int64, error) {
	e := c.s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if d := e.DBs[c.s.o.Database]; d != nil {
		if t := d.Tables[table]; t != nil {
			return int64(len(t.Rows)), nil
		}
	}
	return 0, apperr.New(apperr.NotFound, "La tabla no existe")
}

// ---- RoleTarget / RoleServer ----

func (c *migConn) Exec(_ context.Context, stmt string) error {
	if err := c.needWrite(); err != nil {
		return err
	}
	c.s.e.logf("exec:%s:%s", c.s.o.Database, stmt)
	if strings.HasPrefix(stmt, "CREATE TABLE") {
		name := strings.Trim(strings.TrimPrefix(stmt, "CREATE TABLE"), " `;")
		c.s.e.mu.Lock()
		d := c.s.e.addDB(c.s.o.Database)
		if _, ok := d.Tables[name]; !ok {
			d.Tables[name] = &MigTable{MigrationTable: model.MigrationTable{Name: name}}
			d.Order = append(d.Order, name)
		}
		c.s.e.mu.Unlock()
	}
	return nil
}

func (c *migConn) Begin(context.Context) error {
	if err := c.needWrite(); err != nil {
		return err
	}
	c.tx, c.open = nil, true
	return nil
}

func (c *migConn) Commit(ctx context.Context) error {
	e := c.s.e
	if !c.open {
		return apperr.New(apperr.Internal, "No hay transacción abierta")
	}
	c.open = false
	tx := c.tx
	c.tx = nil
	lose := false
	for _, p := range tx {
		key := fmt.Sprintf("%s#%d", p.table, p.seq)
		e.mu.Lock()
		e.attempts[key]++
		attempt := e.attempts[key]
		e.mu.Unlock()
		if e.OnCommit != nil {
			if err := e.OnCommit(p.table, p.seq, attempt); err != nil {
				return err
			}
		}
		if e.LoseAck != nil && e.LoseAck(p.table, p.seq, attempt) {
			lose = true
		}
	}
	e.mu.Lock()
	d := e.addDB(c.s.o.Database)
	for _, p := range tx {
		t := d.Tables[p.table]
		if t == nil {
			t = &MigTable{MigrationTable: model.MigrationTable{Name: p.table}}
			d.Tables[p.table] = t
			d.Order = append(d.Order, p.table)
		}
		t.Rows = append(t.Rows, p.rows...)
		e.log = append(e.log, fmt.Sprintf("insert:%s.%s#%d", d.Name, p.table, p.seq))
	}
	e.mu.Unlock()
	if lose {
		return apperr.New(apperr.SessionClosed, "la conexión se perdió tras el COMMIT").AsRetryable()
	}
	return nil
}

func (c *migConn) Rollback(context.Context) error {
	c.tx, c.open = nil, false
	return nil
}

func (c *migConn) Insert(ctx context.Context, t model.MigrationTable, ch driver.MigrationChunk, b driver.RowBatch) (int64, error) {
	if c.role() != driver.RoleTarget {
		return 0, apperr.New(apperr.Internal, "Insert solo en RoleTarget")
	}
	if err := c.wait(ctx); err != nil {
		return 0, err
	}
	inflight.Add(-1)
	rows := make([]MigRow, len(b.Rows))
	for i, r := range b.Rows {
		rows[i] = MigRow{Vals: r, Tag: ch.Seq + 1}
	}
	c.addPending(t.Name, rows, ch.Seq)
	return int64(len(rows)), nil
}

func (c *migConn) addPending(table string, rows []MigRow, seq int) {
	for i := range c.tx {
		if c.tx[i].table == table {
			c.tx[i].rows = append(c.tx[i].rows, rows...)
			return
		}
	}
	c.tx = append(c.tx, pending{table: table, rows: rows, seq: seq})
}

func (c *migConn) CopyChunk(ctx context.Context, t model.MigrationTable, ch driver.MigrationChunk) (int64, error) {
	if c.role() != driver.RoleServer || !c.s.e.ServerWriteOK {
		return 0, apperr.New(apperr.Internal, "CopyChunk solo en RoleServer")
	}
	if err := c.wait(ctx); err != nil {
		return 0, err
	}
	rows := c.srcRows(t, ch, c.s.o.Source)
	out := make([]MigRow, len(rows))
	for i, r := range rows {
		out[i] = MigRow{Vals: r.Vals, Tag: ch.Seq + 1}
	}
	c.addPending(t.Name, out, ch.Seq)
	return int64(len(out)), nil
}

func (c *migConn) ClearRange(_ context.Context, t model.MigrationTable, ch driver.MigrationChunk) error {
	if err := c.needWrite(); err != nil {
		return err
	}
	e := c.s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.DBs[c.s.o.Database]
	if d == nil || d.Tables[t.Name] == nil {
		return nil
	}
	tbl := d.Tables[t.Name]
	if ch.Whole {
		tbl.Rows = nil
		return nil
	}
	kept := tbl.Rows[:0]
	for _, r := range tbl.Rows {
		if r.Tag != ch.Seq+1 {
			kept = append(kept, r)
		}
	}
	tbl.Rows = kept
	return nil
}

func (c *migConn) AddForeignKey(_ context.Context, table string, fk model.ForeignKey) error {
	if err := c.needWrite(); err != nil {
		return err
	}
	e := c.s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.addDB(c.s.o.Database)
	d.FKs[table] = append(d.FKs[table], fk)
	e.log = append(e.log, fmt.Sprintf("fk:%s.%s.%s->%s.%s", d.Name, table, fk.Name, fk.RefSchema, fk.RefTable))
	return nil
}

func (c *migConn) FixAutoIncrement(_ context.Context, table, column string, source int64) error {
	c.s.e.logf("autoinc:%s.%s=%d", c.s.o.Database, table, source)
	return c.needWrite()
}

func (c *migConn) CreateObject(_ context.Context, obj model.MigrationCode, sql string) error {
	if err := c.needWrite(); err != nil {
		return err
	}
	e := c.s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.addDB(c.s.o.Database)
	obj.CreateSQL = sql
	switch obj.Kind {
	case "view":
		d.Views = append(d.Views, obj)
	case "trigger":
		d.Triggers = append(d.Triggers, obj)
	default:
		d.Routines = append(d.Routines, obj)
	}
	e.log = append(e.log, fmt.Sprintf("object:%s:%s:%s", d.Name, obj.Kind, obj.Name))
	return nil
}

func (c *migConn) Orphans(_ context.Context, table string, fk model.ForeignKey, limit int) (int64, error) {
	if c.s.e.OrphanCount != nil {
		return min(c.s.e.OrphanCount(table, fk.Name), int64(limit)), nil
	}
	return 0, nil
}

// Orden estable de nombres para las comprobaciones de los tests.
func SortedNames(m map[string]*MigTable) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

var errNoMig = errors.New("fake: sin motor de migración")

var _ = errNoMig
