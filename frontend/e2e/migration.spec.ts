import { connect, expect, test, SAKILA } from "./fixtures";
import type { Page } from "@playwright/test";

// Migración de schema (spec 23), con el backend simulado.
const tree = (page: Page) => page.getByRole("tree", { name: "Esquema" });
const dialog = (page: Page) => page.getByTestId("migration-dialog");

async function openWizard(page: Page) {
  await connect(page);
  const t = tree(page);
  if (!(await t.isVisible())) await page.getByRole("button", { name: "Esquema", exact: true }).click();
  await t.getByText("sakila", { exact: true }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Migrar schema…" }).click();
  await expect(dialog(page)).toBeVisible();
}

/** Pasos 1 → 3 con el nombre de destino dado. */
async function toPlan(page: Page, target = "sakila_copia") {
  await page.getByTestId("migration-target-db").fill(target);
  await page.getByTestId("migration-next").click();
  await expect(page.getByTestId("migration-step-content-body")).toBeVisible();
  await page.getByTestId("migration-next").click();
  await expect(page.getByTestId("migration-step-plan-body")).toBeVisible();
}

test.describe("migrar schema", () => {
  test.use({ seed: { profiles: [SAKILA], migration: { tickMs: 40 } } });

  test("éxito: destino, contenido, plan con avisos y SQL, progreso hasta terminar", async ({ app: page }) => {
    await openWizard(page);
    await expect(page.getByTestId("migration-route-hint")).toContainText("dentro del servidor");
    await toPlan(page);

    await expect(page.getByTestId("migration-plan-route")).toContainText("Dentro del servidor");
    await expect(page.getByTestId("migration-plan-new")).toContainText("sakila_copia");
    await expect(page.getByTestId("migration-warning-no_index")).toContainText("audit_log");
    await expect(page.getByTestId("migration-warning-definer_foreign")).toBeVisible();
    await expect(page.getByTestId("migration-statements")).toContainText("CREATE DATABASE");
    await expect(page.getByTestId("migration-plan-object-film_in_stock")).toContainText("se crea");
    await expect(page.getByTestId("migration-plan-object-customer_list")).toContainText("Vista");

    await page.getByTestId("migration-start").click();
    await expect(page.getByTestId("migration-progress")).toBeVisible();
    await expect(page.getByTestId("migration-state")).toContainText("Terminada", { timeout: 15_000 });
    await expect(page.getByTestId("migration-table-state-rental")).toHaveText("hecha");
    await expect(page.getByTestId("migration-table-state-audit_log")).toHaveText("hecha");
    await expect(page.getByTestId("migration-job-object-state-film_in_stock")).toHaveText("creada");
    await expect(page.getByTestId("migration-job-object-state-rewards_report")).toHaveText("creada");
    await expect(page.getByTestId("migration-progress")).toHaveAttribute("aria-valuenow", "100");
    await expect(page.getByText("Migración terminada: sakila_copia")).toBeVisible();
  });

  test("elegir contenido: desmarcar tablas y vistas llega al plan", async ({ app: page }) => {
    await openWizard(page);
    await page.getByTestId("migration-target-db").fill("solo_actor");
    await page.getByTestId("migration-next").click();
    for (const n of ["film", "customer", "rental", "payment", "audit_log"]) await page.getByTestId(`migration-pick-table-${n}`).getByRole("checkbox").uncheck();
    await page.getByTestId("migration-pick-view-customer_list").getByRole("checkbox").uncheck();
    await page.getByTestId("migration-next").click();
    await expect(page.getByTestId("migration-step-plan-body")).toBeVisible();
    await expect(page.getByTestId("migration-statements")).not.toContainText("rental");
    await expect(page.getByTestId("migration-warning-no_index")).toHaveCount(0);
    await page.getByTestId("migration-start").click();
    await expect(page.getByTestId("migration-state")).toContainText("Terminada", { timeout: 15_000 });
    await expect(page.getByTestId("migration-table-actor")).toBeVisible();
    await expect(page.getByTestId("migration-table-film")).toHaveCount(0);
  });

  test("solo estructura: la tabla se crea vacía, no cuenta filas y el plan y el progreso lo dicen", async ({ app: page }) => {
    await openWizard(page);
    await page.getByTestId("migration-target-db").fill("sin_datos");
    await page.getByTestId("migration-next").click();
    await expect(page.getByTestId("migration-step-content-body")).toBeVisible();
    await page.getByTestId("migration-structure-payment").getByRole("checkbox").check();
    await page.getByTestId("migration-structure-rental").getByRole("checkbox").check();
    await page.getByTestId("migration-pick-table-rental").getByRole("checkbox").uncheck();
    await expect(page.getByTestId("migration-structure-rental").getByRole("checkbox")).toBeDisabled();
    await page.getByTestId("migration-pick-table-rental").getByRole("checkbox").check();
    await page.getByTestId("migration-next").click();
    await expect(page.getByTestId("migration-step-plan-body")).toBeVisible();
    await expect(page.getByTestId("migration-plan-structure")).toContainText("2 solo estructura");

    await page.getByTestId("migration-start").click();
    await expect(page.getByTestId("migration-state")).toContainText("Terminada", { timeout: 15_000 });
    await expect(page.getByTestId("migration-table-structure-payment")).toHaveText("solo estructura");
    await expect(page.getByTestId("migration-table-state-payment")).toHaveText("hecha");
    await expect(page.getByTestId("migration-table-structure-actor")).toHaveCount(0);
  });

  test("nada elegido: no se puede seguir", async ({ app: page }) => {
    await openWizard(page);
    await page.getByTestId("migration-next").click();
    await page.getByRole("button", { name: "Nada" }).first().click();
    await page.getByRole("button", { name: "Nada" }).nth(1).click();
    await page.getByRole("button", { name: "Nada" }).nth(2).click();
    await page.getByRole("button", { name: "Nada" }).nth(3).click();
    await expect(page.getByTestId("migration-empty")).toBeVisible();
    await expect(page.getByTestId("migration-next")).toBeDisabled();
  });

  test("errores de Go: el mismo schema, uno de sistema y un nombre vacío no avanzan", async ({ app: page }) => {
    await openWizard(page);
    await page.getByTestId("migration-target-db").fill("sakila");
    await page.getByTestId("migration-next").click();
    await expect(page.getByTestId("migration-error")).toContainText("el mismo schema");
    await page.getByTestId("migration-target-db").fill("mysql");
    await page.getByTestId("migration-next").click();
    await expect(page.getByTestId("migration-error")).toContainText("sistema");
    await expect(page.getByTestId("migration-step-target-body")).toBeVisible();
    await page.getByTestId("migration-target-db").fill("");
    await expect(page.getByTestId("migration-next")).toBeDisabled();
  });

  test("cancelar a mitad deja la migración como «Cancelada»", async ({ app: page }) => {
    await openWizard(page);
    await toPlan(page, "cancelada");
    await page.getByTestId("migration-start").click();
    await expect(page.getByTestId("migration-cancel")).toBeVisible();
    await page.getByTestId("migration-cancel").click();
    await expect(page.getByTestId("migration-state")).toContainText("Cancelada", { timeout: 10_000 });
    await expect(page.getByTestId("migration-retry")).toHaveCount(0);
  });

  test("cerrar con la copia en curso la deja seguir y reabrir vuelve al progreso", async ({ app: page }) => {
    await openWizard(page);
    await toPlan(page, "segundo_plano");
    await page.getByTestId("migration-start").click();
    await expect(page.getByTestId("migration-cancel")).toBeVisible();
    await page.getByTestId("migration-close").click();
    await expect(dialog(page)).toBeHidden();
    await expect(page.getByText("Migración terminada: segundo_plano")).toBeVisible({ timeout: 15_000 });
    await tree(page).getByText("sakila", { exact: true }).click({ button: "right" });
    await page.getByRole("menuitem", { name: "Migrar schema…" }).click();
    await expect(page.getByTestId("migration-step-run-body")).toBeVisible();
    await expect(page.getByTestId("migration-state")).toContainText("Terminada");
  });
});

test.describe("migrar schema: fallo parcial, verificación y limpieza", () => {
  test.use({ seed: { profiles: [SAKILA], migration: { scenario: "orphans", tickMs: 40 } } });

  test("verificar encuentra una fila huérfana y limpiar pide escribir el nombre", async ({ app: page }) => {
    await openWizard(page);
    await toPlan(page, "con_huerfanos");
    await page.getByTestId("migration-start").click();
    await expect(page.getByTestId("migration-state")).toContainText("Terminada", { timeout: 15_000 });
    await page.getByTestId("migration-verify").click();
    await expect(page.getByTestId("migration-orphans")).toContainText("payment.fk_payment_rental: 3 filas");
    await expect(page.getByTestId("migration-object-rewards_report")).toContainText("Procedimiento rewards_report: código distinto");
    await expect(page.getByTestId("migration-object-film_in_stock")).toContainText("Función film_in_stock: igual"); // los iguales también salen
    await expect(page.getByTestId("migration-objects-checked")).toContainText("rutinas, vistas y triggers comparados");

    await page.getByTestId("migration-cleanup").click();
    await expect(page.getByTestId("migration-cleanup-go")).toBeDisabled();
    await page.getByTestId("migration-cleanup-confirm").fill("con_huerfanos");
    await page.getByTestId("migration-cleanup-go").click();
    await expect(page.getByText("Destino limpiado")).toBeVisible();
    await expect(page.getByTestId("migration-cleanup")).toHaveCount(0);
  });
});

test.describe("migrar schema: una tabla falla", () => {
  test.use({ seed: { profiles: [SAKILA], migration: { scenario: "partial", tickMs: 40 } } });

  test("termina con errores, muestra la tabla fallida y Reintentar la completa", async ({ app: page }) => {
    await openWizard(page);
    await toPlan(page, "parcial");
    await page.getByTestId("migration-start").click();
    await expect(page.getByTestId("migration-state")).toContainText("con errores", { timeout: 15_000 });
    await expect(page.getByTestId("migration-failed")).toContainText("payment");
    await expect(page.getByTestId("migration-table-state-payment")).toHaveText("falló");
    await expect(page.getByTestId("migration-table-state-actor")).toHaveText("hecha");

    await page.getByTestId("migration-retry").click();
    await expect(page.getByTestId("migration-state")).toContainText("Terminada", { timeout: 15_000 });
    await expect(page.getByTestId("migration-table-state-payment")).toHaveText("hecha");
    await expect(page.getByTestId("migration-failed")).toHaveCount(0);
  });
});

test.describe("migrar schema: el destino ya tiene objetos", () => {
  test.use({ seed: { profiles: [SAKILA], migration: { scenario: "exists", tickMs: 40 } } });

  test("se rechaza con el motivo y «omitir lo que ya existe» lo deja pasar", async ({ app: page }) => {
    await openWizard(page);
    await page.getByTestId("migration-target-db").fill("ocupado");
    await page.getByTestId("migration-next").click();
    await expect(page.getByTestId("migration-error")).toContainText("ya tiene objetos");
    await expect(page.getByTestId("migration-error")).toContainText("actor");
    await expect(page.getByTestId("migration-error")).toContainText("film_in_stock");
    await page.getByTestId("migration-skip-existing").getByRole("checkbox").check();
    await page.getByTestId("migration-next").click();
    await expect(page.getByTestId("migration-step-content-body")).toContainText("ya existe, se omite");
    await expect(page.getByTestId("migration-existing-film_in_stock")).toContainText("mismo código");
    await expect(page.getByTestId("migration-existing-customer_list")).toContainText("código distinto");
    await expect(page.getByTestId("migration-pick-routine-film_in_stock")).toHaveCount(0);
    await page.getByTestId("migration-next").click();
    await expect(page.getByTestId("migration-warning-object_exists_same")).toContainText("film_in_stock");
    await expect(page.getByTestId("migration-warning-object_exists_different")).toContainText("NO se reemplaza");
    await expect(page.getByTestId("migration-statements")).not.toContainText("film_in_stock");
    await page.getByTestId("migration-start").click();
    await expect(page.getByTestId("migration-state")).toContainText("Terminada", { timeout: 15_000 });
    await expect(page.getByTestId("migration-table-state-actor")).toHaveText("omitida");
  });
});

test.describe("migrar schema: conexión de solo lectura", () => {
  test.use({ seed: { profiles: [{ ...SAKILA, readOnly: true }], migration: { tickMs: 40 } } });

  test("el destino de solo lectura se rechaza y la lista de destinos lo marca", async ({ app: page }) => {
    await openWizard(page);
    await expect(page.getByTestId("migration-target-session").locator("option").first()).toContainText("solo lectura");
    await page.getByTestId("migration-target-db").fill("no_se_crea");
    await page.getByTestId("migration-next").click();
    await expect(page.getByTestId("migration-error")).toContainText("solo lectura");
    await expect(page.getByTestId("migration-step-target-body")).toBeVisible();
  });
});

test.describe("migrar schema: rutinas que el usuario no puede leer", () => {
  test.use({ seed: { profiles: [SAKILA], migration: { scenario: "unreadable", tickMs: 40 } } });

  test("salen en el paso de contenido con el motivo, no se pueden elegir y si es lo único se explica", async ({ app: page }) => {
    await openWizard(page);
    await page.getByTestId("migration-target-db").fill("sin_leer");
    await page.getByTestId("migration-next").click();
    await expect(page.getByTestId("migration-existing-film_in_stock")).toContainText("no se puede leer su código");
    await expect(page.getByTestId("migration-existing-rewards_report")).toContainText("SHOW_ROUTINE");
    await expect(page.getByTestId("migration-pick-routine-film_in_stock")).toHaveCount(0);

    for (const n of ["actor", "film", "customer", "rental", "payment", "audit_log"]) await page.getByTestId(`migration-pick-table-${n}`).getByRole("checkbox").uncheck();
    await page.getByTestId("migration-pick-view-customer_list").getByRole("checkbox").uncheck();
    await page.getByTestId("migration-pick-trigger-ins_film").getByRole("checkbox").uncheck();
    await expect(page.getByTestId("migration-empty")).toContainText("2 rutinas, vistas o triggers no se pueden leer");
    await expect(page.getByTestId("migration-next")).toBeDisabled();
  });
});
