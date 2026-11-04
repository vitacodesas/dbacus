# 03 — Interfaz de drivers

Objetivo: añadir un motor nuevo = un paquete `internal/driver/<name>` + registro. Sin tocar servicios ni UI.

```go
package driver

type Capabilities struct {
  Schemas         bool // PostgreSQL: true; MySQL: false (BD == schema)
  KillQuery       bool
  MultiStatement  bool
  SSL             bool
  ExplainSupported bool
}

type Driver interface {
  Name() string                       // "mysql"
  DisplayName() string                // "MySQL / MariaDB"
  Capabilities() Capabilities
  DefaultPort() int
  // Construye el DSN/config a partir del perfil; dialer inyectado (para túnel SSH).
  Open(ctx context.Context, cfg ConnConfig, dial DialFunc) (Pool, error)
  ParseDSN(dsn string) (ConnConfig, error)
}

type ConnConfig struct { Profile ConnectionProfile; Password string }
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type Pool interface {
  Ping(ctx context.Context) error
  ServerVersion(ctx context.Context) (string, error)
  Acquire(ctx context.Context) (Conn, error)   // conexión dedicada (por pestaña)
  Schema() SchemaReader
  Close() error
}

type Conn interface {
  ID() string                                  // id de hilo en servidor (para KILL)
  Use(ctx context.Context, database string) error
  // Ejecuta UNA sentencia y devuelve cursor o resultado de exec.
  Run(ctx context.Context, stmt string) (RunResult, error)
  Kill(ctx context.Context) error              // abre conexión auxiliar: KILL QUERY <id>
  Release() error
}

type RunResult interface {
  Columns() []ColumnMeta           // nil si es exec
  Next() bool                      // avanza; false al final o error
  Row() []any                      // valores crudos del driver
  Err() error
  Affected() (rows, lastInsertID int64, warnings int)
  Close() error
}

type SchemaReader interface {
  Databases(ctx) ([]Database, error)
  Tables(ctx, db string) ([]Table, error)
  Describe(ctx, db, table string) (TableDetail, error)
  Catalog(ctx, db string) (CompletionCatalog, error)
}
```
Los DTOs (`Database`, `Table`, …) viven en un paquete neutral `internal/model` compartido con `services` (misma forma que spec 02).

## Registro
```go
func Register(d Driver)         // llamado en init() de cada driver
func Get(name string) (Driver, bool)
func List() []Driver
```

## Splitter de sentencias
`internal/sqlsplit`: divide el script en sentencias respetando strings, comentarios (`--`, `#`, `/* */`), identificadores con backticks y `DELIMITER`. Devuelve `[]Statement{Text, StartOffset, EndOffset}` (offsets para marcar errores en Monaco). Vive en Go; el front no parsea SQL.

## Implementación MySQL (etapa 1)
- `go-sql-driver/mysql` con `mysql.Config`; el túnel SSH entra por `Config.DialFunc` (sin registros globales).
- TLS: `Config.TLS` (`*tls.Config`) construido desde `TLSConfig.Mode` (`disable` sin TLS; `preferred` TLS sin verificar con fallback a texto plano; `required` TLS sin verificar; `verify-ca` valida cadena sin nombre de host; `verify-full` valida todo); mínimo TLS 1.2.
- Tipos: usar `rows.ColumnTypes()` para `ColumnMeta`; `parseTime=false` y formateo propio para fechas. **No** usar `columnsWithAlias` (antepone el alias de tabla al nombre de columna y rompería el grid).
- Parámetros del perfil (`Params`): se envían como `SET k=v` de sesión; claves `[A-Za-z_][A-Za-z0-9_]*`, valores sin `;`/saltos de línea, y se rechazan opciones del driver Go que cambian su seguridad (`multiStatements`, `allowAllFiles`, `parseTime`, `tls`, …). `timeout`, `readTimeout` y `writeTimeout` se interpretan como duraciones.
- `Open` no toca la red; el primer contacto ocurre en `Ping`/`Acquire` (el `SessionManager` hace `Ping` al abrir).
- Pools: general (25 conexiones: pestañas + esquema + holgura) y auxiliar de 2 conexiones solo para `KILL QUERY`, para que cancelar nunca espere al pool agotado.
- Por qué `Kill` y no cancelar el `ctx`: el driver Go cierra la conexión cuando se cancela el contexto de una consulta, y se perderían las variables de sesión de la pestaña (ADR-004). `Cancel` debe llamar a `Kill` primero; cancelar el `ctx` solo como último recurso si `Kill` falla o la consulta no termina tras 2 s.
- Errores del servidor → `AppError`: 1045/1698/1044 `AUTH_FAILED`, 1049 `VALIDATION`, 1064/1149 `QUERY_SYNTAX`, 1317 `QUERY_CANCELED`, 1205/3024 `TIMEOUT`; red/TLS según tipo de error. El mensaje mostrado nunca incluye el texto crudo del servidor para autenticación (puede nombrar usuario/host); va en `Detail`.
- Schema vía `information_schema` (consultas parametrizadas; excluir `mysql`, `sys`, `performance_schema`, `information_schema` marcándolas `IsSystem`).
- `Kill`: `KILL QUERY <thread_id>` desde `Pool` (conexión auxiliar). Funciona también en MariaDB.
- Compatibilidad objetivo: MySQL 5.7, 8.0, 8.4 y MariaDB 10.6+.
- Streaming real: `rows.Next()` sin `ReadAll`; `MaxAllowedPacket` respetado.

