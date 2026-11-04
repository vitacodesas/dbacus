# ADR-006 — Librería de gráficos de series de tiempo para el módulo Monitor

**Estado**: aceptada (2026-11-04, T16.6) · detalle en el [spec 22](../22-monitoreo-dba.md) ⚑D13

**Contexto**: el spec 22 necesita gráficas de series de tiempo con huecos, anotaciones, zoom por rango y cientos de puntos por serie, en modo claro y oscuro. [Seguro] `frontend/package.json` no tiene ninguna librería de gráficos y `AGENTS.md` obliga a un ADR para cambiar el stack. Go ya entrega ≤ 300 puntos por serie (spec 22 ⚑D11), así que el cliente no necesita agregar ni muestrear.

**Decisión**: usar **uPlot** (canvas, sin dependencias) con **versión exacta pineada** en `package.json`, envuelto en un único componente `TimeSeriesChart.vue`; el resto de la app no importa la librería. Versión instalada: `uplot` 1.6.32 (sin `^`). Componente: `frontend/src/components/monitor/TimeSeriesChart.vue`, único importador.

**Condiciones para aceptar (comprobadas en T16.6)**
- `npm audit --omit=dev`: 0 vulnerabilidades. [Seguro] `npm audit` completo marca 16 moderadas, **las mismas con y sin uPlot** (cadena de desarrollo `glob`/`minimatch` vía `eslint` y `@vue/test-utils`); uPlot no aparece. Licencia MIT ([Seguro]: `LICENSE` y `package.json` del paquete instalado).
- Los huecos se pintan como huecos (la línea se corta con un `null` en el punto medio y una banda «sin datos») y las anotaciones como líneas verticales. Verificado a ojo en el navegador y con tests del componente (doble de uPlot, porque jsdom no tiene canvas).
- Colores desde los tokens de la app (`--accent`, `--muted`, `--border`, `--warning`); al cambiar el tema se relee y se reconstruye. Verificado en claro y oscuro.
- Se prueba también con `wails3 dev` (ver `AGENTS.md`, notas de Monaco): el optimizador de Vite ha roto otras dependencias. **No verificado**: solo se probó con el servidor de desarrollo de Vite (con backend simulado) y `npm run build`; falta `wails3 dev` y la ventana nativa.

**Medido** (backend simulado, 5 000 muestras con 2 huecos, navegador integrado): 7 días ⇒ 285 puntos y 4 huecos; datos 7–10 ms; pintado 6–7 ms (92 ms la primera vez, por la carga diferida de la librería). No es una medición en la ventana nativa ni con datos reales de Go.

**Alternativas descartadas**
- **SVG propio**: sin dependencia nueva, pero zoom, cursor, anotaciones y rendimiento con muchas series se vuelven código a mantener. Queda como respaldo si uPlot no cumple las condiciones.
- **Chart.js / ECharts**: más pesadas y con más superficie de dependencias para lo que se necesita; ECharts duplicaría el peso del bundle.
- **D3**: pide construir cada gráfica a mano.

**Consecuencias**
- Una dependencia nueva a vigilar en `npm audit`.
- Canvas no resuelve variables CSS: el componente las lee con `getComputedStyle` y se repinta al cambiar de tema.
- La accesibilidad se resuelve con una tabla de valores alternativa (descripción y datos resumidos), porque canvas no es legible por lectores de pantalla.
