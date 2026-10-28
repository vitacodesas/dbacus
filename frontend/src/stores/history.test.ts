import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({ listHistory: vi.fn(), deleteHistory: vi.fn(), clearHistory: vi.fn() }));
vi.mock("../api/history", () => api);

import { useHistoryStore } from "./history";

const entry = (id: string, over: Record<string, unknown> = {}) => ({ id, profileId: "p1", sql: `SELECT ${id}`, database: "", executedAt: "2026-10-28T10:00:00Z", elapsedMs: 5, ok: true, rowCount: 1, affectedRows: 0, errorMessage: "", ...over }) as never;

beforeEach(() => {
  setActivePinia(createPinia());
  vi.useRealTimers();
  Object.values(api).forEach((f) => f.mockReset());
});

describe("history store", () => {
  it("carga la primera página al elegir conexión y pagina con el cursor", async () => {
    api.listHistory.mockResolvedValueOnce({ entries: [entry("a"), entry("b")], nextCursor: "7" }).mockResolvedValueOnce({ entries: [entry("c")], nextCursor: "" });
    const h = useHistoryStore();
    h.setProfile("p1");
    await vi.waitFor(() => expect(h.entries).toHaveLength(2));
    expect(api.listHistory).toHaveBeenLastCalledWith({ profileId: "p1", search: "", status: "", limit: 50, cursor: "" });

    await h.more();
    expect(api.listHistory).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: "7" }));
    expect(h.entries.map((e) => e.id)).toEqual(["a", "b", "c"]);
    expect(h.nextCursor).toBe("");
    await h.more(); // sin cursor no hay petición
    expect(api.listHistory).toHaveBeenCalledTimes(2);
  });

  it("sin conexión no hay historial ni peticiones", async () => {
    const h = useHistoryStore();
    h.setProfile("p1");
    api.listHistory.mockClear();
    h.setProfile(null);
    await vi.waitFor(() => expect(h.entries).toEqual([]));
    expect(api.listHistory).not.toHaveBeenCalled();
  });

  it("una respuesta lenta de un filtro anterior no pisa la del filtro nuevo", async () => {
    let slow!: (v: unknown) => void;
    api.listHistory
      .mockImplementationOnce(() => new Promise((r) => (slow = r)))
      .mockResolvedValueOnce({ entries: [entry("nuevo")], nextCursor: "" });
    const h = useHistoryStore();
    h.setProfile("p1");
    h.setFilters({ status: "error" });
    await vi.waitFor(() => expect(h.entries.map((e) => e.id)).toEqual(["nuevo"]));
    slow({ entries: [entry("viejo")], nextCursor: "" });
    await Promise.resolve();
    expect(h.entries.map((e) => e.id)).toEqual(["nuevo"]);
    expect(h.loading).toBe(false);
  });

  it("history:added de otra conexión se ignora y las ráfagas se agrupan en una recarga", async () => {
    vi.useFakeTimers();
    api.listHistory.mockResolvedValue({ entries: [], nextCursor: "" });
    const h = useHistoryStore();
    h.setProfile("p1");
    await vi.advanceTimersByTimeAsync(0);
    api.listHistory.mockClear();
    h.handleAdded({ profileId: "otra" });
    h.handleAdded({ profileId: "p1" });
    h.handleAdded({ profileId: "p1" });
    h.handleAdded({ profileId: "p1" });
    await vi.advanceTimersByTimeAsync(300);
    expect(api.listHistory).toHaveBeenCalledTimes(1);
  });

  it("remove quita la entrada sin recargar; clear recarga", async () => {
    api.listHistory.mockResolvedValue({ entries: [entry("a"), entry("b")], nextCursor: "" });
    api.deleteHistory.mockResolvedValue(undefined);
    api.clearHistory.mockResolvedValue(undefined);
    const h = useHistoryStore();
    h.setProfile("p1");
    await vi.waitFor(() => expect(h.entries).toHaveLength(2));
    await h.remove("a");
    expect(api.deleteHistory).toHaveBeenCalledWith("a");
    expect(h.entries.map((e) => e.id)).toEqual(["b"]);
    api.listHistory.mockResolvedValue({ entries: [], nextCursor: "" });
    await h.clear();
    expect(api.clearHistory).toHaveBeenCalledWith("p1");
    expect(h.entries).toEqual([]);
  });

  it("un error de carga queda en error y se limpia al recargar", async () => {
    api.listHistory.mockRejectedValueOnce({ message: "caído" }).mockResolvedValueOnce({ entries: [entry("a")], nextCursor: "" });
    const h = useHistoryStore();
    h.setProfile("p1");
    await vi.waitFor(() => expect(h.error).not.toBeNull());
    await h.reload();
    expect(h.error).toBeNull();
    expect(h.entries).toHaveLength(1);
  });
});
