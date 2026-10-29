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

## Épica 10 — Grupos de conexiones y firma de la organización (spec 17)
**T10.1 ✅ · Firma de la organización** — Specs: 17, 08 · Alcance: texto «Hecho con amor por vitacodesas» (es/en) en la esquina inferior izquierda de `StatusBar`, solo texto, con `shrink-0` y el nombre de la conexión truncable; `StatusBar.test.ts`. Fuera de alcance: enlace externo. DoD: visible en tema claro/oscuro, con la barra lateral colapsada y a 960 px; `i18n.test.ts` en verde.
**T10.2 ✅ · Grupos: modelo y persistencia** — Dep: ninguna · Specs: 17, 02, 01 · Alcance: `model.ConnectionGroup`, `ConnectionProfile.GroupID`, migración `002_connection_groups.sql`, `store/groups.go` (`ListGroups`, `GetGroup`, `SaveGroup`, `DeleteGroup` en una transacción que deja los perfiles en «Sin grupo»). DoD: tests de round-trip, nombre duplicado sin distinguir mayúsculas, borrado transaccional y `TestMigrationsIdempotent` con 002; una `app.db` anterior abre con sus perfiles intactos.
**T10.3 ✅ · Grupos: servicio y contrato** — Dep: T10.2 · Specs: 17, 02 · Alcance: `ConnectionService.ListGroups/SaveGroup/DeleteGroup`, validación (nombre, color, `groupId` existente en `Save`), `Clone` conserva el grupo, bindings regenerados. DoD: tests de servicio (incluye que borrar un grupo no toca secretos ni historial).
**T10.4 ✅ · Grupos: interfaz** — Dep: T10.3 · Specs: 17, 04, 08, 09 · Alcance: `ColorSwatches` extraído del modal, campo «Grupo» con «＋ Nuevo grupo…», lista con secciones colapsables y menú de cabecera (editar/eliminar), `colorOf` en el store y su uso en tarjeta, árbol de esquema y barra de consulta, i18n es/en, mock. Fuera de alcance: configuración global por grupo, reordenar o anidar. DoD: vitest de store, formulario, lista y `ColorSwatches`.
**T10.5 ◐ · Grupos: E2E y cierre** (E2E y specs hechos; falta la comprobación manual en la app real) — Dep: T10.4 · Specs: 11 · Alcance: `e2e/groups.spec.ts`, comprobación manual en la app real (incluida la migración de una base anterior), actualizar specs 02/04/08/09. DoD: E2E 3 veces seguidas en verde con el Chromium del CI.

## Épica 11 — Distribución y actualizaciones (spec 16; solo especificada)
**T11.1 · Versión en la app** — Specs: 16, 02 · Alcance: `internal/version` inyectada con `-ldflags -X` desde `info.version` de `build/config.yml` en los tres Taskfile; expuesta a Vue y visible en la app. DoD: la versión mostrada coincide con la etiqueta.
**T11.2 · Firma y notarización de macOS en CI** — Dep: T11.1, cuenta de Apple Developer · Specs: 16 · Alcance: secretos, llavero temporal, `notarytool store-credentials`, `darwin:sign:notarize` con *entitlements* y `--timestamp`, universal, `.dmg` y `.zip` grapados. DoD: `spctl`, `stapler validate` y `codesign --verify` pasan en CI y el `.dmg` abre sin avisos en un Mac limpio.
**T11.3 · Artefactos y firma del actualizador** — Dep: T11.1 · Specs: 16 · Alcance: nombres `dbacus_<versión>_<os>_<arch>.<ext>` que reconoce el selector del actualizador, `wails3 updater genkey`, firma ed25519 de cada artefacto en `release.yml` con `UPDATER_PRIVATE_KEY`, `SHA256SUMS` y firmas en la *release*. DoD: `wails3 updater verify` pasa sobre una *release* de prueba.
**T11.4 · Integrar el actualizador** — Dep: T11.1, T11.3 · Specs: 16, 02, 08 · Alcance: `app.Updater.Init` con proveedor `github`, `PublicKey`, ventana integrada, ajuste «Buscar actualizaciones» y botón «Buscar ahora», mock y tests. DoD: el contrato está en el spec 02 y los tests cubren el ajuste.
**T11.5 · Instaladores y spike de actualización de extremo a extremo** — Dep: T11.2, T11.4 · Specs: 16 · Alcance: NSIS por usuario, AppImage y `.dmg` en CI; en el repositorio privado, publicar `v0.1.0`, instalarla, publicar `v0.1.1` y comprobar la actualización en los tres sistemas, firma inválida y falta de permisos. DoD: criterios de aceptación del spec 16; lo no verificado queda anotado.

