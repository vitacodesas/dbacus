# 09 — Estado (Pinia) y reparto Go/Vue

## Principio
Go es la fuente de verdad de datos; Pinia guarda **proyecciones** para pintar y estado puramente visual. Todos los stores usan *setup stores* (`defineStore('x', () => {...})`) con TS estricto.

## Stores
| Store | Estado | Acciones principales | Fuente |
|---|---|---|---|
| `connections` | `profiles[]`, `loading`, `formDraft` | `load`, `save`, `clone`, `remove`, `test` | `ConnectionService` |
| `sessions` | `byId`, `activeSessionId` | `connect`, `disconnect`, `onClosed` | `ConnectionService` + evento `session:closed` |
| `schema` | árbol cacheado `[sessionId][db]`, `expanded`, `filter` | `loadDatabases`, `loadTables`, `loadDetail`, `refresh` | `SchemaService` + `schema:invalidated` |
| `queryTabs` | `tabs[]` (`id, sessionId, title, sql, database, dirty`), `activeTabId` | `open`, `close`, `rename`, `setSql` (debounce → persistir en Go), `execute`, `cancel` | `QueryService` |
| `results` | por `tabId`: `status`, `statements[]`, `error`, `rowCounts`, `windows` (LRU) | `onStarted/onStatement/onProgress/onDone/onError`, `fetchWindow` | eventos `query:*` + `GetRows` |
| `history` | `entries[]`, `cursor`, `search` | `load`, `delete`, `clear` | `HistoryService` |
| `ui` | `theme`, `sidebarWidth`, `splitRatio`, `sidebarView`, `locale`, `toasts[]` | setters; persistencia en Go (`ui_state`) | local + `SettingsService` |

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
