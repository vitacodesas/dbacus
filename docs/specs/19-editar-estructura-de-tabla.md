# 19 — Editar estructura de tabla (etapa 2, borrador)

> Estado: **implementado** en T13.1–T13.7 (Épica 13). Verificado con tests unitarios, integración en MySQL 8.0/8.4 y MariaDB 10.11 y E2E con el backend simulado. **Falta el smoke manual en la app nativa** (`docs/smoke-manual.md`). Decisiones del usuario (2026-10-31): v1 con columnas, índices y claves foráneas; editor en un **modal grande** (no pestaña); el SQL se ve **en vivo** mientras se edita y se confirma **siempre** antes de ejecutar; un perfil de solo lectura **nunca** ejecuta un `ALTER`. Cierra el «DDL básico» que el spec 13 y el spec 00 dejaron para después.

## Objetivo
Cambiar la estructura de una tabla desde la UI con el mínimo de ruido: clic derecho en la tabla → **«Editar estructura»**, se editan filas en línea, un panel muestra el `ALTER TABLE` exacto y solo se ejecuta tras revisarlo. Va en la línea de DBeaver, pero con una sola superficie y sin diálogos anidados.

## Alcance v1
- **Incluye**
  - **Columnas**: añadir, renombrar, cambiar tipo, nulabilidad, valor por defecto, `AUTO_INCREMENT` y comentario, borrar y reordenar (`FIRST`/`AFTER`).
  - **Índices**: crear, borrar y renombrar (normal, único, `FULLTEXT`); varias columnas con orden.
  - **Claves foráneas**: crear y borrar, con `ON UPDATE`/`ON DELETE`.
  - SQL en vivo, revisión obligatoria, historial, bloqueo en solo lectura, control de cambios concurrentes.
- **Fuera de alcance**: cambiar la clave primaria, particiones, triggers, restricciones `CHECK`, columnas generadas (se muestran bloqueadas), crear/renombrar/borrar tablas, `TRUNCATE`, motor/charset/collation **de la tabla**, y cualquier motor que no sea MySQL/MariaDB.

## Decisiones
| # | Decisión | Por defecto propuesto | Motivo |
|---|---|---|---|
| ⚑D1 | Punto de entrada | Ítem «Editar estructura» en el menú contextual de **tablas** (no de vistas), entre «Ver CREATE TABLE» y «Refrescar» | Es lo que ya hace el usuario para «Ver CREATE TABLE»; cero UI nueva permanente |
| ⚑D2 | Superficie | `BaseModal` grande con tres pestañas: Columnas · Índices · Claves foráneas | No toca la persistencia de pestañas (`TabState`); se cierra y se vuelve al árbol |
| ⚑D3 | Quién genera el SQL | **Solo Go**. Vue envía un `ChangeSet` de operaciones tipadas y nunca arma SQL (regla de oro 1) | Evita inyección por nombres de tipo/identificador y mantiene un único generador por motor |
| ⚑D4 | Vista del SQL | Panel inferior de solo lectura (`font-data`) recalculado por Go con debounce de 250 ms tras cada cambio; si no hay cambios, vacío y «Aplicar» deshabilitado | El usuario siempre ve lo que se va a ejecutar |
| ⚑D5 | Confirmación | **Siempre** un diálogo de revisión con el SQL final al pulsar «Aplicar». **No** hay ajuste para saltarlo (distinto de `confirmEdits`, spec 13) | El DDL no se puede revertir |
| ⚑D6 | Garantía «lo visto es lo ejecutado» | `Apply` recibe el `sqlHash` de la última `Preview`; Go regenera el SQL y, si el hash difiere, rechaza con `VALIDATION`/`stale_preview` | Impide que la UI y el servidor diverjan |
| ⚑D7 | Atomicidad | **No hay transacción**: MySQL/MariaDB confirman el DDL implícitamente. Se genera **un solo** `ALTER TABLE … , …` por tabla siempre que el servidor lo admita; el resto se emite como sentencias ordenadas y la revisión avisa de que **no hay vuelta atrás** | `Begin/Rollback` del spec 13 no sirve aquí |
| ⚑D8 | Fallo a mitad (varias sentencias) | Se detiene en la primera que falle, se informa cuáles se aplicaron y cuál falló, y el modal se recarga con el estado real del servidor | El usuario no queda con un modelo falso |
| ⚑D9 | Operaciones destructivas | Aviso de **texto fijo** en la revisión (rojo) para `DROP COLUMN`, `DROP INDEX`, `DROP FOREIGN KEY`, y cambios de tipo que puedan truncar o convertir datos (reducir longitud, `VARCHAR`→numérico, quitar `NULL`) | Mismo patrón que el aviso de cascada del spec 14 |
| ⚑D10 | Tablas grandes | Si el servidor estima más de 1 000 000 filas (`information_schema.TABLES.TABLE_ROWS`), la revisión añade un aviso de que el `ALTER` puede tardar y bloquear escrituras. No se fuerza `ALGORITHM`/`LOCK` | Es una estimación, no una promesa; el servidor elige el algoritmo |
| ⚑D11 | Solo lectura | Tres capas (ver «Seguridad»); el ítem del menú sale **deshabilitado** con tooltip «Conexión de solo lectura» y el modal no se puede abrir ni siquiera para consultar | Lo pidió el usuario: nunca un `ALTER` en solo lectura |
| ⚑D12 | Propiedades no editables en v1 | Collation/charset de columna, `ON UPDATE CURRENT_TIMESTAMP`, expresión de columna generada y longitud de prefijo de índice **se preservan** en cualquier columna/índice que se modifique; se muestran como texto atenuado | Si no, un `MODIFY COLUMN` las borraría (ver «Metadatos») |

