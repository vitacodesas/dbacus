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
- [ ] `grep` de la contraseña de prueba en `app.db`, logs y capturas de eventos devuelve vacío (test automatizado).
- [ ] Host key cambiada bloquea la conexión.
- [ ] Modo solo lectura rechaza `DROP TABLE` antes de enviarla.
