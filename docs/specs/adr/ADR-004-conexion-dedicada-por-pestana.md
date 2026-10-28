# ADR-004 — Una conexión dedicada por pestaña de consulta

**Estado**: aceptada

**Contexto**: variables de sesión (`SET @x`), transacciones y `USE` son por conexión; `KILL QUERY` necesita el id de hilo.

**Decisión**: cada sesión mantiene un `*sql.DB` (pool general, límite 25) más un pool auxiliar de 2 conexiones exclusivo para `KILL QUERY`; cada pestaña adquiere `*sql.Conn` dedicada de forma perezosa en su primera ejecución y la mantiene hasta cerrar la pestaña o desconectar. Las consultas de esquema usan el pool general, no las conexiones de pestañas. `Kill` usa el pool auxiliar y aborta la consulta sin cerrar la conexión (cancelar el `ctx` la cerraría y perdería el estado de sesión).

**Consecuencias**
- Más conexiones abiertas por usuario (máx. `MaxTabsPerSession`, default 10; aviso al superarlo).
- Conexión inactiva puede morir por `wait_timeout` → detección en el siguiente run y opción "Reconectar" (spec 04).
- Una ejecución simultánea por pestaña.
