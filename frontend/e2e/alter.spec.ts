import { connect, expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// Editor de estructura de tabla (spec 19), con el backend simulado.
// Idempotente: se puede llamar de nuevo con la barra lateral y los nodos ya desplegados (un segundo clic los plegaría).
async function tableNode(page: Page, table: string) {
  const tree = page.getByRole("tree", { name: "Esquema" });
  if (!(await tree.isVisible())) await page.getByRole("button", { name: "Esquema", exact: true }).click();
  const node = tree.getByText(table, { exact: true });
  // Tras aplicar, Go invalida el esquema y el árbol se recarga un momento: se espera a que reaparezca antes de decidir si hay que desplegar.
  await node.waitFor({ timeout: 1500 }).catch(() => undefined);
  if (!(await node.isVisible())) {
    if (!(await tree.getByRole("treeitem", { name: /^Tablas/ }).isVisible())) await tree.getByText("sakila", { exact: true }).click();
    await tree.getByRole("treeitem", { name: /^Tablas/ }).click();
  }
  return node;
}
async function openEditor(page: Page, table = "actor") {
  await (await tableNode(page, table)).click({ button: "right" });
  // El clic derecho solo abre el menú: no despliega las columnas de la tabla (con el menú abierto el árbol queda aria-hidden: se mira por data-kind).
  await expect(page.locator('[data-kind="table"]', { hasText: table })).toHaveAttribute("aria-expanded", "false");
  await page.getByRole("menuitem", { name: "Editar estructura" }).click();
  await expect(page.getByTestId("alter-editor")).toBeVisible();
}
const sql = (page: Page) => page.getByTestId("alter-sql");

test.describe("editar estructura", () => {
  test.beforeEach(async ({ app: page }) => {
    await connect(page);
  });

  test("clic derecho → Editar estructura, ver el SQL en vivo, revisar y ejecutar", async ({ app: page }) => {
    await openEditor(page);
    await expect(page.getByTestId("alter-sql-empty")).toContainText("Sin cambios");
    await expect(page.getByTestId("alter-apply")).toBeDisabled();

    // Cambiar el tipo de una columna: el SQL aparece solo, calculado por Go.
    await page.getByTestId("alter-col-first_name").getByTestId("alter-col-type").fill("varchar(100)");
    await expect(sql(page)).toContainText("MODIFY COLUMN `first_name` varchar(100)");
    await expect(page.getByTestId("alter-col-first_name")).toHaveAttribute("data-state", "modified");

    // Añadir una columna.
    await page.getByTestId("alter-add-column").click();
    await page.getByTestId("alter-col-new").getByTestId("alter-col-name").fill("apodo");
    await expect(sql(page)).toContainText("ADD COLUMN `apodo`");

    // Aplicar abre SIEMPRE la revisión, con el mismo SQL y el aviso de que no hay vuelta atrás.
    await page.getByTestId("alter-apply").click();
    const review = page.getByTestId("alter-review");
    await expect(review).toBeVisible();
    await expect(review.getByTestId("alter-review-sql")).toContainText("MODIFY COLUMN `first_name` varchar(100)");
    await expect(page.getByTestId("alter-no-rollback")).toContainText("No hay vuelta atrás");
    await page.getByTestId("alter-confirm").click();

    await expect(page.getByText("Estructura modificada")).toBeVisible();
    await expect(page.getByTestId("alter-editor")).toBeHidden();

    // Al volver a abrirlo, el servidor ya tiene la columna y el tipo nuevo.
    await openEditor(page);
    await expect(page.getByTestId("alter-col-apodo")).toBeVisible();
    await expect(page.getByTestId("alter-col-first_name").getByTestId("alter-col-type")).toHaveValue("varchar(100)");
    await expect(page.getByTestId("alter-apply")).toBeDisabled();
  });

  test("borrar una columna avisa en rojo y deshacer lo cancela", async ({ app: page }) => {
    await openEditor(page);
    await page.getByTestId("alter-col-last_name").getByTestId("alter-remove").click();
    await expect(sql(page)).toContainText("DROP COLUMN `last_name`");
    await expect(page.getByTestId("alter-warning")).toContainText("Se borrará la columna «last_name»");
    await page.getByTestId("alter-apply").click();
    await expect(page.getByTestId("alter-review-warning")).toContainText("con todos sus datos");
    await page.getByRole("button", { name: "Cancelar" }).click();
    await expect(page.getByTestId("alter-review")).toBeHidden();

    await page.getByTestId("alter-col-last_name").getByTestId("alter-undo").click();
    await expect(page.getByTestId("alter-sql-empty")).toBeVisible();
    await expect(page.getByTestId("alter-apply")).toBeDisabled();
  });

  test("un error de validación de Go se ve en el panel y bloquea Aplicar", async ({ app: page }) => {
    await openEditor(page);
    await page.getByTestId("alter-add-column").click(); // sin nombre
    await expect(page.getByTestId("alter-preview-error")).toContainText("Falta un nombre");
    await expect(page.getByTestId("alter-apply")).toBeDisabled();
  });

  test("índices y claves foráneas", async ({ app: page }) => {
    await openEditor(page, "film");
    await page.getByRole("tab", { name: "Índices" }).click();
    await expect(page.getByTestId("alter-idx-PRIMARY")).toBeVisible();
    await expect(page.getByTestId("alter-idx-PRIMARY").getByTestId("alter-idx-name")).toBeDisabled(); // la clave primaria no se toca
    await page.getByTestId("alter-add-index").click();
    await page.getByTestId("alter-idx-new").getByTestId("alter-idx-name").fill("idx_title");
    await page.getByTestId("alter-idx-new").getByTestId("alter-idx-columns").fill("title");
    await page.getByTestId("alter-idx-new").getByTestId("alter-idx-columns").press("Tab");
    await expect(sql(page)).toContainText("ADD INDEX `idx_title` (`title`)");

    await page.getByRole("tab", { name: "Claves foráneas" }).click();
    await page.getByTestId("alter-add-fk").click();
    const fk = page.getByTestId("alter-fk-new");
    await fk.getByTestId("alter-fk-name").fill("fk_film_actor");
    await fk.getByTestId("alter-fk-columns").fill("film_id");
    await fk.getByTestId("alter-fk-columns").press("Tab");
    await fk.getByTestId("alter-fk-reftable").fill("actor");
    await fk.getByTestId("alter-fk-refcolumns").fill("actor_id");
    await fk.getByTestId("alter-fk-refcolumns").press("Tab");
    await expect(sql(page)).toContainText("ADD CONSTRAINT `fk_film_actor` FOREIGN KEY (`film_id`) REFERENCES `actor` (`actor_id`)");
    await expect(sql(page)).toContainText("ADD INDEX `idx_title`"); // las tres pestañas alimentan el mismo SQL
  });

  test("si falla a mitad, la revisión cuenta qué pasó y el editor sigue abierto", async ({ app: page }) => {
    await openEditor(page);
    await page.getByTestId("alter-add-column").click();
    await page.getByTestId("alter-col-new").getByTestId("alter-col-name").fill("boom");
    await expect(sql(page)).toContainText("ADD COLUMN `boom`");
    await page.getByTestId("alter-apply").click();
    await page.getByTestId("alter-confirm").click();
    await expect(page.getByTestId("alter-partial")).toContainText("Falló la sentencia 1");
    await expect(page.getByTestId("alter-partial")).toContainText("Duplicate column name");
    await page.getByRole("button", { name: "Cerrar" }).first().click();
    await expect(page.getByTestId("alter-editor")).toBeVisible();
    await expect(page.getByTestId("alter-sql-empty")).toBeVisible(); // recargado con el estado real: ya no hay cambios pendientes
  });
});

test.describe("conexión de solo lectura", () => {
  test.use({ seed: { profiles: [{ id: "p-ro", name: "Producción", user: "app", database: "sakila", readOnly: true, hasPassword: true }] } });

  test("«Editar estructura» sale deshabilitado y no abre nada", async ({ app: page }) => {
    await connect(page, "Producción");
    await (await tableNode(page, "actor")).click({ button: "right" });
    const item = page.getByRole("menuitem", { name: /Editar estructura/ });
    await expect(item).toBeVisible();
    await expect(item).toHaveAttribute("data-disabled", "");
    await expect(item).toContainText("solo lectura");
    await item.click({ force: true });
    await expect(page.getByTestId("alter-editor")).toBeHidden();
  });
});
