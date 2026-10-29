# Correcciones pendientes

Fallos encontrados en el uso real (smoke manual) que aún no se han arreglado. Al corregir uno: añadir un test que lo reproduzca, tachar aquí y, si cambia un contrato, actualizar el spec.

## ~~C1 · «No database selected» con la base elegida en el selector~~ ✅ corregido (2026-10-28)
> `tabDB` ahora guarda sesión + ID de conexión junto a la base y solo se reutiliza en esa misma conexión; tras reconectar se repite el `USE`. Test: `TestDatabaseIsSelectedAgainAfterReconnect` (falla sin el arreglo). Confirmado en la app nativa (2026-10-28).
- **Síntoma**: con la base `identity` visible en el selector de la barra de consulta, `SELECT * FROM \`api_request_logs\` WHERE id=5044;` falla con `ERROR 1046: No database selected`. Debería funcionar igual que `SELECT * FROM \`identity\`.\`api_request_logs\` …`.
- **Contexto**: una conexión a un servidor remoto (Aurora, sin base por defecto en el perfil), justo después de marcar «Solo lectura» y reconectar con la pestaña ya abierta.
- **Causa probable (leída en el código, sin reproducir)**: `QueryService.tabDB` recuerda la base vigente por **pestaña** (`tabID`) y solo se borra en `CloseTab`. Al reconectar, la sesión y su conexión son nuevas pero la pestaña es la misma: `Execute` cree que la conexión ya está en `identity`, omite el `USE` y el servidor no tiene base. Ver `internal/services/query.go` (`tabDB`, `setTabDB`, ~líneas 299–312).
- **Arreglo propuesto**: clave de `tabDB` por sesión+pestaña (o guardar el ID de conexión del servidor junto a la base y comparar), y borrar la entrada cuando la sesión se cierra. Test: abrir sesión, `USE x`, cerrar sesión, abrir otra con la misma pestaña, ejecutar sin cambiar el selector y comprobar que se vuelve a hacer `USE x`.
- **Workaround**: cambiar la base en el selector a otra y volver a la deseada, o cerrar la pestaña y abrir una nueva.
- **Por comprobar**: si también ocurre sin pasar por una reconexión (p. ej. tras `session:closed` por túnel caído).

## ~~C2 · `TestProbeViaSSH` falla de forma intermitente bajo carga~~ ✅ corregido (2026-10-28)
> Causa: `probeTCP` devolvía el resultado de `c.Close()`; si el otro extremo ya había cerrado (un servidor que corta enseguida, o un canal SSH cerrado desde el lado remoto), `Close` devolvía `EOF` y la prueba de conexión salía como «Error interno» aunque se hubiera conectado bien. Era un fallo real del producto, no solo del test. Ahora el error de `Close` se ignora tras conectar. Test determinista `TestProbeTCPIgnoresACloseErrorAfterConnecting` (falla sin el arreglo) y 480 ejecuciones de `TestProbe*` con `-race` y 8 procesos en paralelo, sin fallos (antes fallaba 1 de cada ~50).

- **Síntoma**: 1 fallo en 48 ejecuciones con 6 procesos de test en paralelo (`INTERNAL: Error interno inesperado (EOF)`); sin carga no se ha visto fallar en más de 50 ejecuciones.
- **Alcance**: test del túnel SSH con el servidor de prueba (`internal/testutil/sshsrv`); no relacionado con los cambios de solo lectura ni de base por pestaña. Sin investigar la causa (¿carrera entre el cierre del servidor de prueba y la lectura del cliente?).
- **Riesgo**: un CI cargado podría marcarlo en rojo sin que haya un fallo real. Si aparece en CI, investigar antes de añadir reintentos.

> Actualización: `TestProbeViaSSH` (C2) volvió a fallar una vez en una ejecución normal de `go test -race ./...` (2026-10-28, al compilar T7.3), no solo bajo carga artificial. Sigue sin investigar; si se repite, tratarlo como prioridad antes de confiar en el CI.

