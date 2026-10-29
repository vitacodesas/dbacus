# 12 — Backlog para agentes

Formato: **ID · título** — Depende de · Specs · Alcance · Fuera de alcance · DoD (además del DoD global de spec 11).
Orden = orden de ejecución. Tareas sin dependencia mutua pueden ir en paralelo (marcadas ∥).

## Épica 0 — Fundaciones
**T0.1 ✅ · Spike y esqueleto Wails 3 + Vue + Tailwind + Pinia** — Dep: — · Specs: 01, ADR-001
Alcance: proyecto Wails 3 (v3.0.0, ver ADR-001), Vite+Vue3+TS+Pinia+Tailwind, `internal/app`, un servicio `PingService.Hello()` y un evento de prueba, diálogo nativo de archivo, `scripts/smoke.sh`, CI básica, completar comandos en AGENTS.md.
DoD: `wails3 dev` y `wails3 build` funcionan en macOS; bindings TS generados y usados desde `api/`; documento `adr/ADR-001` actualizado con la versión elegida y hallazgos.

**T0.2 ✅ · apperr, logging y config de rutas** — Dep: T0.1 · Specs: 01, 02, 10
Alcance: `AppError`+códigos, `slog` con redacción, rutas de datos/logs, shutdown ordenado. DoD: tests de redacción.

**T0.3 ✅ · Store SQLite + migraciones** ∥ — Dep: T0.1 · Specs: ADR-003
Alcance: `internal/store` (profiles, history, settings, ui_state), migración 001. DoD: tests CRUD y migraciones idempotentes.

**T0.4 ✅ · Secrets (keyring)** ∥ — Dep: T0.1 · Specs: 04, 10
Alcance: `internal/secrets` con interfaz y mock; claves `profile:<id>:*`. DoD: tests con mock; `Secret` redactado.

**T0.5 ✅ · Sistema de UI base y layout** ∥ — Dep: T0.1 · Specs: 08
Alcance: tokens Tailwind, tema claro/oscuro, componentes `Base*`, layout principal, splitters, `vue-i18n`, store `ui`. DoD: Storybook-less página `/dev/components` con todos los estados; teclado/ARIA.

## Épica 1 — Drivers y sesiones
**T1.1 ✅ · Modelo y interfaz Driver + registry** — Dep: T0.2 · Specs: 03 · DoD: paquete `model`, `driver`, tests del registry.
**T1.2 ✅ · sqlsplit** ∥ — Dep: T0.1 · Specs: 03, 06 · DoD: >60 casos (strings, comentarios, backticks, DELIMITER, offsets), fuzz test.
**T1.3 ✅ · Driver MySQL: Open/Ping/Version/TLS** — Dep: T1.1 · Specs: 03, 04 · DoD: integración con MySQL 8.0 y MariaDB (testcontainers).
**T1.4 ✅ · Túnel SSH** ∥ — Dep: T0.2 · Specs: 04, 10 · Alcance: dialer, auth password/clave, known_hosts propio, keepalive, errores tipados. DoD: integración con sshd en contenedor; `goleak`.
**T1.5 ✅ · SessionManager** — Dep: T1.3, T1.4 · Specs: 01, ADR-004 · DoD: acquire/release de conexiones por pestaña, cierre en cascada, evento `session:closed`.

## Épica 2 — Conexiones (foco)
**T2.1 ✅ · ConnectionService (Go)** — Dep: T0.3, T0.4, T1.5 · Specs: 02, 04
Alcance: List/Get/Save/Clone/Delete/Connect/Disconnect/ParseDSN/TrustHostKey, validaciones completas. DoD: tests unit + secretos fuera de SQLite/logs.
**T2.2 ✅ · Test de conexión por pasos** — Dep: T2.1 · Specs: 04 · DoD: pasos ssh/tcp/tls/auth/query, cancelable, mensajes accionables distintos por causa (integración).
**T2.3 ✅ · Stores `connections` y `sessions` + `api/`** — Dep: T2.1, T0.5 · Specs: 09
**T2.4 ✅ · UI lista de conexiones** — Dep: T2.3 · Specs: 04, 08
**T2.5 ✅ · UI formulario de conexión** — Dep: T2.3 · Specs: 04, 08
Alcance: secciones General/TLS/SSH/Avanzado/Importar, validación, selector de archivos, panel de Test en vivo, diálogo de host key. DoD: criterios de aceptación de spec 04 marcados; tests de componente.
**T2.6 ✅ · Flujo conectar/desconectar y reconexión** — Dep: T2.4, T2.5 · DoD: pasar todos los criterios del spec 04.

