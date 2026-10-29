import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";
import wails from "@wailsio/runtime/plugins/vite";

// VITE_MOCK_API=1: la UI se sirve sin Wails, con un backend simulado en memoria (E2E con Playwright, spec 11).
const mock = process.env.VITE_MOCK_API === "1";
const src = (p: string) => fileURLToPath(new URL(`./src/${p}`, import.meta.url));

// CSP estricta solo en el build (spec 10): en dev, Vite necesita scripts inline y WebSocket para el HMR. Monaco inyecta
// <style> (de ahí 'unsafe-inline' solo en estilos) y trabaja con workers locales de este mismo origen, sin CDN ni eval.
const CSP = [
  "default-src 'self'", "script-src 'self'", "style-src 'self' 'unsafe-inline'", "img-src 'self' data: blob:", "font-src 'self' data:",
  "worker-src 'self' blob:", "connect-src 'self'", "object-src 'none'", "base-uri 'none'", "form-action 'none'",
].join("; ");
const cspPlugin = {
  name: "dbacus-csp",
  apply: "build" as const,
  transformIndexHtml: (html: string) => html.replace("<head>", `<head>\n    <meta http-equiv="Content-Security-Policy" content="${CSP}">`),
};

export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  // El "exports" de monaco-editor no expone esm/vs/...: se apunta directo a los archivos para importar solo lo que usamos.
  resolve: { alias: [
    ...(mock
      ? [
          { find: /^@wailsio\/runtime$/, replacement: src("mock/runtime.ts") },
          { find: /^.*bindings\/github\.com\/vitacodesas\/dbacus\/internal\/services$/, replacement: src("mock/services.ts") },
        ]
      : []),
    { find: /^monaco-editor\/esm\/(.*)$/, replacement: fileURLToPath(new URL("./node_modules/monaco-editor/esm/$1", import.meta.url)) },
  ] },
  // Monaco (~3,4 MB) va en su propio chunk, que solo se carga al abrir una consulta (QueryWorkspace); el resto de la app
  // pesa ~450 kB. Es una app de escritorio con assets locales, así que el aviso de tamaño solo molestaría.
  build: { chunkSizeWarningLimit: 4000 },
  plugins: [vue(), tailwindcss(), cspPlugin, ...(mock ? [] : [wails("./bindings")])],
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts"],
  },
});
