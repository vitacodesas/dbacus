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
