# 18 · Paginación por cursor (keyset)

Estado: **especificado, sin implementar** (Épica 12 del backlog). Decisiones del usuario (2026-10-29): claves y orden **solo numéricos** en esta primera versión; ORDER BY del usuario respetado con clave de desempate; botones `< >` también en consultas sin clave única demostrable (modo aproximado, con salvaguarda); tamaño de página editable (p. ej. 20). Enmienda el [ADR-002](adr/ADR-002-resultados-en-buffer-go.md); decisión en el [ADR-005](adr/ADR-005-paginacion-por-cursor.md).

## 1. Problema
Con el LIMIT automático (por defecto 1000) un resultado grande se corta y **no hay forma de ver el resto** salvo exportarlo; ni siquiera se sabe si había más filas (hoy solo existe `LimitApplied`, spec 06:31 y 07:31). Subir el límite no sirve: el buffer en memoria llega a `MaxRows` (100 000) y a 124 MiB por millón de filas (docs/benchmarks.md). El objetivo es **recorrer una tabla de cualquier tamaño hasta el final**, con memoria acotada y con un coste por página que **no crezca con la profundidad**: el rendimiento es el criterio central de la app.

## 2. Por qué cursor y no OFFSET
- `LIMIT n OFFSET k` obliga al servidor a leer y descartar `k` filas: el coste de cada página crece con la profundidad, y si los datos cambian entre páginas se saltan o repiten filas.
- Un **cursor por clave** (*keyset*) pide `WHERE clave > última ORDER BY clave LIMIT n`: con un índice sobre esas columnas el servidor salta directo al punto y lee solo `n` filas, así que el coste es ≈ el de la primera página [esto es conocimiento general de MySQL, **no está medido en este repositorio**: el criterio de aceptación de §9 exige medirlo].
- Mantiene lo decidido en el ADR-002 (OFFSET descartado) y lo concreta.

## 3. Modelo general
- **Filas por página** = el selector que hoy se llama «Límite» (`ui.autoLimit`): 20, 50, 100, 500, 1000 y **Sin límite**. «Sin límite» desactiva la paginación y se comporta como hoy (lee hasta `MaxRows`).
- Cada página es una **lectura pequeña e independiente** (≤ N filas + 1 de sondeo) que **sustituye el buffer de esa sentencia**. La memoria queda acotada a una página; se puede llegar a cualquier fila sin acumular las anteriores.
- La fila de sondeo (N+1) no se muestra: solo dice si hay página siguiente (`HasNext`) o anterior (`HasPrev`). Así «lo cargado no es todo» pasa a ser un dato real y no una suposición.
- `Execute` calcula el plan de paginación de cada sentencia `SELECT` y, si es paginable, **ejecuta ya la primera página** con el SQL reescrito (en lugar de inyectar `LIMIT`).

### Modos (`Paging.Mode`)
| Modo | Cuándo | Garantía |
|---|---|---|
| `keyset` | SELECT de una tabla con clave única y orden numérico (§4) | **Exacto**: no repite ni salta filas existentes |
| `approx` | Cualquier otro SELECT con una columna numérica no nula en el resultado (§5) | **Aproximado**: avisa y bloquea si detecta que saltaría filas |
| `none` | El resto (§6) | Como hoy: carga N filas y explica por qué no se puede paginar |

