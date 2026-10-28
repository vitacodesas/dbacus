// Package driver define la interfaz que implementa cada motor (spec 03). No conoce services, store ni Wails.
package driver

import (
	"context"
	"net"

	"tabulariswails/internal/model"
	"tabulariswails/internal/secrets"
)

type Capabilities struct {
	Schemas          bool // PostgreSQL: true; MySQL: false (BD == schema)
	KillQuery        bool
	MultiStatement   bool
	SSL              bool
	ExplainSupported bool
}

// DialFunc permite inyectar el transporte (túnel SSH) en lugar de net.Dial.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type ConnConfig struct {
	Profile  model.ConnectionProfile
	Password secrets.Secret
}

type Driver interface {
	Name() string
	DisplayName() string
	Capabilities() Capabilities
	DefaultPort() int
	// QuoteIdent escapa un identificador según el motor (backticks en MySQL, comillas dobles en PostgreSQL).
	QuoteIdent(name string) string
	// QuoteString devuelve s como literal de texto SQL escapado (para generar INSERT desde resultados).
	QuoteString(s string) string
	// Open construye el pool. dial puede ser nil (conexión directa). No debe devolver un pool sin haber validado la configuración.
	Open(ctx context.Context, cfg ConnConfig, dial DialFunc) (Pool, error)
	// ParseDSN interpreta una URL de conexión; la contraseña va en ConnConfig.Password, nunca en el perfil.
	ParseDSN(dsn string) (ConnConfig, error)
}

type Pool interface {
	Ping(ctx context.Context) error
	ServerVersion(ctx context.Context) (string, error)
	// Acquire entrega una conexión dedicada (una por pestaña, ADR-004).
	Acquire(ctx context.Context) (Conn, error)
	// AcquireEdit entrega una conexión efímera para aplicar ediciones de celdas (spec 13): cuenta como «afectadas» las
	// filas encontradas aunque el valor no cambie (CLIENT_FOUND_ROWS), que es lo que necesita el control optimista.
	// No se usa para el SQL del usuario, cuyo recuento de filas afectadas es el normal. Debe liberarse con Release.
	AcquireEdit(ctx context.Context) (Conn, error)
	Schema() SchemaReader
	Close() error
}

type Conn interface {
	// ID es el identificador de hilo en el servidor (para KILL).
	ID() string
	Use(ctx context.Context, database string) error
	// Run ejecuta UNA sentencia. El llamador debe cerrar el RunResult.
	Run(ctx context.Context, stmt string) (RunResult, error)
	// Exec ejecuta UNA sentencia con parámetros (nunca interpolados) y devuelve las filas encontradas por la condición y el
	// último ID autoincremental generado (0 si no hubo)
	// (con CLIENT_FOUND_ROWS, también las que ya tenían el valor). Dentro de una transacción abierta con Begin usa esa
	// transacción. Solo lo usa la edición de celdas (spec 13); el SQL del usuario va por Run.
	Exec(ctx context.Context, stmt string, args ...any) (rowsAffected, lastInsertID int64, err error)
	// Begin/Commit/Rollback gestionan una transacción en esta conexión. Release la revierte si quedó abierta.
	Begin(ctx context.Context) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	// Kill aborta la consulta en curso SIN cerrar la conexión (conserva variables de sesión, ADR-004).
	// Usa un canal aparte del pool para no bloquearse si el pool está agotado.
	Kill(ctx context.Context) error
	Release() error
}

type RunResult interface {
	// Columns es nil si la sentencia no devuelve filas.
	Columns() []model.ColumnMeta
	Next() bool
	// Row devuelve valores crudos del driver; el slice es propiedad del llamador.
	Row() []any
	Err() error
	Affected() (rows, lastInsertID int64, warnings int)
	Close() error
}

type SchemaReader interface {
	Databases(ctx context.Context) ([]model.Database, error)
	Tables(ctx context.Context, db string) ([]model.Table, error)
	Describe(ctx context.Context, db, table string) (model.TableDetail, error)
	Catalog(ctx context.Context, db string) (model.CompletionCatalog, error)
}

// Validator es opcional: un driver que lo implemente valida el perfil (parámetros, rutas TLS…) sin conectar.
type Validator interface {
	Validate(cfg ConnConfig) error
}
