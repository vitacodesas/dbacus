import { connect, expect, mod, test, SAKILA } from "./fixtures";
import type { Page } from "@playwright/test";

// Procedimientos y funciones almacenadas (spec 21), con el backend simulado.
const tree = (page: Page) => page.getByRole("tree", { name: "Esquema" });

/** Despliega sakila y un grupo de rutinas (idempotente). */
async function openGroup(page: Page, group: "Procedimientos" | "Funciones") {
  const t = tree(page);
  if (!(await t.isVisible())) await page.getByRole("button", { name: "Esquema", exact: true }).click();
  const g = t.getByRole("treeitem", { name: new RegExp(`^${group}`) });
  if (!(await g.isVisible())) await t.getByText("sakila", { exact: true }).click();
  if ((await g.getAttribute("aria-expanded")) !== "true") await g.click();
  return g;
}
const routine = (page: Page, name: string) => tree(page).getByRole("treeitem", { name, exact: true });

/** Sustituye el texto del editor Monaco abierto (una sola línea, sin comillas: Monaco autocierra paréntesis y comillas). */
async function typeCode(page: Page, text: string) {
  await page.getByTestId("code-editor").locator(".view-lines").click();
  await page.keyboard.press(`${mod}+a`);
  await page.keyboard.type(text);
}

test.describe("rutinas almacenadas", () => {
  test.beforeEach(async ({ app: page }) => {
    await connect(page);
  });

  test("abrir la base no lista rutinas; expandir el grupo las carga con su contador", async ({ app: page }) => {
    const t = tree(page);
    if (!(await t.isVisible())) await page.getByRole("button", { name: "Esquema", exact: true }).click();
    await t.getByText("sakila", { exact: true }).click();
    await expect(t.getByRole("treeitem", { name: "Procedimientos", exact: true })).toBeVisible(); // sin contador: aún sin cargar
    await expect(routine(page, "film_in_stock")).toHaveCount(0);

    await openGroup(page, "Procedimientos");
    await expect(t.getByRole("treeitem", { name: "Procedimientos (3)" })).toBeVisible();
    await expect(t.getByRole("treeitem", { name: "Funciones (1)" })).toBeVisible();
    await expect(routine(page, "film_in_stock")).toBeVisible();
  });

  test("doble clic abre el código en solo lectura (Monaco) con sus parámetros", async ({ app: page }) => {
    await openGroup(page, "Procedimientos");
    await routine(page, "film_in_stock").dblclick();
    const modal = page.getByTestId("routine-modal");
    await expect(modal).toBeVisible();
    await expect(page.getByTestId("code-editor")).toContainText("film_in_stock");
    await expect(page.getByTestId("routine-edit")).toBeEnabled();
    await expect(page.getByTestId("routine-review")).toHaveCount(0); // sin cambios no hay nada que revisar

    await page.getByRole("tab", { name: "Parámetros" }).click();
    await expect(page.getByTestId("routine-params")).toContainText("p_id");
  });

  test("editar → revisar (siempre) → ejecutar, y el servidor ya tiene el código nuevo", async ({ app: page }) => {
    await openGroup(page, "Procedimientos");
    await routine(page, "film_in_stock").dblclick();
    await page.getByTestId("routine-edit").click();
    await typeCode(page, "CREATE PROCEDURE film_in_stock() SELECT 99");
    await page.getByTestId("routine-review").click();

    const review = page.getByTestId("routine-review-dialog");
    await expect(review).toBeVisible();
    await expect(review.getByTestId("routine-review-sql").first()).toContainText("sql_mode");
    await expect(review.getByTestId("routine-review-sql").last()).toContainText("SELECT 99");
    await expect(page.getByTestId("routine-no-rollback")).toBeVisible();
    await expect(review.getByTestId("routine-review-warning").filter({ hasText: "permisos" })).toBeVisible();
    await page.getByTestId("routine-confirm").click();

    await expect(page.getByText("Rutina guardada")).toBeVisible();
    await expect(page.getByTestId("code-editor")).toContainText("SELECT 99");
    await expect(page.getByTestId("routine-edit")).toBeEnabled(); // volvió a modo lectura
  });

  test("un cuerpo que no compila no toca la original (pre-validación)", async ({ app: page }) => {
    await openGroup(page, "Procedimientos");
    await routine(page, "film_in_stock").dblclick();
    await page.getByTestId("routine-edit").click();
    await typeCode(page, "CREATE PROCEDURE film_in_stock() SYNTAX");
    await page.getByTestId("routine-review").click();
    await page.getByTestId("routine-confirm").click();
    await expect(page.getByTestId("routine-failed")).toBeVisible();
    await expect(page.getByTestId("routine-untouched")).toBeVisible();
  });

  test("si el CREATE final falla, la original se restaura sola", async ({ app: page }) => {
    await openGroup(page, "Procedimientos");
    await routine(page, "film_in_stock").dblclick();
    await page.getByTestId("routine-edit").click();
    await typeCode(page, "CREATE PROCEDURE film_in_stock() BOOM");
    await page.getByTestId("routine-review").click();
    await page.getByTestId("routine-confirm").click();
    await expect(page.getByTestId("routine-failed")).toBeVisible();
    await expect(page.getByTestId("routine-restored")).toContainText("restauró");
  });

  test("cambiar el nombre se rechaza en Go y se queda en el modal", async ({ app: page }) => {
    await openGroup(page, "Procedimientos");
    await routine(page, "film_in_stock").dblclick();
    await page.getByTestId("routine-edit").click();
    await typeCode(page, "CREATE PROCEDURE otro_nombre() SELECT 1");
    await page.getByTestId("routine-review").click();
    await expect(page.getByTestId("routine-action-error")).toContainText("nombre");
    await expect(page.getByTestId("routine-review-dialog")).toHaveCount(0);
  });

  test("cerrar con cambios sin guardar pide confirmación dentro del modal", async ({ app: page }) => {
    await openGroup(page, "Procedimientos");
    await routine(page, "rewards_report").dblclick();
    await page.getByTestId("routine-edit").click();
    await typeCode(page, "CREATE PROCEDURE rewards_report() SELECT 5");
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("routine-close-confirm")).toBeVisible();
    await page.getByTestId("routine-discard-close").click();
    await expect(page.getByTestId("routine-modal")).toBeHidden();
  });

  test("crear: «Nuevo…» abre la plantilla de Go y la rutina aparece en el árbol", async ({ app: page }) => {
    const g = await openGroup(page, "Procedimientos");
    await g.click({ button: "right" });
    await page.getByRole("menuitem", { name: "Nuevo…" }).click();
    await expect(page.getByTestId("code-editor")).toContainText("nueva_rutina");
    await typeCode(page, "CREATE PROCEDURE mi_nuevo() SELECT 1");
    await page.getByTestId("routine-review").click();
    await expect(page.getByTestId("routine-review-sql")).toHaveCount(1);
    await page.getByTestId("routine-confirm").click();
    await expect(page.getByText("Rutina creada")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(routine(page, "mi_nuevo")).toBeVisible(); // el árbol se refrescó solo (schema:invalidated con scope «routines»)
  });

  test("borrar: la revisión enseña el código y quita la rutina del árbol", async ({ app: page }) => {
    await openGroup(page, "Procedimientos");
    await routine(page, "rewards_report").click({ button: "right" });
    await page.getByRole("menuitem", { name: "Eliminar" }).click();
    const review = page.getByTestId("routine-review-dialog");
    await expect(review.getByTestId("routine-delete-body")).toContainText("rewards_report");
    await expect(review.getByTestId("routine-review-sql")).toContainText("DROP PROCEDURE");
    await page.getByTestId("routine-confirm").click();
    await expect(page.getByText("Rutina eliminada")).toBeVisible();
    await expect(routine(page, "rewards_report")).toHaveCount(0);
  });

  test("una rutina sin privilegio para ver el cuerpo avisa y no deja editar", async ({ app: page }) => {
    await openGroup(page, "Procedimientos");
    await routine(page, "secreta").dblclick();
    await expect(page.getByTestId("routine-body-hidden")).toBeVisible();
    await expect(page.getByTestId("routine-edit")).toHaveCount(0);
  });
});

