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

    expect(body().textContent).toContain("Entre 20 y 10 000 000");
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

  it("la sección de monitoreo parte de los valores actuales y muestra el error del campo que Go rechaza", async () => {
    const ui = useUiStore();
    ui.settingsOpen = true;
    await flushPromises();
    const f = (id: string) => body().querySelector<HTMLInputElement>(`[data-testid=${id}]`)!;
    expect(body().querySelector("[data-testid=settings-monitor]")).not.toBeNull();
    expect(f("settings-monitor-interval").value).toBe("30");
    expect(f("settings-monitor-raw").value).toBe("7");
    expect(f("settings-monitor-hourly").value).toBe("90");
    expect(f("settings-monitor-daily").value).toBe("365");
    expect(f("settings-monitor-maxmib").value).toBe("500");
    expect(f("settings-monitor-live-interval").value).toBe("2");

    f("settings-monitor-interval").value = "2";
    f("settings-monitor-interval").dispatchEvent(new Event("input"));
    api.setSettings.mockRejectedValue(Object.assign(new Error("x"), { cause: JSON.stringify({ code: "VALIDATION", message: "Ajuste inválido", detail: "monitorIntervalSec", retryable: false }) }));
    saveBtn().click();
    await flushPromises();
    expect(api.setSettings.mock.calls[0]![0]).toMatchObject({ monitorIntervalSec: 2, monitorRawDays: 7 });
    expect(body().textContent).toContain("Entre 5 y 900 segundos");
    expect(ui.settingsOpen).toBe(true);
  });
});
