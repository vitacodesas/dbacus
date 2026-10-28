import { expect, test as base, type Page } from "@playwright/test";

export interface MockSeed {
  profiles?: Record<string, unknown>[];
  latencyMs?: number;
}

/** Conexión de ejemplo ya guardada (el backend simulado empieza vacío en cada carga). */
export const SAKILA = { id: "p-sakila", name: "Sakila local", host: "127.0.0.1", user: "root", database: "sakila", savePassword: true, hasPassword: true };

export const test = base.extend<{ seed: MockSeed; app: Page; csp: string[] }>({
  // Violaciones de CSP vistas en la consola; al terminar cada test deben ser cero (salvo que el test las provoque a propósito).
  csp: async ({}, use) => {
    const seen: string[] = [];
    await use(seen);
    expect(seen, "la CSP del build bloqueó algo que la app necesita").toEqual([]);
  },
  seed: [{ profiles: [SAKILA] }, { option: true }],
  app: async ({ page, seed, csp }, use) => {
    await page.addInitScript((s) => ((window as unknown as { __MOCK_SEED__: unknown }).__MOCK_SEED__ = s), seed);
    page.on("pageerror", (e) => {
      throw e;
    });
    // La CSP del build no debe bloquear nada que la app necesite (Monaco, fuentes, workers).
    page.on("console", (m) => {
      if (/Content Security Policy|Refused to/i.test(m.text())) csp.push(m.text());
    });
    await page.goto("/");
    await use(page);
  },
});

export { expect };

// Los specs corren en Node: no se añaden los tipos completos de Node al tsconfig del front solo para esto.
declare const process: { platform: string };
/** Tecla de acceso rápido: ⌘ en macOS, Ctrl en el resto. */
export const mod = process.platform === "darwin" ? "Meta" : "Control";

/** Conecta con el perfil de ejemplo y espera a que la sesión aparezca en la barra de estado. */
export async function connect(page: Page, name = SAKILA.name) {
  await page.getByText(name, { exact: true }).first().dblclick();
  await expect(page.getByTestId("status-session")).toContainText(name);
}
