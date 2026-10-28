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

    await expect(page.getByText("Cambios aplicados: 1 actualizadas")).toBeVisible();
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

const row = (page: Page, state: "new" | "deleted") => grid(page).locator(`[data-row-state="${state}"]`);

test.describe("filas nuevas, copias y borrados", () => {
  test.beforeEach(async ({ app: page }) => {
    await connect(page);
    await runSql(page, "SELECT * FROM actor");
    await expect(cell(page, "NOMBRE1")).toBeVisible();
  });

  test("Nueva fila: se rellena en el grid, se ve el INSERT y se aplica", async ({ app: page }) => {
    await page.getByTestId("new-row").click();
    await expect(row(page, "new")).toHaveCount(1);
    const editor = page.getByTestId("cell-editor");
    await expect(editor).toBeVisible(); // abre el editor en la primera columna obligatoria
    await editor.fill("Ana");
    await editor.press("Enter");
    await expect(row(page, "new").getByRole("gridcell", { name: "Ana", exact: true })).toBeVisible();
    // La segunda columna obligatoria (apellido) es la siguiente «DEFAULT» tras el id.
    await row(page, "new").getByRole("gridcell", { name: "DEFAULT" }).nth(1).dblclick();
    await page.getByTestId("cell-editor").fill("Pérez");
    await page.getByTestId("cell-editor").press("Enter");

    await expect(page.getByTestId("edit-pending-text")).toContainText("1 cambios sin aplicar · 1 filas nuevas");
    await page.getByTestId("edit-apply").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.locator('[data-kind="insert"]')).toContainText("INSERT INTO");
    await dialog.getByTestId("edit-confirm").click();
    await expect(page.getByText("Cambios aplicados: 1 nuevas")).toBeVisible();
    await expect(row(page, "new")).toHaveCount(0);
    await expect(cell(page, "Ana")).toBeVisible();
  });

  test("una fila nueva sin los valores obligatorios no se aplica y el motivo se ve en el diálogo", async ({ app: page }) => {
    await page.getByTestId("new-row").click();
    await page.getByTestId("cell-editor").press("Escape"); // se queda vacía
    await page.getByTestId("edit-apply").click();
    await page.getByTestId("edit-confirm").click();
    await expect(page.getByTestId("edit-error")).toContainText("falta un valor");
    await expect(page.getByTestId("edit-pending-text")).toContainText("1 filas nuevas");
  });

  test("Insertar nueva fila a partir de esta copia los valores y deja cambiar lo que se quiera", async ({ app: page }) => {
    await cell(page, "NOMBRE1").click({ button: "right" });
    await page.getByRole("menuitem", { name: "Insertar nueva fila a partir de esta" }).click();
    await expect(row(page, "new")).toHaveCount(1);
    await expect(row(page, "new").getByRole("gridcell", { name: "NOMBRE1", exact: true })).toBeVisible();
    await expect(row(page, "new").getByRole("gridcell", { name: "APELLIDO1", exact: true })).toBeVisible();
    await expect(row(page, "new").getByRole("gridcell", { name: "DEFAULT" }).first()).toBeVisible(); // el autoincremental queda para el servidor

    const editor = page.getByTestId("cell-editor");
    await expect(editor).toHaveValue("NOMBRE1");
    await editor.fill("Copia");
    await editor.press("Enter");
    await page.getByTestId("edit-apply").click();
    await expect(page.getByRole("dialog").locator('[data-kind="insert"]')).toContainText("'Copia'");
    await page.getByTestId("edit-confirm").click();
    await expect(page.getByText("Cambios aplicados: 1 nuevas")).toBeVisible();
    await expect(cell(page, "Copia")).toBeVisible();
  });

  test("Supr marca la fila para borrar, se puede restaurar y al aplicar desaparece", async ({ app: page }) => {
    await cell(page, "NOMBRE1").click();
    await page.keyboard.press("Delete");
    await expect(row(page, "deleted")).toHaveCount(1);
    await expect(page.getByTestId("edit-pending-text")).toContainText("1 filas a borrar");

    await cell(page, "NOMBRE1").click({ button: "right" });
    await page.getByRole("menuitem", { name: "Restaurar fila(s)" }).click();
    await expect(row(page, "deleted")).toHaveCount(0);
    await expect(page.getByTestId("edit-bar")).toBeHidden();

    await cell(page, "NOMBRE1").click();
    await page.keyboard.press("Delete");
    await page.getByTestId("edit-apply").click();
    await expect(page.getByRole("dialog").locator('[data-kind="delete"]')).toContainText("DELETE FROM");
    await page.getByTestId("edit-confirm").click();
    await expect(page.getByText("Cambios aplicados: 1 borradas")).toBeVisible();
    await expect(cell(page, "NOMBRE1")).toBeHidden();
    await expect(cell(page, "NOMBRE2")).toBeVisible();
    await expect(page.getByTestId("range")).toContainText("de 199");
  });

  test("una fila marcada para borrar no se edita y lo explica", async ({ app: page }) => {
    await cell(page, "NOMBRE1").click();
    await page.keyboard.press("Delete");
    await expect(row(page, "deleted")).toHaveCount(1);
    await cell(page, "NOMBRE1").dblclick();
    await expect(page.getByTestId("cell-editor")).toBeHidden();
    await expect(page.getByText("La fila está marcada para borrar")).toBeVisible();
  });

  test("borrar una fila nueva la descarta", async ({ app: page }) => {
    await page.getByTestId("new-row").click();
    await page.getByTestId("cell-editor").press("Escape");
    await expect(row(page, "new")).toHaveCount(1);
    await page.keyboard.press("Delete");
    await expect(row(page, "new")).toHaveCount(0);
    await expect(page.getByTestId("edit-bar")).toBeHidden();
  });

  test("un lote mixto muestra UPDATE, DELETE e INSERT en ese orden", async ({ app: page }) => {
    await edit(page, "NOMBRE1", "Editada");
    await cell(page, "NOMBRE2").click();
    await page.keyboard.press("Delete");
    await page.getByTestId("new-row").click();
    await page.getByTestId("cell-editor").fill("Nueva");
    await page.getByTestId("cell-editor").press("Enter");
    await expect(row(page, "new").getByRole("gridcell", { name: "Nueva", exact: true })).toBeVisible();
    await row(page, "new").getByRole("gridcell", { name: "DEFAULT" }).nth(1).dblclick();
    await page.getByTestId("cell-editor").fill("Fila");
    await page.getByTestId("cell-editor").press("Enter");

    await page.getByTestId("edit-view-sql").click();
    const kinds = await page.getByRole("dialog").locator("[data-kind]").evaluateAll((els) => els.map((e) => e.getAttribute("data-kind")));
    expect(kinds).toEqual(["update", "delete", "insert"]);
  });

  test("borrar en una tabla con claves foráneas avisa de lo que se arrastra", async ({ app: page }) => {
    await runSql(page, "SELECT * FROM film");
    await expect(cell(page, "PELÍCULA 1")).toBeVisible();
    await cell(page, "PELÍCULA 1").click();
    await page.keyboard.press("Delete");
    await page.getByTestId("edit-apply").click();
    await expect(page.getByTestId("edit-cascade")).toContainText("sakila.film_actor");
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
    await expect(page.getByTestId("new-row")).toBeHidden(); // sin edición no hay «Nueva fila»
  });
});