## Épica 12 — Paginación por cursor (spec 18, ADR-005; solo especificada)
**T12.1 ✅ · Analizador de la sentencia** — Specs: 18 · Alcance: ampliar `sqlsplit` para extraer, de un `SELECT` de una tabla, los tramos de lista de columnas, tabla, `WHERE` y `ORDER BY` (columnas desnudas con `ASC`/`DESC`) y detectar lo que lo descalifica (JOIN, GROUP BY, HAVING, DISTINCT, UNION, LIMIT/OFFSET, INTO, FOR/LOCK, expresiones, posiciones, `COLLATE`). Fuera de alcance: reescribir SQL. DoD: tests de tabla con comentarios `--` y `/* */` en el `WHERE`, cadenas y comillas invertidas con palabras clave dentro, y mayúsculas/minúsculas.
**T12.2 ✅ · Plan y generación del SQL por cursor** — Dep: T12.1 · Specs: 18 · Alcance: paquete nuevo (p. ej. `internal/paging`) con `Plan` y `Build(first|next|prev|last)`: orden efectivo (usuario + clave de desempate), predicado expandido con direcciones mixtas, `LIMIT n+1`, literales numéricos validados (`BIGINT UNSIGNED`, `DECIMAL`), salto de línea antes del `)` del `WHERE`. DoD: tests contra un **oráculo en memoria** (recorrer ida y vuelta un conjunto ordenado sin repetir ni saltar) con direcciones mixtas, empates y extremos de tipo; micro-benchmark.
**T12.3 ✅ · Elegibilidad y motivos** — Dep: T12.1 · Specs: 18, 13 · Alcance: decidir `keyset`/`approx`/`none` reutilizando `edit.chooseKey` y `SchemaService.DescribeTable`; tipos numéricos exactos y columnas no nulas; los códigos de motivo del spec 18 §6. DoD: un test por cada código de motivo.
**T12.4 ✅ · Servicio: `Execute` con primera página y `Page`** — Dep: T12.2, T12.3 · Specs: 18, 02, 06 · Alcance: `Execute` ejecuta la primera página del plan; `QueryService.Page` sustituye el buffer de la sentencia, lee N+1, rellena `PagingInfo` (`HasNext/HasPrev/Page/KeyColumns`), no registra historial, es cancelable, respeta solo lectura y `busy`; valores de borde desde las celdas; `EXPLAIN` → `no_index`; estimación de filas. DoD: tests con el driver falso y bindings regenerados.
**T12.5 ✅ · Integración y rendimiento** — Dep: T12.4 · Specs: 18 §9, 11 · Alcance: `TestKeysetPagingPerfIntegration` con 5 M de filas y comparación con OFFSET, y el recorrido completo contra el oráculo en MySQL 8.0/8.4 y MariaDB 10.11; registrar las cifras en `docs/benchmarks.md`. DoD: página profunda ≤ 3× la primera y OFFSET claramente peor, o el spec se corrige con lo medido.
**T12.6 · Interfaz ✅** — Dep: T12.4 · Specs: 18, 08, 09 · Alcance: selector «Filas por página» (20/50/100/500/1000/Sin límite), pie `⏮ ◀ ▶ ⏭` con rango y página, insignias de modo y de motivo, estados deshabilitados, `pageSeq` en la caché de ventanas, confirmación con ediciones pendientes (como volver a ejecutar), `ExportDialog` con «parcial» por `HasNext/HasPrev`, i18n es/en, mock. DoD: vitest de store y componentes. **Hecha**: `results.page` (con `pageSeq` también como guarda de ventanas en vuelo y `cancel` de una página en curso), pie y selector, `ExportDialog` con la propiedad `paged`, mock opt-in (`seed.paging`) y `e2e/paging.spec.ts`.
**T12.7 · Modo aproximado (fase 2)** — Dep: T12.4, T12.6 · Specs: 18 §5 · Alcance: envoltura `SELECT * FROM (…) AS _p`, columna de paginación por defecto y menú «Paginar por…», regla de empate en el borde que bloquea el avance, insignia y advertencias. DoD: tests de que **ninguna fila se omite en silencio** y E2E del bloqueo.
**T12.8 · E2E y cierre** — Dep: T12.6, T12.7 · Specs: 11 · Alcance: `e2e/paging.spec.ts`, comprobación manual en la app real, actualizar specs 02/06/07/08/09/13. DoD: criterios de aceptación del spec 18 y E2E tres veces seguidas con el Chromium del CI.

## Grafo resumido
T0.1 → {T0.2, T0.3, T0.4, T0.5} → T1.x → T2.x (conexiones) → T3.x → T4.x → T5.x → T6.x
Se recomienda ejecutar primero el camino mínimo hasta una consulta real: T0.1–T0.5, T1.1, T1.3, T1.5, T2.1, T2.3–T2.6, T4.1–T4.5.
