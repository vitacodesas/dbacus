# 21 — Procedimientos y funciones almacenadas (etapa 2)

> Estado: **implementado** en T15.1–T15.6 (Épica 15). Verificado con tests unitarios (driver falso), integración en MySQL 8.0, 8.4 y MariaDB 10.11 (500 rutinas, propiedades, pre-validación, restauración, `GRANT`, solo lectura en el servidor), vitest y E2E con el backend simulado. **Falta el smoke manual en la app nativa** (`docs/smoke-manual.md`) y **el recorrido contra un servidor real a través de los bindings de Wails** (solo se probó Go contra el servidor y la UI contra el simulado). Decisiones del usuario (2026-11-02): editar con **copia y restauración automática**; v1 con **ver, editar, crear y borrar**; **ejecutar queda fuera**; el árbol agrupa **Procedimientos / Funciones** bajo cada base y debe tolerar **~500 rutinas por base**.

## Objetivo
Leer y modificar el código de las rutinas almacenadas desde el árbol de esquema, con la misma disciplina que el spec 19: Go genera y valida todo el SQL, la revisión es obligatoria, el modo solo lectura nunca escribe, y **editar una rutina no puede dejarla perdida**. MySQL/MariaDB no permiten cambiar el cuerpo con `ALTER`, así que editar es `DROP` + `CREATE` sin transacción: la mayor parte de este spec es hacer eso seguro.

## Alcance v1
- **Incluye**: listar procedimientos y funciones por base; ver el `CREATE` completo (Monaco, solo lectura); **editar**, **crear** y **borrar**; parámetros y metadatos visibles; revisión con SQL exacto; historial; bloqueo en solo lectura; control de cambios concurrentes.
- **Fuera de alcance**: **ejecutar** rutinas (solo «Copiar CALL» y abrir una pestaña de consulta con la plantilla; la ejecución sigue por `QueryService`); triggers y events; `ALTER PROCEDURE` (comentario/`SQL SECURITY`); preservar `GRANT` de la rutina; autocompletado con rutinas; depurador; formateo masivo; cualquier motor que no sea MySQL/MariaDB.

## Decisiones
| # | Decisión | Por defecto | Motivo |
|---|---|---|---|
| ⚑D1 | Escala | Una consulta de lista **sin cuerpos**; el cuerpo se pide al abrir una rutina. Expandir la base **no** carga rutinas: solo expandir su grupo | 500 cuerpos al abrir una base serían megas inútiles |
| ⚑D2 | Superficie | `BaseModal` grande con `CodeEditor` (Monaco): vista de solo lectura y botón «Editar». Mismo componente para «Ver CREATE» de tablas | No toca `TabState`; cierra la deuda de T4.4 (hoy es un `<pre>`) |
| ⚑D3 | Quién genera el SQL | **Solo Go**. Vue envía el texto que el usuario escribió y una operación tipada; la plantilla de «Nueva rutina» la da `Template(kind)` | Regla de oro 1 |
| ⚑D4 | Qué se edita | El `CREATE` **completo** en un solo texto. El nombre y el tipo no pueden cambiar (`name_changed`); renombrar = crear otra y borrar esta | `RENAME` no existe para rutinas |
| ⚑D5 | Confirmación | **Siempre** revisión con el SQL final; sin ajuste para omitirla (como spec 19 D5) | El DDL no se revierte |
| ⚑D6 | «Lo visto es lo ejecutado» | `Apply` recibe el `sqlHash` de la última `Preview`; si difiere → `VALIDATION/stale_preview` | Igual que spec 19 D6 |
| ⚑D7 | Atomicidad | **No hay transacción** (DDL implícito). Mitigación en tres capas (ver «Aplicar»): pre-validación con rutina temporal, restauración automática y copia entregada al usuario | Lo pidió el usuario |
| ⚑D8 | Solo lectura | **Ver está permitido**; crear/editar/borrar no. Tres capas como el spec 19 | Leer código no es SQL de escritura (`SHOW CREATE` pasa `IsReadOnlySafe`); el spec 19 no deja ni ver porque su modal es solo de edición |
| ⚑D9 | `DEFINER` | Se **conserva**. Si difiere de `CURRENT_USER()`, aviso en la revisión y opción «Recrear con mi usuario» (Go reescribe la cabecera) | Recrear con un definer ajeno puede fallar (1227/1449) |
| ⚑D10 | Propiedades que no se editan | `sql_mode` y `collation_connection` de la rutina **se reaplican** en la conexión de escritura (ver «Aplicar»). **No** se reaplica `character_set_client`: el texto viaja en UTF-8 y declararlo distinto lo corrompería | Si no, el cuerpo cambia de significado al recrearlo |
| ⚑D11 | Permisos | Sin privilegio para ver el cuerpo → `body_hidden` (estado, no error); la lista solo trae lo que el servidor deja ver | [Probable] MySQL ≥ 8.0.20 exige `SHOW_ROUTINE` |
| ⚑D12 | Un `Apply` por rutina | Mapa `applying` por `sesión\0base\0tipo\0nombre`; el segundo recibe `busy` | Patrón de `AlterService` |

