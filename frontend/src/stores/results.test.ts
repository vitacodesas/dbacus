import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { Cell, StatementResult } from "../types";

const api = vi.hoisted(() => ({ execute: vi.fn(), cancel: vi.fn(), getRows: vi.fn(), getCell: vi.fn(), releaseResult: vi.fn(), closeTab: vi.fn() }));
vi.mock("../api/queries", () => api);

import { MAX_CACHED_WINDOWS, WINDOW, useResultsStore } from "./results";

const TAB = "tab1";
const stmt = (index: number, over: Partial<StatementResult> = {}): StatementResult =>
  ({ index, kind: "rows", columns: [{ name: "n" }], rowCount: 0, truncated: false, affectedRows: 0, lastInsertId: 0, warnings: 0, elapsedMs: 0, sql: "", limitApplied: 0, start: 0, end: 0, ...over }) as StatementResult;
const rowsOf = (from: number, n: number): Cell[][] => Array.from({ length: n }, (_, i) => [{ v: from + i }]);
const flush = () => new Promise((r) => setTimeout(r, 0));

async function start(store: ReturnType<typeof useResultsStore>, queryId = "q1") {
  api.execute.mockResolvedValue(queryId);
  await store.execute({ tabId: TAB, sessionId: "s1", sql: "SELECT 1", baseOffset: 5, database: "app", autoLimit: 1000 });
  store.handleStarted({ queryId, tabId: TAB, sessionId: "s1", startedAt: "" });
}

