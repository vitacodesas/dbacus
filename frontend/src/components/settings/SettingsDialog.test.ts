import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { i18n } from "../../i18n";

const api = vi.hoisted(() => ({ getSettings: vi.fn(), setSettings: vi.fn() }));
vi.mock("../../api/settings", () => api);

import { useUiStore } from "../../stores/ui";
import SettingsDialog from "./SettingsDialog.vue";

let w: VueWrapper | null = null;
const body = () => document.body;
const input = (label: string) => {
  const l = Array.from(body().querySelectorAll("label")).find((x) => x.textContent?.includes(label))!;
  return body().querySelector<HTMLInputElement>(`#${l.getAttribute("for")}`)!;
};
const saveBtn = () => body().querySelector<HTMLButtonElement>("[data-testid=settings-save]")!;

beforeEach(() => {
  setActivePinia(createPinia());
  Object.values(api).forEach((f) => f.mockReset());
  w = mount(SettingsDialog, { attachTo: document.body, global: { plugins: [i18n] } });
});
afterEach(() => {
  w?.unmount();
  w = null;
  document.body.innerHTML = "";
});

describe("SettingsDialog", () => {
  it("parte de los valores actuales y guarda los editados", async () => {
    const ui = useUiStore();
    ui.settingsOpen = true;
    await flushPromises();
    expect(input("Máximo de filas").value).toBe("100000");

    const el = input("Máximo de filas");
    el.value = "2500";
    el.dispatchEvent(new Event("input"));
    api.setSettings.mockImplementation(async (s) => s);
    saveBtn().click();
    await flushPromises();

    expect(api.setSettings).toHaveBeenCalledWith(expect.objectContaining({ maxRows: 2500, theme: "system" }));
    expect(ui.maxRows).toBe(2500);
    expect(ui.settingsOpen).toBe(false);
  });

  it("muestra el error de validación de Go en el campo y deja el diálogo abierto", async () => {
    const ui = useUiStore();
    ui.settingsOpen = true;
    await flushPromises();
    const el = input("Máximo de filas");
    el.value = "5";
    el.dispatchEvent(new Event("input"));
    api.setSettings.mockRejectedValue(Object.assign(new Error("rechazado"), { cause: JSON.stringify({ code: "VALIDATION", message: "Ajuste inválido", detail: "maxRows", retryable: false }) }));
    saveBtn().click();
    await flushPromises();

    expect(body().textContent).toContain("Entre 1 000 y 10 000 000");
    expect(ui.settingsOpen).toBe(true);
    expect(ui.maxRows).toBe(100_000);
  });

  it("Cancelar descarta los cambios", async () => {
    const ui = useUiStore();
    ui.settingsOpen = true;
    await flushPromises();
    const el = input("Máximo de filas");
    el.value = "2500";
    el.dispatchEvent(new Event("input"));
    Array.from(body().querySelectorAll("button")).find((b) => b.textContent?.trim() === "Cancelar")!.click();
    await flushPromises();
    expect(api.setSettings).not.toHaveBeenCalled();
    ui.settingsOpen = true;
    await flushPromises();
    expect(input("Máximo de filas").value).toBe("100000");
  });
});
