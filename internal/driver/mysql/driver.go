// Package mysql implementa driver.Driver para MySQL 5.7/8.x y MariaDB (spec 03).
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/vitacodesas/dbacus/internal/driver"
)

const (
	// Pool general: conexiones dedicadas de pestañas (≤ MaxTabsPerSession) + esquema + holgura (ADR-004).
	maxOpenConns = 25
	maxIdleConns = 5
	idleTime     = 5 * time.Minute
	// Pool auxiliar solo para KILL QUERY: nunca compite con el pool general.
	auxMaxConns = 2
)

func init() { driver.Register(&Driver{}) }

type Driver struct{}

func (*Driver) Name() string        { return "mysql" }
func (*Driver) DisplayName() string { return "MySQL / MariaDB" }
func (*Driver) DefaultPort() int    { return 3306 }

func (*Driver) Capabilities() driver.Capabilities {
	return driver.Capabilities{KillQuery: true, MultiStatement: false, SSL: true, ExplainSupported: true}
}

// Open construye los pools sin tocar la red; la primera conexión real ocurre en Ping/Acquire.
func (*Driver) Open(_ context.Context, cfg driver.ConnConfig, dial driver.DialFunc) (driver.Pool, error) {
	mc, err := buildConfig(cfg, dial)
	if err != nil {
		return nil, err
	}
	connector, err := gomysql.NewConnector(mc)
	if err != nil {
		return nil, fmt.Errorf("mysql connector: %w", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxIdleTime(idleTime)

	aux := sql.OpenDB(connector)
	aux.SetMaxOpenConns(auxMaxConns)
	aux.SetConnMaxIdleTime(time.Minute)

	// Misma configuración pero con CLIENT_FOUND_ROWS, solo para aplicar ediciones (ver driver.Pool.AcquireEdit).
	emc := mc.Clone()
	emc.ClientFoundRows = true
	econnector, err := gomysql.NewConnector(emc)
	if err != nil {
		return nil, fmt.Errorf("mysql connector: %w", err)
	}
	edit := sql.OpenDB(econnector)
	edit.SetMaxOpenConns(auxMaxConns)
	edit.SetMaxIdleConns(0) // efímera: no se conserva abierta
	edit.SetConnMaxIdleTime(time.Second)

	p := &pool{db: db, aux: aux, edit: edit, readOnly: cfg.Profile.ReadOnly}
	p.schema = newSchemaReader(db)
	return p, nil
}