## Modelo (Go)
- `Routine{Name, Kind "procedure"|"function", Definer, Comment, SecurityType, LastAltered}` — **lista**, sin cuerpo ni parámetros.
- `RoutineDetail{Routine, Params[]{Name, Mode "IN"|"OUT"|"INOUT", Type, Ordinal}, Returns string, CreateSQL, SQLMode, CharsetClient, CollationConnection, DatabaseCollation, BodyHidden bool, Fingerprint}`; `Fingerprint` = hash de `CreateSQL` + `SQLMode`.
- `RoutineChange{Database, Kind, Name, Op "create"|"replace"|"drop", SQL, Fingerprint, RecreateDefiner bool}`.
- `RoutinePreview{Statements, SQLHash, Warnings []{Code, Message, Severity}, Destructive, NoRollback}` · `RoutineResult{Applied []string, Failed *{Index, Message}, Restored bool, BackupSQL string, Detail *RoutineDetail}`.
- Los DTO van en `internal/model` y el cliente en `frontend/src/api/routines.ts`.

## Lectura y escala
- Interfaz **opcional** `RoutineReader` en `internal/driver` (patrón de `DDLGenerator`, `driver.go:95-110`): `Routines(ctx, db) → []Routine` y `DescribeRoutine(ctx, db, kind, name) → RoutineDetail`. Un driver que no la implemente no ofrece rutinas (`UNSUPPORTED`; el grupo no aparece). El fake la implementa.
- **Lista**: una consulta a `information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?` con `ROUTINE_NAME, ROUTINE_TYPE, DEFINER, SECURITY_TYPE, ROUTINE_COMMENT, LAST_ALTERED` y **sin** `ROUTINE_DEFINITION`. Orden en Go con `nameLess` (el `ORDER BY` depende de la collation del servidor, spec 05). Parámetro `?`, nunca interpolado.
- **Detalle**: `SHOW CREATE PROCEDURE|FUNCTION` con el identificador por `QuoteIdent` (devuelve además `sql_mode`, `character_set_client`, `collation_connection` y `Database Collation` [Verificado en MySQL 8.0/8.4 y MariaDB 10.11]) y `information_schema.PARAMETERS` para los parámetros. Cuerpo NULL ⇒ `BodyHidden`.
- **Caché**: clave `db:<db>:routines` en `schemacache` (cuelga de `db:<db>:`; se invalida con el prefijo, spec 05); el detalle **no** se cachea (se lee fresco al abrir, para el `Fingerprint`). TTL 5 min, singleflight y timeout de 10 s como el resto.
- **Árbol** (solo pinta): bajo cada base, además de Tablas y Vistas, grupos **Procedimientos** y **Funciones**. Al expandir el grupo se carga la lista (skeleton, error con «Reintentar»); el contador `(n)` aparece al cargar. Filas planas en `VirtualList` (26 px): 500 rutinas ⇒ ~30 nodos en el DOM. El filtro del árbol actúa sobre lo cargado (spec 05). El pie del grupo avisa de que se ven solo las rutinas con privilegios.
- `RowKind` gana `routineGroup`, `procedure`, `function`; `TreeCtx` y `loaderFor` se amplían; `handleInvalidated` reconoce la ruta del grupo (hoy su regex solo entiende `db:x[/t:y]`, `stores/schema.ts:121`).

