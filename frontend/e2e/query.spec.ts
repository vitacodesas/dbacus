import { connect, expect, mod, test } from "./fixtures";
import type { Page } from "@playwright/test";


/** Escribe en el editor Monaco de la pestaña activa (sustituyendo lo que hubiera). */
async function setSql(page: Page, sql: string) {
  const editor = page.locator(".monaco-editor").first();
  await editor.click();
  await page.keyboard.press(`${mod}+a`);
  await page.keyboard.insertText(sql);
}
const run = (page: Page) => page.keyboard.press(`${mod}+Enter`);

test.describe("consultas y resultados", () => {
  test.beforeEach(async ({ app: page }) => {
    await connect(page);
    await page.keyboard.press(`${mod}+t`);
    await expect(page.locator(".monaco-editor").first()).toBeVisible();
  });

  test("ejecuta un SELECT y pinta las filas en el grid", async ({ app: page }) => {
    await setSql(page, "SELECT * FROM actor");
    await run(page);
    await expect(page.getByText("NOMBRE1", { exact: true })).toBeVisible();
    await expect(page.getByText("APELLIDO1", { exact: true })).toBeVisible();
    await expect(page.getByTestId("range")).toContainText("de 200");
  });

  test("un resultado enorme se trunca al tope y el grid sigue virtualizado", async ({ app: page }) => {
    await page.getByTestId("limit-select").selectOption({ label: "Sin límite" }); // con el límite por defecto no se trunca
    await setSql(page, "SELECT * FROM big_numbers");
    await run(page);
    await expect(page.getByTestId("range")).toContainText(/100.000\+ filas \(truncado\)/);
    const rows = await page.getByTestId("result-grid").getByRole("row").count();
    expect(rows).toBeGreaterThan(0);
    expect(rows).toBeLessThan(100); // solo las filas visibles están en el DOM
  });

  test("cancelar una consulta larga devuelve el control", async ({ app: page }) => {
    await setSql(page, "SELECT SLEEP(60)");
    await run(page);
    const stop = page.getByRole("button", { name: /Cancelar|Detener/ }).first();
    await expect(stop).toBeVisible();
    await stop.click();
    await expect(page.getByText(/cancelad/i).first()).toBeVisible();
  });

  test("un error de sintaxis se muestra con su mensaje", async ({ app: page }) => {
    await setSql(page, "SELEC 1");
    await run(page);
    await expect(page.getByText(/Error de sintaxis/).first()).toBeVisible();
  });

  test("varias sentencias dan varios resultados y un exec informa filas afectadas", async ({ app: page }) => {
    await setSql(page, "SELECT * FROM actor; UPDATE actor SET first_name = 'X'");
    await run(page);
    await expect(page.getByText("NOMBRE1", { exact: true })).toBeVisible();
    await expect(page.getByRole("tab", { name: /Resultado 2|Mensajes/ }).first()).toBeVisible();
  });
});
