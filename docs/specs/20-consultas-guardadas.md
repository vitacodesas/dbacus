# 20 — Consultas guardadas (etapa 2)

> Estado: **implementado** en T14.1–T14.6 (Épica 14). Verificado con tests de Go (`internal/queryfiles`, `internal/services`), vitest y E2E con el backend simulado. **Falta el smoke manual en la app nativa** (`docs/smoke-manual.md`: permiso de Documentos en macOS, explorador del SO, sincronizadores, Windows). Decisiones del usuario (2026-10-31): las consultas son **archivos `.sql` reales** en una carpeta propia de Dbacus (no SQLite); carpeta por defecto `Documentos/Dbacus/Consultas`, configurable; **subcarpetas libres** y conexión asociada **opcional**; en la v1 entran el **panel «Consultas»** en la barra lateral y los **borradores con indicador de «sin guardar»**, además de «Guardar como consulta» desde el historial.

## Objetivo
Que ninguna consulta se «pierda»: lo que el usuario guarda queda en **un sitio conocido, visible dentro de la app y fuera de ella**, y lo que no ha guardado se ve como tal. Hoy [Seguro, spec 06 y `queryTabs.ts`] las pestañas se restauran solas al reabrir, pero no hay archivo, ni carpeta, ni forma de buscar lo escrito antes: «guardar» no existe, y de ahí la sensación de haberlo perdido.

## Alcance v1
- **Incluye**
  - Carpeta raíz de consultas (por defecto `Documentos/Dbacus/Consultas`), cambiable y siempre visible (ruta en el pie del panel y en Ajustes, con «Mostrar en el explorador»).
  - Panel **Consultas** en la barra de actividad: árbol de carpetas y archivos, búsqueda por nombre y por contenido, sección «Sin guardar», crear carpeta, renombrar, mover, borrar (a una papelera propia).
  - Guardar (`⌘/Ctrl+S`), Guardar como (`⌘/Ctrl+⇧+S`) y abrir; la pestaña queda **vinculada** al archivo.
  - Indicador de «sin guardar» (punto) en la pestaña, aviso al cerrar y detección de cambios hechos fuera de la app.
  - Conexión y base asociadas, opcionales, en un encabezado de comentario SQL.
  - Acción «Guardar como consulta» en cada entrada del historial.
- **Fuera de alcance**: sincronización en la nube propia (la carpeta se puede sincronizar con lo que el usuario ya use), etiquetas, favoritos, plantillas con parámetros, compartir entre usuarios, historial de versiones de un archivo, restaurar desde la papelera con UI (se hace desde el explorador), vigilancia en tiempo real del disco (`fsnotify`; ver «Evolución»), importar carpetas de otros clientes SQL, paleta de comandos para abrir consultas, otras extensiones (`.sql` únicamente).

