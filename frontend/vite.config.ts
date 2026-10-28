import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";
import wails from "@wailsio/runtime/plugins/vite";

export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  // El "exports" de monaco-editor no expone esm/vs/...: se apunta directo a los archivos para importar solo lo que usamos.
  resolve: { alias: [{ find: /^monaco-editor\/esm\/(.*)$/, replacement: fileURLToPath(new URL("./node_modules/monaco-editor/esm/$1", import.meta.url)) }] },
  // Monaco pesa ~3,8 MB en un solo chunk: es una app de escritorio con assets locales, no se sirve por red (se revisa en T6.3).
  build: { chunkSizeWarningLimit: 4000 },
  plugins: [vue(), tailwindcss(), wails("./bindings")],
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts"],
  },
});