describe("results store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    Object.values(api).forEach((f) => f.mockReset());
  });

  it("execute deja la pestaña en 'running' con el contexto de la ejecución", async () => {
    const s = useResultsStore();
    api.execute.mockResolvedValue("q1");
    await s.execute({ tabId: TAB, sessionId: "s1", sql: "SELECT 1", baseOffset: 12, database: "app", autoLimit: 100 });
    expect(api.execute).toHaveBeenCalledWith({ sessionId: "s1", tabId: TAB, sql: "SELECT 1", database: "app", maxRows: 0, autoLimit: 100 });
    expect(s.of(TAB)).toMatchObject({ status: "running", queryId: "q1", baseOffset: 12, sessionId: "s1" });
    expect(s.running(TAB)).toBe(true);
  });

  it("los eventos pueden llegar ANTES que la respuesta de Execute", async () => {
    const s = useResultsStore();
    let resolve!: (id: string) => void;
    api.execute.mockReturnValue(new Promise((r) => (resolve = r)));
    const p = s.execute({ tabId: TAB, sessionId: "s1", sql: "x", baseOffset: 0, database: "", autoLimit: 0 });
    s.handleStarted({ queryId: "q9", tabId: TAB, sessionId: "s1", startedAt: "" });
    s.handleStatement({ queryId: "q9", result: stmt(0) });
    s.handleDone({ queryId: "q9", results: [stmt(0, { rowCount: 3 })], elapsedMs: 5, database: "app" });
    resolve("q9");
    await p;
    expect(s.of(TAB).status).toBe("done");
    expect(s.of(TAB).queryId).toBe("q9");
    expect(s.of(TAB).rowCounts[0]).toBe(3);
  });

  it("un error de Execute (p. ej. pestaña ocupada) deja la pestaña en error", async () => {
    const s = useResultsStore();
    api.execute.mockRejectedValue({ cause: { code: "QUERY_FAILED", message: "Ya hay una ejecución en curso", detail: "busy" } });
    await s.execute({ tabId: TAB, sessionId: "s1", sql: "x", baseOffset: 0, database: "", autoLimit: 0 });
    expect(s.of(TAB)).toMatchObject({ status: "error", error: { detail: "busy" } });
  });

  it("flujo completo: statement → progress → done", async () => {
    const s = useResultsStore();
    await start(s);
    s.handleStatement({ queryId: "q1", result: stmt(0) });
    s.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount: 500 });
    expect(s.of(TAB).rowCounts[0]).toBe(500);
    s.handleDone({ queryId: "q1", results: [stmt(0, { rowCount: 800 })], elapsedMs: 42, database: "otra" });
    expect(s.of(TAB)).toMatchObject({ status: "done", elapsedMs: 42, database: "otra" });
    expect(s.of(TAB).rowCounts[0]).toBe(800);
  });

  it("ignora eventos de ejecuciones que ya no son la actual", async () => {
    const s = useResultsStore();
    await start(s, "q1");
    await start(s, "q2");
    s.handleDone({ queryId: "q1", results: [stmt(0, { rowCount: 9 })], elapsedMs: 1, database: "" });
    expect(s.of(TAB).status).toBe("running");
    s.handleProgress({ queryId: "desconocida", stmtIndex: 0, rowCount: 1 });
  });

  it("el foco va al primer resultado con filas, no se lo roban los siguientes", async () => {
    const s = useResultsStore();
    await start(s);
    s.handleStatement({ queryId: "q1", result: stmt(0, { kind: "exec" }) });
    s.handleStatement({ queryId: "q1", result: stmt(1) });
    s.handleStatement({ queryId: "q1", result: stmt(2) });
    expect(s.of(TAB).activeStmt).toBe(1);
  });

  it("error: guarda sentencia y rango; la cancelación es un estado aparte", async () => {
    const s = useResultsStore();
    await start(s, "q1");
    s.handleError({ queryId: "q1", stmtIndex: 2, error: { code: "QUERY_SYNTAX", message: "mal", detail: "at line 1", retryable: false }, start: 10, end: 20 });
    expect(s.of(TAB)).toMatchObject({ status: "error", errorStmt: 2, errorRange: { start: 10, end: 20 } });

    await start(s, "q2");
    s.handleError({ queryId: "q2", stmtIndex: 0, error: { code: "QUERY_CANCELED", message: "x", retryable: false }, start: 0, end: 5 });
    expect(s.of(TAB).status).toBe("canceled");

    await start(s, "q3");
    s.handleError({ queryId: "q3", stmtIndex: -1, error: { code: "SESSION_CLOSED", message: "x", retryable: false }, start: 0, end: 0 });
    expect(s.of(TAB).errorRange).toBeNull();
  });

  it("cancel solo actúa si hay una ejecución en curso", async () => {
    const s = useResultsStore();
    await s.cancel(TAB);
    expect(api.cancel).not.toHaveBeenCalled();
    await start(s);
    await s.cancel(TAB);
    expect(api.cancel).toHaveBeenCalledWith("q1");
  });

  describe("ventanas de filas", () => {
    it("pide la ventana al leer una fila y devuelve la fila cuando llega", async () => {
      const s = useResultsStore();
      await start(s);
      s.handleStatement({ queryId: "q1", result: stmt(0, { rowCount: 1000 }) });
      s.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount: 1000 });
      api.getRows.mockResolvedValue({ offset: 0, rows: rowsOf(0, WINDOW), total: 1000, complete: true });

      expect(s.rowAt(TAB, 0, 3)).toBeUndefined();
      await flush();
      expect(api.getRows).toHaveBeenCalledWith("q1", 0, 0, WINDOW);
      expect(s.rowAt(TAB, 0, 3)?.[0].v).toBe(3);
      expect(api.getRows).toHaveBeenCalledTimes(1);
    });

    it("peticiones simultáneas de la misma ventana se colapsan", async () => {
      const s = useResultsStore();
      await start(s);
      s.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount: 1000 });
      api.getRows.mockResolvedValue({ offset: 0, rows: rowsOf(0, WINDOW), total: 1000, complete: true });
      for (let i = 0; i < 10; i++) s.rowAt(TAB, 0, i);
      await flush();
      expect(api.getRows).toHaveBeenCalledTimes(1);
    });

    it("al pasar del 60 % de una ventana precarga la siguiente", async () => {
      const s = useResultsStore();
      await start(s);
      s.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount: 1000 });
      api.getRows.mockImplementation(async (_q, _s, off) => ({ offset: off, rows: rowsOf(off, WINDOW), total: 1000, complete: true }));
      s.rowAt(TAB, 0, 150);
      await flush();
      expect(api.getRows.mock.calls.map((c) => c[2]).sort((a, b) => a - b)).toEqual([0, WINDOW]);
    });

    it("conserva como máximo MAX_CACHED_WINDOWS ventanas (las menos usadas salen)", async () => {
      const s = useResultsStore();
      await start(s);
      s.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount: 100_000 });
      api.getRows.mockImplementation(async (_q, _s, off) => ({ offset: off, rows: rowsOf(off, WINDOW), total: 100_000, complete: true }));
      for (let w = 0; w < MAX_CACHED_WINDOWS + 3; w++) {
        s.rowAt(TAB, 0, w * WINDOW);
        await flush();
      }
      api.getRows.mockClear();
      expect(s.rowAt(TAB, 0, (MAX_CACHED_WINDOWS + 2) * WINDOW)).toBeDefined();
      expect(s.rowAt(TAB, 0, 0)).toBeUndefined(); // la más antigua se descartó
    });

    it("una ventana incompleta (consulta aún leyendo) se vuelve a pedir cuando llegan más filas", async () => {
      const s = useResultsStore();
      await start(s);
      s.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount: 50 });
      api.getRows.mockResolvedValueOnce({ offset: 0, rows: rowsOf(0, 50), total: 50, complete: false });
      s.rowAt(TAB, 0, 0);
      await flush();
      expect(s.rowAt(TAB, 0, 49)).toBeDefined();
      expect(s.rowAt(TAB, 0, 60)).toBeUndefined();

      s.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount: 150 });
      api.getRows.mockResolvedValueOnce({ offset: 0, rows: rowsOf(0, 150), total: 150, complete: true });
      s.rowAt(TAB, 0, 0);
      await flush();
      expect(s.rowAt(TAB, 0, 120)?.[0].v).toBe(120);
    });

    it("RESULT_EXPIRED marca el resultado como expirado y deja de pedir", async () => {
      const s = useResultsStore();
      await start(s);
      s.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount: 1000 });
      api.getRows.mockRejectedValue({ cause: { code: "RESULT_EXPIRED", message: "expiró" } });
      s.rowAt(TAB, 0, 0);
      await flush();
      expect(s.of(TAB).expired).toBe(true);
      api.getRows.mockClear();
      s.rowAt(TAB, 0, 5);
      await flush();
      expect(api.getRows).not.toHaveBeenCalled();
    });

    it("una respuesta tardía de una ejecución anterior se descarta", async () => {
      const s = useResultsStore();
      await start(s, "q1");
      s.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount: 1000 });
      let late!: (v: unknown) => void;
      api.getRows.mockReturnValueOnce(new Promise((r) => (late = r)));
      s.rowAt(TAB, 0, 0);

      await start(s, "q2");
      late({ offset: 0, rows: rowsOf(0, WINDOW), total: 1000, complete: true });
      await flush();
      s.handleProgress({ queryId: "q2", stmtIndex: 0, rowCount: 10 });
      api.getRows.mockResolvedValue({ offset: 0, rows: rowsOf(1000, 10), total: 10, complete: true });
      expect(s.rowAt(TAB, 0, 0)).toBeUndefined();
      await flush();
      expect(s.rowAt(TAB, 0, 0)?.[0].v).toBe(1000);
    });

    it("fetchRange trae bloques de 1000 sin pasar por la caché", async () => {
      const s = useResultsStore();
      await start(s);
      api.getRows.mockImplementation(async (_q, _s, off, lim) => ({ offset: off, rows: rowsOf(off, lim), total: 2500, complete: true }));
      const rows = await s.fetchRange(TAB, 0, 0, 2500);
      expect(rows).toHaveLength(2500);
      expect(api.getRows.mock.calls.map((c) => [c[2], c[3]])).toEqual([[0, 1000], [1000, 1000], [2000, 500]]);
    });
  });

  it("forgetTab olvida todo y descarta lo que llegue después", async () => {
    const s = useResultsStore();
    await start(s);
    s.forgetTab(TAB);
    expect(s.of(TAB).status).toBe("idle");
    s.handleDone({ queryId: "q1", results: [], elapsedMs: 1, database: "" });
    expect(s.of(TAB).status).toBe("idle");
  });
});