## Decisiones
| # | Decisión | Por defecto | Motivo |
|---|---|---|---|
| ⚑D1 | Dónde vive la verdad | **El archivo `.sql`**. SQLite no guarda consultas; solo el estado de pestañas (`ui_state`) y la ruta raíz | Visible en el explorador, versionable con git, respaldable, editable fuera |
| ⚑D2 | Ruta raíz | `Documentos/Dbacus/Consultas` (macOS/Linux: `~/Documents/…`; Windows: `%USERPROFILE%\Documents\…`; con `DBACUS_HOME`: `<home>/queries`). Se crea al primer uso con permisos `0700` | Fácil de encontrar y de respaldar. [No verificado] en Windows con Documentos redirigido a OneDrive y en Linux con `XDG_DOCUMENTS_DIR` |
| ⚑D3 | Cambiar la ruta | Solo con el **diálogo nativo** (`PickRoot`) o «Restablecer»; el front nunca envía una ruta absoluta. **No mueve** archivos: cambia de carpeta y el panel muestra la nueva (la anterior sigue intacta en disco) | Spec 10: rutas elegidas por diálogos nativos; mover archivos de un usuario sin pedirlo es destructivo |
| ⚑D4 | Rutas en el contrato | Siempre **relativas a la raíz**, con `/` como separador, validadas en Go (ver «Seguridad») | El front no puede salirse de la carpeta aunque quiera |
| ⚑D5 | Metadatos de conexión | Primera línea del archivo: `-- @dbacus profile=<id> name="<nombre>" database=<base>`. Go la lee y la escribe; **el editor no la muestra** (el cuerpo llega sin ella) | El archivo sigue siendo SQL válido en cualquier otra herramienta; sin cabecera, es SQL puro |
| ⚑D6 | Cabecera al guardar | Casilla «Recordar conexión y base» **activada** en el diálogo; si se desactiva, se quita la cabecera. Nunca se guarda contraseña, solo id y nombre | El nombre ayuda si el id deja de existir (otro equipo); el usuario decide si quiere ese dato en un archivo que quizá suba a git |
| ⚑D7 | Abrir ≠ ejecutar | Abrir o restaurar una consulta **nunca ejecuta nada**. Ejecutar sigue el camino normal (`QueryService.Execute`, solo lectura incluida) | Un archivo ajeno con `DROP` no puede dispararse solo |
| ⚑D8 | Guardar en solo lectura | **Permitido**: guardar un archivo no es SQL. La cabecera puede nombrar un perfil de solo lectura; ejecutarla sigue pasando por su guardia | El modo solo lectura protege la base, no los archivos del usuario |
| ⚑D9 | Conflictos con el disco | Cada archivo lleva una `Version` (`<mtime ns>:<tamaño>`). `Save` la compara con la del disco; si difiere → `VALIDATION`/`changed_on_disk` y la UI ofrece **Recargar** o **Sobrescribir** | Evita pisar ediciones hechas fuera (git, otro editor, sincronizador) |
| ⚑D10 | Detección de cambios externos | **Sin vigilante**: se vuelve a listar al abrir el panel, al recuperar el foco de la ventana y con «Actualizar»; las pestañas vinculadas comparan su `Version` con la lista | Evita una dependencia nueva (`fsnotify` no está en `go.mod`) y su complejidad multiplataforma; suficiente para el caso de uso |
| ⚑D11 | Borrar | Mueve a `<raíz>/.papelera/<AAAAMMDD-HHMMSS>/<ruta>` tras confirmar; al arrancar se purgan las carpetas de la papelera con más de 30 días. La papelera no aparece en el panel | Perder una consulta por un clic era el problema original |
| ⚑D12 | Un archivo = una pestaña | Abrir un archivo que ya está abierto enfoca su pestaña; no se duplica | Dos pestañas editando el mismo archivo se pisarían |
| ⚑D13 | Cerrar la app con cambios | **No pregunta**: los borradores se conservan (`ui_state`, igual que hoy) y al reabrir se ven con el punto de «sin guardar». Solo **cerrar una pestaña** con cambios pregunta | La app ya restaura pestañas; preguntar al salir sería ruido |

## Modelo de archivos (Go, `internal/queryfiles`)
- **Archivo**: `*.sql`, UTF-8. Se rechaza el que no sea UTF-8 válido (`VALIDATION`/`not_utf8`) para no corromperlo al guardar; el BOM se ignora al leer y no se reescribe. El fin de línea **se conserva** (si el original tenía `\r\n`, se guarda con `\r\n`; los archivos nuevos usan `\n`).
- **Límites**: 2 MiB por archivo (`too_large`; un volcado no es una consulta), profundidad máxima 8 niveles, 10 000 archivos listados (`Truncated` si se excede).
- **Nombres**: 1–120 caracteres, sin `/ \ : * ? " < > |` ni caracteres de control, sin punto o espacio final, sin empezar por `.`, sin nombres reservados de Windows (`CON`, `PRN`, `AUX`, `NUL`, `COM1-9`, `LPT1-9`, con o sin extensión). La extensión `.sql` la pone Go (si el usuario la escribe, no se duplica). Unicidad **sin distinguir mayúsculas** (macOS/Windows lo hacen igual a nivel de sistema de archivos), comparando con `strings.EqualFold`.
- **Visibilidad**: se listan `*.sql` y carpetas; se ignoran archivos y carpetas que empiezan por `.`, y **los enlaces simbólicos no se siguen** (ni listan ni abren). La raíz sí puede ser un enlace: se resuelve una vez al fijarla.
- **Escritura atómica**: archivo temporal oculto en la misma carpeta + `fsync` + `rename`; permisos `0600` (archivos) y `0700` (carpetas), porque una consulta puede llevar literales sensibles. Un único mutex por carpeta raíz serializa las operaciones que escriben (basta para un uso de escritorio y evita los casos de mover/borrar a la vez).
- **Cabecera** (⚑D5): solo se interpreta la primera línea no vacía si empieza por `-- @dbacus `. Valores con comillas y `\"` escapado; claves desconocidas se conservan al reescribir. El cuerpo devuelto al editor excluye la cabecera y la línea en blanco que la sigue.