## 4. Modo `keyset` (exacto)
**Condiciones** (todas):
1. Una sola sentencia `SELECT` de **una tabla** (`sqlsplit.SplitSelect`): sin alias de tabla, JOIN, UNION, GROUP BY, HAVING, DISTINCT, INTO, FOR UPDATE/LOCK ni LIMIT/OFFSET propios; no vistas. *(Implementación: la lista puede llevar expresiones o alias —se reescribe tal cual—, siempre que las columnas del cursor aparezcan **sueltas y sin alias** o con `*`; es más amplio que `SimpleSelectTable`, pensado para la edición.)*
2. La tabla tiene **clave única**: la clave primaria o, si no, un índice único sin prefijo con todas sus columnas `NOT NULL` (misma regla que `edit.chooseKey`, que se reutiliza).
3. Las columnas de la clave **y** las del `ORDER BY` son **numéricas exactas**: `TINYINT…BIGINT` (con o sin `UNSIGNED`) y `DECIMAL`; todas `NOT NULL`. (`FLOAT`/`DOUBLE` quedan fuera: no son exactas.)
4. Esas columnas están **en el resultado**: se leen sus valores en la primera y la última fila para construir el cursor. Si faltan: motivo `key_not_selected`.
5. El `ORDER BY`, si existe, es una lista de columnas desnudas (se admite `tabla.col` y comillas invertidas) con `ASC`/`DESC`; no posiciones (`ORDER BY 2`), expresiones, funciones ni `COLLATE`.

**Orden efectivo** = el `ORDER BY` del usuario + las columnas de la clave que falten como desempate, para que el orden sea **total y único**. Sin `ORDER BY`: la clave, ascendente. Se admiten direcciones mixtas en el `ORDER BY` del usuario. **Las columnas añadidas van en la misma dirección que la última columna del usuario**: la dirección del desempate no cambia el resultado pero sí el rendimiento, porque un índice solo sirve un `ORDER BY` cuyas columnas van todas en la misma dirección (con `v DESC, id ASC` la primera página costaba ~530 ms con 5 M de filas; con `v DESC, id DESC`, ~1,8 ms; ver docs/benchmarks.md).

**Consulta de una página** (sin parámetros; los literales son números ya validados que vienen de las celdas, **nunca texto del usuario**):
```sql
SELECT <lista original> FROM <tabla original>
WHERE (<WHERE del usuario>
) AND (<seek>)
ORDER BY <orden efectivo>
LIMIT N+1
```
- El `\n` antes del `)` evita que un comentario `-- …` al final del `WHERE` del usuario se coma el paréntesis. Sin `WHERE` del usuario, solo se pone `<seek>`.
- `<seek>` para **siguiente**, con columnas `c1, c2, …, ck` y valores del borde `v1, …, vk`, es la forma expandida `(c1 ▷ v1) OR (c1 = v1 AND c2 ▷ v2) OR …`, donde `▷` es `>` si la columna es ascendente y `<` si es descendente. Se usa la forma expandida y no la comparación de tuplas `(c1,c2) > (v1,v2)` porque esta **no admite direcciones mixtas**.
- **Anterior**: se invierten direcciones y comparadores, se lee N+1 y se **invierte el orden de las filas en Go** antes de guardarlas.
- **Primera**: sin `<seek>`. **Última**: orden invertido, N filas, invertidas en Go; **no hace falta conocer el total**, así que llegar al final de una tabla de millones de filas cuesta una página.
- **Literales**: enteros como dígitos con signo; decimales con el texto exacto de la celda, validado contra `^-?\d+(\.\d+)?$`; `BIGINT UNSIGNED` por encima de 2⁶³ y todo lo que llega como `bigint` (cadena) se inserta tal cual, sin pasar nunca por `float`. Si un valor no valida, la página falla con `INTERNAL` en lugar de enviar SQL dudoso.

