# 13 — Edición de celdas (etapa 2)

> Estado: **aprobado** (decisiones D1–D6 aceptadas tal como están, 2026-10-28). Aún no hay código de esta etapa.

## Objetivo
Editar valores directamente en el grid de resultados (doble clic o `F2` sobre una celda), revisar los cambios como SQL y aplicarlos al servidor en una sola operación atómica. Sigue la regla de oro: **Go decide, valida, genera el SQL y lo ejecuta; Vue solo captura la intención y pinta**.

## Alcance v1
- **Incluye**: modificar celdas existentes (`UPDATE`) y poner `NULL`; cambios en lote; revisar el SQL antes de aplicar; aplicar en una transacción; detectar conflictos; descartar.
- **Fuera de alcance** (épicas posteriores): insertar y borrar filas, editar BLOB/binarios, editar vistas, editar resultados de consultas con JOIN/agregaciones/expresiones, DDL, editar la clave primaria, deshacer tras aplicar.

## Decisiones
| # | Decisión | Por defecto propuesto | Motivo |
|---|---|---|---|
| ⚑D1 | ¿Cambios inmediatos o en lote? | **En lote**: se acumulan en Go y se aplican con un botón | Permite revisar el SQL y es atómico; evita `UPDATE` accidentales al teclear |
| ⚑D2 | ¿Qué resultados son editables? | Un único `SELECT` simple de **una tabla** con clave primaria (o índice único sin NULL) cuyas columnas estén todas en el resultado | Sin clave no hay forma segura de identificar la fila |
| ⚑D3 | ¿Con qué conexión se aplica? | Una **conexión nueva y efímera** (`edit:<id>`), no la de la pestaña | No toca una transacción abierta por el usuario en la pestaña. Coste: no hereda variables de sesión (`time_zone`, `sql_mode`); se documenta |
| ⚑D4 | ¿Se edita la clave primaria? | **No** en v1 | Cambiar la identidad de la fila complica conflictos y recarga |
| ⚑D5 | ¿Cómo se envían los valores? | **Parametrizados** (`Conn.Exec(stmt, args...)`), nunca interpolados | Sin inyección y sin problemas de escape; el SQL de revisión se renderiza aparte con literales |
| ⚑D6 | ¿Confirmación al aplicar? | **Siempre** muestra el SQL y pide confirmar (ajuste para desactivarlo) | Hay conexiones a bases compartidas (p. ej. staging) |

## Cuándo es editable un resultado
Go lo decide con `EditService.GetEditInfo(queryId, stmtIndex)`; Vue solo lee el veredicto.
1. La sentencia es un `SELECT` de **una sola tabla** (`sqlsplit.SimpleSelectTable`): sin `JOIN`, `UNION`, subconsultas, `GROUP BY`, `DISTINCT`, `INTO`, ni tablas derivadas. Admite `WHERE`, `ORDER BY`, `LIMIT`.
2. La lista de columnas es `*` o columnas simples (con o sin prefijo de tabla), **sin alias ni expresiones**; las columnas que no cumplan quedan solo lectura.
3. La tabla es una **tabla base** (no vista) y tiene clave primaria, o un índice único con todas sus columnas `NOT NULL`. **Todas** las columnas de esa clave deben estar en el resultado.
4. El perfil no es de solo lectura y el resultado sigue en memoria.

Si falla, `Editable=false` con un `Reason` que la UI traduce: `no_single_table`, `view`, `no_primary_key`, `key_not_selected`, `readonly_profile`, `expired`. Por columna: no editable si es clave (D4), `AUTO_INCREMENT`, generada, binaria, o si la celda llegó truncada (`Trunc`).

## Modelo de cambios (viven en Go)
- Un **conjunto de cambios** por `(queryId, stmtIndex)`. Cada cambio guarda: fila del buffer, columna, valor nuevo (`Null` o texto), valor original y los valores de la clave **capturados del buffer en ese momento** (el buffer no cambia hasta aplicar).
- Máximo 1 000 celdas pendientes por resultado; más se rechaza con `VALIDATION`.
- `GetRows` superpone los cambios pendientes y marca esas celdas con `Cell.Edited=true` (así Vue no replica valores: regla de oro 7).
- Re-ejecutar la consulta, cerrar la pestaña o desconectar **descarta** los cambios; la UI pide confirmación si hay pendientes.