## ~~C3 · El selector de base de la consulta no mostraba todas las bases~~ ✅ corregido (2026-10-28)
> Las bases solo se pedían a Go cuando se abría el panel de esquema, y las del sistema se filtraban. Ahora la barra de la consulta las pide al tener sesión y las del sistema aparecen en un grupo aparte. Tests: `query.test.ts` («selector de base…») y E2E «el selector de base lista todas las bases…».

## C4 · Doble clic en la columna clave abría el visor sin explicar nada (2026-10-28)
> Smoke manual de edición: «me abre el modal pero no me permite modificar». Era la columna clave (no editable por diseño, D4), pero la interfaz no lo decía. Ahora las columnas no editables de un resultado editable llevan un candado en la cabecera y el doble clic muestra el motivo («Columna de la clave: no se edita») antes de abrir el visor. Tests: `ResultGrid.test.ts` y E2E «la columna clave no se edita».

## C5 · Editar JSON: error sin pista y editor de una línea (2026-10-28)
> Smoke manual: al añadir una letra a un JSON salía «El valor no es JSON válido» sin más, y un JSON largo no se puede editar cómodamente en una línea. Ahora el error dice la posición y el carácter (probable causa: comillas tipográficas «“» que el sistema sustituyó al escribir), los campos desactivan la autocorrección, y JSON/textos largos/multilínea se editan en un diálogo ampliado con «Formatear JSON». **Sin confirmar** si las comillas tipográficas eran la causa real: depende de lo que escribió el sistema; con el mensaje nuevo se verá en la posición indicada.

## C6 · Dar formato a un JSON enviaba un UPDATE sin cambio real (2026-10-28)
> Smoke manual: «Formatear JSON» + guardar dejaba un cambio pendiente y el servidor decía «1 fila afectada». Causa: la comparación valor nuevo/original de las columnas JSON era textual y, además, la conexión de edición usa `CLIENT_FOUND_ROWS`, así que «afectada» significa «encontrada». Ahora (`edit.Same`) el JSON de MySQL se compara por valor (no por formato ni orden de claves) y un cambio solo de formato no deja nada pendiente. En MariaDB el JSON es texto (`longtext`), los espacios sí son parte del valor y se trata como cambio. Tests: `TestSameJSONIgnoresFormatting`, `TestFormattingJSONIsNotAChange` e integración en los 3 motores.
> Pendiente menor: el diálogo ampliado se cierra sin avisar de que no hubo cambio; un toast «Sin cambios» sería más claro.

## ~~C7 · «sql: connection is already closed» al volver a ejecutar tras detener una consulta~~ ✅ confirmado en la app (2026-10-28)
> Smoke manual: detener una consulta que no había terminado y pulsar «Ejecutar todo» daba `Error al comunicarse con la base de datos (sql: connection is already closed)`. Causa: si el servidor no responde al `KILL QUERY` en 2 s, el contexto se cancela a la fuerza y el driver cierra la conexión; la sesión seguía guardando esa conexión muerta para la pestaña y la reutilizaba. Ahora (1) una cancelación forzada descarta la conexión de la pestaña, (2) `sql.ErrConnDone` se trata como conexión perdida (`SESSION_CLOSED`) y (3) si la primera sentencia de una ejecución falla porque la conexión murió, se abre otra y se reintenta **una vez** (se vuelve a seleccionar la base elegida; el intento fallido no entra en el historial). Reproducido contra servidores reales (`TestDeadTabConnectionIntegration`: falla sin el arreglo con el mismo mensaje) y cubierto con tests unitarios.
> Efecto secundario aceptado: la conexión nueva no hereda variables de sesión (`SET @x`, `SET time_zone…`) de la muerta. Lo que no se sabe: **por qué** esa consulta no respondió al `KILL` en ese servidor (¿Aurora?); si se repite, mirar el log por «KILL QUERY falló».
