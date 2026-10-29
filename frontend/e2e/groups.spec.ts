import { expect, test } from "./fixtures";

// Spec 17: grupos de conexiones. El grupo se define en el modal de la conexión; editar y borrar, desde su cabecera.
const RED = "rgb(239, 68, 68)";
const BLUE = "rgb(59, 130, 246)";
const GREEN = "rgb(34, 197, 94)";

const card = (page: import("@playwright/test").Page, name: string) => page.getByTestId(`connection-${name}`);
const header = (page: import("@playwright/test").Page, name: string) => page.getByTestId(`group-${name}`);

test.describe("crear un grupo desde el modal de la conexión", () => {
  test.use({ seed: { profiles: [], groups: [] } });

  test("el grupo nuevo se crea con color y la conexión aparece bajo su cabecera", async ({ app: page }) => {
    await page.getByRole("button", { name: "Nueva conexión" }).first().click();
    await page.getByRole("textbox", { name: "Nombre" }).fill("Mi MySQL");
    await page.getByRole("textbox", { name: "Usuario" }).fill("root");
    await page.getByLabel("Grupo", { exact: true }).selectOption({ label: "＋ Nuevo grupo…" });
    await page.getByRole("textbox", { name: "Nombre del grupo nuevo" }).fill("Producción");
    await page.getByTestId("new-group").getByRole("radio", { name: "#ef4444" }).click();
    await page.getByRole("button", { name: "Guardar", exact: true }).click();

    await expect(page.getByRole("dialog")).toBeHidden();
    await expect(header(page, "Producción")).toBeVisible();
    await expect(card(page, "Mi MySQL")).toBeVisible();
    await expect(card(page, "Mi MySQL")).toHaveCSS("border-left-color", RED); // hereda el color del grupo
    await expect(header(page, "Producción").getByTestId("group-dot")).toHaveCSS("background-color", RED);
  });

  test("un nombre de grupo repetido se rechaza junto al campo y el modal sigue abierto", async ({ app: page }) => {
    await page.getByRole("button", { name: "Nueva conexión" }).first().click();
    await page.getByRole("textbox", { name: "Nombre" }).fill("Una");
    await page.getByRole("textbox", { name: "Usuario" }).fill("root");
    await page.getByLabel("Grupo", { exact: true }).selectOption({ label: "＋ Nuevo grupo…" });
    await page.getByRole("textbox", { name: "Nombre del grupo nuevo" }).fill("Dev");
    await page.getByRole("button", { name: "Guardar", exact: true }).click();
    await expect(header(page, "Dev")).toBeVisible();

    await page.getByRole("button", { name: "Nueva conexión" }).first().click();
    await page.getByRole("textbox", { name: "Nombre" }).fill("Otra");
    await page.getByRole("textbox", { name: "Usuario" }).fill("root");
    await page.getByLabel("Grupo", { exact: true }).selectOption({ label: "＋ Nuevo grupo…" });
    await page.getByRole("textbox", { name: "Nombre del grupo nuevo" }).fill("DEV");
    await page.getByRole("button", { name: "Guardar", exact: true }).click();

    await expect(page.getByText("Ya existe un grupo con ese nombre")).toBeVisible();
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(card(page, "Otra")).toBeHidden();
  });

  test("un grupo nuevo sin nombre se rechaza antes de enviar nada", async ({ app: page }) => {
    await page.getByRole("button", { name: "Nueva conexión" }).first().click();
    await page.getByRole("textbox", { name: "Nombre" }).fill("Una");
    await page.getByRole("textbox", { name: "Usuario" }).fill("root");
    await page.getByLabel("Grupo", { exact: true }).selectOption({ label: "＋ Nuevo grupo…" });
    await page.getByRole("button", { name: "Guardar", exact: true }).click();
    await expect(page.getByTestId("new-group").getByText("Obligatorio")).toBeVisible();
    await expect(page.getByRole("dialog")).toBeVisible();
  });
});