## Modelo (Go)
- `AlterModel` es la lectura fresca (sin `schemacache`) de la tabla: columnas, índices, claves foráneas, claves entrantes, `CreateSQL`, `TableCollation`, `RowEstimate` y `Fingerprint`: hash de `CreateSQL` **sin** el contador `AUTO_INCREMENT=N`, que cambia con cada `INSERT` y haría saltar `stale_model` sin tocar la estructura.
- `ChangeSet{Database, Table, Fingerprint, Ops[]}`; cada `AlterOp{Kind, Name, NewName, Column, First, After, Index, FK}` es una de:
  - Columnas: `addColumn`, `modifyColumn` (también renombra con `NewName` y reordena con `First`/`After`; `Column` nulo = conservar la definición actual), `dropColumn`. `ColumnSpec{Type, Nullable, Default, DefaultExpr, AutoInc, Comment}`.
  - Índices: `addIndex` (`IndexSpec{Kind index|unique|fulltext, Columns[]{Name, Desc, Prefix}}`), `dropIndex`, `renameIndex`.
  - Claves foráneas: `addFK` (`FKSpec`), `dropFK`.
- El `ChangeSet` vive en Vue como estado de UI efímero (Pinia). La verdad es el servidor: se recalcula todo en cada `Preview`.
- Validación en Go (`VALIDATION` con `Detail` = campo): nombres vacíos o duplicados, tipo desconocido, índice sin columnas, FK con columnas de distinto número, columna `AUTO_INCREMENT` sin índice, borrar una columna usada por un índice o una FK que no se borra en el mismo conjunto (el aviso nombra la dependencia).
- Las columnas generadas se muestran bloqueadas: no se modifican ni renombran (sí se pueden borrar). Una expresión de `DEFAULT` solo se acepta sin `;`, comentarios ni comillas/paréntesis sin cerrar.
- Orden de las operaciones al generar: `DROP FK` → `DROP INDEX` → columnas (`DROP`, `MODIFY`/`CHANGE`/`RENAME`, `ADD`, `MOVE`) → `ADD INDEX` → `ADD FK`. Modificar una FK o un índice (mismo nombre) se expresa como `Drop`+`Add`.

## Metadatos que faltan (hallazgo)
[Verificado en el código, `internal/model/schema.go`]: `Column` no guarda collation/charset por columna, `ON UPDATE`, expresión de columna generada (solo `IsGenerated`) ni la longitud del prefijo de índice (solo el booleano `Prefix`). Un índice funcional sale como `"(expresión)"`. Un `MODIFY COLUMN` generado solo con los campos actuales **perdería** esas propiedades (por ejemplo `updated_at … ON UPDATE CURRENT_TIMESTAMP`).
Decisión (T13.1): `model.Column` gana `DefaultExpr`, `Charset`, `Collation`, `OnUpdate` y `GenerationExpr`; `model.Index` gana `PrefixLens`, `Desc` y `Functional` (en vez de una expresión por columna: la columna `EXPRESSION` de `STATISTICS` no existe en MariaDB ni en MySQL 5.7); `TableDetail.Collation` y `AlterModel.TableCollation` guardan la collation de la tabla. Una collation de columna igual a la de la tabla **no se escribe** en el DDL (si no, regenerar una columna cambiaba el `CREATE TABLE` sin cambiar su significado; lo encontró la integración). Lo que Dbacus no sabe editar se **copia** al `MODIFY` (D12). Una columna o índice con una propiedad que no se pueda reproducir con certeza queda **bloqueado** con un motivo visible en vez de arriesgarse a perderla.

