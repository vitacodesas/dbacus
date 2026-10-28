# Guion de smoke manual (app real)

Los E2E de Playwright usan un backend simulado; esto comprueba lo que solo se ve con la app nativa y un servidor real. Ejecutar antes de cada release, con `wails3 dev` o con el binario de `wails3 build`. Marca cada punto y anota la versión de SO y de MySQL.

Preparación: MySQL 8.x local (p. ej. `docker run --rm --tmpfs /var/lib/mysql -e MYSQL_ROOT_PASSWORD=pw -p 3306:3306 mysql:8.0`).

## Conexiones y secretos
- [ ] Crear conexión con «Guardar contraseña» activo → **Probar** muestra todos los pasos en verde.
- [ ] Cerrar y reabrir la app: la conexión sigue, conecta sin pedir contraseña (llavero del SO: en macOS aparece una entrada de «Tabularis Clon» en Acceso a Llaveros).
- [ ] Con «Guardar contraseña» desactivado, al conectar pide la contraseña y no queda nada en el llavero.
- [ ] Eliminar la conexión borra su entrada del llavero.
- [ ] Contraseña incorrecta → mensaje claro (`AUTH_FAILED`), sin la contraseña en `~/Library/Logs/…` (buscar con `grep`).
- [ ] Túnel SSH con clave y con contraseña; host key desconocida pide confianza; cambiada la bloquea.
- [ ] TLS `required` contra un servidor sin TLS falla con mensaje comprensible.

## Consultas
- [ ] `⌘/Ctrl+Enter` ejecuta todo; `⌘/Ctrl+Shift+Enter` la sentencia bajo el cursor.
- [~] `SELECT SLEEP(60)` + Detener: devuelve el control en < 1 s y el hilo desaparece de `SHOW PROCESSLIST`. (2026-10-28: Detener cancela y muestra «Consulta cancelada» con la fila `1` que MySQL devuelve a SLEEP; **no se cronometró** el tiempo de respuesta ni se miró el PROCESSLIST.)
- [ ] Resultado de 1 M de filas (`docs/benchmarks.md`): primera ventana inmediata, scroll fluido con la rueda y arrastrando la barra, sin que la ventana se congele; vigilar la memoria en el Monitor de Actividad (debe quedar acotada).
- [ ] Dos pestañas ejecutando a la vez no mezclan resultados; `SET @x=1` en una no afecta a la otra.
- [ ] BIGINT UNSIGNED máximo, DECIMAL, BLOB, JSON, DATETIME(6), NULL y emojis se ven sin pérdida.
- [ ] Cerrar la pestaña con una consulta en curso la cancela.

## Exportación (diálogos nativos)
- [x] Exportar → el **diálogo nativo de guardar** se abre, con nombre sugerido y crea carpetas. (2026-10-28, macOS)
- [x] Cancelar el diálogo no deja archivos ni toast de error. (2026-10-28)
- [ ] CSV abre bien en Excel/Numbers (acentos con BOM, comillas, saltos de línea); JSON válido (`jq . archivo.json`).
- [x] Exportar 1 M de filas muestra progreso y se puede cancelar; no queda `.partial`. (2026-10-28: 1 000 001 líneas, 6 888 893 bytes, ids únicos y completos; cancelar no deja archivo)
- [x] No sobrescribe un archivo existente sin pedirlo. (2026-10-28: macOS pregunta; cancelar conserva el original y «Replace» lo sustituye por el contenido completo)

## Modo solo lectura
- [x] Marcar «Solo lectura» con la sesión abierta: aparece el aviso con «Reconectar» y `UPDATE`/`DROP TABLE` ya se rechazan sin reconectar.
- [ ] Tras reconectar: candado en barra de estado, en la barra de la consulta y en la tarjeta. `SELECT 1` funciona; `DROP TABLE no_existe`, `SELECT 1 INTO OUTFILE '/tmp/x'` y `SET @x = 1` se rechazan.
- [ ] Quitar el modo y reconectar vuelve a permitir escribir (barra de estado: «Lectura y escritura»).

