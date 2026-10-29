import { connect, expect, mod, SAKILA, test } from "./fixtures";
import type { Page } from "@playwright/test";

// Spec 18: paginación por cursor. El backend simulado pagina `SELECT * FROM <tabla con clave>` (seed.paging).
async function runSql(page: Page, sql: string, perPage?: string) {
  await page.keyboard.press(`${mod}+t`);
  if (perPage) await page.getByTestId("limit-select").selectOption({ label: perPage });
  const editor = page.locator(".monaco-editor").first();
  await expect(editor).toBeVisible();
  await editor.click();
  await page.keyboard.insertText(sql);
  await page.keyboard.press(`${mod}+Enter`);
}
declare const process: { env: Record<string, string | undefined> };
const range = (page: Page) => page.getByTestId("range");

test.describe("paginación por cursor", () => {
  test.use({ seed: { profiles: [SAKILA], paging: true } });

  test("recorre la tabla con ◀ ▶ ⏮ ⏭ y lo muestra en el pie", async ({ app: page }, info) => {
    await connect(page);
    await runSql(page, "SELECT * FROM actor", "20");

    await expect(range(page)).toContainText("Filas 1–");
    await expect(range(page)).toContainText("Página 1");
    await expect(page.getByText("NOMBRE1", { exact: true })).toBeVisible();
    await expect(page.getByTestId("page-prev")).toBeDisabled();
    await expect(page.getByTestId("page-first")).toBeDisabled();

    await page.getByTestId("page-next").click();
    await expect(range(page)).toContainText("Página 2");
    await expect(page.getByText("NOMBRE21", { exact: true })).toBeVisible();
    await expect(page.getByText("NOMBRE1", { exact: true })).toBeHidden();
    await expect(page.getByTestId("page-prev")).toBeEnabled();
    if (process.env.PAGING_SHOT) await page.screenshot({ path: info.outputPath("paging.png") });

    await page.getByTestId("page-last").click(); // 200 filas → la última página empieza en 181
    await expect(page.getByText("NOMBRE181", { exact: true })).toBeVisible();
    await expect(range(page)).not.toContainText("Página"); // tras «Última» no se conoce el número
    await expect(page.getByTestId("page-next")).toBeDisabled();
    await expect(page.getByTestId("page-last")).toBeDisabled();

    await page.getByTestId("page-prev").click();
    await expect(page.getByText("NOMBRE161", { exact: true })).toBeVisible();

    await page.getByTestId("page-first").click();
    await expect(range(page)).toContainText("Página 1");
    await expect(page.getByText("NOMBRE1", { exact: true })).toBeVisible();
  });

  test("con cambios sin aplicar pide confirmación antes de cambiar de página", async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM actor", "20");
    await page.getByRole("gridcell", { name: "NOMBRE1", exact: true }).dblclick();
    const editor = page.getByTestId("cell-editor");
    await editor.fill("Ana");
    await editor.press("Enter");
    await expect(editor).toBeHidden();
    await expect(page.getByTestId("edit-bar")).toBeVisible();

    await page.getByTestId("page-next").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: "Cancelar" }).click();
    await expect(range(page)).toContainText("Página 1");

    await page.getByTestId("page-next").click();
    await page.getByRole("dialog").getByRole("button", { name: "Descartar" }).click();
    await expect(range(page)).toContainText("Página 2");
    await expect(page.getByTestId("edit-bar")).toBeHidden();
  });

  test("exportar tras paginar propone repetir la consulta completa", async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM actor", "20");
    await page.getByTestId("export-open").click();
    await expect(page.getByTestId("export-source")).toContainText("Solo está cargada una página");
  });

  test("un JOIN no se pagina y se explica el motivo", async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM actor JOIN film ON 1");
    await expect(page.getByTestId("paging-none")).toContainText("JOIN");
    await expect(page.getByTestId("paging")).toHaveCount(0);
  });
});
