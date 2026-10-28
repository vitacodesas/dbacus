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
