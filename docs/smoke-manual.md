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

## Ajustes e interfaz
- [ ] Cambiar tema/idioma en Ajustes (`⌘/Ctrl+,`) y reabrir: se conserva. Ancho del panel y del divisor también.
- [ ] Un límite de filas bajo (p. ej. 1 000) aplica a la siguiente consulta; uno fuera de rango se rechaza en el campo.
- [ ] Barra de título: se puede arrastrar la ventana y los botones nativos no se tapan (macOS).
- [ ] Atajos en macOS (⌘) y en Windows/Linux (Ctrl).
- [ ] Historial: aparece lo ejecutado, «No guardar historial» en la conexión lo desactiva, búsqueda y borrado funcionan.

## Registro
- 2026-10-28 (tarde, macOS): verificado el modo solo lectura con la sesión abierta (protección al instante, aviso «Reconectar», candado visible), el llavero (la entrada se crea al guardar y desaparece al borrar la conexión) y la corrección C1 (base por pestaña tras reconectar).
- 2026-10-28 (macOS, `wails3 dev`, MySQL de prueba): verificado el bloque de exportación completo y la cancelación de SLEEP. Hallazgo: la exportación con LIMIT automático exportaba solo lo cargado; corregido (spec 07). Pendiente: llavero, SSH/TLS, solo lectura en la app nativa, CSP en la ventana nativa, fluidez del scroll con 1 M de filas.
