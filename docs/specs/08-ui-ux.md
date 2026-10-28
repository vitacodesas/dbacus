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
`BaseButton`, `BaseInput`, `BaseSelect`, `BaseCheckbox`, `BaseToggle`, `BaseModal`, `BaseDialogConfirm`, `BaseTabs`, `BaseTooltip`, `BaseContextMenu`, `BaseToast`, `BaseSplitter`, `BaseSpinner`, `BaseEmptyState`, `BaseKbd`. Sin librería de UI pesada (permitido headless: Reka UI/Radix-Vue para accesibilidad de menús/diálogos).

## Componentes de dominio
`ConnectionList`, `ConnectionCard`, `ConnectionForm` (+ `TlsSection`, `SshSection`, `AdvancedSection`, `TestResultPanel`, `HostKeyTrustDialog`), `SchemaTree`, `QueryTabsBar`, `SqlEditor`, `ResultGrid`, `ResultTabs`, `CellViewerModal`, `HistoryPanel`, `ExportDialog`, `StatusBar`, `SettingsDialog`.

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