## Edición de celdas (spec 13)
Hacerlo contra una base y tabla de pruebas, no contra datos compartidos.
- [ ] `SELECT * FROM tabla` de una tabla con clave primaria: doble clic y `F2` abren el editor; `Enter` baja, `Tab` pasa a la siguiente columna, `Esc` cancela.
- [ ] Una celda editada se ve en ámbar; aparece la barra «N cambios sin aplicar». «Ver SQL» muestra los `UPDATE` con los valores.
- [ ] «Aplicar» con confirmación: tras confirmar, el valor queda en el servidor (comprobar con otro cliente) y la fila refleja lo que dejó un trigger o `ON UPDATE`.
- [ ] Conflicto: cargar el resultado, cambiar esa fila desde otro cliente, aplicar → mensaje de conflicto y **ningún** cambio del lote aplicado.
- [ ] Valores extremos: texto con emoji y comillas, cadena vacía frente a NULL (menú «Establecer NULL»), `BIGINT UNSIGNED` máximo, `DECIMAL`, `DATETIME(6)`.
- [ ] Un texto demasiado largo o un entero inválido se rechaza en la celda, sin llegar al servidor.
- [ ] JSON y textos largos o con saltos de línea: doble clic abre el diálogo ampliado; «Formatear JSON» ordena el valor; un JSON inválido dice la posición; `⌘/Ctrl+Intro` guarda.
- [ ] Columnas no editables: clave, `AUTO_INCREMENT`, generadas y binarias muestran el motivo en el tooltip de la cabecera.
- [ ] `JOIN`, alias, vistas y tablas sin clave: no se ofrece edición y el pie del grid explica por qué.
- [ ] Volver a ejecutar o cerrar la pestaña con cambios pendientes pide confirmación (`⌘/Ctrl+W` incluido: **sin probar con el atajo real**, el E2E usa el botón).
- [ ] En una conexión de «Solo lectura» no se ofrece edición.
- [ ] Ajustes → «Confirmar con el SQL a la vista»: desactivado, «Aplicar» aplica sin diálogo.
- [ ] Zona horaria: editar un `TIMESTAMP` con la sesión en una zona distinta de la del servidor (limitación conocida, spec 13).

## Insertar, duplicar y borrar filas (spec 14)
Hacerlo contra una tabla de pruebas (idealmente con un `AUTO_INCREMENT`, un índice único y una clave foránea).
- [x] «Nueva fila» (probada en la app nativa, 2026-10-28): aparece al final en verde con las celdas en «DEFAULT» y el editor abierto en la primera columna obligatoria. Rellenar, «Ver SQL» (un `INSERT` solo con las columnas rellenadas) y aplicar: la fila queda con el ID que asignó el servidor y lo que dejó un trigger o un `DEFAULT`.
- [ ] Sin las columnas obligatorias, «Aplicar» lo dice y no envía nada.
- [ ] Un `SELECT` sin una columna obligatoria de la tabla: «Nueva fila» queda deshabilitado y el pie del grid dice cuál falta.
- [ ] Clic derecho en una fila → «Insertar nueva fila a partir de esta»: copia los valores (salvo autoincremental y clave natural), se puede cambiar lo que se quiera. Con varias filas seleccionadas crea varias.
- [ ] Copiar una fila con un valor `UNIQUE` repetido: el error del servidor señala «Fila nueva N» y no se aplica nada del lote.
- [ ] `Supr` sobre una fila la marca (rojo, tachada); «Restaurar» la desmarca; al aplicar desaparece y el contador de filas baja.
- [ ] Borrar una fila cuyo valor cambió otra sesión: conflicto y **nada** del lote se aplica.
- [ ] Borrar una fila con hijos `ON DELETE CASCADE`: el diálogo avisa de las tablas afectadas; con `RESTRICT` el servidor rechaza y se revierte todo.
- [ ] Lote mixto (editar + borrar + insertar): se ve en ese orden y es atómico.

## Ajustes e interfaz
- [ ] Cambiar tema/idioma en Ajustes (`⌘/Ctrl+,`) y reabrir: se conserva. Ancho del panel y del divisor también.
- [ ] Un límite de filas bajo (p. ej. 1 000) aplica a la siguiente consulta; uno fuera de rango se rechaza en el campo.
- [ ] Barra de título: se puede arrastrar la ventana y los botones nativos no se tapan (macOS).
- [ ] Atajos en macOS (⌘) y en Windows/Linux (Ctrl).
- [ ] Historial: aparece lo ejecutado, «No guardar historial» en la conexión lo desactiva, búsqueda y borrado funcionan.

## Registro
- 2026-10-28 (tarde, macOS): verificado el modo solo lectura con la sesión abierta (protección al instante, aviso «Reconectar», candado visible), el llavero (la entrada se crea al guardar y desaparece al borrar la conexión) y la corrección C1 (base por pestaña tras reconectar).
- 2026-10-28 (macOS, `wails3 dev`, MySQL de prueba): verificado el bloque de exportación completo y la cancelación de SLEEP. Hallazgo: la exportación con LIMIT automático exportaba solo lo cargado; corregido (spec 07). Pendiente: llavero, SSH/TLS, solo lectura en la app nativa, CSP en la ventana nativa, fluidez del scroll con 1 M de filas.
- 2026-10-28 (noche, macOS): verificado «Nueva fila» y la reapertura de la conexión de la pestaña tras detener una consulta (C7). Pendiente de probar en la app nativa: insertar a partir de una fila existente, eliminar/restaurar, conflictos de borrado, CASCADE/RESTRICT y el lote mixto.
