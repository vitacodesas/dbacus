# 01 — Arquitectura

## Capas
```
Vue 3 (views/components) ── Pinia stores ── api/ (wrappers tipados de bindings Wails)
        │ llamadas async + eventos
Wails 3 bridge
        │
internal/services  (API pública, valida, traduce a AppError)
        │
internal/session ─ internal/results ─ internal/store ─ internal/secrets
        │
internal/driver (interfaz) ── internal/driver/mysql ── internal/tunnel (SSH dialer)
```
Regla de dependencias: capas superiores dependen de inferiores; `driver` no conoce `services`, `store` ni Wails.

## Concurrencia
- `SessionManager` (mutex + mapa `sessionId → *Session`). Cada `Session` = `*sql.DB` (pool) + túnel opcional.
- Cada pestaña de consulta pide una `*sql.Conn` dedicada (`session.AcquireConn(tabId)`); se libera al cerrar la pestaña o la sesión (ADR-004).
- Cada ejecución corre en su propia goroutine con `context.WithCancel`; `QueryService.Cancel(queryId)` ejecuta `Conn.Kill` (KILL QUERY por el pool auxiliar) y solo cancela el ctx como último recurso (el driver Go cierra la conexión al cancelar el ctx; ver spec 03).
- Lectura de filas: goroutine productora → `results.Buffer` (append bajo lock) → notificación de progreso con throttling (≤ 10 eventos/s por query).
- Shutdown: `app.OnShutdown` cancela todos los ctx, espera `WaitGroup` con timeout 5 s, cierra túneles y pools.

## Eventos (Go → Vue) — ver spec 02
`query:started`, `query:progress`, `query:done`, `query:error`, `session:closed`, `schema:invalidated`.

## Persistencia
SQLite en `os.UserConfigDir()/Tabularis-Clon/app.db` con migraciones numeradas embebidas (`internal/store/migrations`). Tablas: `connection_profiles`, `query_history`, `settings`, `ui_state`. Secretos **no** van a SQLite (spec 10).

## Frontend
- `api/`: único lugar que importa los bindings generados; expone funciones tipadas y suscripciones a eventos (con unsubscribe).
- `stores/`: `connections`, `sessions`, `schema`, `queryTabs`, `results`, `history`, `ui` (spec 09).
- `components/`: presentacionales; `views/`: composición de pantallas.
- Rendimiento: `shallowRef` para listas grandes, nada de `reactive` profundo sobre filas; el grid solo guarda la ventana visible + margen.

## Manejo de errores
`internal/apperr`: `AppError{Code, Message, Detail, Retryable}`; códigos en spec 02. Servicios traducen errores de driver (p. ej. MySQL 1045 → `AUTH_FAILED`).

## Observabilidad
`log/slog` JSON a archivo rotado en `UserCacheDir`; nivel configurable; redacción de secretos (spec 10).

## Configuración de build
Una sola instancia de ventana principal en etapa 1; multiventana de Wails 3 queda reservada.
