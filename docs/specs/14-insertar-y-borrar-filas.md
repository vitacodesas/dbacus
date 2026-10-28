# 14 — Insertar y borrar filas (etapa 2, borrador)

> Estado: **borrador para aprobar**. Las decisiones ⚑ son propuestas con un valor por defecto. Continúa el spec 13 (edición de celdas): reutiliza su elegibilidad, sus cambios pendientes en Go, la revisión del SQL, la transacción y el control optimista. No hay código de esto todavía.

## Objetivo
Añadir filas nuevas y borrar filas desde el grid de un resultado editable, como cambios pendientes que se revisan y se aplican juntos (con las ediciones de celdas) en **una sola transacción**.

## Alcance v1
- **Incluye**: «Nueva fila» (se rellenan sus celdas en el grid y se aplica como `INSERT`), **«Insertar nueva fila a partir de esta»** (clic derecho sobre una o varias filas: crea filas nuevas ya rellenadas con los valores de las existentes, para cambiar solo lo que se quiera), «Eliminar filas» (selección de una o varias filas, se aplica como `DELETE`), revisión del SQL, atomicidad y detección de conflictos.
- **Fuera de alcance**: pegar varias filas desde el portapapeles, importar CSV, borrado en cascada asistido, `TRUNCATE`, deshacer tras aplicar.

## Decisiones
| # | Decisión | Por defecto propuesto | Motivo |
|---|---|---|---|
| ⚑D1 | ¿Cuándo se ofrece insertar? | Solo si el resultado es editable (spec 13) **y** toda columna `NOT NULL` sin valor por defecto (y no `AUTO_INCREMENT` ni generada) está en el resultado | Si falta una columna obligatoria, el `INSERT` fallaría siempre |
| ⚑D2 | Columnas que el usuario deja vacías en una fila nueva | Se omiten del `INSERT` (valen su `DEFAULT`); para escribir `NULL` o cadena vacía hay que indicarlo | Distinguir «sin valor», `NULL` y `''` evita sorpresas |
| ⚑D3 | Orden dentro de la transacción | `UPDATE` → `DELETE` → `INSERT` | Borrar antes de insertar permite reutilizar una clave única en el mismo lote |
| ⚑D4 | Condición del `DELETE` | Clave + valores originales de las columnas verificables (como el `UPDATE`), hasta 256 bytes por columna | Si otra sesión cambió la fila, no se borra algo que ya no es lo que se vio |
| ⚑D5 | Clave de una fila nueva | Si la clave es `AUTO_INCREMENT` se deja al servidor y se relee con `LAST_INSERT_ID()`; si no, es obligatoria en la fila nueva | Para poder releer la fila tras insertar |
| ⚑D6 | ¿Se piden confirmaciones aparte? | No: el diálogo de revisión del spec 13 (activado por defecto) muestra también `INSERT` y `DELETE`; los borrados se resaltan | Un solo punto de control |
| ⚑D7 | Tras aplicar | Las filas borradas **salen del buffer** (cambia el número de filas) y las nuevas se añaden al final con los valores releídos | El grid refleja el servidor sin re-ejecutar |
| ⚑D8 | ¿Qué se copia al «insertar nueva fila a partir de esta»? | Los valores **tal como se ven** (incluidas las ediciones pendientes de esa fila), incluido `NULL`. **No se copian**: columnas `AUTO_INCREMENT` (las asigna el servidor), generadas, binarias (no se pueden escribir desde aquí) y las columnas de una clave **no autoincremental**, que quedan vacías y marcadas como obligatorias para que no se duplique la clave | Copiar la clave tal cual falla siempre; lo demás es justo lo que el usuario quiere conservar |

## Modelo (Go)
- Un conjunto de cambios por `(queryId, stmtIndex)`, ya existente, pasa a tener tres tipos: **editar celda**, **fila nueva** (celdas parciales) y **marca de borrado** (por índice de fila del buffer).
- `GetRows` superpone: las filas nuevas aparecen **después de la última fila** del resultado (el total que informa `RowWindow.Total` las incluye) y las marcadas para borrar se señalan. DTO: `Cell.Edited` se mantiene; se añade `RowWindow.Deleted []int` (índices dentro de la ventana) y `RowWindow.NewFrom int` (primer índice de fila nueva, o -1).
- Una fila nueva y una marca de borrado sobre la misma fila no coexisten; borrar una fila nueva simplemente la descarta. Borrar una fila con celdas editadas descarta esas ediciones.
- Tope: `MaxPending` (1 000) cuenta celdas editadas + filas nuevas + borradas.

## Contrato (a incorporar en spec 02 al implementar)
`EditService` añade: `AddRow(queryId, stmt) → {Row int, Info}`, `DuplicateRows(queryId, stmt, rows []int) → {Rows []int, Info}` (crea una fila nueva por cada fila origen, con los valores de D8; máx. `MaxPending`), `StageNew(queryId, stmt, row, col, Value) → Info` (mismas conversiones y errores que `Stage`), `DiscardNew(queryId, stmt, row)`, `MarkDelete(queryId, stmt, rows []int) → Info`, `UnmarkDelete(queryId, stmt, rows []int) → Info`.
`edit.Info` añade `CanInsert bool`, `InsertReason string` (`missing_required` con las columnas), `PendingInserts`, `PendingDeletes`; `Pending` sigue siendo el total de cambios.
`ApplyResult` añade `Inserted`, `Deleted`; `Conflict.Reason` gana `"delete_changed"`.

