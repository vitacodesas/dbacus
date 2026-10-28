import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
const api = vi.hoisted(() => ({ getSettings: vi.fn(), setSettings: vi.fn() }));
vi.mock("../api/settings", () => api);

import type { Settings } from "../types";
import { SIDEBAR_MAX, SIDEBAR_MIN, useUiStore } from "./ui";

describe("ui store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.useFakeTimers();
  });
  afterEach(() => vi.useRealTimers());

  it("resuelve el tema 'system' según el sistema", () => {
    const ui = useUiStore();
    ui.systemDark = true;
    expect(ui.resolvedTheme).toBe("dark");
    ui.theme = "light";
    expect(ui.resolvedTheme).toBe("light");
  });

  it("limita el ancho del sidebar", () => {
    const ui = useUiStore();
    ui.setSidebarWidth(9999);
    expect(ui.sidebarWidth).toBe(SIDEBAR_MAX);
    ui.resizeSidebar(-9999);
    expect(ui.sidebarWidth).toBe(SIDEBAR_MIN);
  });

  it("pulsar la vista activa pliega el sidebar y otra vista lo abre", () => {
    const ui = useUiStore();
    ui.selectSidebarView("connections");
    expect(ui.sidebarCollapsed).toBe(true);
    ui.selectSidebarView("history");
    expect(ui.sidebarCollapsed).toBe(false);
    expect(ui.sidebarView).toBe("history");
  });

  it("los toasts normales caducan y los de error no", () => {
    const ui = useUiStore();
    ui.toast("success", "ok");
    ui.toast("error", "mal");
    vi.advanceTimersByTime(5000);
    expect(ui.toasts.map((t) => t.kind)).toEqual(["error"]);
  });

  it("cambiar el idioma actualiza <html lang>", async () => {
    const ui = useUiStore();
    ui.locale = "en";
    await vi.waitFor(() => expect(document.documentElement.lang).toBe("en"));
  });

  describe("ajustes", () => {
    const saved: Settings = {
      theme: "dark", locale: "en", maxRows: 5000, maxResultMemoryMiB: 128, historyRetention: 100,
      showSystemDatabases: true, confirmEdits: false, sidebarWidth: 280, splitRatio: 0.4,
    };
    beforeEach(() => Object.values(api).forEach((f) => f.mockReset()));

    it("load aplica lo guardado y no vuelve a guardarlo", async () => {
      api.getSettings.mockResolvedValue(saved);
      const ui = useUiStore();
      await ui.load();
      expect(ui.theme).toBe("dark");
      expect(ui.locale).toBe("en");
      expect(ui.maxRows).toBe(5000);
      expect(ui.showSystemDatabases).toBe(true);
      await vi.advanceTimersByTimeAsync(1000);
      expect(api.setSettings).not.toHaveBeenCalled();
    });

    it("guarda con debounce un cambio de ancho tras cargar", async () => {
      api.getSettings.mockResolvedValue(saved);
      api.setSettings.mockResolvedValue(saved);
      const ui = useUiStore();
      await ui.load();
      ui.setSidebarWidth(300);
      ui.setSidebarWidth(320);
      await vi.advanceTimersByTimeAsync(499);
      expect(api.setSettings).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(2);
      expect(api.setSettings).toHaveBeenCalledTimes(1);
      expect(api.setSettings).toHaveBeenCalledWith({ ...saved, sidebarWidth: 320 });
    });

    it("no guarda nada si los ajustes no se llegaron a cargar", async () => {
      api.getSettings.mockRejectedValue(new Error("boom"));
      const ui = useUiStore();
      await ui.load();
      ui.setSidebarWidth(400);
      await vi.advanceTimersByTimeAsync(1000);
      expect(api.setSettings).not.toHaveBeenCalled();
      expect(ui.toasts.some((t) => t.kind === "error")).toBe(true);
    });

    it("saveSettings aplica lo que devuelve Go y, si lo rechaza, no cambia nada", async () => {
      const ui = useUiStore();
      api.setSettings.mockResolvedValueOnce(saved);
      expect(await ui.saveSettings(saved)).toBeNull();
      expect(ui.maxRows).toBe(5000);

      api.setSettings.mockRejectedValueOnce(Object.assign(new Error("rechazado"), { cause: JSON.stringify({ code: "VALIDATION", message: "Ajuste inválido", detail: "maxRows", retryable: false }) }));
      const err = await ui.saveSettings({ ...saved, maxRows: 1 });
      expect(err?.detail).toBe("maxRows");
      expect(ui.maxRows).toBe(5000);
    });
  });
});