test.describe("500 rutinas", () => {
  test.use({ seed: { profiles: [SAKILA], routines: 500 } });

  test("el árbol se mantiene ligero y el filtro encuentra una por nombre", async ({ app: page }) => {
    await connect(page);
    await openGroup(page, "Procedimientos");
    await expect(tree(page).getByRole("treeitem", { name: /^Procedimientos \(2\d\d\)/ })).toBeVisible();
    // Virtualizado: solo unas decenas de filas en el DOM aunque haya cientos de rutinas.
    expect(await tree(page).getByRole("treeitem").count()).toBeLessThan(80);

    await page.getByPlaceholder("Filtrar lo cargado…").fill("sp_244");
    await expect(routine(page, "sp_244")).toBeVisible();
  });
});

test.describe("conexión de solo lectura", () => {
  test.use({ seed: { profiles: [{ id: "p-ro", name: "Producción", user: "app", database: "sakila", readOnly: true, hasPassword: true }] } });

  test("se puede ver el código, pero editar, crear y borrar salen deshabilitados", async ({ app: page }) => {
    await connect(page, "Producción");
    const g = await openGroup(page, "Procedimientos");
    await routine(page, "film_in_stock").dblclick();
    await expect(page.getByTestId("code-editor")).toContainText("film_in_stock");
    await expect(page.getByTestId("routine-readonly-note")).toBeVisible();
    await expect(page.getByTestId("routine-edit")).toBeDisabled();
    await page.keyboard.press("Escape");

    await routine(page, "film_in_stock").click({ button: "right" });
    for (const name of [/^Editar/, /^Eliminar/]) await expect(page.getByRole("menuitem", { name })).toHaveAttribute("data-disabled", "");
    await page.keyboard.press("Escape");
    await g.click({ button: "right" });
    await expect(page.getByRole("menuitem", { name: /^Nuevo…/ })).toHaveAttribute("data-disabled", "");
  });
});
