# 02 — Contrato Go ↔ Vue

Fuente de verdad de servicios, DTOs, eventos y errores. Los nombres aquí son literales (Go `PascalCase`, JSON `camelCase`). Los tipos TS se generan con los bindings de Wails; `frontend/src/types` solo re-exporta.

## Errores
```go
type AppError struct {
  Code      string `json:"code"`
  Message   string `json:"message"`   // legible, en español, sin secretos
  Detail    string `json:"detail,omitempty"` // técnico (código MySQL, etc.)
  Retryable bool   `json:"retryable"`
}
```
Códigos: `VALIDATION`, `NOT_FOUND`, `AUTH_FAILED`, `NETWORK_UNREACHABLE`, `TIMEOUT`, `TLS_ERROR`, `SSH_AUTH_FAILED`, `SSH_HOSTKEY_MISMATCH`, `SSH_HOSTKEY_UNKNOWN`, `KEYRING_UNAVAILABLE`, `PASSWORD_REQUIRED` (Detail: `password`|`sshPassword`|`sshKeyPassphrase`), `SESSION_CLOSED`, `QUERY_SYNTAX`, `QUERY_CANCELED`, `QUERY_FAILED`, `RESULT_EXPIRED`, `UNSUPPORTED`, `INTERNAL`.

## DTOs de conexión
```go
type ConnectionProfile struct {
  ID, Name, Driver string       // Driver: "mysql"
  Host string; Port int
  User, Database string         // Database opcional (BD por defecto)
  Color string                  // etiqueta visual (hex o token)
  TLS  TLSConfig
  SSH  SSHConfig
  Params map[string]string      // opciones extra del DSN (charset, timeout, etc.)
  ReadOnly bool                 // solo lectura (spec 10): sesión READ ONLY y Go rechaza lo que no sea lectura (`QUERY_FAILED`, Detail `readonly`)
  SavePassword bool             // si false, se pide al conectar (aplica a todos los secretos del perfil)
  HasPassword, HasSshPassword, HasSshPassphrase bool // solo lectura: los fija el servidor; evita leer el llavero al listar
  CreatedAt, UpdatedAt time.Time
}
type TLSConfig struct { Mode string /* disable|preferred|required|verify-ca|verify-full */; CAPath, CertPath, KeyPath string }
type SSHConfig struct {
  Enabled bool; Host string; Port int; User string
  AuthMethod string // "password" | "key"
  KeyPath string
  // secretos (password / passphrase) NO viajan en el perfil
}
type ConnectionSecrets struct { Password, SSHPassword, SSHKeyPassphrase *string; Clear []string } // nil = sin cambios; Clear: "password"|"sshPassword"|"sshKeyPassphrase" borra el guardado
type TestResult struct { OK bool; LatencyMs int64; ServerVersion string; Steps []TestStep; Error *AppError }
type TestStep struct { Name string /* ssh|tcp|tls|auth|query */; OK, Skipped bool; ElapsedMs int64; Error *AppError } // Skipped: no se ejecutó por un fallo previo. `tls` no aparece si el modo es disable
```

## ConnectionService
| Método | Entrada → Salida |
|---|---|
| `List()` | → `[]ConnectionProfile` |
| `Get(id)` | → `ConnectionProfile` |
| `Save(profile, secrets)` | → `ConnectionProfile` (crea si `ID==""`) |
| `Clone(id)` | → `ConnectionProfile` (nombre "X (copia)", copia secretos) |
| `Delete(id, deleteHistory)` | → `error` (borra secretos del llavero y cierra sesiones; el historial solo si `deleteHistory`) |
| `Test(profile, secrets)` | → `TestResult` (cancelable, timeout 15 s; error solo si el perfil es inválido, los fallos de red/auth van dentro del resultado) |
| `ParseDSN(dsn)` | → `ConnectionProfile` parcial + `ConnectionSecrets` |
| `Connect(id, overrides *ConnectionSecrets)` | → `SessionInfo{SessionID, ProfileID, ServerVersion, CurrentDatabase}`; `PASSWORD_REQUIRED` si falta un secreto no guardado; cancelable |
| `ListSessions()` | → `[]SessionInfo` (sesiones vivas en Go; sirve tras recargar la ventana) |
| `ListDrivers()` | → `[]DriverInfo{Name, DisplayName, DefaultPort, SSL}` |
| `PickFile(title)` | → ruta elegida en el diálogo nativo (`""` si se cancela) |
| `Disconnect(sessionID)` | → `error` |
| `TrustHostKey(host, port, fingerprint)` | → `error` (tras `SSH_HOSTKEY_UNKNOWN`) |

## SchemaService
```go
type Database struct{ Name string; IsSystem bool }
type Table struct{ Name, Schema, Type string /* table|view */; RowEstimate *int64; Comment string }
type Column struct{ Name, DataType, ColumnType string; Nullable bool; Default *string; IsPrimaryKey bool; IsAutoInc bool; Comment string; Ordinal int }
type Index struct{ Name string; Columns []string; Unique bool; Type string }
type ForeignKey struct{ Name string; Columns []string; RefSchema, RefTable string; RefColumns []string; OnUpdate, OnDelete string }
type TableDetail struct{ Table; Columns []Column; Indexes []Index; ForeignKeys []ForeignKey; CreateSQL string }
```
Métodos: `ListDatabases(sessionID)`, `ListTables(sessionID, db)`, `DescribeTable(sessionID, db, table)`, `GetCompletionCatalog(sessionID, db)` (nombres de tablas/columnas para Monaco), `Refresh(sessionID, db?)` (invalida caché y emite `schema:invalidated`).
`SelectStatement(sessionID, db, table, limit)` → `SELECT * … LIMIT n` con identificadores escapados por el driver (`Driver.QuoteIdent`): el front nunca arma SQL (límite por defecto 100, máx. 10 000). Lecturas con timeout de 10 s, caché por sesión (TTL 5 min), peticiones idénticas colapsadas y canceladas al desconectar. Las lecturas devuelven los slices vacíos como `[]`, nunca `null`.