## 5. Modo `approx` (aproximado) — fase 2
Para lo que no es `keyset` pero tiene **al menos una columna numérica no nula** en el resultado (según `ColumnMeta.Nullable`), p. ej. un JOIN. Se pagina envolviendo la sentencia original:
```sql
SELECT * FROM (<sentencia original sin «;»>) AS _p
WHERE _p.<col> > <último valor>
ORDER BY _p.<col>
LIMIT N+1
```
Es una elección explícita del usuario y **tiene costes que la interfaz debe mostrar** (insignia «Paginación aproximada»):
- **El orden pasa a ser por esa columna**: el `ORDER BY` original de una tabla derivada no se respeta.
- **Cada página vuelve a ejecutar la consulta completa**: puede ser lenta; MySQL puede empujar el predicado hacia dentro de la tabla derivada, pero no está garantizado. No tiene la propiedad de coste constante del modo `keyset`.
- **Valores repetidos**: `columna > último` se salta las filas que empatan con el borde. **Salvaguarda**: como se lee N+1, si el valor de la fila N es igual al de la fila N+1 el avance omitiría filas (las omitidas son exactamente las que empatan y vienen justo detrás en el orden), así que **se bloquea «siguiente»** con el aviso `tie_at_edge`, y se sugiere cambiar la columna («Paginar por…») o usar «Sin límite»/exportar. Lo mismo hacia atrás. Esto evita la pérdida **silenciosa** en el borde; no garantiza consistencia si los datos cambian entre páginas.
- **No hay columna por defecto: la elige el usuario** («Paginar por…»). Un orden que cambia solo (el `ORDER BY` original deja de respetarse) y cada página que vuelve a ejecutar la consulta completa no se activan sin que se pida. La interfaz ofrece el selector solo cuando el resultado **llegó al límite** (si cabía entero no hay nada que paginar).
- **Columnas elegibles** (`PagingInfo.Candidates`): numéricas exactas (`TINYINT…BIGINT` con o sin `UNSIGNED`, `DECIMAL`) y no nulas según el servidor (en un `LEFT JOIN` las del lado nulable no lo son). Si dos columnas del resultado se llaman igual (`SELECT * FROM a JOIN b` con dos `id`), la tabla derivada falla en MySQL: motivo `duplicate_columns` y no se ofrece (hay que dar alias).
- **Cómo se elige**: `Page` con `Column` y `Direction` ignorada (siempre la primera página) empieza la paginación; con otra columna en una sentencia ya aproximada la reinicia. El nombre se valida contra `Candidates`: nunca llega a SQL texto del usuario. La sentencia original (`Source()`) no cambia: exportar vuelve a ejecutarla entera.
- **Regla de empate por dirección**: hacia delante la fila de sondeo viene tras la última (`TieNext = más && sondeo == última`); hacia atrás, delante de la primera (`TiePrev`). En la otra dirección no hay empate posible: la página se pidió con comparación estricta contra la anterior. `Page` rechaza el avance bloqueado con `VALIDATION` y detalle `tie_at_edge` y la interfaz deshabilita el botón. Garantía comprobada con un oráculo: o se recorre todo sin repetir ni saltar, o se bloquea y toda fila estrictamente anterior al valor del bloqueo ya se vio.
- Envoltura: `sqlsplit.Wrap` quita el `;` final y comprueba (palabras de nivel superior) `LIMIT` → `user_limit`; `INTO`/`FOR`/`LOCK` → `into_lock`; no `SELECT` o modificadores como `SQL_CALC_FOUND_ROWS` → `not_select`. Un `LIMIT` dentro de una subconsulta no cuenta. El texto va seguido de un salto de línea antes del `)`.
- No disponible (motivo `no_numeric_column`, etc.) si no hay columna numérica no nula, si la sentencia no empieza por `SELECT` (`WITH`/CTE no se puede envolver), si lleva `INTO`/`FOR`/`LOCK`, o si tiene `LIMIT` propio.

## 6. Modo `none`: motivos
`Paging.Reason` es un código estable; la UI muestra su texto en la barra del resultado junto a una sugerencia.
| Código | Significado | Sugerencia |
|---|---|---|
| `not_select` | no es un `SELECT` (SHOW, EXPLAIN, CALL, WITH…) | — |
| `not_simple` | `SELECT` con modificadores (`SQL_CALC_FOUND_ROWS`…), sin `FROM`, con alias de tabla o con una forma que el analizador no reconoce | simplifica la consulta |
| `user_limit` | la consulta ya trae `LIMIT`/`OFFSET` | quítalo para paginar |
| `multi_table` | JOIN o subconsulta en `FROM` | usa «Paginación aproximada» si hay columna numérica |
| `grouped` | GROUP BY / HAVING / DISTINCT / agregados | — |
| `compound` | UNION / INTERSECT / EXCEPT | — |
| `into_lock` | INTO / FOR UPDATE / LOCK | — |
| `view` | la tabla es una vista (sin clave) | — |
| `no_key` | la tabla no tiene clave única | añade una clave primaria |
| `key_type` | clave o `ORDER BY` no numéricos (texto, fecha, UUID…) | llegará en una versión posterior |
| `key_not_selected` | la clave o el orden no están en la lista de columnas | usa `*` o añádelas |
| `order_by_unsupported` | `ORDER BY` con expresión, posición o `COLLATE` | ordena por columnas desnudas |
| `nullable_order` | una columna del orden admite `NULL` | — |
| `no_numeric_column` | (solo modo aproximado) ninguna columna numérica no nula | — |
| `duplicate_columns` | (solo modo aproximado) dos columnas del resultado con el mismo nombre | pon alias distintos |