## Aplicar (⚑D7)
Todo por `AcquireEdit` (conexión efímera, falla cerrado en solo lectura; **no** `AcquireConn`, ver «Hallazgos»). Cada paso es **una** sentencia (`multiStatements` está bloqueado, `config.go:26`; un `CREATE … BEGIN a; b; END` es una sola sentencia para el servidor y no necesita `DELIMITER`, que es una orden del cliente `mysql`).

**`replace`** (editar):
0. Releer el detalle y comparar `Fingerprint` (`stale_model` si cambió). Guardar `CreateSQL` y `SQLMode` originales en memoria como **copia**.
1. `SET SESSION sql_mode = …, collation_connection = …` con los valores guardados en la rutina (no el charset del cliente, ⚑D10). La conexión es efímera: no contamina otras. [Verificado] el `sql_mode` sobrevive a la edición en los tres motores (`TestRoutineReplacePreservesPropertiesIntegration`).
2. **Pre-validación**: `CREATE` del texto nuevo con el nombre cambiado a `__dbacus_tmp_<aleatorio>` (Go reescribe el nombre con los offsets de `sqlsplit.RoutineHead`) y `DROP` inmediato del temporal. Atrapa errores de sintaxis y de privilegio de `DEFINER` **antes** de tocar la original. Si falla: se informa y **no se ha cambiado nada**. [Verificado] un cuerpo con error de sintaxis falla en este paso en los tres motores y la original sigue intacta (`TestRoutineBadSyntaxLeavesOriginalIntegration`); el privilegio de `DEFINER` no se probó contra un servidor (solo con el driver falso). El nombre temporal es **determinista** (hash del SQL) para que `Preview` y `Apply` produzcan el mismo `sqlHash`. Limitación conocida: no detecta referencias a objetos inexistentes (MySQL las resuelve al ejecutar).
3. `DROP PROCEDURE|FUNCTION` de la original → `CREATE` del texto nuevo.
4. Si el `CREATE` final falla: **restauración automática** ejecutando la copia con su `sql_mode`. `Restored = true` si funcionó. Si la restauración también falla, `BackupSQL` llega a la UI con «Copiar» y queda en el historial.

**`create`**: `CREATE` directo (sin pre-validación: no hay original que proteger). **`drop`**: `DROP` tras una revisión que muestra el cuerpo original con «Copiar».

- Un fallo a mitad no es error de la llamada: llega en `Failed`; `Detail` es el estado real releído (con o sin éxito).
- Un temporal que quedara por un corte (cierre de la app entre 2 y 3) se reconoce por el prefijo `__dbacus_tmp_`; **no** se oculta ni se borra solo: la lista lo muestra y la revisión lo menciona. [Suposición] es un caso rarísimo que no justifica limpieza automática.
- `GRANT`: [Verificado en MySQL 8.0/8.4 y MariaDB 10.11, `TestRoutineGrantsAndDefinerIntegration`] `DROP` elimina los privilegios concedidos sobre esa rutina concreta. v1 **no** los preserva; la revisión lo avisa con texto fijo.
- **Avisos fijos de la revisión**: sin transacción y sin vuelta atrás (rojo); `DROP` quita los `GRANT` de la rutina; definer ajeno (⚑D9); rutina temporal que se creará y borrará (informativo).

## Validación (Go, `VALIDATION` con `Detail` = campo)
`sqlsplit.RoutineHead(sql) → {Kind, Name, Database, Definer, IfNotExists, NameStart, NameEnd, DefinerStart, DefinerEnd, ok}` interpreta solo la cabecera (saltando comentarios, `DEFINER = user@host | CURRENT_USER`, `IF NOT EXISTS`, nombres con backticks o `base.nombre`). Rechazos:
- no empieza por `CREATE … PROCEDURE|FUNCTION` → `head`; tipo o nombre distintos de los de la operación → `name_changed`; base calificada distinta de la actual → `database`.
- Líneas `DELIMITER` → `delimiter` (el servidor no las entiende). Tamaño > 1 MiB → `too_large`. Texto vacío → `empty`.
- Una segunda sentencia pegada tras `END` no se detecta en Go (el splitter no entiende `BEGIN…END`); la rechaza el servidor (sin multi-sentencia) y es un **error de sintaxis en la pre-validación**, antes de tocar nada.
- El nombre no puede empezar por `__dbacus_tmp_` (`name`) ni superar 64 caracteres. Las bases de sistema se rechazan con `system`.

