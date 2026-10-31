# 06 — Consultas y resultados (foco principal de etapa 1)

## Objetivo
Escribir SQL, ejecutarlo sin bloquear la UI, ver resultados grandes con fluidez y poder cancelar.

## Pestañas de consulta
- Barra de pestañas: nueva (`⌘/Ctrl+T`), cerrar (`⌘/Ctrl+W`), renombrar (doble clic), reordenar (drag), indicador de "ejecutando" y de cambios sin guardar. Cada pestaña pertenece a **una sesión** y tiene su conexión dedicada (ADR-004).
- Estado persistido (Go, `ui_state`): contenido SQL, nombre, BD seleccionada; al reabrir la app se restauran las pestañas (sin re-ejecutar).
- **Vínculo con archivo (spec 20)**: una pestaña puede estar vinculada a una consulta guardada (`.sql`); el título es el nombre del archivo, el punto ● marca «sin guardar» (`sql !== baseSql`), `⌘/Ctrl+S` guarda y cerrar con cambios pregunta. Abrir o restaurar nunca ejecuta.
- Selector de BD por pestaña (ejecuta `USE` en su conexión).

## Editor (Monaco)
- Lenguaje `mysql`/`sql`, tema sincronizado con la app, minimapa off, atajos: **Ejecutar todo** `⌘/Ctrl+Enter`, **Ejecutar selección/sentencia actual** `⌘/Ctrl+Shift+Enter`, **Cancelar** `Esc` (con foco en resultados) o botón Stop, formatear `⌘/Ctrl+Alt+L` (formateo en Go vía librería; el front solo llama).
- Sentencia actual = la que contiene el cursor, calculada por `sqlsplit` en Go (`QueryService.StatementAt(sql, offset)`, ver spec 02); si hay selección, el front envía el texto seleccionado.
- Autocompletado: keywords, tablas, columnas, funciones MySQL (spec 05). Marcadores de error: tras `QUERY_SYNTAX`, subrayar posición cuando MySQL la informa ("near … at line N").

## Ejecución
1. `Execute` valida sesión/tab, divide en sentencias (`sqlsplit`), crea `queryId`, retorna inmediato.
2. Goroutine por run: adquiere la conexión de la pestaña (una ejecución simultánea por pestaña; segunda → `QUERY_FAILED` "ya hay una ejecución en curso" o se encola; **decisión: rechazar**).
3. Ejecuta sentencias en orden; si una falla, se detiene (opción futura "continuar en error"). Emite `query:started`, por sentencia `query:statement` (columnas conocidas), `query:progress` throttled, y `query:done`/`query:error`.
4. Resultado de filas → `results.Buffer` por (queryId, stmtIndex): almacenamiento columnar-simple `[][]Cell` en memoria, tope `MaxRows` (default 100 000, ajustable por ajustes). Al superarlo: `Truncated=true`, se detiene la lectura y se cierra el cursor (no hay conteo total en etapa 1; la exportación completa re-ejecuta, spec 07).
5. Exec (INSERT/UPDATE/DDL): `AffectedRows`, `LastInsertID`, `Warnings`.
6. Límite de memoria global de buffers (default 512 MiB): LRU libera runs antiguos de otras pestañas → `RESULT_EXPIRED` al pedir sus ventanas (UI ofrece "Re-ejecutar").

## Grid de resultados (Vue)
- Virtualización vertical y horizontal; ventana pedida con `GetRows(queryId, stmt, offset, limit≤1000)`, prefetch ±1 ventana, caché local de máx. 5 ventanas.
- Cabecera fija con nombre y tipo; ancho de columna redimensionable; número de fila; ordenar/filtrar en cliente **no** se hace (si el usuario quiere, edita el SQL; futuro: `ORDER BY` reescrito en Go).
- Render por tipo: NULL como chip gris `NULL`, números alineados a la derecha, bigint/bytes/JSON con indicador, textos truncados con tooltip y "Ver completo" (modal; `GetCell`).
- Selección de celdas/filas/columnas, copiar como TSV, copiar como INSERT (Go lo arma), `⌘/Ctrl+C`.
- Múltiples sentencias → sub-pestañas de resultado ("Resultado 1", "Resultado 2", "Mensajes").
- Barra de estado: filas (`1–100 de 52 340` o `100 000+ (truncado)`), tiempo, BD, sesión.
- **Paginación por cursor (spec 18)**: el selector pasa a ser «Filas por página»; para un `SELECT` de una tabla con clave numérica única la app pagina por cursor de clave (`< >`, primera y última) y llega al final sin acumular filas; el resto se comporta como se describe a continuación (con el motivo en la barra). El texto siguiente describe el LIMIT automático que sigue vigente en el modo `none` y con «Sin límite». Antes: scroll virtual es la paginación; adicionalmente selector "Limitar a N filas" (100/500/1000/sin límite=MaxRows) que **inyecta LIMIT solo si** la sentencia es un `SELECT` simple sin LIMIT (decisión en Go con el splitter; se informa al usuario "LIMIT 1000 aplicado").

