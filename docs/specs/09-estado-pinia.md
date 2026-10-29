# 09 — Estado (Pinia) y reparto Go/Vue

## Principio
Go es la fuente de verdad de datos; Pinia guarda **proyecciones** para pintar y estado puramente visual. Todos los stores usan *setup stores* (`defineStore('x', () => {...})`) con TS estricto.

## Stores
| Store | Estado | Acciones principales | Fuente |
|---|---|---|---|
| `connections` | `profiles[]`, `groups[]`, `loading`, `formDraft` | `load`, `save`, `clone`, `remove`, `test`, `saveGroup`, `removeGroup`; getter `colorOf(profile)` (color propio, si no el del grupo) | `ConnectionService` |
| `sessions` | `byId`, `activeSessionId` | `connect`, `disconnect`, `onClosed` | `ConnectionService` + evento `session:closed` |
| `schema` | árbol cacheado `[sessionId][db]`, `expanded`, `filter` | `loadDatabases`, `loadTables`, `loadDetail`, `refresh` | `SchemaService` + `schema:invalidated` |
| `queryTabs` | `tabs[]` (`id, sessionId, title, sql, database, dirty`), `activeTabId` | `open`, `close`, `rename`, `setSql` (debounce → persistir en Go), `execute`, `cancel` | `QueryService` |
| `results` | por `tabId`: `status`, `statements[]` (con `paging`), `error`, `rowCounts`, `pageSeq` y `paging` (páginas en curso), `windows` (LRU; cambiar de página vacía la caché y `pageSeq` descarta las ventanas pedidas antes del cambio) | `onStarted/onStatement/onProgress/onDone/onError`, `fetchWindow`, `page(tabId, stmt, direction, column?)` | eventos `query:*` + `GetRows` |
| `history` | `entries[]`, `cursor`, `search` | `load`, `delete`, `clear` | `HistoryService` |
| `ui` | `theme`, `sidebarWidth`, `splitRatio`, `sidebarView`, `locale`, `collapsedGroups[]`, `toasts[]` | setters; persistencia en Go (`ui_state`) | local + `SettingsService` |

## Reglas
- Un solo punto de suscripción a eventos: `api/events.ts` registra listeners al arrancar y despacha a stores; los componentes **no** se suscriben directamente.
- Filas de resultado: nunca en estado reactivo profundo; `results.windows` usa `shallowRef` y `Map`; el grid pide por `(tabId, stmtIndex, offset)`.
- `queryTabs.setSql` guarda en Go con debounce 500 ms; el texto vive en Pinia para el editor, y Go lo persiste.
- Efectos al desmontar: cancelar fetch de ventanas obsoletas (token por petición, descartar respuesta si cambió el offset objetivo).
- Errores: cada acción captura `AppError` y lo deja en el store correspondiente (`lastError`) o lo manda a `ui.toasts`; nada de `console.error` como único manejo.
- Pinia persist plugin **no** se usa; la persistencia es explícita vía Go.
- Tests: Vitest + `@pinia/testing`, mockeando `api/`.

## Ajustes (T6.1)
`ui` carga `SettingsService.Get()` al arrancar y proyecta tema, idioma, anchos y límites. Tema, idioma, anchos de panel y «mostrar bases del sistema» se guardan solos con debounce de 500 ms **solo después de una carga correcta** (si no, el primer redimensionado pisaría lo guardado con los valores por defecto). El diálogo (`⌘/Ctrl+,` o el engranaje) edita una copia y guarda con `ui.saveSettings`: si Go devuelve `VALIDATION`, el error se pinta en el campo `Detail` y no cambia nada. `schema.showSystem` es una vista de `ui.showSystemDatabases`.

## Edición de celdas (T7.5)
Store `edits`: `infos[queryId:stmt]` (el veredicto `EditInfo` de Go, con el contador de cambios pendientes), el diálogo de revisión (`review`) y la pregunta de descartar (`discardPrompt`). **No guarda valores**: lo escrito va a Go (`Stage`), que valida y lo superpone en `GetRows`; tras cada cambio `results.reloadCached` vuelve a pedir las ventanas en caché y las sustituye sin vaciarlas (sin parpadeo). `results.execute` y `queryTabs.closeTab` piden confirmación solo si hay cambios pendientes (sin ellos no esperan nada). Ajuste `confirmEdits` en `ui`.

## Filas nuevas y borrados (T8.4)
`edits` añade `addRow`/`markDelete`/`unmarkDelete` y `focusRequest` (el grid lo consume para seleccionar la fila creada y abrir su editor). `results` guarda por ventana las filas borradas y `newFrom` (`rowFlags`), y `reloadCached` sincroniza el total de filas con el de Go, que ya cuenta las nuevas y descuenta las borradas.