Matices del motivo: el analizador de una tabla se rinde en lo primero que no entiende, así que un JOIN con alias de tabla (`FROM a x JOIN b y`) sale como `not_simple`, no `multi_table`; y un `LIMIT` o `FOR UPDATE` posterior prevalece (`user_limit`, `into_lock`) porque es lo que el usuario puede cambiar. En modo aproximado `multi_table` pasa a `no_numeric_column`/`duplicate_columns` cuando eso explica por qué no se ofrece.

En `none` la sentencia se ejecuta como hoy: se inyecta `LIMIT N` si es un SELECT simple (`sqlsplit.InjectLimit`), `LimitApplied` se rellena y no hay botones `< >`.

## 7. Estado, contrato y flujo
**Estado en Go** (por sentencia, junto al `results.Buffer`): el plan (`PagePlan`: partes de la sentencia, orden efectivo, columnas del cursor, N, modo, columna aproximada), la sentencia **original** (la fuente del buffer, `Source()`, no cambia) y los valores de borde de la primera y última fila cargadas (texto exacto de las celdas). `Page` solo acepta un run terminado y no expirado; si el buffer se liberó devuelve `RESULT_EXPIRED` (la UI ofrece «Re-ejecutar», como hoy).

**Contrato** (spec 02, actualizado en el mismo cambio que este spec):
```go
type StatementResult struct { /* … */; Paging *PagingInfo } // nil si Kind=="exec"
type PagingInfo struct {
  Mode string; Reason string          // keyset|approx|none ; código de §6 si none
  PageSize int; HasNext, HasPrev bool
  Page int                            // 1-basada si se conoce; 0 tras «Última» o al volver desde ella
  KeyColumns []string                 // columnas del cursor, en orden
  Warning string                      // "" | tie_at_edge | no_index
  Estimated int64                     // ≈ filas (information_schema), solo keyset sin WHERE; 0 = desconocido
}
type PageRequest struct{ QueryID string; StmtIndex int; Direction string /* first|next|prev|last */; Column string /* solo approx */ }
```
Método nuevo: `QueryService.Page(req)` → `StatementResult`.
- **Sustituye el buffer** de esa sentencia dentro del mismo `queryId`; las demás sentencias del script no se tocan. Es síncrono hasta tener la página (N ≤ 1000), `Cancel(queryId)` la cancela (KILL QUERY) y emite `query:progress`; **no** emite `query:done`.
- **No se registra en el historial** (el historial guarda lo que escribió el usuario, no las páginas).
- Se rechaza con `busy` si la pestaña está ejecutando; respeta el modo solo lectura (es un `SELECT`).
- **Cambios de edición pendientes** (specs 13/14) se indexan por posición de fila del buffer: cambiar de página los descartaría, así que la UI pide confirmación **exactamente como al volver a ejecutar** y descarta antes de llamar a `Page`.
- **Edición**: `edit.Analyze` sigue viendo la sentencia **original** (`Source()`), nunca el SQL reescrito; una página de un SELECT editable sigue siendo editable.
- **Exportar**: el *rerun* usa la sentencia original sin límites, así que exporta **todo**; `ExportDialog` calcula «parcial» con `HasNext || HasPrev` además de `Truncated`.
- Frontend: la caché de ventanas se invalida al cambiar de página (la clave incluye un contador `pageSeq`); la altura del grid es `N × 24 px`, con N ≤ 1000: desaparece el problema de las alturas de millones de píxeles en el scroll.

