import { connect, expect, mod, test } from "./fixtures";

test.use({ seed: { profiles: [] } });

test.describe("conexiones", () => {
  test("crear, probar y guardar una conexión", async ({ app: page }) => {
    await page.getByRole("button", { name: "Nueva conexión" }).first().click();
    await page.getByRole("textbox", { name: "Nombre" }).fill("Mi MySQL");
    await page.getByRole("textbox", { name: "Usuario" }).fill("root");
    await page.getByRole("textbox", { name: "Contraseña" }).fill("secreto");

    await page.getByRole("button", { name: "Probar conexión" }).click();
    await expect(page.getByText("8.0.36-mock")).toBeVisible();

    await page.getByRole("button", { name: "Guardar", exact: true }).click();
    await expect(page.getByRole("dialog")).toBeHidden();
    await expect(page.getByText("Mi MySQL", { exact: true }).first()).toBeVisible();
  });

  test("una prueba fallida muestra el paso que falló", async ({ app: page }) => {
    await page.getByRole("button", { name: "Nueva conexión" }).first().click();
    await page.getByRole("textbox", { name: "Nombre" }).fill("Roto");
    await page.getByRole("textbox", { name: "Usuario" }).fill("root");
    await page.getByPlaceholder("127.0.0.1").fill("bad.invalid");
    await page.getByRole("button", { name: "Probar conexión" }).click();
    await expect(page.getByText("La prueba falló")).toBeVisible();
    await expect(page.getByText("No se pudo resolver bad.invalid").first()).toBeVisible();
    await expect(page.getByRole("dialog")).toBeVisible(); // sigue abierto: no se guardó
  });

  test("importar desde un DSN rellena el formulario", async ({ app: page }) => {
    await page.getByRole("button", { name: "Nueva conexión" }).first().click();
    await page.getByRole("tab", { name: "Importar" }).click();
    await page.getByLabel("URL de conexión").fill("mysql://ana:pw@db.example.com:3307/ventas");
    await page.getByRole("button", { name: "Rellenar formulario" }).click();
    await page.getByRole("tab", { name: "General" }).click();
    await expect(page.getByRole("textbox", { name: "Usuario" })).toHaveValue("ana");
    await expect(page.getByRole("spinbutton", { name: "Puerto" })).toHaveValue("3307");
  });
});

test.describe("modo solo lectura", () => {
  test.use({ seed: { profiles: [{ id: "p-ro", name: "Producción", user: "app", readOnly: true, hasPassword: true }] } });

  test("rechaza una escritura antes de enviarla y lo señala en la barra de estado", async ({ app: page }) => {
    await connect(page, "Producción");
    await expect(page.getByTestId("status-readonly")).toBeVisible();
    await expect(page.getByTestId("card-readonly")).toBeVisible();

    await page.keyboard.press(`${mod}+t`);
    await page.locator(".monaco-editor").first().click();
    await page.keyboard.insertText("DROP TABLE actor");
    await page.keyboard.press(`${mod}+Enter`);
    await expect(page.getByText(/solo lectura/i).first()).toBeVisible();
    await expect(page.getByText("NOMBRE1")).toBeHidden();
    await expect(page.getByTestId("toolbar-readonly")).toBeVisible();
  });
});

test.describe("cambiar el modo con la sesión abierta", () => {
  test.use({ seed: { profiles: [{ id: "p-rw", name: "Staging", user: "app", hasPassword: true }] } });

  test("marcar «Solo lectura» protege al instante y ofrece reconectar", async ({ app: page }) => {
    await connect(page, "Staging");
    await expect(page.getByTestId("status-readwrite")).toBeVisible();
    await page.keyboard.press(`${mod}+t`);
    await page.locator(".monaco-editor").first().click();
    await page.keyboard.insertText("UPDATE actor SET first_name = 'X'");
    await page.keyboard.press(`${mod}+Enter`);
    await expect(page.getByText("3 filas afectadas")).toBeVisible(); // en lectura-escritura sí escribe

    await page.getByRole("button", { name: "Editar" }).first().click();
    await page.getByLabel("Solo lectura").check();
    await page.getByRole("button", { name: "Guardar", exact: true }).click();
    await expect(page.getByText(/se aplicarán al reconectar/)).toBeVisible();

    // Sin reconectar, Go ya rechaza la escritura.
    await page.locator(".monaco-editor").first().click();
    await page.keyboard.press(`${mod}+Enter`);
    await expect(page.getByText(/solo lectura/i).first()).toBeVisible();

    await page.getByRole("button", { name: "Reconectar" }).click();
    await expect(page.getByTestId("status-readonly")).toBeVisible();
  });
});
