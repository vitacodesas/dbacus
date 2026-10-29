import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { i18n, setLocale } from "../../i18n";
import StatusBar from "./StatusBar.vue";

beforeEach(() => setActivePinia(createPinia()));
afterEach(() => setLocale("es"));

const signature = () => mount(StatusBar, { global: { plugins: [i18n] } }).get("[data-testid=signature]");

describe("StatusBar · firma de la organización", () => {
  it("se muestra en español y no se encoge", () => {
    const el = signature();
    expect(el.text()).toBe("Hecho con amor por vitacodesas");
    expect(el.classes()).toContain("shrink-0");
  });

  it("se muestra en inglés", () => {
    setLocale("en");
    expect(signature().text()).toBe("Made with love by vitacodesas");
  });

  it("va antes que el estado de sesión (esquina inferior izquierda)", () => {
    const html = mount(StatusBar, { global: { plugins: [i18n] } }).html();
    expect(html.indexOf("data-testid=\"signature\"")).toBeGreaterThan(-1);
    expect(html.indexOf("data-testid=\"signature\"")).toBeLessThan(html.indexOf("Sin sesión"));
  });
});
