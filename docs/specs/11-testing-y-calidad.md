# 11 — Testing y calidad

## Pirámide
| Nivel | Herramienta | Qué cubre |
|---|---|---|
| Unit Go | `testing`, `testify` | validación de perfiles, `sqlsplit` (tabla de casos extensa), serialización de celdas, buffers, throttling, errores→`AppError` |
| Integración Go (`-tags=integration`) | `testcontainers-go` con MySQL 5.7/8.0/8.4 y MariaDB 10.6; contenedor sshd para túnel | conexión, TLS, SSH, schema, streaming, cancelación (`KILL QUERY`), tipos MySQL |
| Concurrencia | `go test -race`, `goleak` | cero goroutines huérfanas tras desconectar/cerrar pestaña/cancelar |
| Unit front | Vitest + Vue Test Utils + `@pinia/testing` | stores, validaciones de formulario, composables del grid |
| Componentes | Vitest browser/jsdom | ConnectionForm, ResultGrid (virtualización con datos mock) |
| E2E | Playwright contra build web del front con `api/` mockeado (modo `VITE_MOCK_API`) + smoke manual en app real | flujos completos de UI |
| Rendimiento | benchmarks Go (`-bench`) y script de carga | 1 M filas: tiempo a primera ventana, memoria, throughput de export |

## Reglas
- Toda tarea del backlog incluye sus tests; sin tests no está "hecha".
- Cobertura mínima: 80 % en `internal/{services,driver,results,sqlsplit,apperr}`; sin mínimo en UI pero sí en stores.
- Fixtures MySQL en `testdata/sql/*.sql` (esquema `sakila` reducido + tabla `big_numbers` generada con 1 M filas bajo demanda).
- CI (GitHub Actions): matriz macOS/Linux/Windows para build + unit; integración solo Linux. Pasos: `go vet`, `golangci-lint`, `govulncheck`, `go test -race ./...`, `npm run typecheck lint test`, build Wails.

## Definition of Done (global)
Compila en las 3 plataformas · lint/typecheck sin warnings nuevos · tests nuevos pasan con `-race` · spec actualizado si cambió un contrato · sin secretos en logs · criterios de aceptación del spec marcados.

## Implementación (T6.2)
- **E2E**: `frontend/e2e/*.spec.ts` (Playwright, 14 casos: conexiones, consultas y grid, esquema, historial, exportación, ajustes). `npm run test:e2e` levanta Vite con `VITE_MOCK_API=1`, que sustituye por alias `@wailsio/runtime` y los bindings de servicios por `src/mock/` (backend en memoria con los mismos nombres y DTOs; no entra en el build normal). Cada test parte de un estado limpio y recibe sus datos con `window.__MOCK_SEED__`. En local `PW_CHANNEL=chrome npm run test:e2e` usa el Chrome instalado; en CI se instala chromium.
- **Límite conocido**: el mock puede divergir del backend real. Los contratos reales se prueban en Go (unit e integración); el mock solo valida la UI. Si cambia un DTO o evento, hay que actualizar `src/mock/services.ts` (el typecheck no lo detecta porque devuelve `any`).
- **Rendimiento**: `scripts/bench.sh [--integration]`; cifras y alcance en `docs/benchmarks.md`. El criterio «primera ventana < 500 ms con 1 M de filas» lo exige `TestQueryPerfIntegration`; el de paginación por cursor (página profunda ≤ 3× la primera en 5 M de filas, comparada con OFFSET) lo exigirá `TestKeysetPagingPerfIntegration` (spec 18 §9).
- **Smoke manual**: `docs/smoke-manual.md` (diálogos nativos, llavero, SSH/TLS y fluidez real del grid, que ningún test automático cubre).
