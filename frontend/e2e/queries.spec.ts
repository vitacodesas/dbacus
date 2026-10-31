import { connect, expect, mod, SAKILA, test } from "./fixtures";
import type { Page } from "@playwright/test";

// Consultas guardadas como archivos .sql (spec 20), con el backend simulado.
async function setSql(page: Page, sql: string) {
  const editor = page.locator(".monaco-editor").first();
  await editor.click();
  await page.keyboard.press(`${mod}+a`);
  await page.keyboard.insertText(sql);
}
const editorText = (page: Page) => page.locator(".monaco-editor .view-lines").first();
const openPanel = async (page: Page) => {
  if (!(await page.getByTestId("queries-panel").isVisible())) await page.getByRole("button", { name: "Consultas", exact: true }).click();
  await expect(page.getByTestId("queries-panel")).toBeVisible();
};
const newTab = async (page: Page) => {
  await page.keyboard.press(`${mod}+t`);
  await expect(page.locator(".monaco-editor").first()).toBeVisible();
};

test.describe("consultas guardadas", () => {
  test("⌘S guarda en la carpeta, el panel lo muestra y se reabre desde ahí", async ({ app: page }) => {
    await connect(page);
    await newTab(page);
    await setSql(page, "SELECT total FROM pedidos");
    await expect(page.getByTestId("tab-dirty")).toBeVisible();

    await page.keyboard.press(`${mod}+s`);
    const dialog = page.getByTestId("save-query-dialog");
    await expect(dialog).toBeVisible();
    await dialog.getByTestId("save-query-name").fill("ventas");
    // La ruta final completa se ve antes de guardar.
    await expect(dialog.getByTestId("save-query-path")).toHaveText("/Users/demo/Documentos/Dbacus/Consultas/ventas.sql");
    await page.getByTestId("save-query-submit").click();
    await expect(page.getByText("Guardada en ventas.sql")).toBeVisible();
    await expect(page.getByTestId("tab-dirty")).toHaveCount(0);
    await expect(page.getByTestId("tab-ventas")).toBeVisible();

    // El panel lo lista, con la ruta de la carpeta siempre a la vista.
    await openPanel(page);
    await expect(page.locator('[data-path="ventas.sql"]')).toBeVisible();
    await expect(page.getByTestId("queries-root")).toContainText("Consultas");

    // Cerrar la pestaña (ya guardada, no pregunta) y reabrirla desde el panel.
    await page.getByRole("button", { name: "Cerrar pestaña: ventas" }).click();
    await expect(page.getByTestId("tab-close-message")).toHaveCount(0);
    await page.locator('[data-path="ventas.sql"]').dblclick();
    await expect(page.getByTestId("tab-ventas")).toBeVisible();
    await expect(editorText(page)).toContainText("SELECT total FROM pedidos");
    // Abrirla no ejecutó nada: no hay resultados.
    await expect(page.getByTestId("tab-dirty")).toHaveCount(0);
  });

  test("cerrar una pestaña con texto sin guardar pregunta; «No guardar» la cierra", async ({ app: page }) => {
    await connect(page);
    await newTab(page);
    await setSql(page, "SELECT 1");
    await openPanel(page);
    // La sección «Sin guardar» lista lo que está abierto sin archivo.
    await expect(page.getByTestId("queries-unsaved")).toContainText("Consulta 1");

    await page.getByRole("button", { name: "Cerrar pestaña: Consulta 1" }).click();
    await expect(page.getByTestId("tab-close-message")).toContainText("no se ha guardado");
    await page.getByTestId("tab-close-cancel").click();
    await expect(page.getByTestId("tab-Consulta 1")).toBeVisible();

    await page.getByRole("button", { name: "Cerrar pestaña: Consulta 1" }).click();
    await page.getByTestId("tab-close-discard").click();
    await expect(page.getByTestId("tab-Consulta 1")).toHaveCount(0);
  });

  test("«Guardar» en la pregunta de cierre abre el diálogo y cierra al guardar", async ({ app: page }) => {
    await connect(page);
    await newTab(page);
    await setSql(page, "SELECT 2");
    await page.getByRole("button", { name: "Cerrar pestaña: Consulta 1" }).click();
    await page.getByTestId("tab-close-save").click();
    await page.getByTestId("save-query-name").fill("dos");
    await page.getByTestId("save-query-submit").click();
    await expect(page.getByTestId("tab-dos")).toHaveCount(0);
    await openPanel(page);
    await expect(page.locator('[data-path="dos.sql"]')).toBeVisible();
  });

  test("un nombre repetido pide sobrescribir de forma explícita", async ({ app: page }) => {
    await connect(page);
    await newTab(page);
    await setSql(page, "SELECT 1");
    await page.keyboard.press(`${mod}+s`);
    await page.getByTestId("save-query-name").fill("x");
    await page.getByTestId("save-query-submit").click();
    await expect(page.getByText("Guardada en x.sql")).toBeVisible();
    await page.keyboard.press(`${mod}+Shift+s`);
    await page.getByTestId("save-query-name").fill("X");
    await page.getByTestId("save-query-submit").click();
    await expect(page.getByTestId("save-query-exists")).toBeVisible();
    await page.getByTestId("save-query-overwrite").click();
    await expect(page.getByTestId("save-query-dialog")).toHaveCount(0);
  });
});

