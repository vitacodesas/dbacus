import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({ saveTabs: vi.fn(), loadTabs: vi.fn(), closeTab: vi.fn(), execute: vi.fn(), cancel: vi.fn(), getRows: vi.fn() }));
vi.mock("../api/queries", () => api);

import { useQueryTabsStore } from "./queryTabs";
import { useResultsStore } from "./results";

describe("queryTabs store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    Object.values(api).forEach((f) => f.mockReset());
    api.saveTabs.mockResolvedValue(undefined);
    api.closeTab.mockResolvedValue(undefined);
    api.loadTabs.mockResolvedValue([]);
    vi.useFakeTimers();
  });
  afterEach(() => vi.useRealTimers());

  it("abre pestañas con títulos únicos por conexión y la activa", async () => {
    const s = useQueryTabsStore();
    await s.restore();
    const a = s.openTab({ profileId: "p1" });
    const b = s.openTab({ profileId: "p1" });
    const c = s.openTab({ profileId: "p2" });
    expect([a, b, c].map((id) => s.byId(id)?.title)).toEqual(["Consulta 1", "Consulta 2", "Consulta 1"]);
    expect(s.activeTabId).toBe(c);
    await s.closeTab(a);
    expect(s.byId(s.openTab({ profileId: "p1" }))?.title).toBe("Consulta 1");
  });

  it("restaura las pestañas guardadas en orden y activa la primera", async () => {
    api.loadTabs.mockResolvedValue([
      { id: "b", sessionProfileId: "p", title: "B", sql: "SELECT 2", database: "", order: 1 },
      { id: "a", sessionProfileId: "p", title: "A", sql: "SELECT 1", database: "app", order: 0 },
    ]);
    const s = useQueryTabsStore();
    await s.restore();
    expect(s.tabs.map((t) => t.id)).toEqual(["a", "b"]);
    expect(s.activeTabId).toBe("a");
    expect(s.tabs[0]).toMatchObject({ profileId: "p", sql: "SELECT 1", database: "app" });
  });

  it("no guarda nada antes de restaurar (para no pisar lo persistido)", () => {
    const s = useQueryTabsStore();
    s.openTab({ profileId: "p" });
    vi.advanceTimersByTime(2000);
    expect(api.saveTabs).not.toHaveBeenCalled();
  });

  it("persiste con debounce y envía el orden", async () => {
    const s = useQueryTabsStore();
    await s.restore();
    const a = s.openTab({ profileId: "p", sql: "SELECT 1" });
    const b = s.openTab({ profileId: "p" });
    for (const sql of ["S", "SE", "SEL"]) s.setSql(b, sql);
    expect(api.saveTabs).not.toHaveBeenCalled();
    vi.advanceTimersByTime(500);
    await vi.waitFor(() => expect(api.saveTabs).toHaveBeenCalledTimes(1));
    expect(api.saveTabs.mock.calls[0][0].map((t: { id: string; order: number; sql: string }) => [t.id, t.order, t.sql])).toEqual([[a, 0, "SELECT 1"], [b, 1, "SEL"]]);
  });

  it("un cambio sin efecto no dispara guardado", async () => {
    const s = useQueryTabsStore();
    await s.restore();
    const id = s.openTab({ profileId: "p", sql: "x" });
    vi.advanceTimersByTime(600);
    await vi.waitFor(() => expect(api.saveTabs).toHaveBeenCalledTimes(1));
    s.setSql(id, "x");
    s.setDatabase(id, "");
    vi.advanceTimersByTime(600);
    expect(api.saveTabs).toHaveBeenCalledTimes(1);
  });

  it("cerrar la pestaña activa activa la vecina, libera en Go y olvida su resultado", async () => {
    const s = useQueryTabsStore();
    await s.restore();
    const [a, b, c] = [s.openTab({ profileId: "p" }), s.openTab({ profileId: "p" }), s.openTab({ profileId: "p" })];
    s.setActive(b);
    const results = useResultsStore();
    const forget = vi.spyOn(results, "forgetTab");
    await s.closeTab(b);
    expect(api.closeTab).toHaveBeenCalledWith(b);
    expect(forget).toHaveBeenCalledWith(b);
    expect(s.activeTabId).toBe(c);
    await s.closeTab(c);
    expect(s.activeTabId).toBe(a);
    await s.closeTab(a);
    expect(s.activeTabId).toBeNull();
    await s.closeTab("no-existe");
  });

  it("cerrar tolera que Go ya no tenga la sesión", async () => {
    api.closeTab.mockRejectedValue(new Error("sesión cerrada"));
    const s = useQueryTabsStore();
    await s.restore();
    const id = s.openTab({ profileId: "p" });
    await expect(s.closeTab(id)).resolves.toBeUndefined();
    expect(s.tabs).toHaveLength(0);
  });

  it("renombrar ignora vacíos, mover reordena", async () => {
    const s = useQueryTabsStore();
    await s.restore();
    const [a, b, c] = [s.openTab({ profileId: "p" }), s.openTab({ profileId: "p" }), s.openTab({ profileId: "p" })];
    s.rename(a, "  Ventas  ");
    s.rename(b, "   ");
    expect(s.byId(a)?.title).toBe("Ventas");
    expect(s.byId(b)?.title).toBe("Consulta 2");
    s.move(0, 2);
    expect(s.tabs.map((t) => t.id)).toEqual([b, c, a]);
    s.move(0, 9);
    s.move(-1, 0);
    expect(s.tabs.map((t) => t.id)).toEqual([b, c, a]);
  });

  it("un fallo al guardar queda en lastError sin romper", async () => {
    api.saveTabs.mockRejectedValue({ cause: { code: "INTERNAL", message: "disco lleno" } });
    const s = useQueryTabsStore();
    await s.restore();
    s.openTab({ profileId: "p" });
    vi.advanceTimersByTime(600);
    await vi.waitFor(() => expect(s.lastError).toBe("disco lleno"));
  });
});
