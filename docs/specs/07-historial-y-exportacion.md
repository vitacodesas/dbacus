# 07 — Historial y exportación

## Historial
- Cada ejecución terminada (ok/error/cancelada) escribe una `HistoryEntry` en SQLite (en goroutine aparte, sin bloquear el run). Se guarda el SQL tal cual se envió + metadatos; **sin** resultados.
- Retención: 5 000 entradas por perfil (las más antiguas se purgan) y SQL truncado a 100 KiB. Ajustable en settings.
- UI: panel "Historial" por conexión: búsqueda (FTS5 o `LIKE`), filtro ok/error, fecha relativa, duración, filas. Acciones: abrir en nueva pestaña, insertar en pestaña activa, copiar, eliminar, limpiar todo (confirmar).
- Privacidad: opción "No guardar historial" por perfil; el SQL con credenciales literales no se redacta (aviso en settings).

## Exportación (CSV / JSON)
- Origen: un resultado (`queryId`, `stmtIndex`) con buffer vigente. Si está truncado, ofrecer "Re-ejecutar y exportar todo" (Go re-ejecuta en streaming directo a archivo sin pasar por buffer, con la misma sentencia).
- Diálogo nativo de guardado abierto desde Go. Escritura en goroutine con `bufio.Writer`, cancelable, progreso por eventos.
- **CSV**: separador (`,` `;` `\t`), comillas, cabecera sí/no, `NULL` como vacío o literal, BOM UTF-8 opcional, saltos `\n`/`\r\n`.
- **JSON**: array de objetos o NDJSON; bigint como string; bytes en base64; fechas ISO 8601.
- Nombre sugerido `resultado-YYYYMMDD-HHMMSS.csv`.

## Criterios de aceptación
- [ ] Exportar 1 M filas a CSV sin superar +100 MB de memoria adicional.
- [ ] Cancelar exportación borra el archivo parcial.
- [ ] CSV abre correctamente en Excel con BOM y caracteres unicode.
- [ ] Historial registra queries fallidas con su mensaje y no registra las de otra conexión en el panel.

## Implementación (T5.1–T5.2)
- **Historial**: `services.NewHistoryRecorder` es el `History` de `QueryService`; consulta el perfil en cada sentencia y no guarda si `ConnectionProfile.NoHistory` (casilla "No guardar el historial" en la pestaña General). Tras guardar emite `history:added{profileId}`; el panel agrupa ráfagas (150 ms) en una recarga. `HistoryService` valida (`profileId` obligatorio, estado `""|ok|error`) y pagina con cursor (50 por página). Búsqueda con `LIKE` escapado (sin FTS5). Acciones: abrir en pestaña nueva (doble clic), insertar al final de la pestaña activa, copiar, eliminar, limpiar con confirmación. El historial mostrado es el de la conexión de la sesión activa o, si no hay, la de la pestaña abierta.
- **Exportación**: `internal/export` (CSV y JSON, sin dependencias de drivers) recibe `results.Cell`, así que el buffer y la re-ejecución comparten formato. `ExportService.Export` abre el diálogo nativo (inyectado como `PickSave`), devuelve `{jobId, path}` y sigue en una goroutine; termina con `export:done`. Dos orígenes: `buffer` (lo cargado) y `rerun` (vuelve a ejecutar la sentencia original, sin el LIMIT automático, en una conexión propia `export:<jobId>` y escribe fila a fila). La UI propone `rerun` cuando el resultado está truncado.
- **Archivo parcial**: se escribe en `<destino>.partial` y se renombra al terminar; cancelar o fallar lo borra y nunca pisa un archivo existente. Permisos 0600.
- **Cancelación**: `CancelExport` hace `KILL QUERY` en la conexión de la exportación y, a los 2 s, cancela el contexto. Al abortar a mitad de lectura también se hace `KILL` antes de cerrar el cursor.
- **Formatos**: CSV (separador `,` `;` tab, encabezado, `NULL` vacío o literal, BOM, CRLF, entrecomillar todo, protección contra fórmulas `= + - @` solo en textos). JSON (array o NDJSON; BIGINT/DECIMAL como string; binarios en base64 del contenido completo; DATETIME/TIMESTAMP en ISO 8601; JSON embebido si es válido; columnas repetidas reciben sufijo `_2`). Se usa `Cell.Full()`, nunca el valor recortado para la UI.
- **Verificado** (integración en MySQL 8.0, 8.4 y MariaDB 10.11): 1 M de filas en ~0,3 s con +3–4 MB de memoria máxima; cancelación en pleno vuelo borra el parcial y deja la sesión usable; tipos extremos a JSON.
- **No verificado**: el diálogo nativo de guardado (el modo servidor no lo soporta; el error se muestra en un aviso) y abrir el CSV en Excel real.

### Corrección (smoke manual, 2026-10-28)
El diálogo solo ofrecía elegir origen cuando el resultado estaba `truncated`. Con el LIMIT automático de la barra (por defecto 1000) el resultado no se marca truncado, así que se exportaban solo las filas cargadas sin avisar. Ahora el selector también aparece si `limitApplied > 0`, con aviso propio y «volver a ejecutar» (sentencia original, sin el LIMIT automático) como opción por defecto. Cubierto en `ExportDialog.test.ts` y en el E2E «exportación con LIMIT automático».