## Cancelación
`Cancel(queryId)` → `Conn.Kill` (`KILL QUERY`; el ctx solo se cancela si Kill falla o pasan 2 s, porque cancelarlo cierra la conexión y pierde el estado de sesión) → estado `canceled` (`QUERY_CANCELED`), buffer parcial queda consultable. UI: botón Stop visible durante la ejecución; cerrar pestaña cancela.

## Estados de UI
Vacío ("Ejecuta una consulta"), ejecutando (spinner + cronómetro + filas recibidas), éxito, éxito-sin-filas ("0 filas"), error (panel con mensaje, código MySQL, posición), cancelado, conexión perdida.

## Criterios de aceptación
- [~] `SELECT * FROM t` con 1 M de filas: primera ventana visible < 500 ms; scroll fluido; memoria de la app acotada por el tope configurado. (Primera ventana 5–44 ms y memoria acotada medidas en `docs/benchmarks.md`; la fluidez del scroll en la ventana nativa sigue pendiente del smoke manual.)
- [ ] Cancelar `SELECT SLEEP(60)` retorna control en < 1 s y el hilo desaparece de `SHOW PROCESSLIST`.
- [ ] Script con 3 sentencias muestra 3 resultados; error en la 2ª detiene y conserva el resultado de la 1ª.
- [ ] Dos pestañas ejecutan concurrentemente sin bloquearse ni mezclar resultados; `SET @x=1` en una no afecta a la otra.
- [ ] Valores BIGINT UNSIGNED máximos, DECIMAL, BLOB, JSON, DATETIME(6), NULL y emojis se muestran sin pérdida.
- [ ] Cerrar pestaña/desconectar libera conexión y buffers (test con `goleak` y medición de heap).
- [ ] Atajos funcionan en macOS y Windows/Linux (Cmd vs Ctrl).

## Implementación (T4.1–T4.7)
- **Go**: `results.Manager` (buffer por ejecución, LRU global por memoria; solo se expulsan ejecuciones terminadas) y `QueryService`. Una ejecución por pestaña (la segunda recibe `QUERY_FAILED` con `Detail: "busy"`). Al lanzar una nueva ejecución se libera la anterior de la pestaña.
- **Truncado**: al llegar a `MaxRows` se lee una fila más para saber si había más; si las había se hace `Conn.Kill` y se cierra el cursor (cerrarlo con filas pendientes obligaría al driver a leerlas todas). La conexión sigue viva.
- **LIMIT automático**: `sqlsplit.InjectLimit` solo toca SELECT simples sin `LIMIT`/`UNION`/`INTO`/`FOR UPDATE`/`LOCK`; lo inserta tras el último carácter de código, así un comentario final no se lo traga.
- **Errores**: `query:error` lleva `stmtIndex` y la posición de la sentencia; el editor marca la sentencia, o solo la línea si el servidor dice "at line N". El marcador desaparece al editar.
- **Cancelación**: `Cancel` → `KILL QUERY` primero; si la consulta termina "bien" tras el KILL (p. ej. `SLEEP()` devuelve 1) se informa igualmente como cancelada y no se ejecutan las sentencias restantes.
- **Grid**: ventanas de 200 filas, 5 en caché por pestaña (`results.ts`); las incompletas mientras la consulta lee se vuelven a pedir con cada `query:progress`. Selección de celda/fila/columna/rango con Shift, copia TSV (con o sin encabezados, máx. 100 000 filas), copia como INSERT (lo arma Go), valor completo en modal (`GetCell`), anchos redimensionables. `Esc` cancela si hay consulta en curso.
- **Editor**: Monaco "a medida" (`src/monaco.ts`: solo las contribuciones usadas + lenguaje MySQL, sin CDN). Un modelo por pestaña (conserva deshacer y scroll). Atajos: ⌘/Ctrl+↵ ejecutar todo, ⌘/Ctrl+⇧+↵ selección o sentencia actual, ⌘/Ctrl+⌥+L formatear, ⌘/Ctrl+T nueva pestaña, ⌘/Ctrl+W cerrar. Formateo básico en Go (`internal/sqlfmt`): cambia espacios y mayúsculas de palabras reservadas, conserva comentarios y deja intactos los scripts con `DELIMITER`.
- **Pestañas**: se persisten en `ui_state` con debounce de 500 ms; al reabrir la app se restauran sin ejecutar y sin sesión (se conecta al ejecutar).
