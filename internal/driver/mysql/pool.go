package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"tabulariswails/internal/driver"
	"tabulariswails/internal/sqlsplit"
)

type pool struct {
	db     *sql.DB
	aux    *sql.DB // solo KILL QUERY
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

func (p *pool) Close() error { return errors.Join(p.db.Close(), p.aux.Close()) }

func (p *pool) Acquire(ctx context.Context) (driver.Conn, error) {
	c, err := p.db.Conn(ctx)
	if err != nil {
		return nil, mapError(err)
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

func (c *conn) Release() error { return c.c.Close() }