## Generación de DDL (spec 19, implementado en T13.2)
Interfaz **opcional** `DDLGenerator` (patrón de `Validator`): `Generate(ChangeSet, Model, ServerInfo) → []string, []Warning`. `ServerInfo` expone la versión y si es MariaDB. Identificadores con `QuoteIdent`, literales con `QuoteString`. El driver no ejecuta: ejecuta `AlterService` por una conexión de `Pool.AcquireEdit` (efímera; igual que `AcquireConn` ejecuta `SET SESSION TRANSACTION READ ONLY` en perfiles de solo lectura), así el modo solo lectura se aplica igual.

## Rutinas almacenadas (spec 21)
Interfaz **opcional** `RoutineReader` (patrón de `DDLGenerator`): `Routines(ctx, db) → []Routine` (sin cuerpos) y `DescribeRoutine(ctx, db, kind, name) → RoutineDetail`. Un driver que no la implemente no ofrece rutinas (`UNSUPPORTED`). La escritura (pre-validación, `DROP`, `CREATE`, restauración) la ejecuta `RoutineService` por `AcquireEdit`, una sentencia por llamada.

## Monitoreo (spec 22)
Interfaz **opcional** `ActivityReader` (patrón de `RoutineReader`, pero la implementa el **`Pool`**, no el `SchemaReader`, porque la conexión `mon` vive en el pool): `Probe(ctx) → MonitorCaps`, `Processes(ctx) → []Process`, `GlobalStatus(ctx, names) → map[string]uint64`, `MaxConnections(ctx) → uint64` (`SELECT @@global.max_connections`), `Digests(ctx) → []DigestCounters` (sin texto) y `DigestTexts(ctx, digests) → map[string]string`. El servicio la obtiene con `sess.Pool.(driver.ActivityReader)`. Un driver que no la implemente no ofrece monitoreo (`UNSUPPORTED`). Cada llamada tiene un tope de 5 s; una fuente sin privilegios o sin `performance_schema` es un estado de `Probe`, no un error. `SHOW FULL PROCESSLIST` devuelve 8 columnas en MySQL y 9 en MariaDB (`Progress`). El MySQL la implementa sobre un `*sql.DB` propio `mon` de **1 conexión**, abierto bajo demanda: no consume plazas de `MaxTabs` ni compite con el pool general. Toda sentencia es de solo lectura y la conexión ejecuta `SET SESSION TRANSACTION READ ONLY` aunque el perfil no sea de solo lectura. Los contadores se leen como `uint64` (`SUM_TIMER_WAIT` puede superar `int64`). Los procesos salen de `INFORMATION_SCHEMA.PROCESSLIST` con columnas explícitas y respaldo `SHOW FULL PROCESSLIST`.

## Escritura parametrizada (T7.2, spec 13)
`Pool.AcquireEdit(ctx)` entrega una conexión efímera con `CLIENT_FOUND_ROWS`, y `Conn` añade `Exec(ctx, stmt, args...) (rowsAffected, error)` (parámetros del driver, nunca interpolados), `Begin`, `Commit` y `Rollback` (idempotente sin transacción; `Release` revierte una transacción olvidada). Solo la edición de celdas los usa; el SQL del usuario sigue por `Run`. Verificado en MySQL 8.0/8.4 y MariaDB 10.11: `BIGINT UNSIGNED` máximo, `DECIMAL`, `DATETIME(6)`, bytes, emojis, NULL con `<=>`, intento de inyección guardado literalmente, commit/rollback y aislamiento entre conexiones.
