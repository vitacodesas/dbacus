# Benchmarks (T6.2)

Medidos el 2026-10-28 en un Apple M4 (macOS 26, Go 1.26, colima con Docker local). Se repiten con `scripts/bench.sh --integration`. Son cifras de **una** máquina y de datos sintéticos pequeños (dos columnas, ~18 bytes por fila): sirven para detectar regresiones y comprobar los criterios del spec 06, no para prometer rendimiento en otro equipo ni con filas anchas.

## 1 M de filas contra servidores reales (`-tags integration`)

Consulta: producto cruzado de seis tablas de 10 filas → `id` + `txt`, `MaxRows = 1 000 000`, conexión por loopback.

| Motor | Primera ventana (200 filas) | Resultado completo | Filas/s | Buffer (estimado) | Heap extra | `GetRows(200)` ya cargado |
|---|---|---|---|---|---|---|
| mysql:8.0 | 33 ms | 376 ms | 2,66 M | 124 MiB | +177 MiB | 2 µs |
| mysql:8.4 | 27 ms | 363 ms | 2,76 M | 124 MiB | +177 MiB | 3 µs |
| mariadb:10.11 | 5 ms | 263 ms | 3,81 M | 124 MiB | +176 MiB | 3 µs |

Rango de la primera ejecución suelta con mysql:8.0: 44 ms / 468 ms. Criterio del spec 06 «primera ventana < 500 ms»: **cumplido con un margen de 10×** en este escenario; el test (`TestQueryPerfIntegration`) lo exige y falla si se supera.

Exportación de las mismas 1 M de filas a CSV re-ejecutando la consulta (`TestExportServiceIntegration/…/1M`): 18 MB en disco en 294–379 ms con **3 MB** de memoria extra como máximo (el test falla por encima de 100 MB). La exportación no acumula filas.

Lo que **no** cubre: filas anchas (BLOB/JSON/textos largos), red real o túnel SSH, ni MySQL 5.7.

## Micro-benchmarks Go (`go test -bench`)

| Benchmark | Resultado |
|---|---|
| `results.BufferAppend` (1000 filas × 4 columnas) | ~16 µs por 1000 filas (≈ 62 M filas/s), 19 allocs |
| `results.BufferWindow` (200 filas a mitad de 100 000) | 5 ns, 0 allocs (devuelve un sub-slice) |
| `export.CSV` | ≈ 8,5 M filas/s |
| `export.JSON` / `export.NDJSON` | ≈ 5,0 M filas/s |
| `sqlsplit.Split` (script de ~1 MiB, 10 000 sentencias) | ≈ 350 MB/s |

`BufferAppend` mide solo el almacenamiento; el coste real por fila está en la lectura del driver y en la conversión a `Cell`, que sí entra en la tabla anterior.

## Interfaz

No hay medición de FPS ni de memoria del WebView: el grid se virtualiza (el E2E `un resultado enorme se trunca…` comprueba que con 100 000 filas solo hay unas decenas de filas en el DOM), pero la fluidez de scroll con 1 M de filas en la ventana nativa **no se ha medido**; está en el guion de `docs/smoke-manual.md`.

## Paginación por cursor (spec 18, ADR-005)
Medido el 2026-10-29 con `TestKeysetPagingPerfIntegration` (`scripts` no lo incluye aún: se lanza con
`go test -tags integration -run TestKeysetPagingPerfIntegration -v ./internal/services`). Apple M4, Go 1.26, Docker
(colima, 4 CPU, 4 GiB) por loopback, datos en `tmpfs`. Tabla `big(id BIGINT PK AUTO_INCREMENT, v BIGINT, pad CHAR(8))`
con índice en `v`, **5 000 000 de filas**, páginas de 1000. Tiempo de la consulta de la página (mediana de 5, cruda, sin
servicio); «cursor» es el SQL que genera `paging.Plan.Build`; «OFFSET» es `LIMIT 1000 OFFSET k` con el mismo orden.

| Servidor | Orden | 1ª página | Cursor al 50 % | Cursor al 99 % | OFFSET al 50 % | OFFSET al 99 % |
|---|---|---|---|---|---|---|
| mysql:8.0 | `id` (PK) | 0,81 ms | 0,88 ms | 0,86 ms | 192 ms (×218) | 378 ms (×439) |
| mysql:8.0 | `v DESC` + desempate | 1,8 ms | 4,7 ms | 3,6 ms | 1,42 s (×305) | 1,50 s (×414) |
| mysql:8.4 | `id` (PK) | 0,77 ms | 0,90 ms | 0,87 ms | 195 ms (×217) | 385 ms (×442) |
| mysql:8.4 | `v DESC` + desempate | 1,7 ms | 4,6 ms | 3,5 ms | 1,43 s (×308) | 1,50 s (×422) |
| mariadb:10.11 | `id` (PK) | 0,84 ms | 0,98 ms | 0,96 ms | 236 ms (×241) | 478 ms (×500) |
| mariadb:10.11 | `v DESC` + desempate | 2,0 ms | 4,3 ms | 3,5 ms | 1,52 s (×350) | 1,62 s (×462) |

- «Última» con el servicio real (`QueryService.Page`): ~1 ms en las tres imágenes con 5 M de filas.
- El coste de la página con cursor no depende de la profundidad (≤ 5 ms con el índice adecuado); OFFSET crece con ella.
- **Hallazgo de la medición**: con el desempate en la dirección contraria (`v DESC, id ASC`) la primera página de `v DESC`
  costaba ~530 ms (el índice no sirve direcciones mezcladas y ordena en cada página). Con el desempate en la misma
  dirección que la última columna del usuario (`v DESC, id DESC`) baja a ~1,8 ms. Es lo que hace `paging.EffectiveOrder`.
- Alcance: tablas estrechas (3 columnas), servidor local, sin red ni SSH, sin carga concurrente, ni MySQL 5.7. Con una
  columna de orden **sin** índice cada página ordena y se acerca al coste de OFFSET (el servicio lo avisa con `no_index`).
- En CI la prueba usa 1 M de filas (`CI` definido) para no gastar minutos ni memoria; `DBACUS_PERF_ROWS` lo fija a mano.
