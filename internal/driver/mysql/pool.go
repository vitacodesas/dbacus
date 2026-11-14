package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/sqlsplit"
)

type pool struct {
	db  *sql.DB
	aux *sql.DB // solo KILL QUERY
	// edit: conexiones con CLIENT_FOUND_ROWS, solo para aplicar ediciones de celdas (driver.Pool.AcquireEdit).
	edit *sql.DB
	// mon: una sola conexión para el monitoreo (spec 22): no consume plazas de pestaña ni compite con el pool general.
	// Se abre bajo demanda (sql.OpenDB no conecta) y cada uso la deja en solo lectura (ver withMon).
	mon     *sql.DB
	monMu   sync.Mutex
	monKind int // 0 sin detectar, 1 MySQL, 2 MariaDB (decide max_execution_time o max_statement_time)
	// procDigest: 0 sin saber, 1 el servidor ofrece STATEMENT_DIGEST, -1 no lo ofrece (spec 22 ⚑D17). Lo fija Probe o el primer Processes.
	procDigest atomic.Int32
	// readOnly: cada conexión dedicada arranca en una transacción de solo lectura (spec 10).
	readOnly bool
	// cfg es la configuración con la que se abrió el pool: la migración (spec 23) abre su propio pool `mig` a partir de ella.
	cfg    *gomysql.Config
	schema driver.SchemaReader
}

func (p *pool) Ping(ctx context.Context) error { return mapError(p.db.PingContext(ctx)) }

func (p *pool) ServerVersion(ctx context.Context) (string, error) {
	var v string
	if err := p.db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v); err != nil {
		return "", mapError(err)
	}
	return v, nil
}

func (p *pool) Schema() driver.SchemaReader { return p.schema }

func (p *pool) Close() error {
	return errors.Join(p.db.Close(), p.aux.Close(), p.edit.Close(), p.mon.Close())
}

func (p *pool) Acquire(ctx context.Context) (driver.Conn, error) { return p.acquire(ctx, p.db) }

func (p *pool) AcquireEdit(ctx context.Context) (driver.Conn, error) { return p.acquire(ctx, p.edit) }

func (p *pool) acquire(ctx context.Context, db *sql.DB) (driver.Conn, error) {
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	// Falla cerrado: si el servidor no acepta el modo, la conexión no se entrega en modo lectura-escritura.
	if p.readOnly {
		if _, err := c.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
			_ = c.Close()
			return nil, mapError(err)
		}
	}
	var id uint64
	if err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		_ = c.Close()
		return nil, mapError(err)
	}
	return &conn{c: c, id: strconv.FormatUint(id, 10), pool: p}, nil
}

type conn struct {
	c    *sql.Conn
	id   string
	pool *pool
	tx   *sql.Tx // transacción abierta con Begin (solo la edición de celdas, spec 13)
}

func (c *conn) ID() string { return c.id }

func (c *conn) Use(ctx context.Context, database string) error {
	_, err := c.c.ExecContext(ctx, "USE "+QuoteIdent(database))
	return mapError(err)
}

// Run ejecuta una sentencia: por Query si normalmente devuelve filas y por Exec en otro caso (para obtener filas afectadas).
func (c *conn) Run(ctx context.Context, stmt string) (driver.RunResult, error) {
	if sqlsplit.ReturnsRows(sqlsplit.FirstKeyword(stmt)) {
		rows, err := c.c.QueryContext(ctx, stmt)
		if err != nil {
			return nil, mapError(err)
		}
		return newRowsResult(rows)
	}
	res, err := c.c.ExecContext(ctx, stmt)
	if err != nil {
		return nil, mapError(err)
	}
	r := &execResult{}
	r.rows, _ = res.RowsAffected()
	r.lastID, _ = res.LastInsertId()
	// Los warnings no vienen en sql.Result; un SELECT barato en la misma conexión los recupera.
	var w int
	if err := c.c.QueryRowContext(ctx, "SELECT @@warning_count").Scan(&w); err == nil {
		r.warnings = w
	}
	return r, nil
}

// Kill aborta la consulta sin cerrar la conexión (a diferencia de cancelar el ctx, que el driver Go resuelve cerrándola).
func (c *conn) Kill(ctx context.Context) error {
	if _, err := strconv.ParseUint(c.id, 10, 64); err != nil {
		return fmt.Errorf("id de hilo inválido %q", c.id)
	}
	_, err := c.pool.aux.ExecContext(ctx, "KILL QUERY "+c.id)
	return mapError(err)
}

var _ driver.ProcessKiller = (*pool)(nil)

// KillProcess termina el hilo `id` por el pool auxiliar (nunca por la conexión de solo lectura del monitor). El id es un
// entero: no hay texto externo en la sentencia. 1094 = el hilo ya no existe; 1095 = sin privilegio sobre ese hilo.
func (p *pool) KillProcess(ctx context.Context, id uint64, queryOnly bool) error {
	stmt := "KILL " + strconv.FormatUint(id, 10)
	if queryOnly {
		stmt = "KILL QUERY " + strconv.FormatUint(id, 10)
	}
	_, err := p.aux.ExecContext(ctx, stmt)
	var me *gomysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1094:
			return apperr.New(apperr.NotFound, "El proceso ya terminó").WithDetail("process_gone")
		case 1095:
			return apperr.New(apperr.QueryFailed, "El servidor no permite terminar ese proceso: falta el privilegio CONNECTION_ADMIN o SUPER").WithDetail("kill_denied")
		}
	}
	return mapError(err)
}

func (c *conn) Exec(ctx context.Context, stmt string, args ...any) (int64, int64, error) {
	var (
		res sql.Result
		err error
	)
	if c.tx != nil {
		res, err = c.tx.ExecContext(ctx, stmt, args...)
	} else {
		res, err = c.c.ExecContext(ctx, stmt, args...)
	}
	if err != nil {
		return 0, 0, mapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, 0, mapError(err)
	}
	id, _ := res.LastInsertId()
	return n, id, nil
}

func (c *conn) Begin(ctx context.Context) error {
	if c.tx != nil {
		return apperr.New(apperr.Internal, "Ya hay una transacción abierta en esta conexión")
	}
	tx, err := c.c.BeginTx(ctx, nil)
	if err != nil {
		return mapError(err)
	}
	c.tx = tx
	return nil
}

func (c *conn) Commit(context.Context) error {
	tx := c.tx
	if tx == nil {
		return apperr.New(apperr.Internal, "No hay transacción abierta")
	}
	c.tx = nil
	return mapError(tx.Commit())
}

func (c *conn) Rollback(context.Context) error {
	tx := c.tx
	if tx == nil {
		return nil // idempotente: revertir sin transacción no es un error
	}
	c.tx = nil
	return mapError(tx.Rollback())
}

func (c *conn) Release() error {
	_ = c.Rollback(context.Background()) // una transacción olvidada no debe sobrevivir en la conexión devuelta al pool
	return c.c.Close()
}