## Contrato (a incorporar en spec 02 al implementar)
`AlterService` (nuevo, en `internal/services`):
- `GetModel(sessionID, database, table) → AlterModel` — lectura sin caché; `NOT_FOUND` si la tabla ya no existe; `QUERY_FAILED/readonly` en perfiles de solo lectura.
- `Preview(sessionID, ChangeSet) → AlterPreview{Statements, SQLHash, Warnings []AlterWarning{Code, Message, Severity}, Destructive, NoRollback, EstimatedRows}` — no ejecuta nada; también rechaza en solo lectura.
- `Apply(sessionID, ChangeSet, SQLHash) → AlterResult{Applied []string, Failed *AlterFailure{Index, Message}, Model *AlterModel}` — ejecuta; un fallo de la base **no** es un error de la llamada: llega en `Failed`. `Model` es la lectura fresca tras el intento (con o sin éxito). Los errores de la llamada (solo lectura, `busy`, `stale_*`, validación) sí se devuelven como `AppError`.
- Errores: `QUERY_FAILED` con `Detail` `readonly` (perfil/sesión de solo lectura) o `busy` (ya hay un `Apply` sobre esa tabla); `VALIDATION` con `Detail` = campo, o `stale_preview` (hash distinto), o `stale_model` (el `Fingerprint` cambió: otra sesión alteró la tabla); `UNSUPPORTED` si la versión del servidor no soporta una operación; `QUERY_CANCELED` si se cancela.
- Reutiliza el evento `schema:invalidated` (se emite tras cualquier intento que aplicó al menos una sentencia). Historial: una entrada por sentencia aplicada.
Los DTO van en `internal/model` (spec 03:62) y el cliente en `frontend/src/api/alter.ts`.

## Generación del DDL (spec 03)
Interfaz **opcional** `DDLGenerator` en `internal/driver` (mismo patrón que `Validator`, `driver.go:96`): `Generate(ChangeSet, Model, ServerInfo) → []string, []Warning`. `ServerInfo` lleva la versión y el flag MariaDB (que hoy solo usa `schema.go`). Todo identificador pasa por `Driver.QuoteIdent` y todo literal por `QuoteString`; los parámetros `?` no sirven para identificadores ni tipos.
Diferencias [Verificado en MySQL 8.0/8.4 y MariaDB 10.11 salvo donde se indica]:
- Renombrar una columna usa siempre `CHANGE COLUMN` (vale en todas las versiones; `RENAME COLUMN` exige MySQL 8.0 / MariaDB 10.5.2). El `AFTER` de otra columna del mismo `ALTER` usa el nombre **nuevo** si fue renombrada.
- `RENAME INDEX`: MySQL ≥ 5.7 y MariaDB ≥ 10.5.2; en servidores anteriores, `UNSUPPORTED` (probado por versión con cadenas de versión, no contra 5.7 ni MariaDB 10.4 reales).
- Quitar restricciones: `DROP FOREIGN KEY` en ambos; `DROP CHECK` frente a `DROP CONSTRAINT` solo importa si más adelante se admiten `CHECK` (fuera de v1).
- `DROP` y `ADD` de una FK con el mismo nombre en un solo `ALTER` solo es seguro con `ALGORITHM=INPLACE`; con `COPY` MySQL lo rechaza. Se emiten como **dos** sentencias.
- Borrar una FK junto a un índice o una columna, o rehacer una FK con el mismo nombre, se emite en **dos** sentencias (primero las FK): `TestAlterApplyIntegration` pasa por esos casos en los tres motores. [No verificado] si MySQL aceptaría una sola sentencia en esos casos: se parte por precaución.
- Compatibilidad objetivo: MySQL 5.7 (sin `RENAME COLUMN`), 8.0, 8.4 y MariaDB 10.6+.

