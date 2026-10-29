# ADR-002 — Resultados en buffer Go, ventanas bajo demanda

**Estado**: aceptada

**Contexto**: empujar todas las filas por eventos al webview satura el puente IPC y la memoria del front; el front debe ser ligero.

**Decisión**: Go lee el cursor en streaming y guarda las filas (ya serializadas a `Cell`) en `results.Buffer` por (`queryId`, `stmtIndex`). Los eventos solo llevan metadatos y progreso. El grid pide ventanas con `GetRows(offset, limit≤1000)`.

**Alternativas descartadas**
- Push de chunks por evento: backpressure difícil, duplica memoria.
- Paginación con `LIMIT/OFFSET` re-ejecutando: costosa y inconsistente en datos mutables. *(Enmendado por el [ADR-005](ADR-005-paginacion-por-cursor.md): la paginación por **cursor de clave** sí se adopta; OFFSET sigue descartado.)*

**Consecuencias**
- Tope `MaxRows` y tope global de memoria con LRU (spec 06); `RESULT_EXPIRED` cuando se libera.
- Exportación completa re-ejecuta en streaming directo a archivo.
- Posible evolución: spill a disco (SQLite temporal/archivo mmap) en etapa posterior.
