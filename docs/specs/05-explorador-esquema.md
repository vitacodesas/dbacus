# 05 — Explorador de esquema

## Objetivo
Árbol navegable por sesión: **Bases de datos → Tablas / Vistas → Columnas / Índices / Claves foráneas**, cargado de forma lazy y cacheado en Go.

## Comportamiento
- **Varias conexiones**: cada sesión abierta es una **raíz** del árbol (nombre, versión del servidor, color del perfil) con sus bases debajo; se despliega sola al conectar y se olvida al desconectar. Tocar cualquier nodo de una conexión la convierte en la **activa** (la que usan el editor y la barra de estado); un clic en una conexión abierta de la lista de conexiones también la activa. Menú de la raíz: Refrescar, Desconectar. El filtro muestra solo las conexiones con coincidencias (o cuyo nombre coincide).
- Al conectar: `ListDatabases`. Bases de sistema agrupadas/atenuadas (toggle "mostrar sistema", off por defecto).
- Expandir BD → `ListTables(db)` (tablas y vistas separadas, contador). Expandir tabla → `DescribeTable` (columnas con tipo, PK/FK/NOT NULL, índices, FKs).
- Doble clic en tabla: abre pestaña de consulta con `SELECT * FROM \`db\`.\`tabla\` LIMIT 100` y la ejecuta. Menú contextual: "Copiar nombre", "Copiar SELECT", "Ver CREATE TABLE" (modal de solo lectura con Monaco), "Refrescar".
- Filtro de texto sobre el árbol (filtra nombres ya cargados; búsqueda en servidor no incluida).
- Base activa de la sesión: marcada; el editor puede cambiarla con selector (`USE`).

## Caché (Go)
- Caché por `sessionId → db → tablas → detalle` con TTL 5 min y invalidación manual (`Refresh`).
- Invalidación automática tras ejecutar DDL (`CREATE|ALTER|DROP|RENAME|TRUNCATE`) detectado por el splitter → evento `schema:invalidated`.
- Consultas de `information_schema` con timeout 10 s, cancelables al desconectar; peticiones concurrentes idénticas se colapsan (`singleflight`).
- Bases con >5 000 tablas: `Tables` pagina internamente pero devuelve lista completa de nombres (ligera); detalle siempre bajo demanda.

## Catálogo para autocompletado
`GetCompletionCatalog(sessionID, db)` devuelve tablas y columnas (nombres y tipos) de la BD activa; el front lo carga una vez por (sesión, BD) y alimenta el proveedor de completado de Monaco (keywords MySQL + tablas + columnas + alias básicos resueltos en front solo con regex ligera; **sin** parser SQL en front, el filtrado fino queda a Monaco).

## UI
Árbol virtualizado (nodos >1 000), iconos por tipo, tooltips con comentario/tipo completo, estados: cargando (skeleton), vacío, error con "Reintentar".

## Criterios de aceptación
- [ ] Expandir una BD con 2 000 tablas no congela la UI (>50 fps al hacer scroll).
- [ ] Tras `ALTER TABLE` ejecutado en la app, el árbol de esa tabla se refresca sin acción manual.
- [ ] Con dos conexiones abiertas se ven ambas a la vez y el clic cambia la activa (verificado en vivo).
- [ ] Desconectar mientras carga cancela las consultas de esquema (verificado con test).
- [ ] Doble clic abre y ejecuta el SELECT con identificadores correctamente escapados (nombres con backticks/espacios).

## Implementación (T3.1–T3.3)
- Orden de nombres: se ordena en Go (minúsculas, luego bytes); el `ORDER BY` de `information_schema` depende de la collation del servidor y difiere entre MySQL y MariaDB.
- Diferencias de motor absorbidas en el reader: MariaDB devuelve `DEFAULT` como literal SQL (`'anon'`) y conserva el ancho de enteros (`int(10)`); MySQL 8 no. Los `DEFAULT NULL` no se reportan como valor por defecto.
- Árbol: filas planas construidas por `composables/schemaTree.ts` (puro, probado) y renderizadas con `VirtualList` (altura fija 26 px); con 2 000 tablas solo hay ~30 nodos en el DOM y el scroll va a 60 fps.
- Pendiente de otras tareas: doble clic en tabla abre pestaña con SELECT (T4.4), "Ver CREATE" con Monaco de solo lectura (T4.4; hoy `<pre>`), invalidación automática tras DDL ejecutado desde el editor (T4.2, vía `SchemaService.invalidate`).
