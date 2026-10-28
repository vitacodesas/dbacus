import { defineConfig } from "@playwright/test";

declare const process: { env: Record<string, string | undefined> };

const PORT = 5199;

// E2E contra el front con el backend simulado (VITE_MOCK_API, spec 11). En local usa el Chrome instalado
// (PW_CHANNEL=chrome) para no descargar navegadores; en CI se instala chromium con `npx playwright install chromium`.
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    channel: process.env.PW_CHANNEL || undefined,
    trace: "retain-on-failure",
    viewport: { width: 1280, height: 800 },
  },
  // Por defecto se prueba el BUILD (con su CSP y el Monaco en chunk aparte), que es lo que se empaqueta.
  // E2E_DEV=1 usa el servidor de desarrollo de Vite: más rápido para iterar, pero sin CSP.
  webServer: {
    command: process.env.E2E_DEV ? `npx vite --port ${PORT}` : `npx vite build --outDir dist-e2e --emptyOutDir && npx vite preview --outDir dist-e2e --port ${PORT} --strictPort`,
    env: { VITE_MOCK_API: "1", WAILS_VITE_PORT: String(PORT) },
    url: `http://127.0.0.1:${PORT}`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
