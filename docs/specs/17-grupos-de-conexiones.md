# 17 · Grupos de conexiones y firma de la organización

Estado: aprobado (2026-10-29). Épica 10 del backlog. Contrato en el spec 02; persistencia en el 01; lista y formulario en el 04.

## A. Grupos de conexiones

### Objetivo
Agrupar conexiones (p. ej. «Producción», «Staging») y darle a cada grupo una configuración propia. **Hoy solo color**; más adelante, configuración global por grupo (fuera de alcance, ver «Evolución»).

### Modelo
- `ConnectionGroup { id, name, color, createdAt, updatedAt }` (spec 02). `name`: 1–100 caracteres sin espacios laterales, **único sin distinguir mayúsculas, también en letras no ASCII** (`Producción` = `PRODUCCIÓN`): `COLLATE NOCASE` de SQLite solo pliega ASCII, así que el servicio lo comprueba con `strings.EqualFold` y el índice `UNIQUE` queda como segunda barrera. `color`: `#RRGGBB` o vacío (la misma regla que el color de una conexión).
- `ConnectionProfile.groupId`: id del grupo o `""` («Sin grupo»). Va dentro del JSON del perfil (`connection_profiles.data`), sin columna nueva.
- Tabla nueva `connection_groups` (migración `002_connection_groups.sql`). No hay clave foránea (el perfil es un blob JSON): la integridad la mantiene el servicio.
- **No** se crea columna `settings` ahora: la configuración global por grupo llegará con su propia migración, cuando se sepa qué lleva.

### Reglas
1. **Color efectivo** de una conexión = su color si lo tiene; si no, el de su grupo; si no, ninguno. Se aplica en la tarjeta de la lista, el árbol de esquema y la barra de consulta (los tres sitios que hoy usan `profile.color`). El cálculo vive en un único lugar del store (`colorOf`).
2. **Crear un grupo** se hace desde el modal de la conexión (campo «Grupo» → «＋ Nuevo grupo…», con nombre y color). El grupo se crea **antes** de guardar la conexión; si guardar la conexión falla, el grupo queda creado y seleccionado, de modo que reintentar no lo duplica.
3. **Editar y eliminar** un grupo se hace desde su cabecera en la lista (el modal de la conexión solo crea): un lápiz visible (usable con teclado) y el menú contextual abren el diálogo de edición (nombre y color), que lleva además «Eliminar grupo». El menú contextual solo se abre con clic derecho, por eso no es la única vía.
4. **Eliminar un grupo** deja sus conexiones en «Sin grupo», en una sola transacción. **No** borra conexiones, contraseñas del llavero ni historial. Pide confirmación indicando cuántas conexiones se mueven.
5. `Save` de una conexión con un `groupId` que no existe → `VALIDATION`, Detail `groupId`.
6. Clonar una conexión conserva su grupo.
7. Un grupo sin conexiones **no se borra solo**: es una entidad propia (se ve vacío para poder gestionarlo).

### Interfaz
- **Modal** (pestaña General, tras el nombre): campo «Grupo» con «Sin grupo», los grupos existentes y «＋ Nuevo grupo…». Al elegir esto último aparecen el nombre y la fila de colores del grupo. La fila de colores es el componente `ColorSwatches`, compartido con el color de la conexión.
- **Lista** (`ConnectionList`): si no hay grupos, la lista plana de hoy. Con grupos: una sección por grupo ordenada por nombre y «Sin grupo» al final. Cabecera: chevron, punto de color, nombre y contador; colapsable. El estado de plegado se guarda en `Settings.collapsedGroups` (ids; `"__none__"` = «Sin grupo»; máx. 500 ids de ≤ 64 caracteres) y el id se olvida al borrar el grupo. Con una búsqueda activa todo se ve desplegado (si no, las coincidencias quedarían ocultas) y al limpiarla vuelve el plegado de antes.
- **Búsqueda**: filtra conexiones por nombre, host, base y usuario, y también por nombre de grupo; con filtro activo se ocultan los grupos sin resultados; sin filtro, los grupos vacíos se ven.
- Accesibilidad: la cabecera es un botón con `aria-expanded`; el menú contextual es accesible por teclado como el de las tarjetas.

### Casos borde
- Nombre de grupo repetido (otra capitalización): error en el campo, sin cerrar el modal.
- Se elimina un grupo mientras alguna de sus conexiones tiene sesión abierta: la sesión sigue; solo cambia el color efectivo y la sección.
- Se importa o carga un perfil con `groupId` huérfano (p. ej. restaurado a mano): la lista lo trata como «Sin grupo» y el siguiente `Save` lo normaliza.
- Base de datos anterior sin la tabla: la migración 002 se aplica sola al arrancar y no toca los perfiles.

### Criterios de aceptación
- [ ] Desde el modal se crea un grupo con color y la conexión aparece bajo su cabecera.
- [ ] Editar el color del grupo cambia el color de sus conexiones que no tienen color propio; las que sí lo tienen lo conservan.
- [ ] Colapsar y reabrir la app conserva el estado.
- [ ] Eliminar un grupo mueve sus conexiones a «Sin grupo» sin perder contraseñas ni historial.
- [ ] Nombre duplicado (sin distinguir mayúsculas) y `groupId` inexistente se rechazan con `VALIDATION`.
- [ ] Una base `app.db` anterior abre con todos sus perfiles intactos.
- [ ] `es` y `en` tienen las mismas claves; E2E en verde con el Chromium del CI.

### Evolución (no se implementa ahora)
Configuración global por grupo (la idea: valores por defecto que heredan sus conexiones —solo lectura, TLS, SSH, límite automático…—, con la conexión pudiendo anularlos). Requerirá decidir qué heredan, cómo se muestra el origen de cada valor y una migración para guardar esos valores. También: ordenar grupos a mano y anidarlos.

## B. Firma de la organización

Texto fijo «Hecho con amor por vitacodesas» (en inglés: «Made with love by vitacodesas») en la **esquina inferior izquierda**, dentro de la barra de estado, que es el único elemento siempre visible en esa esquina (no desaparece al colapsar la barra lateral).

- Solo texto, sin enlace: un `<a>` normal navegaría la propia ventana de la app, y abrir el navegador del sistema exigiría `Browser.OpenURL` y ampliar el mock de E2E. Si se quiere enlace, será una tarea aparte.
- Estilo: el de la propia barra (`text-xs text-muted`), hereda el tema claro/oscuro; separada del estado de sesión por un borde fino.
- No se encoge (`shrink-0`); el nombre de la conexión de la barra se trunca para no chocar con ella.
- Criterios: visible en tema claro y oscuro, con la barra lateral colapsada y a 960 px de ancho; `es`/`en` con la misma clave.