## 8. Interfaz
- Pie del resultado: `⏮ ◀  Filas 1–20 · Página 3 ▶ ⏭` (`Página` solo si se conoce; con `Estimated`: «≈ 5,2 M filas»). `◀`/`▶` son los controles principales (los dos iconos pedidos); **`⏮`/`⏭` (primera/última) son un añadido propuesto aquí**, porque con solo `<` `>` llegar al final de una tabla grande exige recorrerla entera, y con cursor la última página cuesta una consulta. Si no se quieren, se quitan sin afectar al resto.
- El rango del pie es el de **la página entera** (`Filas 21–40`), no el que enseña el scroll del grid; tras «Última» el número de página no se conoce y el rango es relativo a la página.
- Botones deshabilitados según `HasPrev`/`HasNext`; con `tie_at_edge` el avance bloqueado muestra el aviso.
- Insignias: «Paginación aproximada» (modo `approx`, con su advertencia), «Sin índice para este orden» (`no_index`), y en `none` el motivo.
- El selector se renombra «Filas por página» (20/50/100/500/1000/Sin límite). Sigue siendo estado de UI (no se persiste en `Settings`).
- Accesibilidad: botones con `aria-label`, estado `disabled` real, texto del rango en una región `aria-live="polite"`. Sin atajos de teclado en esta versión (Alt+←/→ choca con el historial del webview).
- Mientras llega una página, los botones se deshabilitan y el pie muestra «Cargando página…»; el botón «Detener» de la barra la cancela (`Cancel(queryId)`).
- `ExportDialog` recibe `paged` (HasNext o HasPrev) y propone repetir la consulta completa con el texto «Solo está cargada una página…».
- El backend simulado de los E2E pagina solo si el seed trae `paging: true` (los E2E anteriores dan por hecho el LIMIT automático); pagina `SELECT * FROM <tabla con clave>` y marca los JOIN como `multi_table`.
- i18n es/en con las mismas claves (exige `i18n.test.ts`).

## 9. Rendimiento (criterio central)
- **Prueba de integración** `TestKeysetPagingPerfIntegration` (MySQL 8.0, 8.4 y MariaDB 10.11) con una tabla de **5 M filas** (PK `BIGINT` autoincremental y una columna numérica con índice; 1 M en CI): mide la página 1, una al 50 %, una al 99 % y «Última» con keyset, y las compara con `LIMIT N OFFSET k` en la misma tabla. **Implementada y cumplida** (cifras en docs/benchmarks.md).
- **Criterio**: el tiempo de una página profunda con keyset ≤ **3×** el de la primera (coste casi constante); OFFSET profundo debe resultar claramente peor (es la prueba de que el diseño importa). **Medido (5 M de filas, páginas de 1000)**: cursor 0,7–1,0 ms por clave primaria y 1,2–4,7 ms por una columna indexada, a cualquier profundidad; OFFSET 150–480 ms por clave primaria y 1,1–1,6 s por la columna indexada (×205 a ×970 más lento). «Última»: ~1 ms. Alcance y límites de la medición en docs/benchmarks.md.
- Línea base actual (docs/benchmarks.md, mysql:8.0): primera ventana de 200 filas 33 ms; 1 M filas completas 376 ms con 124 MiB de buffer. Con paginación la memoria queda acotada a la página (≤ 1000 filas).
- **Sin índice**: si el orden efectivo no está cubierto por un índice, cada página ordena y es tan lenta como OFFSET. Se detecta con `EXPLAIN` sobre la primera página (`Using filesort`) → `Warning: no_index` (informativo, no bloquea; en MariaDB el texto de `Extra` es el mismo). [Mejor esfuerzo: si `EXPLAIN` falla, no hay aviso.]
- La reescritura y los tests de generación de SQL son lógica pura de Go y se miden con micro-benchmarks (como `sqlsplit`).

