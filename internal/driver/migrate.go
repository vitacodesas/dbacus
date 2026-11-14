package driver

import (
	"context"

	"github.com/vitacodesas/dbacus/internal/model"
)

// Migración de schema (spec 23, ADR-008). Interfaz opcional que implementa el Pool; sin ella el servicio devuelve UNSUPPORTED.
// Todo el SQL lo genera el driver con identificadores escapados; el orquestador (internal/migrate) solo dice qué hacer.

// Roles de una sesión de migración (spec 23 ⚑D2, ⚑D19).
const (
	// RoleSource: conexiones del origen. Nacen READ ONLY (lo impone el servidor) y solo leen.
	RoleSource = "source"
	// RoleTarget: conexiones del destino (ruta por Go): escriben lotes. No leen el origen.
	RoleTarget = "target"
	// RoleServer: conexiones del destino que además leen el origen (ruta dentro del servidor, INSERT … SELECT). No pueden
	// ser READ ONLY: la garantía de «el origen no se escribe» la da el WriteGuard del driver.
	RoleServer = "server"
)

// MigrationChunk es un trozo de tabla (spec 23 ⚑D7): un rango [Lo, Hi) de la clave de troceo, una partición, las filas con
// clave NULL o la tabla entera.
type MigrationChunk struct {
	Seq       int
	Whole     bool
	Partition string
	Null      bool
	Lo, Hi    []any // nil = sin cota; Lo inclusivo, Hi exclusivo; un valor por columna de la clave
}

// RowBatch son filas leídas del origen. Los valores son los del driver (bytes crudos, enteros, flotantes), nunca texto formateado.
type RowBatch struct {
	Rows  [][]any
	Bytes int
}

type MigrationOpen struct {
	Role     string
	Database string // schema por defecto de las conexiones: el origen (RoleSource) o el destino (RoleTarget y RoleServer)
	Source   string // RoleServer: schema que se lee, para el WriteGuard
	Size     int
	SQLMode  string // sql_mode del origen; el driver quita los modos que rechazan fechas cero
}

// MigrationSession es un pool propio de la migración (`mig`): no consume plazas de pestaña. Cada conexión que abre, también
// tras reconectar, ya lleva las variables de sesión fijadas (spec 23 ⚑D4).
type MigrationSession interface {
	Acquire(ctx context.Context) (MigrationConn, error)
	Close() error
}

// MigrationConn es una conexión de una sesión de migración. Un método que no corresponde a su rol devuelve un error interno.
type MigrationConn interface {
	// ThreadID es el id de hilo en el servidor, para el KILL QUERY de la cancelación (solo hilos propios).
	ThreadID() uint64
	Kill(ctx context.Context) error
	Release() error

	// RoleSource.
	// Chunks recorre la clave de la tabla y emite los trozos a medida que los calcula (planificador perezoso).
	Chunks(ctx context.Context, t model.MigrationTable, chunkRows int, emit func(MigrationChunk) error) error
	// Read transmite el trozo en lotes de a lo sumo batchBytes.
	Read(ctx context.Context, t model.MigrationTable, c MigrationChunk, batchBytes int, emit func(RowBatch) error) error
	// CountRows cuenta las filas de una tabla del schema de la conexión.
	CountRows(ctx context.Context, table string) (int64, error)

	// RoleTarget y RoleServer (escrituras en el destino).
	// Exec ejecuta DDL o una sentencia de limpieza ya generada; pasa por el WriteGuard.
	Exec(ctx context.Context, stmt string) error
	Begin(ctx context.Context) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	// Insert escribe un lote (RoleTarget) dentro de la transacción abierta y devuelve las filas escritas. c es el trozo al que
	// pertenece (el driver real lo ignora; el falso lo usa para ClearRange).
	Insert(ctx context.Context, t model.MigrationTable, c MigrationChunk, b RowBatch) (int64, error)
	// CopyChunk copia un trozo dentro del servidor (RoleServer) dentro de la transacción abierta.
	CopyChunk(ctx context.Context, t model.MigrationTable, c MigrationChunk) (int64, error)
	// ClearRange borra del destino el rango del trozo, antes de reintentarlo (spec 23 ⚑D9). Una tabla de un solo trozo se vacía entera.
	ClearRange(ctx context.Context, t model.MigrationTable, c MigrationChunk) error
	AddForeignKey(ctx context.Context, table string, fk model.ForeignKey) error
	// FixAutoIncrement deja el contador en max(valor del origen, MAX(columna)+1).
	FixAutoIncrement(ctx context.Context, table, column string, source int64) error
	// CreateObject crea una vista, rutina o trigger con el SQL ya reescrito, fijando antes el sql_mode y los charsets de origen.
	CreateObject(ctx context.Context, obj model.MigrationCode, sql string) error
	// Orphans cuenta, hasta limit, las filas de table cuya clave foránea no tiene fila padre.
	Orphans(ctx context.Context, table string, fk model.ForeignKey, limit int) (int64, error)
}

// SchemaMigrator lo implementa el Pool de un motor que sepa copiar un schema.
type SchemaMigrator interface {
	// MigrationInspect lee el schema origen: tablas (DDL sin FKs, columnas copiables, clave de troceo), vistas, rutinas y triggers.
	MigrationInspect(ctx context.Context, db string) (model.MigrationSource, error)
	// MigrationPreflight comprueba el destino sin crear nada.
	MigrationPreflight(ctx context.Context, in model.MigrationPreflightInput) (model.MigrationPreflight, error)
	// MigrationObjectCode lee el CREATE que `db` tiene de las vistas, rutinas y triggers pedidos; solo salen los que existen, con el texto
	// vacío si no se pudo leer (sin SHOW_ROUTINE o SHOW VIEW).
	MigrationObjectCode(ctx context.Context, db string, refs []model.ObjectRef) ([]model.MigrationCode, error)
	// MigrationEnsureDatabase crea el schema destino si no existe y dice si lo creó.
	MigrationEnsureDatabase(ctx context.Context, name, charset, collation string) (created bool, err error)
	// MigrationDrop borra, en orden, solo lo que la migración creó (spec 23 ⚑D12).
	MigrationDrop(ctx context.Context, db string, objs []model.MigrationCreated) error
	OpenMigration(ctx context.Context, o MigrationOpen) (MigrationSession, error)
}