## UI (Vue, solo pinta)
- **Árbol**: `menuFor` de `SchemaTree.vue` añade `editStructure`; `SchemaPanel.vue` abre el modal (patrón de `viewCreate`). `disabled: true` si `profile.readOnly`.
- **Modal**: título `db.tabla`; pestañas `BaseTabs`. Cada pestaña es una tabla de filas editables en línea (tipo en `BaseSelect` con escritura libre validada por Go, `BaseCheckbox` para `NOT NULL`/`AUTO_INCREMENT`). «＋ Añadir» al pie de cada lista; arrastrar o `↑↓` para reordenar columnas.
- **Estados de fila**: chip `nuevo` (verde), `modificado` (ámbar), `borrado` (rojo, tachada). Cada fila cambiada tiene «Deshacer»; el pie tiene «Descartar todo». Las filas bloqueadas (D12) muestran un candado con el motivo.
- **Panel SQL en vivo** (D4): `<pre class="font-data">` bajo las pestañas, con las sentencias separadas por `;` y las advertencias debajo (`role="note"`). Botón «Copiar SQL».
- **Revisión** (D5): `AlterReviewDialog.vue` (modelo: `EditReviewDialog.vue`) con las sentencias, el aviso fijo de «sin vuelta atrás» (D7), los avisos destructivos en rojo (D9) y el de tabla grande (D10). Botones Cancelar y «Ejecutar» (`BaseButton` danger si `Destructive`). Errores en `role="alert"`.
- **Tras aplicar**: el modal se recarga con `Model`; si todo fue bien se cierra con un toast; si falló a mitad (D8) queda abierto mostrando qué se aplicó.
- **Solo lectura**: no hay modo «ver». Quien quiera ver la estructura usa «Ver CREATE TABLE».
- i18n es/en; mock en `frontend/src/mock/services.ts` (respeta `readOnly`, como hoy en `services.ts:448`).

## Seguridad y casos borde
- **Solo lectura, tres capas** (D11):
  1. UI: ítem deshabilitado. No es una barrera, solo evita el intento.
  2. **Guardia en Go**: `Preview` y `Apply` releen el **perfil guardado** en cada llamada y rechazan si el perfil **o** la sesión son de solo lectura (`QUERY_FAILED/readonly`). La lógica «perfil OR sesión» hoy está en `QueryService.Execute` (`query.go:218`) y en `EditService.load` (`edit.go:92`); se **extrae a una función compartida** para no copiarla una tercera vez.
  3. Servidor: la conexión sale de `Session.AcquireConn`/`AcquireEdit`, que ya ejecutan `SET SESSION TRANSACTION READ ONLY` y fallan cerrado (`pool.go:47-54`).
  - Comprobación adicional: cada sentencia generada pasa por `sqlsplit.IsReadOnlySafe` y **debe devolver `false`** (un `ALTER` nunca es de lectura); un test lo fija para que una futura sentencia de lectura mal clasificada no se cuele.
- Quitar el modo solo lectura exige reconectar (spec 10): una sesión abierta con el flag lo conserva en el servidor aunque el perfil cambie. Es el comportamiento actual y es el seguro.
- **Cambios concurrentes**: `Apply` compara el `Fingerprint` de `GetModel` con una lectura fresca justo antes de ejecutar; si cambió → `VALIDATION/stale_model`. Es **mejor esfuerzo**: entre la comprobación y el `ALTER` otra sesión puede cambiar la tabla (carrera inevitable sin bloquear).
- **Un `Apply` por tabla**: mapa `applying` (como `EditService`); el segundo recibe `busy`.
- **Cancelación**: `Apply` recibe `context`; cancelar emite `KILL QUERY` (spec 03; `TestAlterCancelKillsTheStatement`, solo con el driver falso: [No verificado] contra un servidor real con un `ALTER` largo). El estado exacto tras cancelar un `ALTER` de varias sentencias se relee y se muestra.
- **Bloqueos de metadatos**: un `ALTER` espera a transacciones abiertas sobre la tabla; el diálogo muestra el spinner y el botón «Cancelar». No se fija `lock_wait_timeout` propio en v1.
- **Logs**: no se registra SQL con literales (regla 5); un `DEFAULT 'texto'` es un literal, así que el log de errores usa solo el código y el índice de la sentencia. El historial **sí** guarda el SQL (el usuario lo vio; igual que en el spec 13) y respeta `NoHistory`.
- **Nombres**: validación de longitud (64) y caracteres de control en Go; el escapado lo hace `QuoteIdent`.
- **Vistas y tablas temporales**: no ofrecen «Editar estructura».
- **Tabla sin clave primaria**: se permite editar, con un aviso informativo.

