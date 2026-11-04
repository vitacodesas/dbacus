import { connect, expect, mod, test, SAKILA } from "./fixtures";
import type { Page } from "@playwright/test";

// Monitoreo DBA (spec 22), con el backend simulado. El mock devuelve un ranking y 5 000 muestras con huecos de ejemplo.
const monitor = (page: Page) => page.getByTestId("monitor-view");

async function openMonitor(page: Page) {
  await connect(page);
  await page.getByTestId("open-monitor").click();
  await expect(monitor(page)).toBeVisible();
}
const tab = (page: Page, name: string) => page.getByRole("tab", { name, exact: true }).click();
const readClipboard = (page: Page) => page.evaluate(() => navigator.clipboard.readText());

test.beforeEach(async ({ context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
});

test.describe("monitor DBA", () => {
  test.use({ seed: { profiles: [SAKILA], monitor: { sampleMs: 150 } } });

  test("iniciar y detener: estado, cronómetro y muestras en vivo", async ({ app: page }) => {
    await openMonitor(page);
    await expect(page.getByTestId("monitor-run-state")).toContainText("Sin grabar");
    await expect(page.getByTestId("live-badge")).toContainText("no se guarda");
    await page.getByTestId("monitor-start").click();
    await expect(page.getByTestId("monitor-run-state")).toContainText("Grabando");
    await expect(page.getByTestId("live-badge")).toContainText("se está grabando");
    await expect(page.getByTestId("monitor-elapsed")).toHaveText(/^\d\d:\d\d:\d\d$/);
    await expect(page.getByTestId("monitor-stop")).toBeVisible();
    await expect(page.getByTestId("card-qps")).toContainText(/\d{3}/); // llegó un monitor:sample

    await page.getByTestId("monitor-stop").click();
    await expect(page.getByTestId("monitor-start")).toBeVisible();
    await expect(page.getByTestId("monitor-run-state")).toContainText("Sin grabar");
  });

  test("En vivo: Pausar y Reanudar controlan la lectura de procesos", async ({ app: page }) => {
    await openMonitor(page);
    await expect(page.getByTestId("process-list")).toBeVisible();
    await page.getByTestId("live-pause").click();
    await expect(page.getByTestId("live-paused")).toBeVisible();
    await expect(page.getByTestId("live-resume")).toBeVisible();
    await page.getByTestId("live-resume").click();
    await expect(page.getByTestId("live-pause")).toBeVisible();
    await expect(page.getByTestId("process-list")).toBeVisible();
  });

  test("En vivo: procesos, máximo de conexiones y «Copiar KILL»", async ({ app: page }) => {
    await openMonitor(page);
    await expect(page.getByTestId("process-list")).toBeVisible();
    const kills = page.getByTestId("copy-kill");
    expect(await kills.count()).toBeGreaterThan(3);
    await expect(page.getByTestId("card-max-connections")).toContainText("/");

    await kills.first().click();
    await expect.poll(() => readClipboard(page)).toMatch(/^KILL \d+;$/);

    await page.getByTestId("process-time").first().click(); // la fila entera abre el detalle
    await expect(page.getByTestId("process-detail")).toBeVisible();
    await page.getByTestId("copy-kill-query").click();
    await expect.poll(() => readClipboard(page)).toMatch(/^KILL QUERY \d+;$/);
  });

  test("En vivo: «Terminar…» cancela la consulta o cierra la conexión tras confirmar", async ({ app: page }) => {
    await openMonitor(page);
    await expect(page.getByTestId("process-list")).toBeVisible();
    const before = await page.getByTestId("kill-open").count();
    expect(before).toBeGreaterThan(3);

    // Cancelar no hace nada.
    await page.getByTestId("kill-open").first().click();
    await expect(page.getByTestId("kill-dialog")).toBeVisible();
    await expect(page.getByTestId("kill-mode-query")).toBeChecked();
    await page.getByTestId("kill-cancel").click();
    await expect(page.getByTestId("kill-dialog")).toHaveCount(0);

    // KILL (cierra la conexión): la fila desaparece de la vista en vivo.
    const idText = await page.getByTestId("kill-open").first().evaluate((el) => el.closest("[data-testid^=process-]")!.getAttribute("data-testid"));
    await page.getByTestId("kill-open").first().click();
    await page.getByTestId("kill-mode-conn").check();
    await page.getByTestId("kill-confirm").click();
    await expect(page.getByTestId(idText!)).toHaveCount(0);
    await expect(page.getByTestId("kill-open")).toHaveCount(before - 1);
  });

  test("Top queries: cambia de criterio y marca la regresión al comparar", async ({ app: page }) => {
    await openMonitor(page);
    await tab(page, "Top queries");
    const first = () => page.locator('[data-testid^="top-row-"]').first();
    await expect(page.getByTestId("top-list")).toBeVisible();
    await expect(first()).toHaveAttribute("data-testid", "top-row-d1"); // por tiempo total
    const compare = page.getByLabel("Comparar con el periodo anterior");
    await expect(compare).toBeChecked(); // por defecto compara con el periodo previo
    await compare.uncheck();
    await expect(page.getByTestId("chip-regression")).toHaveCount(0); // sin comparar no hay cambio

    await page.getByTestId("top-order-execs").click();
    await expect(first()).toHaveAttribute("data-testid", "top-row-d3"); // 2,4 M de ejecuciones

    await compare.check();
    await expect(page.getByTestId("top-summary-regressions")).toContainText("1 regresiones");
    await expect(page.getByTestId("top-row-d1").getByTestId("chip-regression")).toBeVisible();
    await expect(page.getByTestId("top-row-d4").getByTestId("chip-new")).toBeVisible();

    await page.getByTestId("top-row-d1").click();
    await expect(page.getByTestId("digest-detail")).toBeVisible();
    await expect(page.getByTestId("digest-text")).toContainText("orders");
    await page.getByTestId("digest-copy").click();
    await expect.poll(() => readClipboard(page)).toContain("orders");
  });

  test("Métricas: los huecos se rotulan y las anotaciones se añaden y se borran", async ({ app: page }) => {
    await openMonitor(page);
    await tab(page, "Métricas");
    await page.getByLabel("Rango", { exact: true }).selectOption("7d");
    const qps = page.getByTestId("chart-qps");
    await expect(qps).toBeVisible();
    await expect(qps.getByRole("img")).toHaveAttribute("aria-label", /[1-9]\d* huecos sin datos/);

    await page.getByTestId("metrics-add-note").click();
    await page.getByTestId("note-input").fill("despliegue v2.14");
    await page.getByTestId("note-save").click();
    const note = page.getByTestId("metrics-annotations").locator("li", { hasText: "despliegue v2.14" });
    await expect(note).toBeVisible();

    await note.getByTestId("note-delete").click();
    await expect(page.getByText("despliegue v2.14")).toHaveCount(0);
  });

  test("Ejecuciones: avisa de servidores distintos y «Borrar datos» lo vacía", async ({ app: page }) => {
    await openMonitor(page);
    await tab(page, "Ejecuciones");
    await expect(page.getByTestId("runs-table")).toBeVisible();
    await expect(page.getByTestId("runs-mixed")).toBeVisible(); // la última ejecución de ejemplo viene de otro servidor
    await expect(page.getByTestId("run-h2").getByTestId("run-reason")).toHaveText("App cerrada");

    await page.getByTestId("runs-purge").click();
    await page.getByRole("dialog").getByRole("button", { name: "Borrar datos" }).click();
    await expect(page.getByText("Datos de monitoreo borrados")).toBeVisible();
    await expect(page.getByTestId("runs-empty")).toBeVisible();
  });

  test("el Monitor no desmonta las pestañas de consulta", async ({ app: page }) => {
    await connect(page);
    await page.keyboard.press(`${mod}+t`);
    const editor = page.locator(".monaco-editor").first();
    await editor.click();
    await page.keyboard.insertText("SELECT 42");
    await page.evaluate(() => ((window as unknown as { __ed: Element | null }).__ed = document.querySelector(".monaco-editor")));

    await page.getByTestId("open-monitor").click();
    await expect(monitor(page)).toBeVisible();
    await page.getByTestId("open-monitor").click(); // volver a las consultas
    await expect(monitor(page)).toBeHidden();
    await expect(editor).toBeVisible();
    expect(await page.evaluate(() => (window as unknown as { __ed: Element | null }).__ed === document.querySelector(".monaco-editor"))).toBe(true);
    await expect(page.locator(".monaco-editor").first()).toContainText("SELECT 42");
  });
});

test.describe("monitor DBA: fuentes no disponibles", () => {
  test.describe("performance_schema apagado", () => {
    test.use({ seed: { profiles: [SAKILA], monitor: { caps: "perf_schema_off" } } });
    test("avisa por fuente, explica que requiere reinicio y los procesos siguen", async ({ app: page }) => {
      await openMonitor(page);
      const banner = page.getByTestId("banner-digest");
      await expect(banner).toBeVisible();
      await expect(banner).toContainText("performance_schema=ON");
      await expect(banner).toContainText("reiniciarlo");
      await expect(page.getByTestId("process-list")).toBeVisible(); // En vivo no depende del digest

      await tab(page, "Top queries");
      await expect(page.getByTestId("source-empty")).toContainText("Ranking de consultas: no disponible");
      await expect(page.getByTestId("top-list")).toHaveCount(0);
    });
  });

  test.describe("sin privilegio", () => {
    test.use({ seed: { profiles: [SAKILA], monitor: { caps: "no_privilege" } } });
    test("muestra el GRANT copiable y no ejecuta nada", async ({ app: page }) => {
      await openMonitor(page);
      await tab(page, "Top queries");
      await expect(page.getByTestId("source-empty-hint")).toContainText("GRANT SELECT ON performance_schema.*");
      await page.getByTestId("source-empty-copy").click();
      await expect.poll(() => readClipboard(page)).toContain("GRANT SELECT ON performance_schema.*");
    });
  });

  test.describe("sin PROCESS", () => {
    test.use({ seed: { profiles: [SAKILA], monitor: { caps: "own_only" } } });
    test("avisa de que solo se ven los procesos propios", async ({ app: page }) => {
      await openMonitor(page);
      await expect(page.getByTestId("banner-processlist")).toContainText("solo se ven los procesos de tu propio usuario");
      await expect(page.getByTestId("banner-processlist").getByTestId("copy-hint")).toBeVisible();
    });
  });
});

test.describe("monitor DBA: perfil de solo lectura", () => {
  test.use({ seed: { profiles: [{ ...SAKILA, readOnly: true }], monitor: { sampleMs: 150 } } });
  test("monitorear está permitido", async ({ app: page }) => {
    await openMonitor(page);
    await page.getByTestId("monitor-start").click();
    await expect(page.getByTestId("monitor-run-state")).toContainText("Grabando");
    await expect(page.getByTestId("monitor-error")).toHaveCount(0);
    await page.getByTestId("monitor-stop").click();
    await expect(page.getByTestId("monitor-start")).toBeVisible();
  });
});