## Contrato (a incorporar en spec 02 al implementar)
> Implementado en T7.1: los tipos `EditInfo`/`EditColumn` son `edit.Info`/`edit.Column` (paquete `internal/edit`, con la lógica pura de elegibilidad en `edit.Analyze`) y `EditService.GetEditInfo`. Metadatos nuevos del esquema: `Column.IsGenerated` e `Index.Prefix`. Motivos de columna: `key`, `auto_increment`, `generated`, `binary`, `expression`, `unmapped`, `duplicate`; de resultado además `not_rows` y `table_not_found`. Si hay varios candidatos de clave, se elige la primaria y, si no está entera en el resultado, el primer índice único utilizable.
```go
type EditColumn struct{ Editable bool; Reason string; DataType string; Nullable bool }
type EditInfo struct {
  Editable bool; Reason string
  Database, Table string
  KeyColumns []int          // índices de columna del resultado
  Columns []EditColumn      // una por columna del resultado
  Pending int
}
type EditValue struct{ Null bool; Text string }
type EditStatement struct{ Row int; Cols []int; SQL string } // SQL de revisión, con literales
type ApplyResult struct{ Applied int; Conflict *EditConflict } // Conflict != nil → nada se aplicó
type EditConflict struct{ Row int; Reason string /* "changed" | "missing" */ }
```
`EditService`: `GetEditInfo`, `Stage(queryId, stmt, row, col, EditValue) → EditInfo` (valida y convierte; error por celda), `Unstage(queryId, stmt, row, col)`, `Discard(queryId, stmt)`, `Preview(queryId, stmt) → []EditStatement`, `Apply(queryId, stmt) → ApplyResult`. Evento `edit:applied {queryId, stmtIndex, rows}` para refrescar el grid.

> Implementado en T7.3: `EditService.Stage/Unstage/Discard/Preview` (`Preview` devuelve `edit.Preview{Row, Cols, SQL}` con valores a la vista; los parámetros reales nunca viajan a Vue). Los cambios viven en `results.Buffer` (`Stage`, `PendingList`…), así mueren con el resultado, y `GetRows` los superpone con `Cell.Edited`. Escribir el valor original retira el cambio. Tope: `MaxPending = 1000`.
>
> **Control optimista y tipos** (hallazgo de diseño): el WHERE solo compara el valor original de columnas *verificables* (enteros, `DECIMAL`, texto, `ENUM/SET`, `DATE`, `DATETIME`, `TIME`, `YEAR`). En `FLOAT/DOUBLE` (un FLOAT no coincide con el texto que lo representa), `JSON` (se compara como valor JSON) y `TIMESTAMP` (depende de la zona horaria) la fila se localiza solo por la clave: se puede pisar un cambio ajeno de esa columna. Los enteros se envían tipados (`int64`/`uint64`), no como texto.

## Conversión y validación (Go)
El front envía texto; Go convierte según el tipo de columna y rechaza antes de tocar el servidor:
- Enteros (con signo/sin signo y rango), `DECIMAL` (texto numérico, sin pasar por float), `FLOAT/DOUBLE`, `BIT/BOOL`.
- `DATE/DATETIME/TIMESTAMP`: formatos ISO con fracción opcional; se conserva `DATETIME(6)`.
- `JSON`: debe ser JSON válido. `ENUM/SET`: texto; lo valida el servidor.
- `NULL` en columna `NOT NULL` → error inmediato en la celda. Cadena vacía **no** es `NULL`.
- Los `BIGINT` mayores de 2^53 viajan y vuelven como texto (ya lo hace el grid).

## Aplicar
1. Comprobar de nuevo perfil no solo lectura (el guardia de `QueryService` no cubre este camino: se hace explícito en `Apply`), sesión viva y resultado en memoria.
2. Conexión efímera (D3), obtenida con `Pool.AcquireEdit` → `Begin`. Es una conexión aparte con `CLIENT_FOUND_ROWS`: **hallazgo de T7.2**: sin esa opción MySQL cuenta 0 filas afectadas cuando la fila existe pero el valor queda igual, lo que daría falsos conflictos; no se activa en las conexiones normales para no cambiar el recuento de «filas afectadas» de las sentencias del usuario.
3. Por fila, **una** sentencia: ``UPDATE `db`.`t` SET `c1`=?, `c2`=? WHERE `pk`=? AND `c1` <=> ? AND `c2` <=> ?`` — los `<=>` con el valor original dan control optimista (null-safe). Con `CLIENT_FOUND_ROWS`, `affected=1` significa «la fila existe y estaba como la vimos».
4. Si alguna sentencia afecta 0 filas → `ROLLBACK` y `Conflict{Row, Reason}`; si falla el servidor → `ROLLBACK` y `AppError` con la fila. Si todo va bien → `COMMIT`.
5. Tras el commit, Go relee las filas tocadas por clave (misma conversión a `Cell`), reemplaza esas filas del buffer (copia al escribir bajo el mutex del manager), vacía el conjunto de cambios y emite `edit:applied`.
6. Cada sentencia se registra en el historial (spec 07) con su SQL de revisión; el SQL con valores **no** va a logs (regla 5).