test.describe("lista agrupada", () => {
  test.use({
    seed: {
      groups: [{ id: "g1", name: "Prod", color: "#ef4444" }, { id: "g2", name: "Dev", color: "" }],
      profiles: [
        { id: "a", name: "Alfa", groupId: "g1" },
        { id: "b", name: "Beta", groupId: "g1", color: "#3b82f6" },
        { id: "c", name: "Gamma", groupId: "g2" },
        { id: "d", name: "Delta" },
      ],
    },
  });

  test("secciones por grupo, «Sin grupo» al final, y el color de la conexión anula el del grupo", async ({ app: page }) => {
    await expect(header(page, "Dev")).toBeVisible();
    await expect(header(page, "Prod")).toBeVisible();
    await expect(header(page, "Sin grupo")).toBeVisible();
    const order = await page.getByTestId("group-toggle").evaluateAll((els) => els.map((e) => e.getAttribute("aria-label")));
    expect(order).toEqual(["Plegar o desplegar el grupo Dev", "Plegar o desplegar el grupo Prod", "Plegar o desplegar el grupo Sin grupo"]);

    await expect(card(page, "Alfa")).toHaveCSS("border-left-color", RED); // color del grupo
    await expect(card(page, "Beta")).toHaveCSS("border-left-color", BLUE); // el suyo propio manda
    await expect(card(page, "Gamma")).toHaveCSS("border-left-color", "rgba(0, 0, 0, 0)"); // grupo sin color
  });

  test("plegar oculta las conexiones y buscar las vuelve a mostrar", async ({ app: page }) => {
    const toggle = header(page, "Prod").getByTestId("group-toggle");
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(card(page, "Alfa")).toBeHidden();
    await expect(card(page, "Gamma")).toBeVisible(); // otros grupos no se tocan

    await page.getByRole("searchbox").fill("alfa");
    await expect(card(page, "Alfa")).toBeVisible(); // con búsqueda, las coincidencias no quedan ocultas
    await expect(header(page, "Dev")).toBeHidden(); // y los grupos sin resultados desaparecen
    await page.getByRole("searchbox").fill("");
    await expect(card(page, "Alfa")).toBeHidden(); // sigue plegado al limpiar la búsqueda

    await toggle.click();
    await expect(card(page, "Alfa")).toBeVisible();
  });

  test("buscar por el nombre de un grupo muestra todas sus conexiones", async ({ app: page }) => {
    await page.getByRole("searchbox").fill("prod");
    await expect(card(page, "Alfa")).toBeVisible();
    await expect(card(page, "Beta")).toBeVisible();
    await expect(card(page, "Gamma")).toBeHidden();
  });

  test("editar el color del grupo cambia el de las conexiones sin color propio y no el de las que lo tienen", async ({ app: page }) => {
    await header(page, "Prod").getByTestId("group-edit").click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("radio", { name: "#22c55e" }).click();
    await dialog.getByTestId("group-save").click();
    await expect(dialog).toBeHidden();
    await expect(card(page, "Alfa")).toHaveCSS("border-left-color", GREEN);
    await expect(card(page, "Beta")).toHaveCSS("border-left-color", BLUE);
    await expect(header(page, "Prod").getByTestId("group-dot")).toHaveCSS("background-color", GREEN);
  });

  test("eliminar un grupo deja sus conexiones en «Sin grupo» sin borrarlas", async ({ app: page }) => {
    await header(page, "Prod").getByTestId("group-edit").click();
    await page.getByRole("dialog").getByTestId("group-delete").click();
    const confirm = page.getByRole("dialog");
    await expect(confirm).toContainText("Sus conexiones (2) pasarán a «Sin grupo»");
    await confirm.getByRole("button", { name: "Eliminar grupo" }).click();

    await expect(header(page, "Prod")).toBeHidden();
    await expect(page.getByText("Grupo eliminado")).toBeVisible();
    await expect(card(page, "Alfa")).toBeVisible();
    await expect(card(page, "Beta")).toBeVisible();
    await expect(card(page, "Alfa")).toHaveCSS("border-left-color", "rgba(0, 0, 0, 0)"); // ya no hereda el rojo
    await expect(card(page, "Beta")).toHaveCSS("border-left-color", BLUE);
    await expect(header(page, "Sin grupo")).toContainText("3");
  });

  test("al editar una conexión se ve su grupo y se puede cambiar", async ({ app: page }) => {
    await card(page, "Alfa").hover();
    await card(page, "Alfa").getByRole("button", { name: "Editar" }).click();
    const group = page.getByLabel("Grupo", { exact: true });
    await expect(group).toHaveValue("g1");
    await group.selectOption({ label: "Dev" });
    await page.getByRole("button", { name: "Guardar", exact: true }).click();
    await expect(page.getByRole("dialog")).toBeHidden();
    await expect(header(page, "Dev")).toContainText("2");
    await expect(header(page, "Prod")).toContainText("1");
  });
});