## QueryService
```go
type ExecuteRequest struct {
  SessionID, TabID, SQL string
  Database string           // base a usar (USE) si difiere de la de la pestaña
  MaxRows int               // tope de filas por resultado; 0 = ajuste del usuario (default 100_000)
  AutoLimit int             // >0: añade LIMIT n a SELECT simples sin LIMIT (spec 06); 0 = no
}
type ExecuteResponse struct{ QueryID string }          // inmediato; el trabajo sigue en goroutine
type ColumnMeta struct{ Name, DataType string; Nullable bool; Precision, Scale *int }
type StatementResult struct {
  Index int; Kind string /* "rows" | "exec" */
  Start, End int            // posición de la sentencia en el SQL enviado (UTF-16): para marcar errores
  LimitApplied int          // LIMIT automático añadido (0 = ninguno)
  Columns []ColumnMeta
  RowCount int64; Truncated bool
  AffectedRows int64; LastInsertID int64; Warnings int
  ElapsedMs int64; SQL string
}
type RowWindow struct{ Offset int; Rows [][]Cell; Total int64; Complete bool }
type Cell struct{ V any /* null|string|number|bool */; T string /* "", "bytes", "bigint", "datetime", "json" */; Trunc bool }
```
Métodos:
- `Execute(req)` → `ExecuteResponse`.
- `Cancel(queryID)` → `error` (idempotente).
- `GetRows(queryID, stmtIndex, offset, limit)` → `RowWindow` (limit ≤ 1000). `RESULT_EXPIRED` si el buffer fue liberado.
- `ReleaseResult(queryID)` → libera buffer.
- `CloseTab(tabID)` → cancela runs, libera conexión dedicada y buffers.
- `RowsAsInsert(queryID, stmtIndex, offset, limit, table)` → `INSERT` con las filas indicadas (máx. 1000) y literales/identificadores escapados por el driver (`QuoteIdent`/`QuoteString`); el front nunca arma SQL.
- `StatementAt(sql, offset)` → `{Text, Start, End}` (sentencia bajo el cursor, vía `sqlsplit`).
- `FormatSQL(sql)` → `string`.
- `SaveTabs(sessionID?, []TabState)` / `LoadTabs()` → `[]TabState{ID, SessionProfileID, Title, SQL, Database, Order}` (persistencia en `ui_state`).
- `Explain(...)`: fuera de etapa 1.

## SettingsService
`Get()` → `Settings{Theme, Locale, MaxRows, MaxResultMemoryMiB, HistoryRetention, ShowSystemDatabases, SidebarWidth, SplitRatio}`; `Set(Settings)` → `Settings` (lo guardado). Cambios se validan en Go (`VALIDATION` con `Detail` = nombre del campo) y se persisten en `settings`. `QueryService` y el historial releen el store en cada uso: los límites aplican a la siguiente consulta sin reiniciar.

Reglas de serialización de celdas: enteros > 2^53 → `T:"bigint"` con `V` string; `[]byte` no UTF-8 → `T:"bytes"` con hex truncado; `DATETIME/TIMESTAMP` → string ISO sin zona salvo `TIMESTAMP`; `JSON` → string; textos > 64 KiB se truncan (`Trunc:true`) con opción de "ver completo" vía `GetCell(queryID, stmtIndex, row, col)`.

## HistoryService / ExportService
```go
type HistoryEntry struct{ ID, ProfileID, SQL string; Database string; ExecutedAt time.Time; ElapsedMs int64; OK bool; RowCount, AffectedRows int64; ErrorMessage string }
```
`HistoryService`: `List(HistoryQuery{ProfileID, Search, Status /*""|ok|error*/, Limit, Cursor})` → `{Entries, NextCursor}`, `Delete(id)`, `Clear(profileID)`. El perfil tiene `NoHistory` (cero = se guarda).
`ExportService`: `Export(ExportRequest{QueryID, StmtIndex, Source /*buffer|rerun*/, Options{Format /*csv|json*/, Delimiter, Header, NullAs, BOM, CRLF, QuoteAll, EscapeFormulas, NDJSON}})` → `ExportStart{JobID, Path, Canceled}` (`Canceled` = el usuario cerró el diálogo de guardado); abre el diálogo nativo en Go; progreso por `export:progress`/`export:done`; `CancelExport(jobID)`.

## Eventos (Go → Vue), payload JSON
| Evento | Payload |
|---|---|
| `query:started` | `{queryId, tabId, sessionId, startedAt}` |
| `query:statement` | `{queryId, result: StatementResult}` (al terminar de definir columnas/primer chunk de cada sentencia) |
| `query:progress` | `{queryId, stmtIndex, rowCount}` (throttled) |
| `query:done` | `{queryId, results: []StatementResult, elapsedMs}` |
| `query:error` | `{queryId, stmtIndex, error: AppError}` |
| `session:closed` | `{sessionId, reason}` (p. ej. túnel caído) |
| `schema:invalidated` | `{sessionId, database?}` |
| `export:progress` / `export:done` | `{jobId, rows, bytes}` / `{jobId, path, rows, bytes, canceled, error?}` |
| `history:added` | `{profileId}` |

## Reglas
- Todas las llamadas devuelven `(T, *AppError)` semánticamente: en Wails se expone como excepción con `AppError` serializado en JSON; `api/` lo reconstruye.
- Ninguna llamada bloquea más de 100 ms salvo `Test`, `Connect` y `Export` (con spinner); `Execute` retorna inmediatamente.
