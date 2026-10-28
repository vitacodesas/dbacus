import { connect, expect, mod, test } from "./fixtures";
import type { Page } from "@playwright/test";

async function runSql(page: Page, sql: string) {
  await page.keyboard.press(`${mod}+t`);
  await page.locator(".monaco-editor").first().click();
  await page.keyboard.insertText(sql);
  await page.keyboard.press(`${mod}+Enter`);
}
const grid = (page: Page) => page.getByTestId("result-grid");
const cell = (page: Page, text: string) => grid(page).getByRole("gridcell", { name: text, exact: true });

async function edit(page: Page, from: string, to: string) {
  await cell(page, from).dblclick();
  const editor = page.getByTestId("cell-editor");
  await expect(editor).toBeVisible();
  await editor.fill(to);
  await editor.press("Enter");
}

test.describe("edición de celdas", () => {
  test.beforeEach(async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM actor");
    await expect(cell(page, "NOMBRE1")).toBeVisible();
  });

  test("editar, revisar el SQL y aplicar", async ({ app: page }) => {
    await edit(page, "NOMBRE1", "Ana");
    await expect(cell(page, "Ana")).toBeVisible();
    await expect(page.getByTestId("edit-bar")).toContainText("1 cambios sin aplicar");

    await page.getByTestId("edit-apply").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByTestId("edit-sql")).toContainText("UPDATE `sakila`.`actor` SET");
    await expect(dialog.getByTestId("edit-sql")).toContainText("'Ana'");
    await dialog.getByTestId("edit-confirm").click();

    await expect(page.getByText("Se aplicaron 1 filas")).toBeVisible();
    await expect(page.getByTestId("edit-bar")).toBeHidden();
    await expect(cell(page, "Ana")).toBeVisible();
  });

  test("Go rechaza un valor inválido y el editor se queda abierto con el motivo", async ({ app: page }) => {
    await edit(page, "NOMBRE1", "ESTE TEXTO ES DEMASIADO LARGO");
    await expect(page.getByTestId("cell-editor-error")).toContainText("admite 10");
    await expect(page.getByTestId("cell-editor")).toBeVisible();
    await page.getByTestId("cell-editor").press("Escape");
    await expect(page.getByTestId("cell-editor")).toBeHidden();
    await expect(page.getByTestId("edit-bar")).toBeHidden();
    await expect(cell(page, "NOMBRE1")).toBeVisible();
  });

  test("la columna clave no se edita", async ({ app: page }) => {
    await expect(grid(page).getByTestId("col-locked")).toHaveCount(1); // un candado en la cabecera de la clave
    await cell(page, "1").first().dblclick();
    await expect(page.getByTestId("cell-editor")).toBeHidden();
    await expect(page.getByText("Columna de la clave: no se edita")).toBeVisible();
  });

  test("escribir el valor original no deja cambios pendientes", async ({ app: page }) => {
    await edit(page, "NOMBRE1", "otro");
    await expect(page.getByTestId("edit-bar")).toBeVisible();
    await edit(page, "otro", "NOMBRE1");
    await expect(page.getByTestId("edit-bar")).toBeHidden();
  });

  test("descartar vuelve a mostrar el valor del servidor", async ({ app: page }) => {
    await edit(page, "NOMBRE1", "Ana");
    await page.getByTestId("edit-discard").click();
    await expect(page.getByTestId("edit-bar")).toBeHidden();
    await expect(cell(page, "NOMBRE1")).toBeVisible();
  });

  test("un conflicto no aplica nada y ofrece descartar y volver a ejecutar", async ({ app: page }) => {
    await edit(page, "NOMBRE1", "Ana");
    await page.evaluate(() => ((window as unknown as { __mock: { forceConflict: string } }).__mock.forceConflict = "changed"));
    await page.getByTestId("edit-apply").click();
    await page.getByTestId("edit-confirm").click();
    await expect(page.getByTestId("edit-conflict")).toContainText("La fila 1 cambió en el servidor");
    await expect(page.getByTestId("edit-bar")).toContainText("1 cambios sin aplicar");

    await page.evaluate(() => ((window as unknown as { __mock: { forceConflict: string } }).__mock.forceConflict = ""));
    await page.getByTestId("edit-reload").click();
    await expect(cell(page, "NOMBRE1")).toBeVisible();
    await expect(page.getByTestId("edit-bar")).toBeHidden();
  });

  test("volver a ejecutar con cambios pendientes pide confirmación", async ({ app: page }) => {
    await edit(page, "NOMBRE1", "Ana");
    await page.locator(".monaco-editor").first().click();
    await page.keyboard.press(`${mod}+Enter`);

    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("¿Descartar los cambios?");
    await dialog.getByRole("button", { name: "Cancelar" }).click();
    await expect(cell(page, "Ana")).toBeVisible(); // sigue todo como estaba

    await page.keyboard.press(`${mod}+Enter`);
    await page.getByRole("dialog").getByRole("button", { name: "Descartar" }).click();
    await expect(cell(page, "NOMBRE1")).toBeVisible();
    await expect(page.getByTestId("edit-bar")).toBeHidden();
  });

  test("cerrar la pestaña con cambios pendientes pide confirmación", async ({ app: page }) => {
    await edit(page, "NOMBRE1", "Ana");
    await page.getByRole("button", { name: /^Cerrar pestaña/ }).click();
    await expect(page.getByRole("dialog")).toContainText("¿Descartar los cambios?");
    await page.getByRole("dialog").getByRole("button", { name: "Cancelar" }).click();
    await expect(page.getByTestId("result-grid")).toBeVisible();
  });
});