test.describe("consultas guardadas · carpetas y búsqueda", () => {
  test.use({
    seed: {
      profiles: [SAKILA],
      queries: [
        { path: "Informes/ventas", sql: "-- ventas\nSELECT total FROM pedidos", profileId: SAKILA.id, profileName: SAKILA.name, database: "sakila" },
        { path: "Informes/clientes", sql: "SELECT nombre FROM clientes" },
        { path: "suelta", sql: "SELECT 1" },
      ],
    },
  });

  test("el árbol agrupa por carpetas y abre con la conexión y la base de la cabecera", async ({ app: page }) => {
    await connect(page);
    await openPanel(page);
    await expect(page.locator('[data-path="Informes"]')).toBeVisible();
    await expect(page.locator('[data-path="Informes/ventas.sql"]')).toBeVisible();
    await page.locator('[data-path="Informes"]').click(); // pliega
    await expect(page.locator('[data-path="Informes/ventas.sql"]')).toHaveCount(0);
    await page.locator('[data-path="Informes"]').click();
    await page.locator('[data-path="Informes/ventas.sql"]').dblclick();
    await expect(page.getByTestId("tab-ventas")).toBeVisible();
    await expect(editorText(page)).toContainText("SELECT total FROM pedidos");
  });

  test("busca por nombre y, si se marca, dentro del SQL", async ({ app: page }) => {
    await connect(page);
    await openPanel(page);
    await page.getByTestId("queries-search").fill("clientes");
    await expect(page.getByTestId("queries-hit")).toHaveCount(1);
    await page.getByTestId("queries-search").fill("TOTAL");
    await expect(page.getByTestId("queries-hits")).toContainText("Ninguna consulta coincide");
    await page.getByTestId("queries-search-content").locator("input").check();
    await expect(page.getByTestId("queries-hit")).toHaveCount(1);
    await expect(page.getByTestId("queries-hits")).toContainText("2: SELECT total FROM pedidos");
    await page.getByTestId("queries-hit").click();
    await expect(page.getByTestId("tab-ventas")).toBeVisible();
  });

  test("renombrar la pestaña renombra el archivo; mover una carpeta mantiene las pestañas apuntando bien", async ({ app: page }) => {
    await connect(page);
    await openPanel(page);
    await page.locator('[data-path="suelta.sql"]').dblclick();
    await page.getByTestId("tab-suelta").dblclick();
    await page.getByLabel("Renombrar").fill("renombrada");
    await page.keyboard.press("Enter");
    await expect(page.getByTestId("tab-renombrada")).toBeVisible();
    await expect(page.locator('[data-path="renombrada.sql"]')).toBeVisible();

    await page.locator('[data-path="Informes/clientes.sql"]').dblclick();
    await page.locator('[data-path="Informes"]').click({ button: "right" });
    await page.getByRole("menuitem", { name: "Renombrar" }).click();
    await page.getByTestId("query-entry-name").fill("Reportes");
    await page.getByTestId("query-entry-submit").click();
    await expect(page.locator('[data-path="Reportes/clientes.sql"]')).toBeVisible();
    // La pestaña sigue vinculada a su archivo (sin aviso de «ya no existe»).
    await page.getByTestId("tab-clientes").click();
    await expect(page.getByTestId("file-banner")).toHaveCount(0);
    await page.keyboard.press(`${mod}+s`);
    await expect(page.getByText("«clientes» guardada")).toBeVisible();
  });

  test("eliminar pide confirmación, deja el texto en la pestaña y permite guardar de nuevo", async ({ app: page }) => {
    await connect(page);
    await openPanel(page);
    await page.locator('[data-path="suelta.sql"]').dblclick();
    await page.locator('[data-path="suelta.sql"]').click({ button: "right" });
    await page.getByRole("menuitem", { name: "Eliminar" }).click();
    await page.getByRole("button", { name: "Eliminar", exact: true }).click();
    await expect(page.locator('[data-path="suelta.sql"]')).toHaveCount(0);
    await expect(page.getByTestId("file-banner")).toHaveAttribute("data-state", "missing");
    await expect(editorText(page)).toContainText("SELECT 1");
    await page.getByTestId("banner-save-again").click();
    await expect(page.getByTestId("file-banner")).toHaveCount(0);
    await expect(page.locator('[data-path="suelta.sql"]')).toBeVisible();
  });

  test("un archivo editado fuera de la app no se pisa: aviso con recargar / mantener / sobrescribir", async ({ app: page }) => {
    await connect(page);
    await openPanel(page);
    await page.locator('[data-path="Informes/clientes.sql"]').dblclick();
    await setSql(page, "SELECT mi_edicion");
    // Otro programa modifica el archivo.
    await page.evaluate(() => (window as unknown as { __mock: { queryFiles: { touch(p: string, s: string): void } } }).__mock.queryFiles.touch("Informes/clientes.sql", "SELECT desde_fuera"));
    await page.keyboard.press(`${mod}+s`);
    const banner = page.getByTestId("file-banner");
    await expect(banner).toHaveAttribute("data-state", "changed");
    // Nada se escribió: el disco sigue con lo de fuera.
    expect(await page.evaluate(() => (window as unknown as { __mock: { queryFiles: { files: Map<string, { sql: string }> } } }).__mock.queryFiles.files.get("Informes/clientes.sql")!.sql)).toBe("SELECT desde_fuera");

    await page.getByTestId("banner-reload").click();
    await expect(banner).toHaveCount(0);
    await expect(editorText(page)).toContainText("SELECT desde_fuera");
    await expect(page.getByTestId("tab-dirty")).toHaveCount(0);
  });

  test("la carpeta de consultas se ve y se cambia desde Ajustes sin mover archivos", async ({ app: page }) => {
    await connect(page);
    await page.getByTestId("open-settings").click();
    await expect(page.getByTestId("settings-queries-path")).toContainText("/Users/demo/Documentos/Dbacus/Consultas");
    await page.getByTestId("settings-queries-change").click();
    await expect(page.getByTestId("settings-queries-path")).toContainText("/Users/demo/Otra carpeta");
  });
});

test.describe("consultas guardadas · solo lectura", () => {
  test.use({ seed: { profiles: [{ ...SAKILA, readOnly: true }] } });

  test("se puede guardar un DROP, pero ejecutarlo se sigue rechazando", async ({ app: page }) => {
    await connect(page);
    await newTab(page);
    await setSql(page, "DROP TABLE actor");
    await page.keyboard.press(`${mod}+s`);
    await page.getByTestId("save-query-name").fill("peligro");
    await page.getByTestId("save-query-submit").click();
    await expect(page.getByText("Guardada en peligro.sql")).toBeVisible();
    await page.keyboard.press(`${mod}+Enter`);
    await expect(page.getByText(/solo lectura/i).first()).toBeVisible();
  });
});
