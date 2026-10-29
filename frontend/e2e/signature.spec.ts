import { connect, expect, mod, SAKILA, test } from "./fixtures";

// Spec 17 §B: la firma vive en la barra de estado, que es lo único siempre visible en la esquina inferior izquierda.
test.describe("firma de la organización", () => {
  test("está abajo a la izquierda con la barra lateral colapsada y a la anchura mínima", async ({ app: page }) => {
    await page.setViewportSize({ width: 960, height: 600 });
    await page.keyboard.press(`${mod}+b`);
    const sig = page.getByTestId("signature");
    await expect(sig).toHaveText("Hecho con amor por vitacodesas");
    const box = (await sig.boundingBox())!;
    expect(box.x, "pegada al borde izquierdo").toBeLessThan(24);
    expect(box.y + box.height, "en la franja inferior").toBeGreaterThan(560);
  });

  test.describe("con un nombre de conexión larguísimo", () => {
    const name = "Producción · réplica de lectura · clúster principal de facturación · región europa-oeste ".repeat(3).trim();
    test.use({ seed: { profiles: [{ ...SAKILA, name }] } });

    test("el nombre se trunca y la firma queda entera", async ({ app: page }) => {
      await page.setViewportSize({ width: 960, height: 600 });
      await connect(page, name);
      const sig = (await page.getByTestId("signature").boundingBox())!;
      const session = (await page.getByTestId("status-session").boundingBox())!;
      expect(sig.width, "la firma no se encoge").toBeGreaterThan(150);
      expect(session.x, "la sesión empieza a la derecha de la firma").toBeGreaterThanOrEqual(sig.x + sig.width);
      expect(session.x + session.width, "el nombre no se sale de la ventana").toBeLessThanOrEqual(960);
      expect(session.height, "una sola línea: la barra mide 24 px").toBeLessThanOrEqual(24);
      const nameEl = page.getByTestId("status-session").locator(".truncate");
      expect(await nameEl.evaluate((el) => el.scrollWidth > el.clientWidth), "el nombre se corta con puntos suspensivos").toBe(true);
    });
  });
});
