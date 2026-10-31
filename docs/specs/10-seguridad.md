# 10 — Seguridad

## Secretos
- Contraseñas DB/SSH y passphrases **solo** en llavero del SO (`go-keyring`) o memoria de sesión. Nunca en SQLite, JSON, logs, eventos ni DTOs hacia Vue (los DTOs usan `ConnectionSecrets` solo en la dirección Vue→Go).
- Go no devuelve secretos al front; la UI muestra solo `hasPassword: bool` derivado.
- En memoria: tipos `Secret` con `String()` redactado; sobrescribir buffers cuando sea viable.
- Sin llavero → no hay fallback en claro (spec 04).

## Red
- TLS MySQL: por defecto `preferred`; `verify-full` recomendado; mín. TLS 1.2; certificados cliente opcionales.
- SSH: verificación estricta de host key (spec 04); algoritmos modernos por defecto de `x/crypto/ssh`; sin `InsecureIgnoreHostKey` en ningún camino de producción (lint que lo prohíba).
- Timeouts de conexión/lectura por defecto (10 s / 0 con cancelación por ctx).

## SQL
- La app ejecuta el SQL del usuario tal cual (es su función). Identificadores generados por la app (SELECT desde árbol, esquema) siempre escapados con backticks (`` ` `` duplicado) y valores parametrizados en `information_schema`.
- No hay `multiStatements` implícito en el driver; la división la hace `sqlsplit` y cada sentencia se envía por separado.
- Opción por perfil "Solo lectura": la sesión ejecuta `SET SESSION TRANSACTION READ ONLY` y Go rechaza sentencias no-SELECT/SHOW/DESCRIBE/EXPLAIN (defensa en profundidad; la garantía real es del servidor).

## Logs y telemetría
- `slog` con redacción: claves `password|passphrase|secret|token` → `***`; el SQL se registra solo en nivel debug y truncado a 200 chars.
- Sin telemetría ni llamadas de red salvo las del usuario.

## Frontend / Wails
- CSP estricta en el webview; sin `eval` salvo lo que Monaco requiera (workers locales empaquetados, sin CDN).
- Sin `v-html` con datos de BD; render de celdas como texto.
- Los servicios expuestos validan todo input; IDs desconocidos → `NOT_FOUND`.
- Archivos: rutas elegidas por diálogos nativos o validadas; no leer rutas arbitrarias enviadas por el front para fines distintos a TLS/SSH key declarados.

## Dependencias
`go mod verify`, `govulncheck` y `npm audit` en CI; versiones pineadas (incluida Wails 3, ADR-001).

## Criterios de aceptación
- [x] `grep` de la contraseña de prueba en `app.db`, logs y capturas de eventos devuelve vacío (test automatizado: `TestSecretsNeverReachDBLogsOrEvents`; comprobado por mutación que falla si se registra el secreto).
- [x] Host key cambiada bloquea la conexión (`TestHostKeyMismatchBlocksAndCannotBeOverwritten`, `TestConnectViaSSHHostKeyFlow`).
- [x] Modo solo lectura rechaza `DROP TABLE` antes de enviarla (`TestReadOnlyProfileRejectsWritesBeforeSending`) y el servidor lo impide aunque algo se colara (`TestReadOnlyProfileEnforcedByServerIntegration`, MySQL 8.0/8.4 y MariaDB 10.11).

## Auditoría (T6.4, 2026-10-28)
Qué se comprobó y cómo; lo que no, queda dicho.

| Punto del spec | Estado | Evidencia |
|---|---|---|
| Secretos fuera de SQLite, logs, eventos y DTOs | ✅ | `TestSecretsNeverReachDBLogsOrEvents` + `TestSaveCreateStoresSecretOutsideSQLite`; los DTOs solo exponen `hasPassword` |
| Redacción en logs (`password\|passphrase\|secret\|token`), SQL truncado a 200 | ✅ | `internal/logging`; ningún `slog` de `internal/` registra SQL (solo mensajes de `KILL`) |
| Host key SSH estricta, sin `InsecureIgnoreHostKey` | ✅ | tests de `tunnel` y `TestNoInsecureHostKeyCallbackInProduction` (falla si aparece en código no-test) |
| TLS mínimo 1.2, modos `verify-ca`/`verify-full` | ✅ | `driver/mysql/config.go` y sus tests |
| Sin `multiStatements`, una sentencia por envío | ✅ | `mysql_test.go` (también rechaza el parámetro reservado) |
| Editar estructura (spec 19) | ✅ | `AlterService` (`GetModel`, `Preview`, `Apply`) repite el guardia de solo lectura con `sessionReadOnly` (perfil guardado OR sesión; compartido con `Execute` y `EditService`) y se apoya en `SET SESSION TRANSACTION READ ONLY`; un `ALTER` generado nunca es `IsReadOnlySafe`. Tests: `TestAlterReadOnly*`, `TestGeneratedDDLIsNeverReadOnlySafe`; capa de servidor verificada en MySQL 8.0/8.4 y MariaDB 10.11 |
| Identificadores escapados por el driver | ✅ | `QuoteIdent`/`QuoteString`; Vue no arma SQL |
| **Modo solo lectura** | ✅ (**estaba sin implementar**: el spec lo pedía y ninguna tarea lo recogió) | Perfil `ReadOnly`: `SET SESSION TRANSACTION READ ONLY` al abrir cada conexión dedicada (falla cerrado) + `sqlsplit.IsReadOnlySafe` antes de enviar. Rechaza todo salvo SELECT/SHOW/DESCRIBE/EXPLAIN/USE/WITH/VALUES/TABLE/HELP, `INTO` de nivel superior (`OUTFILE`), `SET`, `CALL` y `START TRANSACTION` |
| CSP estricta en el webview | ✅ | Meta en el `index.html` del **build** (no en dev, donde Vite necesita HMR): `default-src 'self'`, `script-src 'self'`, `connect-src 'self'`, `object-src 'none'`… Verificada con Playwright sobre el build (`security.spec.ts`: bloquea inline y orígenes externos; ningún test ve violaciones) y con el servidor Wails real en el navegador (Monaco, worker, WebSocket de eventos sin violaciones). **No** verificada en la ventana nativa de macOS/Windows/Linux |
| Sin `v-html` ni CDN | ✅ | `grep` sin resultados; Monaco y su worker salen del bundle |
| `go mod verify`, `npm audit` | ✅ | «all modules verified»; 0 vulnerabilidades |
| `govulncheck` | ✅ con una excepción | Había **17 vulnerabilidades de la biblioteca estándar** alcanzables con Go 1.26.4 (`net/http`, `crypto/tls`, `crypto/x509`…); se corrigen fijando `toolchain go1.26.10` en `go.mod` (CI usa `go-version-file`). Queda `GO-2026-5932` en `golang.org/x/crypto@v0.58.0`: **sin versión corregida** y el código no la alcanza; revisar al salir un parche |
| Validación de entradas de los servicios | ⚠️ parcial | Revisados `ConnectionService`, `HistoryService`, `SettingsService`, `ExportService`, `QueryService.Execute` (tests de validación y `NOT_FOUND`). **No** se auditó línea a línea cada método de `SchemaService` |
| Rutas de archivo | ✅ | La ruta de exportación la elige el diálogo nativo en Go; las rutas TLS/SSH vienen del perfil y solo se leen para eso |

Riesgos aceptados o abiertos:
- `preferred` y `required` cifran pero **no verifican** el certificado (`InsecureSkipVerify`), igual que el cliente `mysql`; para verificar hay que usar `verify-ca`/`verify-full`.
- `style-src 'unsafe-inline'` es necesario porque Monaco inyecta `<style>`.
- El modo solo lectura no impide efectos secundarios de funciones llamadas desde un `SELECT` ni `INTO` dentro de subconsultas; la garantía real es del servidor y de los privilegios del usuario de BD. Tampoco permite `SET @x = …`.
- El llavero real del SO solo se ha ejercido con un perfil temporal en macOS; Windows y Linux no se han probado.

### Modo solo lectura con la sesión abierta (smoke manual, 2026-10-28)
Hallazgo: marcar «Solo lectura» en un perfil con una sesión ya abierta no protegía hasta reconectar (la sesión conserva el flag con el que se abrió), sin avisar. Ahora:
- `QueryService.Execute` lee el perfil guardado en **cada ejecución**: marcar el modo protege al instante. El servidor sigue sin `READ ONLY` en esa conexión hasta reconectar, pero Go ya no deja pasar escrituras.
- Quitar el modo con sesión abierta exige reconectar (lado conservador: no se reabre la escritura sola).
- Guardar una conexión que tiene sesión abierta muestra «Los cambios se aplicarán al reconectar» con el botón **Reconectar**; «Guardar y conectar» reconecta directamente.
- El modo es visible: candado «Solo lectura» (o «Lectura y escritura») en la barra de estado, etiqueta en la barra de la consulta y candado en la tarjeta de la conexión.
