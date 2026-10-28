// Package fake es un driver en memoria para probar sesiones y servicios sin base de datos.
package fake

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"tabulariswails/internal/driver"
	"tabulariswails/internal/model"
)

type Driver struct {
	name string

	mu    sync.Mutex
	Pools []*Pool

	// Hooks configurables por test.
	PingErr   error
	OpenErr   error
	Version   string
	OnRun     func(ctx context.Context, stmt string) (driver.RunResult, error)
	OnExec    func(ctx context.Context, stmt string, args []any) (int64, error) // si es nil, Exec afecta 1 fila
	LastID    int64                                                             // lo que Exec devuelve como último ID autoincremental en un INSERT
	LastDial  driver.DialFunc
	LastCfg   driver.ConnConfig
	AcquireFn func(ctx context.Context) error // se llama antes de entregar una conexión
	ParseFn   func(dsn string) (driver.ConnConfig, error)

	SchemaReader driver.SchemaReader // si es nil se usa un *Schema vacío
	OnKill       func(connID string) // se llama en cada Conn.Kill
}

func New(name string) *Driver { return &Driver{name: name, Version: "fake-1.0"} }

func (d *Driver) Name() string               { return d.name }
func (d *Driver) DisplayName() string        { return "Fake " + d.name }
func (d *Driver) DefaultPort() int           { return 1 }
func (d *Driver) QuoteIdent(n string) string { return "`" + strings.ReplaceAll(n, "`", "``") + "`" }
func (d *Driver) QuoteString(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
}
func (d *Driver) Capabilities() driver.Capabilities { return driver.Capabilities{KillQuery: true} }
func (d *Driver) ParseDSN(dsn string) (driver.ConnConfig, error) {
	if d.ParseFn != nil {
		return d.ParseFn(dsn)
	}
	return driver.ConnConfig{}, errors.New("fake: ParseDSN no configurado")
}

func (d *Driver) Open(_ context.Context, cfg driver.ConnConfig, dial driver.DialFunc) (driver.Pool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.LastDial, d.LastCfg = dial, cfg
	if d.OpenErr != nil {
		return nil, d.OpenErr
	}
	p := &Pool{d: d}
	d.Pools = append(d.Pools, p)
	return p, nil
}

type Pool struct {
	d        *Driver
	Closed   atomic.Bool
	Acquired atomic.Int64 // total entregadas
	Open     atomic.Int64 // actualmente sin liberar
	seq      atomic.Int64
	mu       sync.Mutex
	Conns    []*Conn
	Edits    []*Conn // las entregadas por AcquireEdit
}

func (p *Pool) Ping(context.Context) error { return p.d.PingErr }
func (p *Pool) ServerVersion(context.Context) (string, error) {
	return p.d.Version, nil
}
func (p *Pool) Schema() driver.SchemaReader {
	if p.d.SchemaReader != nil {
		return p.d.SchemaReader
	}
	return &Schema{}
}
func (p *Pool) Close() error { p.Closed.Store(true); return nil }

// AcquireEdit entrega una conexión como Acquire y la anota en Edits (para que los tests distingan las de edición).
func (p *Pool) AcquireEdit(ctx context.Context) (driver.Conn, error) {
	c, err := p.Acquire(ctx)
	if err == nil {
		p.mu.Lock()
		p.Edits = append(p.Edits, c.(*Conn))
		p.mu.Unlock()
	}
	return c, err
}

func (p *Pool) Acquire(ctx context.Context) (driver.Conn, error) {
	if fn := p.d.AcquireFn; fn != nil {
		if err := fn(ctx); err != nil {
			return nil, err
		}
	}
	c := &Conn{pool: p, id: strconv.FormatInt(p.seq.Add(1), 10)}
	p.Acquired.Add(1)
	p.Open.Add(1)
	p.mu.Lock()
	p.Conns = append(p.Conns, c)
	p.mu.Unlock()
	return c, nil
}

// ExecCall es una llamada a Conn.Exec registrada por el fake.
type ExecCall struct {
	Stmt string
	Args []any
}

type Conn struct {
	pool     *Pool
	id       string
	mu       sync.Mutex
	Execs    []ExecCall
	TxLog    []string // "begin", "commit", "rollback" en orden
	inTx     bool
	Released atomic.Bool
	Killed   atomic.Int64
	UsedDB   atomic.Value // string
}