## Contrato (a incorporar en spec 02 al implementar)
`RoutineService` (nuevo, `internal/services`):
- `List(sessionID, db) → []Routine` · `Get(sessionID, db, kind, name) → RoutineDetail` (`NOT_FOUND` si ya no existe) · `Template(kind) → string` · `CallStatement(sessionID, db, kind, name) → string` (el `CALL`, o `SELECT` para una función, con un `NULL` comentado por parámetro; lo arma Go).
- `Preview(sessionID, RoutineChange) → RoutinePreview` — no ejecuta nada; rechaza en solo lectura.
- `Apply(sessionID, RoutineChange, sqlHash) → RoutineResult`.
- Errores: `QUERY_FAILED` con `Detail` `readonly` o `busy`; `VALIDATION` con `Detail` `head|name_changed|name|database|system|kind|op|delimiter|too_large|empty|body_hidden|stale_preview|stale_model`; `NOT_FOUND`; `UNSUPPORTED`; `QUERY_CANCELED`.
- **Evento**: `schema:invalidated` gana `scope?: "routines"` (payload `{sessionId, database?, scope?}`): invalida solo `db:<db>:routines`, sin tirar las tablas. **Spec 02 se actualiza primero.** `afterStatement` (`query.go:540`) reconoce `CREATE|DROP PROCEDURE|FUNCTION` desde el editor de consultas con un `sqlsplit.RoutineDDL` y emite ese ámbito (hoy invalida toda la sesión).
- Historial: una entrada por sentencia aplicada realmente (no la rutina temporal); respeta `NoHistory`.

## UI (Vue, solo pinta)
- **Menú de rutina** (clic derecho, sin desplegar): «Ver código», «Editar» (deshabilitado en solo lectura, tooltip «Conexión de solo lectura»), «Copiar nombre», «Copiar CALL» (plantilla textual por Go), «Eliminar» (deshabilitado en solo lectura), «Refrescar». Menú del **grupo**: «Nueva…» (deshabilitado en solo lectura), «Refrescar». Doble clic = «Ver código».
- **`CodeEditor.vue`** (nuevo): Monaco con prop `readOnly`, modelo `inmemory://routine/<id>` que se destruye al cerrar; un solo editor vivo a la vez. No reutiliza `SqlEditor.vue` (acoplado a pestañas, sin `readOnly`).
- **Modal**: título `base.nombre` con chip del tipo; pestañas «Código» y «Parámetros». En «Editar»: «Revisar y aplicar» (deshabilitado sin cambios) y «Descartar». `body_hidden` muestra el motivo y sin editor.
- **Revisión** (`RoutineReviewDialog.vue`, modelo `AlterReviewDialog`): sentencias en `font-data`, avisos fijos, botón danger si destructivo, spinner con «Cancelar» (`KILL QUERY`, spec 03).
- **Resultado**: éxito ⇒ toast y recarga del modal; `Failed` ⇒ el modal queda abierto con qué se aplicó, si hubo `Restored`, y `BackupSQL` copiable.
- i18n es/en; mock en `src/mock/services.ts` (respeta `readOnly`, con una base de 500 rutinas para E2E).

