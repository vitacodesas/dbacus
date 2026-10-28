import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { i18n } from "../../i18n";

const api = vi.hoisted(() => ({ listHistory: vi.fn(), deleteHistory: vi.fn(), clearHistory: vi.fn(), saveTabs: vi.fn(), loadTabs: vi.fn(), closeTab: vi.fn() }));
vi.mock("../../api/history", () => api);
vi.mock("../../api/queries", () => api);

import { useConnectionsStore } from "../../stores/connections";
import { useQueryTabsStore } from "../../stores/queryTabs";
import { useSessionsStore } from "../../stores/sessions";
import { useUiStore } from "../../stores/ui";
import HistoryPanel from "./HistoryPanel.vue";

const entry = (id: string, over: Record<string, unknown> = {}) =>
  ({ id, profileId: "p1", sql: `SELECT ${id}\n  FROM t`, database: "app", executedAt: new Date(Date.now() - 120_000).toISOString(), elapsedMs: 12, ok: true, rowCount: 3, affectedRows: 0, errorMessage: "", ...over }) as never;

function boot(profileOver: Record<string, unknown> = {}) {
  const connections = useConnectionsStore();
  connections.profiles = [{ id: "p1", name: "Prod", color: "", ssh: {}, ...profileOver } as never];
  const sessions = useSessionsStore();
  sessions.byId = { s1: { sessionId: "s1", profileId: "p1", serverVersion: "8.0", currentDatabase: "app" } };
  sessions.activeSessionId = "s1";
}
const mountPanel = () => mount(HistoryPanel, { global: { plugins: [i18n] } });

beforeEach(() => {
  setActivePinia(createPinia());
  Object.values(api).forEach((f) => f.mockReset());
  api.saveTabs.mockResolvedValue(undefined);
  api.closeTab.mockResolvedValue(undefined);
  api.listHistory.mockResolvedValue({ entries: [entry("a"), entry("b", { ok: false, errorMessage: "Error de sintaxis", rowCount: 0 })], nextCursor: "9" });
});

describe("HistoryPanel", () => {
  it("lista las consultas de la conexión activa, con el error de las fallidas", async () => {
    boot();
    const w = mountPanel();
    await flushPromises();
    expect(api.listHistory).toHaveBeenCalledWith(expect.objectContaining({ profileId: "p1" }));
    const items = w.findAll('[data-testid="history-item"]');
    expect(items).toHaveLength(2);
    expect(items[0].text()).toContain("SELECT a FROM t"); // saltos de línea colapsados
    expect(items[0].text()).toContain("3 filas");
    expect(items[1].text()).toContain("Error de sintaxis");
    expect(w.text()).toContain("Prod");
  });

  it("sin conexión ni pestaña pide conectarse y no consulta nada", async () => {
    const w = mountPanel();
    await flushPromises();
    expect(w.text()).toContain("Conéctate");
    expect(api.listHistory).not.toHaveBeenCalled();
  });

  it("abrir en pestaña nueva usa el SQL y la base de la entrada", async () => {
    boot();
    const w = mountPanel();
    await flushPromises();
    await w.findAll('[data-testid="history-item"]')[0].find('button[aria-label="Abrir en una pestaña nueva"]').trigger("click");
    const tabs = useQueryTabsStore();
    expect(tabs.tabs).toHaveLength(1);
    expect(tabs.active).toMatchObject({ profileId: "p1", sql: "SELECT a\n  FROM t", database: "app" });
  });

  it("insertar añade al final de la pestaña activa; sin texto previo lo reemplaza", async () => {
    boot();
    const tabs = useQueryTabsStore();
    const id = tabs.openTab({ profileId: "p1", sql: "SELECT 1;  \n" });
    const w = mountPanel();
    await flushPromises();
    await w.findAll('[data-testid="history-item"]')[0].find('button[aria-label="Insertar en la pestaña activa"]').trigger("click");
    expect(tabs.byId(id)?.sql).toBe("SELECT 1;\n\nSELECT a\n  FROM t");
    expect(tabs.tabs).toHaveLength(1);
  });

  it("filtrar por estado y buscar vuelven a pedir a Go", async () => {
    boot();
    const w = mountPanel();
    await flushPromises();
    await w.findAll("button").find((b) => b.text() === "Con error")!.trigger("click");
    await flushPromises();
    expect(api.listHistory).toHaveBeenLastCalledWith(expect.objectContaining({ status: "error" }));

    vi.useFakeTimers();
    await w.find('input[type="search"]').setValue("  users ");
    await vi.advanceTimersByTimeAsync(300);
    vi.useRealTimers();
    expect(api.listHistory).toHaveBeenLastCalledWith(expect.objectContaining({ search: "users", status: "error" }));
  });

  it("eliminar una entrada la quita de la lista", async () => {
    boot();
    api.deleteHistory.mockResolvedValue(undefined);
    const w = mountPanel();
    await flushPromises();
    await w.findAll('[data-testid="history-item"]')[0].find('button[aria-label="Eliminar"]').trigger("click");
    await flushPromises();
    expect(api.deleteHistory).toHaveBeenCalledWith("a");
    expect(w.findAll('[data-testid="history-item"]')).toHaveLength(1);
  });

  it("copiar usa el portapapeles y avisa", async () => {
    boot();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const w = mountPanel();
    await flushPromises();
    await w.findAll('[data-testid="history-item"]')[0].find('button[aria-label="Copiar SQL"]').trigger("click");
    await flushPromises();
    expect(writeText).toHaveBeenCalledWith("SELECT a\n  FROM t");
    expect(useUiStore().toasts.at(-1)?.kind).toBe("success");
  });

  it("avisa cuando el perfil tiene el historial desactivado", async () => {
    boot({ noHistory: true });
    const w = mountPanel();
    await flushPromises();
    expect(w.text()).toContain("desactivado");
  });

  it("'Cargar más' pide la página siguiente con el cursor", async () => {
    boot();
    const w = mountPanel();
    await flushPromises();
    api.listHistory.mockResolvedValueOnce({ entries: [entry("c")], nextCursor: "" });
    await w.findAll("button").find((b) => b.text() === "Cargar más")!.trigger("click");
    await flushPromises();
    expect(api.listHistory).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: "9" }));
    expect(w.findAll('[data-testid="history-item"]')).toHaveLength(3);
  });

  it("limpiar pide confirmación antes de borrar", async () => {
    boot();
    api.clearHistory.mockResolvedValue(undefined);
    const w = mountPanel();
    await flushPromises();
    await w.find('button[aria-label="Limpiar historial"]').trigger("click");
    await flushPromises();
    expect(api.clearHistory).not.toHaveBeenCalled();
    const confirm = Array.from(document.body.querySelectorAll("button")).find((b) => b.textContent?.trim() === "Limpiar historial" && b.closest('[role="dialog"]'));
    expect(confirm).toBeTruthy();
    confirm!.click();
    await flushPromises();
    expect(api.clearHistory).toHaveBeenCalledWith("p1");
  });
});