test.describe("editor ampliado", () => {
  test("JSON: se edita en un diálogo, Go dice dónde falla y Formatear lo ordena", async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM film");
    const json = cell(page, '{"id": 1, "host": ["lk-stg-internal"]}');
    await expect(json).toBeVisible();
    await json.dblclick();

    const area = page.getByTestId("cell-editor-textarea");
    await expect(area).toBeVisible();
    await expect(page.getByTestId("cell-editor")).toBeHidden(); // no es el editor de una línea

    await area.fill('{“id": 1}'); // comillas tipográficas, como las que mete el sistema al escribir
    await page.getByTestId("cell-editor-save").click();
    await expect(page.getByTestId("cell-editor-modal-error")).toContainText("no es JSON válido");
    await expect(area).toBeVisible();

    await area.fill('{"id": 1, "host": ["a"]}');
    await page.getByTestId("cell-editor-format").click();
    await expect(area).toHaveValue(/\n {2}"id": 1,/);
    await page.getByTestId("cell-editor-save").click();
    await expect(area).toBeHidden();
    await expect(page.getByTestId("edit-bar")).toContainText("1 cambios sin aplicar");
  });

  test("un texto corto se sigue editando en la propia celda", async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM film");
    await cell(page, "PELÍCULA 1").dblclick();
    await expect(page.getByTestId("cell-editor")).toBeVisible();
    await expect(page.getByTestId("cell-editor-textarea")).toBeHidden();
  });
});

test.describe("lo que no se puede editar", () => {
  test("un JOIN explica por qué", async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM actor JOIN film ON 1");
    await expect(cell(page, "NOMBRE1")).toBeVisible();
    await expect(page.getByTestId("edit-note")).toContainText("una única tabla");
    await cell(page, "NOMBRE1").dblclick();
    await expect(page.getByTestId("cell-editor")).toBeHidden();
  });

  test("NULL desde el menú contextual en una columna que lo admite", async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM film");
    await expect(cell(page, "Descripción 2")).toBeVisible();
    await cell(page, "Descripción 2").click({ button: "right" });
    await page.getByRole("menuitem", { name: "Establecer NULL" }).click();
    await expect(page.getByTestId("edit-bar")).toContainText("1 cambios sin aplicar");
    // la columna `title` no admite NULL: el menú no lo ofrece
    await cell(page, "PELÍCULA 2").click({ button: "right" });
    await expect(page.getByRole("menuitem", { name: "Establecer NULL" })).toBeHidden();
  });
});

test.describe("conexión de solo lectura", () => {
  test.use({ seed: { profiles: [{ id: "p-ro", name: "Producción", user: "app", readOnly: true, hasPassword: true }] } });

  test("no ofrece edición y lo dice", async ({ app: page }) => {
    await connect(page, "Producción");
    await runSql(page, "SELECT * FROM actor");
    await expect(cell(page, "NOMBRE1")).toBeVisible();
    await expect(page.getByTestId("edit-note")).toContainText("solo lectura");
    await cell(page, "NOMBRE1").dblclick();
    await expect(page.getByTestId("cell-editor")).toBeHidden();
  });
});
