// Package driver define la interfaz que implementa cada motor (spec 03). No conoce services, store ni Wails.
package driver

import (
	"context"
	"net"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/secrets"
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

// ServerInfo describe el servidor para generar DDL (las sintaxis difieren entre versiones y motores).
type ServerInfo struct {
	Version string // tal como la entrega el servidor
	MariaDB bool
}

// DDLGenerator es opcional (spec 19): traduce un ChangeSet validado a sentencias ALTER. No ejecuta nada; ejecuta
// AlterService por una conexión del pool, así el modo solo lectura se aplica igual. m es la lectura fresca de la tabla.
type DDLGenerator interface {
	GenerateDDL(cs model.ChangeSet, m model.AlterModel, info ServerInfo) ([]string, error)
}

// ActivityReader es opcional (spec 22): lo implementa el Pool de un motor con procesos y estadísticas por digest. El
// servicio lo obtiene con sess.Pool.(ActivityReader) y devuelve UNSUPPORTED si falta. Todo es de solo lectura y va por una
// conexión propia (no consume plazas de pestaña ni compite con el pool general). Una fuente que no se puede leer por
// falta de privilegios o de performance_schema es un estado de Probe, no un error.
type ActivityReader interface {
	// Probe sondea cada fuente y devuelve su estado con un Hint fijo. Solo falla si no hay conexión.
	Probe(ctx context.Context) (model.MonitorCaps, error)
	// Processes lista los hilos (INFO truncado a 2 KiB), sin la conexión del monitor, por TimeSec descendente.
	Processes(ctx context.Context) ([]model.Process, error)
	// GlobalStatus devuelve los contadores numéricos pedidos; una variable ausente o no numérica no aparece en el mapa.
	GlobalStatus(ctx context.Context, names []string) (map[string]uint64, error)
	// MaxConnections devuelve @@global.max_connections (el máximo con el que se compara Threads_connected).
	MaxConnections(ctx context.Context) (uint64, error)
	// Digests lee los contadores acumulados de cada digest, sin su texto.
	Digests(ctx context.Context) ([]model.DigestCounters, error)
	// DigestTexts devuelve el texto normalizado (≤ 4 KiB) de los digests pedidos.
	DigestTexts(ctx context.Context, digests []string) (map[string]string, error)
}

// ProcessKiller es opcional (spec 22 ⚑D8): lo implementa el Pool. Termina el hilo `id` del servidor: queryOnly=true cancela
// su consulta y conserva la conexión (KILL QUERY); false cierra la conexión (KILL). El permiso lo decide el servidor.
type ProcessKiller interface {
	KillProcess(ctx context.Context, id uint64, queryOnly bool) error
}

// RoutineReader es opcional (spec 21): lo implementa el SchemaReader de un motor con procedimientos y funciones.
// Routines lista sin cuerpos (una base puede tener cientos); DescribeRoutine lee el CREATE completo, sin caché.
type RoutineReader interface {
	Routines(ctx context.Context, db string) ([]model.Routine, error)
	DescribeRoutine(ctx context.Context, db, kind, name string) (model.RoutineDetail, error)
}