## 10. Consistencia
Cada página es una lectura independiente: **no hay instantánea**. Con `keyset` no se repiten ni se saltan filas que existen a ambos lados mientras otras se insertan o borran *detrás* del cursor; sí pueden aparecer filas nuevas por delante y desaparecer las borradas. No se abre una transacción larga para fijar una instantánea (bloquearía purgas y sería peor). En `approx` vale lo mismo y además la regla de empate del §5.

## 11. Casos borde
- `next` devuelve 0 filas (se borraron): se queda en la página actual y avisa «No hay más filas».
- Exactamente N filas en total: `HasNext=false` (la fila de sondeo no llegó).
- Resultado vacío: sin botones. Varias sentencias en el script: cada una tiene su `Paging`; los botones actúan sobre la sentencia seleccionada y `Page` solo sustituye su buffer.
- Cambio de esquema o de datos entre páginas: error normal de la consulta. Pérdida de conexión: se reintenta como en `Execute` (spec 06) y, si no, se vuelve a la primera página.
- Valores de borde de columnas `Trunc` (celda truncada) no pueden darse: las claves son numéricas.
- `ORDER BY` del usuario sobre una columna que **no** está en la clave: la clave se añade al final; si aun así la combinación no es única o alguna columna admite `NULL`, es `none`.
- `DECIMAL` con escala: se compara con el texto exacto de la celda (sin formateo de la interfaz).

## 12. Criterios de aceptación
- [x] Un SELECT de una tabla con PK numérica y 1 M de filas se recorre de la primera a la última página con `<`/`>`, sin repetir ni saltar ninguna fila (test contra un oráculo en memoria con datos ordenados y con direcciones mixtas).
- [x] «Última» muestra las últimas N filas y `<` retrocede sin saltos, con el mismo oráculo.
- [x] Con `ORDER BY precio DESC` (columna numérica no nula) la paginación respeta ese orden y la clave desempata (valores repetidos incluidos).
- [x] Una página profunda con keyset tarda ≤ 3× la primera en la tabla de 5 M filas (§9); OFFSET profundo es mucho peor y queda registrado.
- [x] Un resultado con `LIMIT` propio, JOIN, GROUP BY, UNION o sin PK no usa `keyset` y muestra el motivo correcto de §6.
- [x] Modo aproximado: un JOIN con columna numérica pagina; con valores repetidos en el borde se bloquea «siguiente» con aviso y **ninguna fila se omite en silencio**.
- [x] Las páginas no aparecen en el historial; cambiar de página con ediciones pendientes pide confirmación; editar una celda de la página sigue funcionando.
- [x] «Exportar» tras paginar exporta la tabla completa (rerun), no solo la página.
- [x] Memoria acotada a la página; el grid no usa alturas de millones de píxeles.
- [x] `es`/`en` con las mismas claves; E2E en verde con el Chromium del CI tres veces seguidas.

Verificado el 2026-10-29: tests de Go con SQLite y con MySQL 8.0/8.4 y MariaDB 10.11 reales (recorridos completos ida y vuelta, empates, ids hasta 2⁶⁴-1, DECIMAL(30,10)); 5 M de filas medidas; E2E (57) tres veces seguidas con el Chromium de Playwright; y prueba manual en el servidor Go real (`build:server`) contra MySQL 8.0 con 10 000 filas: páginas por clave con `ORDER BY price DESC`, «Última», JOIN con «Paginar por…» y bloqueo por empate. **No** se ha probado en la ventana nativa de Wails (el servidor usa el mismo código de Go y el mismo front, pero no el WebView).

## 13. Fuera de alcance
Total exacto (`COUNT(*)`); saltar a la página N; claves o `ORDER BY` de texto, UUID, fecha y `FLOAT` (versión posterior: collation, espacios finales, zona horaria de `TIMESTAMP`); ordenar haciendo clic en la cabecera (reescribiría el `ORDER BY`); `ORDER BY` por expresiones; paginación exacta de JOIN; instantánea consistente; atajos de teclado; persistir las filas por página.
