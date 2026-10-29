import { connect, expect, mod, test } from "./fixtures";
import type { Page } from "@playwright/test";


async function runSql(page: Page, sql: string) {
  await page.keyboard.press(`${mod}+t`);
  await page.locator(".monaco-editor").first().click();
  await page.keyboard.insertText(sql);
  await page.keyboard.press(`${mod}+Enter`);
}

test.describe("esquema", () => {
  test("lista las bases y oculta las del sistema hasta pedirlas", async ({ app: page }) => {
    await connect(page);
    await page.getByRole("button", { name: "Esquema" }).click();
    const tree = page.getByRole("tree", { name: "Esquema" });
    await expect(tree.getByText("sakila", { exact: true })).toBeVisible();
    await expect(tree.getByText("information_schema", { exact: true })).toBeHidden();

    await page.getByRole("button", { name: "Mostrar bases del sistema" }).click();
    await expect(tree.getByText("information_schema", { exact: true })).toBeVisible();
  });

  test("desplegar una base y su grupo de tablas las lista", async ({ app: page }) => {
    await connect(page);
    await page.getByRole("button", { name: "Esquema" }).click();
    const tree = page.getByRole("tree", { name: "Esquema" });
    await tree.getByText("sakila", { exact: true }).click();
    await tree.getByRole("treeitem", { name: /^Tablas/ }).click();
    await expect(tree.getByText("actor", { exact: true })).toBeVisible();
    await expect(tree.getByText("film", { exact: true })).toBeVisible();
  });
});

test.describe("historial", () => {
  test("registra lo ejecutado, se filtra y se vacía", async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM actor");
    await expect(page.getByText("NOMBRE1", { exact: true })).toBeVisible();

    await page.getByRole("button", { name: "Historial" }).click();
    const panel = page.getByTestId("history-panel");
    await expect(panel.getByTestId("history-item")).toHaveCount(1);
    await expect(panel.getByTestId("history-item")).toContainText("SELECT * FROM actor");

    await panel.getByRole("searchbox").fill("no-existe");
    await expect(panel.getByTestId("history-item")).toHaveCount(0);
    await panel.getByRole("searchbox").fill("actor");
    await expect(panel.getByTestId("history-item")).toHaveCount(1);

    await panel.getByRole("button", { name: "Limpiar historial" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Limpiar historial" }).click();
    await expect(panel.getByTestId("history-item")).toHaveCount(0);
  });
});

test.describe("exportación", () => {
  test("exporta el resultado a CSV y avisa de la ruta", async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM actor");
    await expect(page.getByText("NOMBRE1", { exact: true })).toBeVisible();

    await page.getByTestId("export-open").click();
    await page.getByRole("dialog").getByRole("button", { name: "Exportar…" }).click();
    await expect(page.getByText(/mock-export\.csv/).first()).toBeVisible();
  });
});

test.describe("exportación con LIMIT automático", () => {
  test("avisa de que lo cargado no es todo y por defecto re-ejecuta sin el límite", async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM big_numbers"); // el límite por defecto (1000) se aplica solo
    await expect(page.getByTestId("range")).toContainText(/1[.,]000/);

    await page.getByTestId("export-open").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByTestId("export-source")).toContainText(/LIMIT automático de 1[.,]000 filas/);
    await dialog.getByRole("button", { name: "Exportar…" }).click();
    await expect(page.getByText(/mock-export\.csv/).first()).toBeVisible();
    const sent = await page.evaluate(() => (window as unknown as { __mock: { exports: { source: string }[] } }).__mock.exports);
    expect(sent.map((r) => r.source)).toEqual(["rerun"]);
  });
});

test.describe("ajustes", () => {
  test("cambiar el tema aplica la clase dark y se guarda", async ({ app: page }) => {
    await page.keyboard.press(`${mod}+,`);
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Tema", { exact: true }).selectOption("dark");
    await dialog.getByTestId("settings-save").click();
    await expect(dialog).toBeHidden();
    await expect(page.locator("html")).toHaveClass(/dark/);
    expect(await page.evaluate(() => (window as unknown as { __mock: { settings: { theme: string } } }).__mock.settings.theme)).toBe("dark");
  });

  test("un valor fuera de rango lo rechaza Go y se ve en el campo", async ({ app: page }) => {
    await page.getByRole("button", { name: "Ajustes" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("Máximo de filas por resultado").fill("5");
    await dialog.getByTestId("settings-save").click();
    await expect(dialog.getByText("Entre 20 y 10 000 000")).toBeVisible();
    await expect(dialog).toBeVisible();
  });
});
