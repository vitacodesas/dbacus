import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import BaseButton from "./BaseButton.vue";
import BaseInput from "./BaseInput.vue";
import BaseSplitter from "./BaseSplitter.vue";
import ColorSwatches from "./ColorSwatches.vue";

describe("BaseButton", () => {
  it("se deshabilita y marca aria-busy al cargar", () => {
    const w = mount(BaseButton, { props: { loading: true }, slots: { default: "Ok" } });
    expect(w.attributes("disabled")).toBeDefined();
    expect(w.attributes("aria-busy")).toBe("true");
  });
  it("emite click cuando está activo", async () => {
    const w = mount(BaseButton);
    await w.trigger("click");
    expect(w.emitted("click")).toHaveLength(1);
  });
});

describe("BaseInput", () => {
  it("enlaza label, error y v-model con ARIA", async () => {
    const w = mount(BaseInput, { props: { label: "Host", error: "Requerido", modelValue: "" } });
    const input = w.get("input");
    expect(w.get("label").attributes("for")).toBe(input.attributes("id"));
    expect(input.attributes("aria-invalid")).toBe("true");
    expect(input.attributes("aria-describedby")).toBe(w.get("p").attributes("id"));
    await input.setValue("db1");
    expect(w.emitted("update:modelValue")?.[0]).toEqual(["db1"]);
  });
});

describe("BaseSplitter", () => {
  it("emite deltas con el teclado según la orientación", async () => {
    const v = mount(BaseSplitter, { props: { orientation: "vertical" } });
    await v.trigger("keydown", { key: "ArrowRight" });
    await v.trigger("keydown", { key: "ArrowLeft" });
    expect(v.emitted("drag")).toEqual([[16], [-16]]);
    const h = mount(BaseSplitter, { props: { orientation: "horizontal" } });
    await h.trigger("keydown", { key: "ArrowLeft" });
    expect(h.emitted("drag")).toBeUndefined();
  });
});

describe("ColorSwatches", () => {
  const mk = (modelValue = "", extra = {}) => mount(ColorSwatches, { props: { label: "Color", noneLabel: "Sin color", modelValue, ...extra } });

  it("es un grupo de radios con «ninguno» y marca el seleccionado", () => {
    const w = mk("#22c55e");
    expect(w.get("[role=radiogroup]").attributes("aria-label")).toBe("Color");
    const radios = w.findAll("[role=radio]");
    expect(radios).toHaveLength(9); // ninguno + 8 colores
    expect(radios[0].attributes("aria-label")).toBe("Sin color");
    expect(radios.filter((r) => r.attributes("aria-checked") === "true").map((r) => r.attributes("aria-label"))).toEqual(["#22c55e"]);
  });

  it("sin valor marca «ninguno»", () => {
    const w = mk("");
    expect(w.findAll("[role=radio]").filter((r) => r.attributes("aria-checked") === "true")).toHaveLength(1);
    expect(w.get("[role=radio]").attributes("aria-checked")).toBe("true");
  });

  it("elegir un color o «ninguno» emite el valor", async () => {
    const w = mk("#22c55e");
    await w.get("[aria-label='#3b82f6']").trigger("click");
    await w.get("[aria-label='Sin color']").trigger("click");
    expect(w.emitted("update:modelValue")).toEqual([["#3b82f6"], [""]]);
  });

  it("muestra el error", () => {
    expect(mk("", { error: "Formato #RRGGBB" }).text()).toContain("Formato #RRGGBB");
  });
});
