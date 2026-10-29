# 08 — UI / UX

## Layout principal
```
┌ Titlebar (nativa) ───────────────────────────────────────────┐
│ ActivityBar │ Sidebar (Conexiones | Esquema | Historial) │ Main │
│  (iconos)   │  árbol / lista                              │ Tabs │
│             │                                             │ Editor (Monaco)   ← splitter vertical
│             │                                             │ Resultados (grid)  │
├─────────────┴─────────────────────────────────────────────┴──────┤
│ StatusBar: sesión · BD · filas · tiempo · estado                  │
└──────────────────────────────────────────────────────────────────┘
```
- Sin sesión activa: pantalla de bienvenida con lista de conexiones y CTA "Nueva conexión".
- Sidebar redimensionable (240–480 px) y colapsable (`⌘/Ctrl+B`); splitter editor/resultados persistido.

## Design tokens (Tailwind)
- Tailwind con `darkMode: 'class'`; paleta neutra (slate/zinc) + acento configurable; tokens como CSS variables (`--bg`, `--panel`, `--border`, `--accent`, `--danger`) mapeadas en `tailwind.config`.
- Tipografía: UI sans del sistema; datos/editor mono (JetBrains Mono embebida, `font-variant-ligatures: none` en grid).
- Densidad compacta (filas 24 px). Radio 6 px. Foco visible siempre (a11y).
- Tema claro/oscuro/sistema; Monaco usa temas equivalentes.

## Componentes base (`components/ui`)
`BaseButton`, `BaseInput`, `BaseSelect`, `BaseCheckbox`, `BaseToggle`, `BaseModal`, `BaseDialogConfirm`, `BaseTabs`, `BaseTooltip`, `BaseContextMenu`, `BaseToast`, `BaseSplitter`, `BaseSpinner`, `BaseEmptyState`, `BaseKbd`, `ColorSwatches` (selector de color #RRGGBB, compartido por conexión y grupo). Sin librería de UI pesada (permitido headless: Reka UI/Radix-Vue para accesibilidad de menús/diálogos).

## Componentes de dominio
`ConnectionList` (con secciones de grupo), `ConnectionGroupHeader`, `ConnectionGroupDialog`, `ConnectionCard`, `ConnectionForm` (+ `TlsSection`, `SshSection`, `AdvancedSection`, `TestResultPanel`, `HostKeyTrustDialog`), `SchemaTree`, `QueryTabsBar`, `SqlEditor`, `ResultGrid`, `ResultTabs`, `CellViewerModal`, `HistoryPanel`, `ExportDialog`, `StatusBar` (lleva la firma «Hecho con amor por vitacodesas» en la esquina inferior izquierda, spec 17), `SettingsDialog`.

## Atajos globales
| Acción | macOS | Win/Linux |
|---|---|---|
| Nueva pestaña | ⌘T | Ctrl+T |
| Cerrar pestaña | ⌘W | Ctrl+W |
| Ejecutar todo | ⌘↵ | Ctrl+Enter |
| Ejecutar selección/actual | ⌘⇧↵ | Ctrl+Shift+Enter |
| Cancelar | ⎋ / Stop | Esc / Stop |
| Paleta de comandos | ⌘K | Ctrl+K |
| Alternar sidebar | ⌘B | Ctrl+B |
| Guardar conexión (en formulario) | ⌘S | Ctrl+S |

## Accesibilidad e i18n
- Navegación por teclado completa en formulario, árbol y menús; roles ARIA correctos; contraste AA.
- Textos en `vue-i18n` desde el inicio (es por defecto; en preparado). Nada de strings en componentes.

## Feedback
Toasts para acciones (guardado, copiado); errores de operación en panel contextual con `Detail` expandible y botón "Copiar error". Operaciones >300 ms muestran spinner; >2 s muestran texto de progreso.

## Criterios de aceptación
- [ ] Todo flujo del spec 04 y 06 es completable solo con teclado.
- [ ] Cambiar tema no recarga Monaco ni pierde estado.
- [ ] Ventana mínima 960×600 sin solapamientos.

## Edición de celdas (T7.5)
Doble clic o `F2` sobre una celda editable abre un editor en la celda (`Enter` guarda y baja, `Tab` pasa a la siguiente columna editable, `Esc` cancela; si Go rechaza el valor el editor sigue abierto con el motivo). Menú contextual: «Editar celda», «Establecer NULL» (solo si la columna lo admite), «Revertir celda». Las celdas con cambio pendiente llevan fondo ámbar y una marca a la izquierda. Barra bajo el grid con «N cambios sin aplicar · Ver SQL · Descartar · Aplicar». Si el resultado no es editable, el pie del grid explica por qué y la cabecera de cada columna no editable lo indica en su tooltip.

## Filas nuevas y borrados (T8.4)
«Nueva fila» (barra del resultado), «Insertar nueva fila a partir de esta» y «Eliminar fila(s)»/«Restaurar» (menú contextual; `Supr`/`⌫` también elimina). Filas nuevas en verde, borradas en rojo y tachadas, celdas sin valor como «DEFAULT» y las obligatorias vacías con borde rojo. La barra de cambios dice «N cambios sin aplicar · M filas nuevas · K filas a borrar». El diálogo de revisión etiqueta cada sentencia (Modificar/Insertar/Borrar) y avisa en un texto fijo si un borrado arrastra filas de otras tablas por claves foráneas.