## Contrato (a incorporar en spec 02 al implementar)
`QueryFilesService` (nuevo, `internal/services`). **No añade eventos**: todo el estado vuelve en las respuestas.
```go
type QueryRoot struct{ Path string; IsDefault, Exists, Writable bool }
type QueryNode struct {
  Path string        // relativa, '/'
  Name string        // sin .sql
  Kind string        // "folder" | "file"
  Size int64
  ModifiedAt time.Time
  Version string     // solo archivos
  ProfileID, ProfileName, Database string // de la cabecera, si la hay
}
type QueryTree struct{ Root QueryRoot; Nodes []QueryNode; Truncated bool }
type QueryFile struct{ Path, Name, SQL, Version, ProfileID, ProfileName, Database string; ProfileMissing bool }
type SaveQueryRequest struct{ Path, SQL, ProfileID, Database string; RememberConnection bool; ExpectedVersion string; Overwrite bool }
type QueryRef struct{ Path, Name, Version string }
type PathChange struct{ From, To string }
type QuerySearchRequest struct{ Text string; Content bool; Limit int }   // Limit ≤ 100
type QueryHit struct{ Path, Name string; Line int; Snippet string }      // Line/Snippet solo si Content
```
- `Root() → QueryRoot` · `List() → QueryTree` (crea la raíz si falta) · `Open(path) → QueryFile` (`ProfileMissing` si la cabecera nombra un perfil que ya no existe).
- `Save(SaveQueryRequest) → QueryRef`: `ExpectedVersion == ""` crea (falla con `exists` si el archivo ya está, salvo `Overwrite`); con versión, actualiza y compara (⚑D9). `ProfileID` se valida contra los perfiles guardados (`VALIDATION`/`profileId`); `RememberConnection == false` quita la cabecera.
- `CreateFolder(path)` · `Move(from, to) → []PathChange` (renombrar es mover en la misma carpeta; si es una carpeta, devuelve un cambio por cada archivo afectado para que las pestañas actualicen su ruta; nunca pisa: `exists`) · `Delete(path)` (a la papelera, ⚑D11).
- `Search(QuerySearchRequest) → []QueryHit`: nombre y, con `Content`, contenido (subcadena sin distinguir mayúsculas ni acentos, secuencial, cancelable, omite archivos > 2 MiB). Sin `Content`, solo nombre.
- `PickRoot() → *QueryRoot` (diálogo nativo; `nil` si se cancela; valida y aplica) · `ResetRoot() → QueryRoot` · `Reveal(path)` (`""` = raíz; abre el explorador del SO con argumentos fijos, sin shell; en modo servidor abre el explorador de la máquina donde corre el servidor).
- La ruta raíz elegida se guarda en `settings` (clave `queries_dir`); **no** forma parte del DTO `Settings`.
- `Save` con `Overwrite` sobre un nombre que solo difiere en mayúsculas escribe en el archivo que ya existe y devuelve su nombre real (en un sistema que distingue mayúsculas, si no, «Sobrescribir» fallaría con el mismo `exists`).
- `PurgeTrash()` también queda expuesto por Wails (cualquier método exportado lo está); solo purga lo que creó `Delete` hace más de 30 días y no crea la carpeta si no existe.
- Errores: `VALIDATION` con `Detail` = `name` | `path` | `exists` | `changed_on_disk` | `too_large` | `not_utf8` | `depth` | `profileId` | `root` (raíz inválida o inaccesible: relativa, la raíz del disco, el directorio personal, no se puede crear o no es escribible); `NOT_FOUND` (archivo o carpeta ya no existe); `UNSUPPORTED` (diálogo no disponible); `INTERNAL` (fallo del sistema de archivos al leer/escribir un archivo, sin ruta absoluta en `Message`).
- `TabState` (spec 02, `SaveTabs`/`LoadTabs`) gana `FilePath string` y `BaseSQL string` (el texto del archivo en el último guardado/apertura; tope 2 MiB). **Sin cambios de migración**: va dentro del JSON de `ui_state`.