## UI (Vue, solo pinta)
- Doble clic o `F2` abre un editor en la celda; `Enter` guarda el cambio pendiente y baja, `Tab` pasa a la siguiente columna editable, `Esc` cancela. Menú contextual: «Establecer NULL», «Revertir celda».
- Celdas con cambio pendiente: fondo distinto y valor original en el tooltip. Columnas no editables: cursor normal y tooltip con el motivo.
- Barra bajo el grid, solo si hay cambios: «N cambios en M filas · Ver SQL · Descartar · Aplicar».
- Aplicar abre un diálogo con el SQL (`Preview`) y confirmar/cancelar. Conflicto → mensaje con la fila y opción de recargar el resultado.
- Store `edits` (Pinia): solo `info` y contadores por resultado; nada de valores.

## Seguridad y casos borde
- Identificadores escapados por el driver (`QuoteIdent`); valores siempre como parámetros (D5). La tabla y la base salen del análisis de Go + `DescribeTable`, **nunca** de texto enviado por Vue.
- Perfil de solo lectura: `Editable=false` y `Apply` lo rechaza aunque alguien llame al servicio directamente.
- Triggers, `ON UPDATE CURRENT_TIMESTAMP` y valores por defecto cambian la fila: por eso se relee tras aplicar.
- Dos pestañas editando la misma fila: gana la primera; la segunda recibe `Conflict` (no pisa en silencio).
- Resultado ya liberado por el LRU: `RESULT_EXPIRED`, los cambios pendientes se pierden con aviso.
- Zona horaria: con D3 los `TIMESTAMP` se interpretan en la zona de la conexión efímera; se avisa en el diálogo si difiere de la de la pestaña (`@@time_zone`).

## Criterios de aceptación
- [ ] Editar una celda de `SELECT * FROM t` (tabla con PK) y aplicar actualiza solo esa fila; el grid muestra el valor nuevo sin re-ejecutar.
- [ ] Una consulta con `JOIN`, alias, `GROUP BY` o una vista no ofrece edición y explica por qué.
- [ ] Tabla sin clave primaria ni índice único: no editable, con mensaje claro.
- [ ] Clave compuesta: el `WHERE` incluye todas sus columnas.
- [ ] Si otra sesión cambió la fila entre medias, `Apply` devuelve conflicto y **no se aplica ningún cambio** del lote (atomicidad).
- [ ] Conexión de solo lectura: no se puede editar y `Apply` directo falla con `QUERY_FAILED`/`readonly`.
- [ ] `NULL` explícito, cadena vacía, `BIGINT UNSIGNED` máximo, `DECIMAL`, `DATETIME(6)`, `JSON` y emojis se guardan sin pérdida (integración en MySQL 8.0/8.4 y MariaDB 10.11).
- [ ] Valores inválidos (texto en un entero, `NULL` en `NOT NULL`) se rechazan en la celda sin llegar al servidor.
- [ ] Re-ejecutar o cerrar la pestaña con cambios pendientes pide confirmación.
- [ ] Los valores editados no aparecen en logs ni en eventos (ampliar `TestSecretsNeverReachDBLogsOrEvents`).

## Pruebas
Unitarias: análisis de `SimpleSelectTable` (tabla de casos), elegibilidad, conversión por tipo, generación del `UPDATE`. Integración (`-tags integration`): lo anterior contra servidores reales, incluidos conflicto, rollback y clave compuesta. E2E con el mock: editar, ver la barra, revisar SQL, aplicar y conflicto. Smoke manual: edición real, conflicto con dos pestañas y conexión de solo lectura.
