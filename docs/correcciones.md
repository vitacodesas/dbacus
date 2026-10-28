# Correcciones pendientes

Fallos encontrados en el uso real (smoke manual) que aún no se han arreglado. Al corregir uno: añadir un test que lo reproduzca, tachar aquí y, si cambia un contrato, actualizar el spec.

## ~~C1 · «No database selected» con la base elegida en el selector~~ ✅ corregido (2026-10-28)
> `tabDB` ahora guarda sesión + ID de conexión junto a la base y solo se reutiliza en esa misma conexión; tras reconectar se repite el `USE`. Test: `TestDatabaseIsSelectedAgainAfterReconnect` (falla sin el arreglo). Confirmado en la app nativa (2026-10-28).
- **Síntoma**: con la base `identity` visible en el selector de la barra de consulta, `SELECT * FROM \`api_request_logs\` WHERE id=5044;` falla con `ERROR 1046: No database selected`. Debería funcionar igual que `SELECT * FROM \`identity\`.\`api_request_logs\` …`.
- **Contexto**: conexión «Laika STG» (Aurora, sin base por defecto en el perfil), justo después de marcar «Solo lectura» y reconectar con la pestaña ya abierta.
- **Causa probable (leída en el código, sin reproducir)**: `QueryService.tabDB` recuerda la base vigente por **pestaña** (`tabID`) y solo se borra en `CloseTab`. Al reconectar, la sesión y su conexión son nuevas pero la pestaña es la misma: `Execute` cree que la conexión ya está en `identity`, omite el `USE` y el servidor no tiene base. Ver `internal/services/query.go` (`tabDB`, `setTabDB`, ~líneas 299–312).
- **Arreglo propuesto**: clave de `tabDB` por sesión+pestaña (o guardar el ID de conexión del servidor junto a la base y comparar), y borrar la entrada cuando la sesión se cierra. Test: abrir sesión, `USE x`, cerrar sesión, abrir otra con la misma pestaña, ejecutar sin cambiar el selector y comprobar que se vuelve a hacer `USE x`.
- **Workaround**: cambiar la base en el selector a otra y volver a la deseada, o cerrar la pestaña y abrir una nueva.
- **Por comprobar**: si también ocurre sin pasar por una reconexión (p. ej. tras `session:closed` por túnel caído).

## C2 · `TestProbeViaSSH` falla de forma intermitente bajo carga (2026-10-28)
- **Síntoma**: 1 fallo en 48 ejecuciones con 6 procesos de test en paralelo (`INTERNAL: Error interno inesperado (EOF)`); sin carga no se ha visto fallar en más de 50 ejecuciones.
- **Alcance**: test del túnel SSH con el servidor de prueba (`internal/testutil/sshsrv`); no relacionado con los cambios de solo lectura ni de base por pestaña. Sin investigar la causa (¿carrera entre el cierre del servidor de prueba y la lectura del cliente?).
- **Riesgo**: un CI cargado podría marcarlo en rojo sin que haya un fallo real. Si aparece en CI, investigar antes de añadir reintentos.