## UI (Vue, solo pinta)
- **Barra de actividad**: nuevo ítem «Consultas» (`ui.sidebarView = "queries"`), junto a Conexiones, Esquema e Historial.
- **Panel `QueriesPanel`**:
  - Cabecera: búsqueda, conmutador «Contenido» (busca dentro de los archivos), «Nueva carpeta», «Actualizar», orden Nombre/Modificación.
  - Sección **Sin guardar** (si hay): pestañas abiertas con cambios o sin archivo y con texto; clic enfoca la pestaña; botón «Guardar».
  - Árbol virtualizado (`VirtualList`, como el de esquema): carpetas primero; cada archivo con su nombre, un punto del color de su conexión (`colorOf`) y la base en gris; tooltip con ruta completa y fecha. Doble clic o Enter abre; menú contextual: Abrir, Renombrar, Mover a…, Duplicar, Mostrar en el explorador, Eliminar. Los resultados de búsqueda por contenido muestran la línea coincidente.
  - **Pie con la ruta de la carpeta** (abreviada, con tooltip) que al pulsarla ejecuta «Mostrar en el explorador». Es la respuesta directa a «no sé dónde quedó».
  - Vacío: explica dónde se guardan las consultas y cómo guardar la actual (`⌘/Ctrl+S`). Error de raíz: mensaje y botón «Elegir otra carpeta».
- **Diálogo Guardar** (`⌘/Ctrl+S` en pestaña sin archivo, y `⌘/Ctrl+⇧+S`): nombre (por defecto `consulta-AAAAMMDD-HHMM`), selector de carpeta (raíz por defecto, o la última usada en la sesión), casilla «Recordar conexión y base» y la **ruta final completa** en vivo. Si el nombre existe: error en el campo con «Sobrescribir» explícito.
- **Pestañas**: el título es el nombre del archivo; un **punto ●** indica «sin guardar» (`sql !== baseSql`, comparación de texto, sin analizar SQL); el tooltip muestra la ruta relativa. Una pestaña abierta con SQL de partida (una tabla del árbol, el historial) **no** cuenta como sin guardar hasta que se edita: lo generado se puede regenerar, lo escrito no. Renombrar una pestaña vinculada **renombra el archivo** (`Move`). Cerrar una pestaña con cambios (o sin archivo y con texto no vacío) abre «Guardar / No guardar / Cancelar».
- **Banners en la pestaña vinculada** (sobre el editor): «El archivo cambió en el disco» (`Recargar` / `Mantener lo mío`, esta última marca la pestaña como sin guardar) y «El archivo ya no existe» (`Guardar de nuevo`). Si la pestaña **no** tiene cambios, se recarga sola con un aviso discreto.
- **Abrir una consulta con cabecera**: si el perfil existe, la pestaña usa esa conexión y base (no se conecta sola: como cualquier pestaña sin sesión); si no existe, se abre con la conexión activa y un aviso; si no hay ninguna conexión guardada, no se abre y se avisa de que hay que crear una (una pestaña necesita una conexión a la que pertenecer, spec 06).
- **Historial** (spec 07): cada entrada gana «Guardar como consulta», que abre el diálogo con el SQL precargado y la conexión de la entrada.
- **Ajustes**: sección «Consultas guardadas» con la ruta, «Cambiar…», «Restablecer» y «Mostrar en el explorador».
- **Atajos** (spec 08): `⌘/Ctrl+S` guardar (en el editor; el `⌘S` del formulario de conexión sigue siendo contextual), `⌘/Ctrl+⇧+S` guardar como.
- Todo el texto en `vue-i18n` (es/en, mismas claves).