## Épica 3 — Esquema
**T3.1 ✅ · SchemaReader MySQL + SchemaService + caché** — Dep: T1.5 · Specs: 03, 05 · DoD: integración con `sakila`, singleflight, invalidación DDL.
**T3.2 ✅ · Store `schema` + árbol virtualizado** — Dep: T3.1, T2.6 · Specs: 05, 09, 08
**T3.3 ✅ · Catálogo de autocompletado** ∥ — Dep: T3.1 · Specs: 05 · DoD: endpoint `GetCompletionCatalog`.

## Épica 4 — Consultas y resultados (foco)
**T4.1 ✅ · results.Buffer + serialización de celdas** ∥ — Dep: T1.1 · Specs: 02, 06, ADR-002 · Nota: el driver MySQL devuelve valores tipados (`int64`, `float64`, `[]byte` para texto/decimal/blob, `nil`); el serializador decide por `ColumnMeta.DataType`, no por el tipo Go. DoD: tests de tipos extremos, LRU, límites; benchmark.
**T4.2 ✅ · QueryService: Execute/Cancel/GetRows/CloseTab/GetCell** — Dep: T1.2, T1.5, T4.1 · Specs: 02, 06 · Incluye: tras ejecutar DDL (`sqlsplit.IsDDL`), llamar a `SchemaService.invalidate(sessionID, db)` (spec 05)
Alcance: goroutine por run, eventos, throttling, `KILL QUERY`, LIMIT automático, historial hook (interfaz). DoD: criterios 2–4 y 6 del spec 06 en integración; `-race`; `goleak`.
**T4.3 ✅ · Stores `queryTabs` y `results` + listeners de eventos** — Dep: T4.2, T0.5 · Specs: 09
**T4.4 ✅ · SqlEditor (Monaco) + pestañas + atajos** — Dep: T4.3, T3.3 · Specs: 06, 08 · Incluye: doble clic en tabla del árbol abre pestaña con `selectStatement` y la ejecuta; `schema.loadCatalog` alimenta el autocompletado; el modal "Ver CREATE" pasa a Monaco de solo lectura · DoD: workers locales empaquetados, completado funcionando, restauración de pestañas.
**T4.5 ✅ · ResultGrid virtualizado** — Dep: T4.3 · Specs: 06 · DoD: 1 M filas fluido (medición documentada), selección y copia, tipos especiales, sub-pestañas de resultados.
**T4.6 ✅ · Persistencia de pestañas y estado de UI** ∥ — Dep: T0.3, T4.3 · Specs: 06, 09
**T4.7 ✅ · Formateo SQL y `StatementAt`** ∥ — Dep: T1.2 · Specs: 06

## Épica 5 — Historial y exportación
**T5.1 ✅ · HistoryService + panel** — Dep: T4.2 · Specs: 07
**T5.2 ✅ · ExportService CSV/JSON + diálogo** — Dep: T4.2 · Specs: 07 · DoD: 1 M filas con memoria acotada; cancelación.