## Seguridad y casos borde
- **Solo lectura, tres capas** (⚑D8): UI deshabilitada; guardia en Go con `sessionReadOnly` (`readonly.go:19`) en `Preview` y `Apply` (perfil guardado **o** sesión, releído en cada llamada); `AcquireEdit` ejecuta `SET SESSION TRANSACTION READ ONLY`. Un test fija que **ninguna sentencia generada** es `IsReadOnlySafe`. `List`/`Get` sí funcionan en solo lectura.
- **Logs** (regla 5): el cuerpo de una rutina puede traer literales; el servicio no registra SQL, solo código e índice de sentencia. El historial sí guarda el SQL (el usuario lo vio) y la copia de seguridad.
- **Cambios concurrentes**: `Fingerprint` comparado justo antes de ejecutar; mejor esfuerzo (carrera inevitable sin bloqueo).
- **Cancelación**: `Apply` recibe `context`. Cancelar entre el `DROP` y el `CREATE` activa la **restauración** (con contexto propio de 10 s); el resultado dice el estado real.
- **Rutinas de sistema**: las bases de sistema no ofrecen «Editar», «Nueva» ni «Eliminar» (solo ver).
- **Nombres** ≤ 64 caracteres y sin control; el escapado es de `QuoteIdent`.
- **Estimaciones no verificadas**: tiempo de listado con 500 y con miles de rutinas; comportamiento de `information_schema.ROUTINES` con el `SHOW_ROUTINE` de MySQL 8.0.20+; que MariaDB 10.11 devuelva las mismas columnas en `SHOW CREATE`.

## Hallazgos que corrige este spec
- `AlterService` usa `AcquireEdit`, no `AcquireConn` (specs 19:80 y 03:89 lo decían mal): corregido.
- «Ver CREATE» es un `<pre>`, no Monaco (spec 05:10 y T4.4 lo daban por hecho): lo resuelve T15.5.
- `afterStatement` invalida toda la sesión ante cualquier DDL: con este spec el ámbito se acota para rutinas.
- **No corregido aquí**: el splitter no entiende `BEGIN…END` sin `DELIMITER`, así que un `CREATE PROCEDURE` pegado en una pestaña de consulta sin `DELIMITER $$` se parte por los `;`. No afecta a este flujo (nunca pasa por el splitter) pero sí al editor de consultas; es una tarea aparte.