## Criterios de aceptación
- [x] Clic derecho en una tabla muestra «Editar estructura» y **no despliega** sus columnas; en una vista no se ofrece (`alter.spec.ts`; `SchemaTree.test.ts` para el clic derecho y el Ctrl+clic).
- [x] En un perfil de solo lectura el ítem está deshabilitado y no abre nada (`alter.spec.ts`).
- [x] En solo lectura, `GetModel`, `Preview` y `Apply` llamados directamente devuelven `QUERY_FAILED`/`readonly` y **no envían nada** (`TestAlterReadOnlyRejectsBeforeSending`).
- [x] Un perfil marcado como solo lectura **después** de abrir la sesión bloquea el siguiente `Apply` (`TestAlterReadOnlyAppliesToOpenSession`).
- [x] Contra un servidor real, una conexión de solo lectura no ejecuta un `ALTER` aunque se salte el guardia de Go (`TestAlterReadOnlyServerLayerIntegration`, MySQL 8.0/8.4 y MariaDB 10.11).
- [x] Todo `ALTER` generado es no seguro para `IsReadOnlySafe` y es una sola sentencia (`TestGeneratedDDLIsNeverReadOnlySafe`).
- [x] El panel SQL muestra el mismo SQL que luego se ejecuta; `Apply` con un hash distinto falla con `stale_preview` (`TestAlterApplyRejectsStalePreview`, `alter.spec.ts`).
- [x] «Aplicar» **siempre** abre la revisión; no hay forma de omitirla (`alter.spec.ts`, `alter.test.ts`).
- [x] Añadir, modificar, renombrar, mover y borrar columnas, índices y FKs funciona en MySQL 8.0/8.4 y MariaDB 10.11 (`TestAlterApplyIntegration`).
- [x] Regenerar una columna sin tocarla no cambia la tabla: conserva collation, comentario, `DEFAULT` con expresión, `ON UPDATE` y enteros sin signo (`TestModifyColumnPreservesPropertiesIntegration`, `TestResolvePreservesPropertiesOnlyWhereTheTypeAllowsThem`).
- [x] Una tabla alterada por otra sesión entre `GetModel` y `Apply` da `stale_model`; un `INSERT` no (`TestAlterStaleModel`, `TestAlterFingerprintIgnoresAutoIncrement`).
- [x] Si falla una sentencia, el resultado dice cuáles se aplicaron y cuál falló, y el modelo devuelto es el real (`TestAlterPartialFailure`, `alter.spec.ts`).
- [x] Los avisos destructivos aparecen en la revisión (`TestWarnings`, `alter.spec.ts`). El aviso de tabla grande solo está cubierto por `TestWarnings`, con una estimación inventada.
- [x] Tras aplicar, el árbol de esa tabla se refresca sin acción manual y cada sentencia queda en el historial (`TestAlterApply`; el evento `schema:invalidated` ya lo consume el store).
- [x] Ningún log contiene el SQL con literales (`TestAlterLogsNoLiterals`: el servicio no registra nada).
- [ ] **Smoke manual en la app nativa** (flujo completo, tabla grande, cancelación de un `ALTER` real y conexión de solo lectura): pendiente.

## Pruebas
Unitarias: generación del DDL por operación y combinaciones (tabla de casos por versión de servidor y flag MariaDB, con `driver/fake`), validación del `ChangeSet`, avisos, guardia de solo lectura, hash de la vista previa. Integración (`-tags integration`, `mysqlbox`): matriz mysql:8.0, mysql:8.4, mariadb:10.11 — un test por familia de operación, preservación de propiedades, FK con índice de respaldo, fallo parcial y capa de servidor en solo lectura. Vitest: menú, store del modal (estados de fila, descartar), panel SQL con debounce, diálogo de revisión. E2E con el mock: editar, ver el SQL, confirmar, y perfil de solo lectura. Smoke manual: `ALTER` real, tabla grande, cancelación y conexión de solo lectura.