## Aplicar
Igual que en el spec 13 (conexión de edición, modo estricto, una transacción, reversión total ante conflicto o error) con estas sentencias, en este orden:
1. `UPDATE …` por fila editada (spec 13).
2. ``DELETE FROM `db`.`t` WHERE `k` = ? AND `c1` <=> ? …`` por fila marcada. `affected = 0` → conflicto (`changed`/`missing`).
3. ``INSERT INTO `db`.`t` (`c1`, `c2`) VALUES (?, ?)`` por fila nueva, solo con las columnas rellenadas. Un error del servidor (clave duplicada, FK, `NOT NULL`) revierte todo y se muestra con el número de fila nueva.
Tras el commit: se releen las filas insertadas por su clave (o `LAST_INSERT_ID()`), se eliminan las borradas del buffer y se emite `edit:applied`.

## UI (Vue, solo pinta)
- Menú contextual de una o varias filas seleccionadas: **«Insertar nueva fila a partir de esta»** (o «…de estas N filas»). Las filas nuevas aparecen al final, se selecciona la primera y se abre el editor en su primera columna vacía obligatoria o, si no hay, en la primera columna editable.
- Botón «Nueva fila» en la barra del resultado; la fila aparece al final (fondo verde); `Tab`/`Enter` recorren sus columnas editables. Las columnas obligatorias vacías se marcan antes de aplicar.
- Menú contextual de fila(s) y tecla `Supr`/`⌫` sobre filas seleccionadas: «Eliminar fila(s)» → se muestran tachadas en rojo; «Restaurar» las desmarca.
- La barra de cambios dice «N cambios · M filas nuevas · K filas a borrar».
- Re-ejecutar o cerrar la pestaña con cualquiera de estos cambios pide confirmación (spec 13).

## Seguridad y casos borde
- Un perfil de solo lectura no ofrece nada de esto y `Apply` lo rechaza (ya cubierto).
- Borrar con FK: el servidor decide (`RESTRICT` falla y revierte todo; `CASCADE` borra también filas de otras tablas **sin que la app lo sepa**: el diálogo de revisión avisa en un texto fijo cuando la tabla tiene FKs entrantes con `CASCADE`).
- Resultados truncados por el límite: se pueden borrar/añadir filas del trozo cargado; el total mostrado no es el total real.
- Al copiar, un índice único distinto de la clave elegida (p. ej. un correo único) hará fallar el `INSERT` en el servidor si no se cambia: el error indica la fila y no se aplica nada. No se intenta adivinar cuál cambiar.
- Valores de las filas nuevas: las mismas conversiones que en el spec 13; `DEFAULT` solo cuando la celda está vacía **y** sin marcar como «cadena vacía/NULL».
- Un `INSERT` en una tabla con `AUTO_INCREMENT` consume el contador aunque se revierta: es comportamiento del servidor.

## Criterios de aceptación
- [ ] «Nueva fila» + rellenar + aplicar inserta una fila y el grid la muestra con los valores que dejó el servidor (autoincrement, `DEFAULT`, triggers).
- [ ] Clic derecho sobre una fila → «Insertar nueva fila a partir de esta» crea una fila nueva con los mismos valores (incluido `NULL`), salvo las columnas autoincrementales, generadas, binarias y las de una clave no autoincremental, que quedan vacías; se puede cambiar lo que se quiera y aplicar.
- [ ] Con varias filas seleccionadas se crean varias filas nuevas, en el mismo orden.
- [ ] Copiar una fila cuya clave es natural (no autoincremental) no se puede aplicar hasta rellenar la clave; el motivo se ve en la celda.
- [ ] Copiar una fila con un valor único repetido (p. ej. un correo) devuelve el error del servidor indicando la fila nueva, sin aplicar nada.
- [ ] Una tabla con una columna `NOT NULL` sin `DEFAULT` que no está en el `SELECT` no ofrece «Nueva fila» y dice cuál falta.
- [ ] Borrar una o varias filas y aplicar las elimina; el grid pierde esas filas y el total baja.
- [ ] Si otra sesión cambió la fila que se iba a borrar, `Apply` da conflicto y no se aplica **nada** del lote (incluidas las inserciones y ediciones).
- [ ] Borrar y reinsertar la misma clave única en un lote funciona (D3).
- [ ] Una clave duplicada o una FK incumplida revierte todo y el mensaje indica la fila.
- [ ] Las filas nuevas distinguen vacío (`DEFAULT`), `NULL` y cadena vacía.
- [ ] Re-ejecutar o cerrar con filas nuevas o borradas pide confirmación.
- [ ] Probado en MySQL 8.0/8.4 y MariaDB 10.11 con claves compuestas, `BIGINT UNSIGNED` y `AUTO_INCREMENT`.

## Pruebas
Unitarias: generación de `INSERT`/`DELETE`, orden del lote, validación de obligatorias, superposición en `GetRows`. Integración (`-tags integration`): inserción con autoincrement y triggers, borrado con conflicto, atomicidad de un lote mixto, FK `RESTRICT`/`CASCADE`. E2E con el mock y smoke manual.