## Criterios de aceptación
- [x] Una base con 500 rutinas se lista en < 1 s y el árbol mantiene unas decenas de nodos en el DOM. **Medido**: 3–8 ms en MySQL 8.0/8.4 y MariaDB 10.11 (`TestRoutinesFiveHundredIntegration`); `SchemaTree.test.ts` («500 rutinas: solo se montan unas decenas de filas»); E2E «500 rutinas».
- [x] Expandir una base **no** consulta rutinas; solo expandir su grupo, y una sola lectura sirve a los dos grupos (`SchemaTree.test.ts` «expandir la base NO lee rutinas…», `schema.test.ts` «las rutinas se piden solo con loadRoutines», E2E «abrir la base no lista rutinas…»).
- [x] La lista no contiene cuerpos: `model.Routine` no tiene campo de cuerpo y la consulta no pide `ROUTINE_DEFINITION` (`TestRoutineListIsCachedAndHasNoBodies`, que además fija la caché).
- [x] Ver el código funciona en un perfil de solo lectura; crear/editar/borrar llamados directamente devuelven `QUERY_FAILED`/`readonly` sin enviar nada (`TestRoutineReadOnlyRejectsBeforeSending`, `TestRoutineReadOnlyAppliesToOpenSession`, `TestRoutineReadOnlyServerLayerIntegration` en los 3 motores; E2E «conexión de solo lectura»).
- [x] Todo SQL generado no es `IsReadOnlySafe` (`TestRoutineDDLIsNeverReadOnlySafe`).
- [x] Editar conserva `sql_mode`, `collation_connection`, `DEFINER`, comentario y `SQL SECURITY` (`TestRoutineReplacePreservesPropertiesIntegration`, 3 motores). **No** se comprobó `DETERMINISTIC` ni el charset del cliente.
- [x] Un cuerpo con error de sintaxis falla en la pre-validación y **la original queda intacta** (`TestRoutineBadSyntaxLeavesOriginalIntact` con el driver falso, `TestRoutineBadSyntaxLeavesOriginalIntegration` en los 3 motores).
- [x] Si el `CREATE` final falla, la original se restaura y `Restored = true` (`TestRoutineApplyRestoresOnFailure`); si la restauración falla, `BackupSQL` está en el resultado y en el historial (`TestRoutineRestoreFailureReturnsBackup`). En un servidor real solo se probó que `restore()` recrea la original idéntica tras un `DROP` (`TestRoutineRestoreIntegration`): **no se provocó un fallo real del `CREATE` final**, porque la pre-validación lo evita casi siempre.
- [x] `DEFINER` ajeno avisa y «Recrear con mi usuario» reescribe la cabecera (`TestRoutineForeignDefinerWarns`, `TestRoutineRecreateDefinerRewritesHeader`; con servidor real el aviso, `TestRoutineGrantsAndDefinerIntegration`). **No** se probó recrear con un definer sin privilegios contra un servidor.
- [x] `name_changed`, `name`, `database`, `system`, `delimiter`, `head`, `too_large`, `empty` y `op` se rechazan en Go (`TestRoutineValidation`; `sqlsplit.TestParseRoutineHead` y `FuzzParseRoutineHead`).
- [x] `stale_preview`, `stale_model` y `busy` (`TestRoutineStalePreviewAndModel`, `TestRoutineBusy`).
- [~] Sin cuerpo visible: estado `body_hidden`, no error. Cubierto en la UI (`routines.test.ts` «con el cuerpo oculto no se puede editar», E2E «una rutina sin privilegio…») y en `Preview` (`body_hidden`); **no hay test de Go del lector ni con un usuario real sin privilegio**, y en MySQL 8 la rutina puede ni aparecer en la lista (`information_schema.ROUTINES` filtra por privilegios).
- [x] Tras aplicar, solo el grupo de rutinas se refresca; las tablas cargadas no se recargan (`TestRoutineInvalidationScope`, `schema.test.ts` «scope routines… NO toca las tablas»); `CREATE/DROP PROCEDURE` desde el editor de consultas hace lo mismo (`TestRoutineDDLFromQueryEditorInvalidatesOnlyRoutines`, `sqlsplit.TestIsRoutineDDL`).
- [x] Cada sentencia aplicada queda en el historial (el `SET` y la rutina temporal no) y los logs no contienen SQL (`TestRoutineApplyReplace`, `TestRoutineCreateAndDrop`, `TestRoutineLogsNoSQL`).
- [x] «Ver CREATE» de tablas usa `CodeEditor` de solo lectura (`SchemaPanel.vue`). **Sin test propio**: solo lo cubren el typecheck y la suite E2E existente, que no abre ese modal.
- [ ] **Smoke manual en la app nativa** (flujo completo, base de 500 rutinas real, cancelación entre `DROP` y `CREATE`, usuario con privilegios parciales, rutina con `DEFINER` ajeno, Monaco en la ventana nativa y en `wails3 dev`): pendiente.

## Pruebas
- **Unitarias** (driver fake): lista, detalle, `body_hidden`, generación de pasos por operación, reescritura del nombre temporal y del `DEFINER`, restauración ante fallo inyectado en cada paso, guardia de solo lectura, hash.
- **`sqlsplit`**: `RoutineHead` (DEFINER en sus formas, backticks, `IF NOT EXISTS`, nombre con base, comentarios previos, texto hostil) + fuzz; `RoutineDDL`.
- **Integración** (`-tags integration`, `mysqlbox`; MySQL 8.0, 8.4, MariaDB 10.11): 500 rutinas, propiedades preservadas, fallo de sintaxis, fallo de `DEFINER`, `DROP` y `GRANT`, usuario con privilegios parciales, capa de servidor en solo lectura.
- **Vitest**: árbol (grupos lazy, filtro, `parseNodeId`), store `schema` (`handleInvalidated` con `scope`), store `routines` y `SchemaTree`. **No hay tests de componente de `RoutineModal`, `RoutineReviewDialog` ni `CodeEditor`**: los cubre el E2E. **E2E** (mock con 500 rutinas): ver, editar, fallo con restauración, solo lectura, crear y borrar.

## Evolución (no se implementa ahora)
Ejecutar con diálogo de parámetros, preservar `GRANT` (leer y reaplicar `SHOW GRANTS`), triggers y events con el mismo modal, autocompletado con rutinas, diferencias (diff) antes de aplicar, búsqueda de texto dentro de los cuerpos (servidor), `ALTER PROCEDURE` para comentario y `SQL SECURITY`.
