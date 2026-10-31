# 00 — Visión y alcance

## Visión
Cliente SQL de escritorio, rápido y ligero, funcionalmente equivalente a Tabularis en lo esencial: gestionar conexiones, explorar esquema, ejecutar consultas y ver datos cómodamente. Construido con Wails 3 (Go) + Vue 3.

## Principios
- **Go pesado, Vue ligero**: ejecución, streaming, buffers, exportación y tunelización en Go con goroutines; Vue solo renderiza y captura intención.
- **Extensible por drivers**: MySQL primero; añadir PostgreSQL/SQLite no debe tocar UI ni servicios, solo implementar `Driver` (spec 03).
- **Seguro por defecto**: secretos en llavero, TLS configurable, host keys SSH verificadas.
- **Responsivo**: resultados de millones de filas no bloquean la UI (virtualización + ventanas de datos).

## Etapa 1 (este documento cubre)
| Área | Incluye |
|---|---|
| Conexiones | Formulario, test, guardar/editar/clonar/eliminar, llavero, TLS, túnel SSH, importar DSN |
| Esquema | Explorador lazy: BD → tablas/vistas → columnas/índices/FK |
| Consultas | Monaco multi-pestaña, ejecutar selección/todo/multi-statement, cancelar, grid virtualizado, paginación por ventanas |
| Extras | Historial por conexión, exportar CSV/JSON, atajos, tema claro/oscuro |

## No-objetivos (etapa 1)
Otros drivers, edición inline de filas, constructor visual de queries, diagramas ER, notebooks, asistente IA, servidor MCP, plugins, sincronización en la nube. Diseñar sin cerrarles la puerta.

## Etapas futuras (orientativo)
2: edición de datos + DDL básico (edición de celdas: spec 13, hecho; insertar y borrar filas: spec 14, borrador; editar estructura de tablas: spec 19, borrador) · 3: PostgreSQL/SQLite · 4: notebooks, ER, IA/MCP.

## Glosario
- **Perfil**: configuración guardada de una conexión (sin secretos).
- **Sesión**: conexión activa a un perfil (`sessionId`); contiene un pool.
- **Pestaña de consulta (tab)**: editor + resultados; posee una conexión dedicada de la sesión (ADR-004).
- **Query run (`queryId`)**: una ejecución; su resultado vive en un buffer en Go (ADR-002).
- **Ventana**: rango `[offset, offset+limit)` de filas pedido por el grid.

## Plataformas
macOS (prioritario, desarrollo), Windows y Linux (build y CI desde etapa 1; verificación manual posterior).
