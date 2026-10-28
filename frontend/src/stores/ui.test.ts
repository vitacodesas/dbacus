import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
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
});