## Seguridad y casos borde
- **Escape de la carpeta** [núcleo del spec]: `resolve(rel)` rechaza vacía, absoluta, con `..`, con `\`, NUL o caracteres de control, y compone con `filepath.Join` + `filepath.Rel` contra la raíz; en rutas existentes comprueba que ningún tramo sea un enlace simbólico. Mismo nivel de rigor que la exportación (spec 07) y la regla de rutas del spec 10.
- **Nunca se registra SQL**: los logs llevan solo el nombre de la operación y un contador (regla de oro 5); las rutas relativas pueden aparecer en `debug`, el contenido no. Los `Snippet` de búsqueda viajan solo hacia la UI.
- **Contenido ajeno**: una carpeta compartida (git, Dropbox) puede traer archivos de otros; por ⚑D7 abrir no ejecuta. La cabecera solo elige conexión, no puede contener credenciales ni SQL ejecutable (es un comentario).
- **El nombre del perfil en la cabecera** viaja con el archivo a donde vaya (p. ej. un repositorio): por eso la casilla del diálogo es explícita (⚑D6) y se avisa en el tooltip.
- **Permisos del SO**: [Probable, no verificado] macOS puede pedir permiso para acceder a Documentos la primera vez; si se deniega, el panel muestra el error de raíz y ofrece elegir otra carpeta.
- **Carpeta sincronizada** (iCloud, OneDrive, Dropbox): la escritura atómica (temp + rename) puede generar copias en conflicto del sincronizador si dos equipos editan; fuera del control de Dbacus. [No verificado] con ningún sincronizador real.
- **Ediciones simultáneas**: ⚑D9 y el mutex por ruta; el segundo en guardar recibe `changed_on_disk`.
- **Mover una carpeta con archivos abiertos**: `Move` devuelve los `PathChange` y el store actualiza `filePath` de cada pestaña afectada; ninguna queda apuntando a una ruta vieja.
- **Borrar un archivo abierto**: la pestaña conserva el texto y muestra «El archivo ya no existe».
- **Restauración al arrancar**: una pestaña con `FilePath` relee el archivo; si no existe, banner «ya no existe»; si existe y su versión difiere de la que tenía la pestaña, banner de cambio en disco; si no, el punto depende de `sql !== baseSql` (el borrador se conserva).
- **Archivos muy grandes o binarios renombrados a `.sql`**: `too_large`/`not_utf8`, la lista lo marca pero no rompe el panel.
- **Raíz inaccesible a mitad de sesión** (disco externo, carpeta borrada): `List` falla con `INTERNAL` y el panel muestra el error con «Elegir otra carpeta»; las pestañas abiertas no pierden texto.
- **Modo servidor** (`./bin/dbacus-server`): `PickRoot` y `Reveal` devuelven `UNSUPPORTED` y la UI lo dice (igual que la exportación).

## Criterios de aceptación
- [x] Guardar una consulta crea un `.sql` en la carpeta raíz y la pestaña queda vinculada y sin punto (`TestSaveCreatesFileAndHeader`, vitest «⌘S sobre una pestaña sin archivo…», E2E «⌘S guarda en la carpeta…»).
- [ ] La ruta de la carpeta se ve en el panel y en Ajustes (E2E, hecho) y «Mostrar en el explorador» la abre (`TestRevealResolvesInsideRoot` cubre la ruta; **falta verlo en los tres sistemas**).
- [x] El panel lista lo guardado, incluidas subcarpetas, y la búsqueda encuentra por nombre y por contenido (`TestSearchNameAndContent`, E2E «busca por nombre y, si se marca, dentro del SQL»).
- [x] Ninguna ruta puede salir de la raíz: `..`, absolutas, `\`, NUL, enlaces simbólicos y nombres reservados de Windows se rechazan (`TestResolveRejectsEscapes`, `TestSymlinksAreNotFollowed`, `TestReservedNames`).
- [x] Nombres con tildes, ñ, emojis y espacios funcionan; dos nombres que difieren solo en mayúsculas se tratan como el mismo (`TestUnicodeNamesAndCaseInsensitiveUniqueness`).
- [x] Un archivo editado fuera de la app no se pisa en silencio: `Save` devuelve `changed_on_disk` y la UI ofrece recargar, mantener o sobrescribir (`TestSaveDetectsChangeOnDisk`, vitest, E2E «un archivo editado fuera de la app no se pisa»).
- [x] Abrir o restaurar una consulta no ejecuta nada (`TestOpenNeverExecutes`: el driver falso no recibe sentencias).
- [x] En un perfil de solo lectura se puede guardar y abrir, pero ejecutar un `DROP` guardado sigue rechazándose antes de enviarse (`TestSavedQueryStillHitsReadOnlyGuard`, E2E «solo lectura»).
- [x] La cabecera sobrevive al viaje de ida y vuelta, el editor no la ve y el archivo sigue siendo SQL válido sin ella (`TestHeaderRoundTrip`, `TestHeaderKeepsUnknownKeysAndBlankBody`, `TestFileWithoutHeaderIsPlainSQL`); el fin de línea `\r\n` se conserva (`TestCRLFPreserved`).
- [x] Un archivo no UTF-8 o de más de 2 MiB no se abre y no se corrompe (`TestNotUTF8AndTooLarge`).
- [x] Un fallo de escritura a medias nunca deja el archivo truncado (`TestAtomicWriteLeavesOriginalOnFailure`).
- [x] Mover o renombrar una carpeta con archivos abiertos actualiza las rutas de sus pestañas (`TestMoveFolderReturnsPathChanges`, vitest «mover o renombrar actualiza las rutas…», E2E «renombrar la pestaña renombra el archivo…»).
- [x] Borrar mueve a la papelera, no a la nada; la papelera no aparece en el panel y se purga a los 30 días (`TestDeleteMovesToTrash`, `TestTrashPurge`, `TestPurgeTrashDoesNotCreateTheRoot`).
- [x] Cambiar la raíz no mueve ni borra nada; restablecer vuelve a la de por defecto (`TestPickRootDoesNotMoveFiles`, `TestPickRootRejectsHomeAndUnsupported`).
- [ ] Una pestaña con texto sin guardar muestra el punto y pregunta al cerrarse (E2E, hecho); al cerrar y reabrir la app conserva el borrador con el punto (vitest de restauración, hecho; **el E2E con recarga no es posible con el backend simulado en memoria**, queda en el smoke manual).
- [x] «Guardar como consulta» desde el historial abre el diálogo con el SQL de la entrada (`HistoryPanel.test.ts`, vitest del store).
- [x] Sin SQL en logs (`TestQueryFilesLogsNoSQL`).
- [x] `es` y `en` con las mismas claves (bloque `queries` en ambos); E2E en verde 4 veces seguidas con el Chrome instalado (74 tests). Antes hubo dos ejecuciones con fallos intermitentes (21 y 1 test) que no se reprodujeron; causa sin determinar, probablemente arranque en frío del build o carga de la máquina. **Falta probarlo con el Chromium del CI.**

## Pruebas
- Go (`internal/queryfiles`, directorio temporal): tabla de rutas hostiles, enlaces simbólicos, nombres reservados y Unicode, escritura atómica con fallo inyectado, versiones, cabecera, EOL, papelera con reloj inyectado, 10 000 archivos y `Truncated`.
- Go (`internal/services`): `QueryFilesService` con perfiles falsos (validación de `profileId`, `ProfileMissing`), solo lectura con el driver falso y `TestOpenNeverExecutes`.
- Vitest: store de pestañas (vínculo, punto, cierre, banners, `PathChange`), panel, diálogo Guardar.
- E2E (mock): guardar → aparece en el panel → cerrar pestaña → reabrir desde el panel; conflicto con el disco; búsqueda por contenido.
- **Smoke manual** (`docs/smoke-manual.md`, a añadir): aviso de Documentos en macOS, «Mostrar en el explorador» en los tres sistemas, editar el `.sql` desde otro editor y volver a la app, carpeta dentro de iCloud/OneDrive, nombre con caracteres reservados en Windows.

## Evolución (no se implementa ahora)
Vigilante de disco (`fsnotify`), restaurar desde la papelera, favoritos y etiquetas, abrir con la paleta `⌘K`, plantillas con parámetros, exportar/importar una carpeta, ejecutar una consulta guardada sin abrirla.
