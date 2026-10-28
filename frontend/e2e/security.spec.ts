import { expect, test } from "./fixtures";

declare const process: { env: Record<string, string | undefined> };

test.describe("CSP del build (spec 10)", () => {
  test("bloquea scripts inline y conexiones a otros orígenes", async ({ app: page, csp }) => {
    test.skip(!!process.env.E2E_DEV, "El servidor de desarrollo no lleva CSP");

    await page.evaluate(() => {
      const s = document.createElement("script");
      s.textContent = "window.__inline = true";
      document.head.append(s);
    });
    expect(await page.evaluate(() => (window as unknown as { __inline?: boolean }).__inline)).toBeUndefined();

    const reached = await page.evaluate(() => fetch("https://example.com/").then(() => true, () => false));
    expect(reached).toBe(false);

    expect(csp.some((m) => /script-src/.test(m))).toBe(true);
    expect(csp.some((m) => /connect-src/.test(m))).toBe(true);
    csp.length = 0; // provocadas a propósito
  });
});