## Épica 6 — Cierre
**T6.1 · Ajustes (SettingsService + diálogo)** ✅ — Dep: T0.3, T0.5 · límites (MaxRows, memoria), tema, idioma.
**T6.2 · E2E y rendimiento** ✅ — Dep: Épicas 2–5 · Specs: 11 · DoD: suite Playwright (mock API) + guion de smoke manual en app real; reporte de benchmarks.
**T6.3 · Empaquetado y CI multiplataforma** ✅ (parcial: ver notas) — Dep: T6.2 · DoD: artefactos macOS/Win/Linux en CI.
> Notas T6.3: `.github/workflows/ci.yml` compila en los 3 SO, sube artefactos (`.app` zip en macOS, `.exe` en Windows, `tar.gz` en Linux), y añade jobs `integration` (MySQL/MariaDB reales) y `security` (`govulncheck`, `npm audit`). **Solo el empaquetado macOS está verificado** (`wails3 package` → `bin/dbacus.app` de 19 MB, firma ad-hoc válida, sin avisos del enlazador). Los pasos de Windows/Linux y el workflow completo **no se han ejecutado** (el repo no tiene remoto). Sin instaladores (NSIS/AppImage/deb) ni firma/notarización: requieren certificados y herramientas que no hay aquí. Monaco ahora es un chunk aparte que carga al abrir una consulta (el chunk principal pasó de 3,8 MB a 458 kB).
**T6.4 · Auditoría de seguridad** ✅ — Dep: T6.2 · Specs: 10 · DoD: criterios del spec 10 verificados, `govulncheck`/`npm audit` limpios.
> Notas T6.4: informe en `docs/specs/10-seguridad.md` («Auditoría»). Hallazgos corregidos: modo solo lectura sin implementar, CSP ausente y 17 vulnerabilidades de la stdlib (toolchain fijado a go1.26.10). Pendiente sin solución upstream: `GO-2026-5932` (x/crypto, no alcanzable).

## Épica 7 — Edición de celdas (etapa 2; spec 13 aprobado)
**T7.1 ✅ · Elegibilidad** — Dep: Épicas 4–5 · Specs: 13, 05 · Alcance: `sqlsplit.SimpleSelectTable`, ampliar `Column` con `IsGenerated` y el esquema con índices únicos sin NULL, `EditService.GetEditInfo`. Fuera de alcance: cualquier escritura. DoD: tabla de casos (JOIN, alias, vista, sin PK, clave compuesta, clave no seleccionada).
**T7.2 ✅ · Driver: ejecución parametrizada** — Dep: T7.1 · Specs: 03, 13 · Alcance: `Conn.Exec(ctx, stmt, args...)` y transacción (`Begin/Commit/Rollback`) en `driver.Conn`, implementación MySQL y fake. DoD: tests unitarios + integración en 3 motores.
**T7.3 ✅ · Cambios pendientes y conversión** — Dep: T7.1 · Specs: 13, 02 · Alcance: `Stage/Unstage/Discard/Preview`, conversión por tipo, superposición en `GetRows` (`Cell.Edited`), límite de 1 000. DoD: tests por tipo, sin valores en logs/eventos.
**T7.4 ✅ · Aplicar** — Dep: T7.2, T7.3 · Specs: 13, 10 · Alcance: `Apply` atómico con control optimista, relectura y reemplazo en el buffer, historial, rechazo en solo lectura, evento `edit:applied`. DoD: integración con conflicto, rollback, clave compuesta y valores extremos en MySQL 8.0/8.4 y MariaDB 10.11.
**T7.5 ✅ · UI** — Dep: T7.3, T7.4 · Specs: 13, 08, 09 · Alcance: editor en celda, barra de cambios, diálogo de SQL, menú contextual, store `edits`, confirmación al descartar. DoD: tests de componente.
**T7.6 ✅ · E2E y cierre** — Dep: T7.5 · Specs: 11 · Alcance: mock del servicio, E2E, guion de smoke manual, actualizar specs 02/05/06/07/09.
> Quedan **fuera** de la épica 7: insertar/borrar filas, BLOB, vistas, JOIN y DDL (épica 8 propuesta).

