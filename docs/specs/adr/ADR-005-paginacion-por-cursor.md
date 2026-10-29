# ADR-005 — Paginación por cursor (keyset) para recorrer resultados grandes

**Estado**: aceptada (2026-10-29) · enmienda el [ADR-002](ADR-002-resultados-en-buffer-go.md) · detalle en el [spec 18](../18-paginacion-por-cursor.md)

**Contexto**: el buffer en memoria (ADR-002) llega a `MaxRows` (100 000) y cuesta ~124 MiB por millón de filas; con el LIMIT automático (1000) el resto del resultado es inalcanzable salvo exportando, y no se sabe si había más filas. Se quiere llegar al final de cualquier tabla con coste por página estable: el rendimiento es el requisito principal.

**Decisión**: paginar por **cursor de clave** (*keyset*): cada página es una consulta pequeña `WHERE <clave> ▷ <último valor> ORDER BY <orden> LIMIT n+1` que sustituye el buffer de esa sentencia. Se lee una fila de más solo para saber si hay página siguiente. «Primera» y «Última» (orden invertido, filas invertidas en Go) son una consulta cada una. En esta versión solo para claves y orden **numéricos exactos** (enteros y decimales, no nulos).

**Alternativas descartadas**
- `LIMIT/OFFSET`: coste lineal con la profundidad y filas saltadas o repetidas si los datos cambian (ya descartado en el ADR-002). No se usa ni como alternativa para lo que no se puede paginar por clave.
- Seguir acumulando en el buffer hasta `MaxRows`: no llega al final de tablas grandes y consume memoria proporcional a lo cargado.
- Cursor de servidor de MySQL: no es desplazable hacia atrás y mantendría una conexión ocupada.
- Instantánea consistente (`START TRANSACTION WITH CONSISTENT SNAPSHOT`): mantiene una transacción larga abierta por pestaña; peor que la inconsistencia leve entre páginas.

**Consecuencias**
- Solo hay paginación exacta para SELECT de una tabla con clave única numérica. Para el resto, un **modo aproximado** (elegido por el usuario) pagina por una columna numérica del resultado envolviendo la consulta, con la salvaguarda de bloquear el avance si detecta un empate en el borde de la página (evita la pérdida silenciosa de filas) y con la advertencia de que cada página re-ejecuta la consulta completa. Lo demás sigue como hoy, con el motivo.
- No hay total exacto ni salto a la página N; sí «primera» y «última».
- El orden de las filas depende del índice: sin un índice que cubra el orden, cada página ordena y deja de tener coste constante (se avisa con `EXPLAIN`).
- Se necesita ampliar `sqlsplit` (WHERE y ORDER BY) y el driver sigue sin parámetros: los literales son números validados que vienen de las celdas, nunca texto del usuario.
- El rendimiento no se supone: se mide con una prueba contra una tabla de 5 M filas y se compara con OFFSET (spec 18 §9).
