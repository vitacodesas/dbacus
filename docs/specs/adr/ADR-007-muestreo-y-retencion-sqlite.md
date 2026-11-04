# ADR-007 — Muestreo por deltas y retención por tiempo en SQLite

**Estado**: propuesta (2026-11-04) · detalle en el [spec 22](../22-monitoreo-dba.md) ⚑D5, ⚑D7, ⚑D10 · enmienda el [ADR-003](ADR-003-sqlite-y-llavero.md)

**Contexto**: el spec 22 guarda métricas durante días. Las estadísticas por digest del servidor son **acumulativas** y los textos de `INFORMATION_SCHEMA.PROCESSLIST.INFO` traen SQL con literales. SQLite tiene **una sola conexión de escritura serializada** (ADR-003) que ya usa el historial. La retención actual del historial es por **conteo** dentro de la transacción de cada inserción, lo que no escala a miles de filas por hora.

**Decisión**
- **Se muestrea y se restan lecturas** (delta por `(schema, digest)`); no se hace `TRUNCATE` de `performance_schema` (escritura global que afectaría a otros usuarios y borraría su historia).
- **Se guarda el delta del top-K (por tiempo ∪ por ejecuciones) más una fila `__other__`**, no el snapshot completo. El texto del digest se guarda **una vez** en un diccionario y solo para digests nuevos.
- **Nunca se persiste `INFO`** ni `QUERY_SAMPLE_TEXT`; los procesos se guardan como agregados.
- **Cada muestra es una transacción**; **la retención es por tiempo y por lotes** con agregación previa (crudo → hora → día), y un tope de tamaño que recorta primero lo crudo.
- Los contadores se leen como `uint64` y el tiempo se guarda en microsegundos (`int64`).

**Alternativas descartadas**
- **Guardar el snapshot completo de cada muestra**: del orden de MB por muestra en servidores con ~10 000 digests.
- **`TRUNCATE` del digest en cada muestra**: simplifica los deltas pero es destructivo para terceros y requiere privilegio de escritura.
- **Retención por conteo como el historial**: un `DELETE … NOT IN` por inserción es proporcional al tamaño de la tabla.
- **Otra base de datos local (p. ej. DuckDB)**: añade un motor y una dependencia nativa; el volumen esperado cabe en SQLite. Se reevalúa si las mediciones de T16.3 lo contradicen.
- **Guardar el texto con literales para poder hacer `EXPLAIN`**: persistiría datos del negocio y credenciales literales (spec 07 ya lo reconoce para el historial).

**Consecuencias**
- La primera muestra de cada ejecución no produce delta.
- Un digest que reaparece con contador menor se trata como reinicio (`delta = actual`); puede sobrestimar si el contador bajó por otra causa. Se documenta en el spec.
- El top-K pierde la cola larga individual; `__other__` conserva los totales.
- Los rankings que cruzan niveles de agregación pueden marcar `Approx` en los bordes.
- La purga compite con el historial por la conexión de escritura: por eso va en lotes con pausa. [Suposición] el volumen (~170 000 filas/día) es manejable; se mide en T16.3.
