# ADR-008 — Migración de schema: copia en el servidor o por Go, trozos por clave y pool de workers

**Estado**: aceptada e implementada (2026-11-14; mediciones en el spec 23) · detalle en el [spec 23](../23-migracion-de-schema.md) ⚑D2, ⚑D4, ⚑D6, ⚑D7, ⚑D9, ⚑D10 · se apoya en el [ADR-004](ADR-004-conexion-dedicada-por-pestana.md) (conexiones dedicadas) y en el pool `mon` del [spec 22](../22-monitoreo-dba.md) ⚑D3

**Contexto**: hay que copiar un schema entero a otro, en el mismo servidor o entre conexiones, con las comprobaciones de claves foráneas desactivadas y la mayor velocidad posible con goroutines. Nada de esto está medido todavía. Las restricciones reales son: el cuello de botella es el servidor (disco, `fsync`, locks), no la CPU del cliente; las variables de sesión se pierden al reconectar; un `INSERT … SELECT` corre entero en el servidor; y **el origen no puede recibir ninguna escritura de dbacus** (regla mandatoria del usuario). La consistencia del origen frente a otros usuarios **no** es asunto de dbacus (decisión del usuario).

**Decisión**
- **Dos rutas**: si origen y destino son **la misma sesión**, la copia es **`INSERT INTO dst (cols) SELECT cols FROM src WHERE <trozo>`** dentro del servidor (los datos no cruzan la red); en cualquier otro caso se **transmiten por Go** (lector → canal acotado en bytes → escritores con `INSERT` multifila preparado).
- **Trozos por rango de una clave de troceo** (PK → clave única `NOT NULL` → una partición por trozo → primer índice no único con las filas `NULL` aparte → un solo trozo si no hay ningún índice), de ≈ 64 MiB acotados a 10 000–500 000 filas, descubiertos por un **planificador perezoso** que recorre el índice (`WHERE clave > último ORDER BY clave LIMIT 1 OFFSET n−1`) y alimenta una cola acotada. **Un trozo = una transacción = la unidad de reintento y de progreso.** Tabla sin clave usable: un solo trozo.
- **Pool de N workers** (4 por defecto, 1–16, ≤ 50 % de las conexiones libres), **una conexión por worker**, dentro de un `errgroup`. Las tablas más grandes se parten primero.
- **Las variables de sesión van en el conector** (`foreign_key_checks=0`, `unique_checks=0`, `time_zone`, `sql_mode`, tiempos de espera) y no en un `SET` posterior, para que **cada conexión nueva las tenga** tras una reconexión. El aislamiento es `READ COMMITTED` para que `INSERT … SELECT` lea el origen sin bloquearlo.
- **DDL desde `SHOW CREATE TABLE`** sin las líneas de claves foráneas, y las **foráneas se añaden al final con las comprobaciones apagadas**; los **triggers se crean al final** para que no se disparen durante la carga.
- **Origen siempre de solo lectura** (spec 23 ⚑D19): ruta por Go con conexiones del origen `READ ONLY` impuestas por MySQL; ruta en el servidor con un **`WriteGuard`** en Go que valida cada sentencia antes de enviarla (decisión del usuario: se mantiene la ruta rápida con garantía por código). **No se gestiona la consistencia** frente a otros escritores del origen (procedimental). Cancelación con **`KILL QUERY`** solo de hilos propios por el pool auxiliar.
- **Reintento idempotente**: antes de repetir un trozo se borra su rango en el destino.

**Alternativas descartadas**
- **`CREATE TABLE … LIKE` + `INSERT … SELECT *`**: `LIKE` no copia las foráneas ni el siguiente `AUTO_INCREMENT`, y `*` omite las columnas invisibles; habría dos caminos de DDL (servidor y Go) en vez de uno.
- **`mysqldump`/`mydumper` externos**: añaden un binario que empaquetar y credenciales que pasar por línea de comandos o archivos (spec 10).
- **Snapshot consistente (`FLUSH TABLES WITH READ LOCK`, transacciones de lectura largas)**: descartado por el usuario (la consistencia es procedimental) y además mantendría bloqueos y *undo* en el origen.
- **Una sola transacción por tabla**: *undo log* proporcional a la tabla, reintento caro y cancelación lenta.
- **`SET FOREIGN_KEY_CHECKS=0` como sentencia suelta**: se pierde en cada reconexión del pool.
- **Muchísimos workers por defecto o ajuste automático en v1**: sin mediciones no hay base para elegirlos; el limitador del planificador es redimensionable para añadirlo después.
- **`LOAD DATA LOCAL INFILE` en v1**: depende de `local_infile` en el servidor y de escapes de formato por tipo; se mide primero contra el `INSERT` multifila.

**Consecuencias**
- La ruta en servidor exige que **una sola sesión** vea ambos schemas; dos sesiones del mismo servidor van por Go aunque podrían ir por dentro.
- Con las foráneas apagadas y el origen en movimiento, el destino puede quedar inconsistente **sin error**; dbacus no lo gestiona ni lo avisa (decisión del usuario). La verificación opcional es la única herramienta.
- En la ruta dentro del servidor **solo el `WriteGuard` impide escribir en el origen** (una sola conexión lee y escribe, no puede ser `READ ONLY`): un fallo del guardia sería una escritura en el origen, por lo que tiene tests de mutación y fuzz propios. La recomendación de un usuario con solo `SELECT` sobre el origen queda documentada y no se impone.
- Un `INSERT … SELECT` cancelado revierte en proporción a sus filas: la cancelación puede tardar.
- `lock_wait_timeout` corto hace fallar el DDL de la migración si hay transacciones largas abiertas; falla pronto en vez de bloquear al resto.
- No se reanuda tras cerrar la app: el destino queda parcial y hay que limpiarlo a mano.
- **Medido** (spec 23, «Mediciones»; Docker local, no un servidor remoto): la ruta dentro del servidor es la más rápida y las dos escalan hasta 4 workers; más no suma, lo que respalda el valor por defecto de 4. En la ruta por la app con 8–16 workers hubo paradas de varios segundos en MySQL 8.x que **no** se han explicado (no son reintentos, ni bloqueos de fila, ni redo log); por eso hay aviso por encima de 8 workers.
- Quedan sin implementar las dos optimizaciones que el ADR dejaba abiertas: aplazar los índices secundarios y ajustar los workers automáticamente.