func (c *Conn) ID() string { return c.id }
func (c *Conn) Use(_ context.Context, db string) error {
	c.UsedDB.Store(db)
	return nil
}
func (c *Conn) Run(ctx context.Context, stmt string) (driver.RunResult, error) {
	if fn := c.pool.d.OnRun; fn != nil {
		return fn(ctx, stmt)
	}
	return &Result{}, nil
}
func (c *Conn) Exec(ctx context.Context, stmt string, args ...any) (int64, int64, error) {
	c.mu.Lock()
	c.Execs = append(c.Execs, ExecCall{stmt, args})
	c.mu.Unlock()
	var id int64
	if strings.HasPrefix(stmt, "INSERT") {
		id = c.pool.d.LastID
	}
	if fn := c.pool.d.OnExec; fn != nil {
		n, err := fn(ctx, stmt, args)
		return n, id, err
	}
	return 1, id, nil
}
func (c *Conn) tx(event string) {
	c.mu.Lock()
	c.TxLog = append(c.TxLog, event)
	c.inTx = event == "begin"
	c.mu.Unlock()
}
func (c *Conn) Begin(context.Context) error  { c.tx("begin"); return nil }
func (c *Conn) Commit(context.Context) error { c.tx("commit"); return nil }
func (c *Conn) Rollback(context.Context) error {
	c.mu.Lock()
	open := c.inTx
	c.mu.Unlock()
	if open {
		c.tx("rollback")
	}
	return nil
}
func (c *Conn) Kill(context.Context) error {
	c.Killed.Add(1)
	if fn := c.pool.d.OnKill; fn != nil {
		fn(c.id)
	}
	return nil
}
func (c *Conn) Release() error {
	_ = c.Rollback(context.Background())
	if c.Released.CompareAndSwap(false, true) {
		c.pool.Open.Add(-1)
	}
	return nil
}

// Result es un RunResult sobre filas en memoria.
type Result struct {
	Cols     []model.ColumnMeta
	Rows     [][]any
	Rows_    int64
	LastID   int64
	Warnings int
	Error    error

	i      int
	Closed bool
}

func (r *Result) Columns() []model.ColumnMeta { return r.Cols }
func (r *Result) Next() bool {
	if r.i >= len(r.Rows) {
		return false
	}
	r.i++
	return true
}
func (r *Result) Row() []any { return r.Rows[r.i-1] }
func (r *Result) Err() error { return r.Error }
func (r *Result) Affected() (int64, int64, int) {
	return r.Rows_, r.LastID, r.Warnings
}
func (r *Result) Close() error { r.Closed = true; return nil }

// Schema es un SchemaReader en memoria que cuenta llamadas y puede retrasarse o fallar.
type Schema struct {
	DBs      []model.Database
	TablesBy map[string][]model.Table
	Details  map[string]model.TableDetail // clave "db.tabla"
	Cat      map[string]model.CompletionCatalog
	Delay    func(ctx context.Context) error // se ejecuta antes de cada lectura
	Err      error
	Calls    atomic.Int64
}

func (s *Schema) enter(ctx context.Context) error {
	s.Calls.Add(1)
	if s.Delay != nil {
		if err := s.Delay(ctx); err != nil {
			return err
		}
	}
	return s.Err
}

func (s *Schema) Databases(ctx context.Context) ([]model.Database, error) {
	return s.DBs, s.enter(ctx)
}
func (s *Schema) Tables(ctx context.Context, db string) ([]model.Table, error) {
	return s.TablesBy[db], s.enter(ctx)
}
func (s *Schema) Describe(ctx context.Context, db, table string) (model.TableDetail, error) {
	return s.Details[db+"."+table], s.enter(ctx)
}
func (s *Schema) Catalog(ctx context.Context, db string) (model.CompletionCatalog, error) {
	return s.Cat[db], s.enter(ctx)
}

// Rows construye un resultado con columnas y filas de texto/enteros.
func Rows(cols []string, rows ...[]any) *Result {
	meta := make([]model.ColumnMeta, len(cols))
	for i, c := range cols {
		meta[i] = model.ColumnMeta{Name: c, DataType: "VARCHAR"}
	}
	return &Result{Cols: meta, Rows: rows}
}