## Épica 8 — Insertar y borrar filas (etapa 2; spec 14 aprobado)
**T8.1 ✅ · Modelo de cambios ampliado** — Dep: Épica 7 · Specs: 14, 13 · Alcance: tipos «fila nueva» y «marca de borrado» en el buffer y en `EditService` (`AddRow`, `DuplicateRows`, `StageNew`, `DiscardNew`, `MarkDelete`, `UnmarkDelete`), `Info.CanInsert/InsertReason`, superposición en `GetRows` (`RowWindow.Deleted`, `NewFrom`, total con filas nuevas). DoD: tests unitarios del modelo y de la elegibilidad de inserción.
**T8.2 ✅ · Generación de INSERT y DELETE** — Dep: T8.1 · Specs: 14 · Alcance: `edit.BuildInserts/BuildDeletes` parametrizados (con `<=>` en el DELETE), orden UPDATE→DELETE→INSERT, revisión con valores. DoD: tabla de casos.
**T8.3 ✅ · Aplicar el lote mixto** — Dep: T8.2 · Specs: 14, 13 · Alcance: `Apply` con inserts y deletes, conflicto de borrado, relectura por clave/`LAST_INSERT_ID()`, filas borradas fuera del buffer, historial. DoD: integración en MySQL 8.0/8.4 y MariaDB 10.11 (autoincrement, triggers, FK, clave duplicada, atomicidad).
**T8.4 ✅ · UI** — Dep: T8.3 · Specs: 14, 08, 09 · Alcance: botón «Nueva fila» y «Insertar nueva fila a partir de esta» en el menú contextual (una o varias filas), fila nueva en el grid, `Supr` y menú para eliminar/restaurar, barra de cambios ampliada, aviso de FKs en cascada. DoD: tests de componente.
**T8.5 ✅ · E2E y cierre** — Dep: T8.4 · Specs: 11 · Alcance: mock ampliado, E2E, smoke manual, actualizar specs 02/08/09/13.

## Épica 9 — Dbacus: renombrado, repositorio público y CI (spec 15)
**T9.1 ◐ · Auditoría previa a publicar** (hecha: secretos, historial, licencias y menciones internas; falta decidir el correo de los commits y añadir `THIRD_PARTY_NOTICES.md` al publicar) — Specs: 15, 10 · Alcance: escaneo de secretos y datos internos en árbol e historial, revisión de licencias de dependencias, generalizar menciones internas en docs, decisión sobre el correo de los commits. DoD: informe y árbol limpio.
**T9.2 ◐ · Renombrar a Dbacus con migración** (hecho y verificado con tests, smoke y E2E; falta la prueba manual con datos reales) — Dep: T9.1, dueño de GitHub · Specs: 15 · Alcance: nombre visible, binario, identificador, módulo Go y bindings, carpeta de datos, llavero y variable de entorno, `secrets.Migrate` y copia de `app.db`/`known_hosts`. DoD: tests de migración (idempotencia, llavero caído, copia corrupta), batería completa en verde y prueba manual con datos reales sin borrar los viejos.
**T9.3 ✅ · Estabilizar `TestProbeViaSSH` (C2)** — Alcance: encontrar la causa del fallo intermitente. DoD: 200 ejecuciones seguidas con `-race` y carga sin fallo.
**T9.4 ◐ · CI y release** (escritos y con `actionlint` limpio; falta que corran en GitHub: T9.5) — Dep: T9.3 · Specs: 15 · Alcance: reestructurar `ci.yml` (check, integration, security, build), `release.yml`, caché, permisos mínimos, `dependabot.yml`. DoD: `actionlint` limpio.
**T9.5 · Primer push privado y bucle de CI** — Dep: T9.2, T9.4 · Alcance: repositorio privado, primer push, corregir lo que falle en Windows y Linux. DoD: verde en los tres sistemas.
**T9.6 · Publicar** — Dep: T9.5 · Alcance: `LICENSE`, `README`, `SECURITY.md`, `CHANGELOG`, protección de `main`, hacerlo público, etiqueta `v0.1.0` y *release*. DoD: criterios de aceptación del spec 15.

## Grafo resumido
T0.1 → {T0.2, T0.3, T0.4, T0.5} → T1.x → T2.x (conexiones) → T3.x → T4.x → T5.x → T6.x
Se recomienda ejecutar primero el camino mínimo hasta una consulta real: T0.1–T0.5, T1.1, T1.3, T1.5, T2.1, T2.3–T2.6, T4.1–T4.5.
