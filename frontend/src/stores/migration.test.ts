import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({
  planMigration: vi.fn(), startMigration: vi.fn(), cancelMigration: vi.fn(), migrationJob: vi.fn(), retryMigration: vi.fn(), verifyMigration: vi.fn(), cleanupMigration: vi.fn(),
}));
vi.mock("../api/migration", () => api);

import { useMigrationStore } from "./migration";
import { useUiStore } from "./ui";
import type { MigrationJob, MigrationPlan } from "../types";

const table = (name: string, over = {}) => ({ name, estRows: 100, estBytes: 1000, chunks: 1, chunkKey: "pk", columns: 2, skippedColumns: [], skipped: false, structureOnly: false, warnings: [], ...over });
const plan = (over: Partial<MigrationPlan> = {}): MigrationPlan => ({
  planHash: "h1", route: "server", tables: [table("a"), table("b")], views: [{ name: "v", kind: "view", table: "", skipped: false, compare: "" }], routines: [{ name: "p", kind: "procedure", table: "", skipped: false, compare: "" }],
  triggers: [{ name: "t", kind: "trigger", table: "a", skipped: false, compare: "" }], workers: 4, warnings: [], statements: ["CREATE TABLE a"], serverCaps: { maxConnections: 100, freeConnections: 80 }, targetNew: true, ...over,
});
const job = (over: Partial<MigrationJob> = {}): MigrationJob => ({
  id: "j1", state: "running", phase: "data", route: "server", startedAt: "2026-11-14T10:00:00Z", elapsedMs: 0, rows: 0, estRows: 200, bytes: 0, tablesDone: 0, tablesAll: 2,
  tables: [{ name: "a", state: "copying", structureOnly: false, rowsDone: 0, rowsEst: 100, chunksDone: 0, chunksAll: 1 }, { name: "b", state: "pending", structureOnly: false, rowsDone: 0, rowsEst: 100, chunksDone: 0, chunksAll: 1 }], objects: [], ...over,
});

const apiError = (detail: string, message = "x", code = "QUERY_FAILED") => Object.assign(new Error(message), { cause: JSON.stringify({ code, message, detail, retryable: false }) });

beforeEach(() => {
  setActivePinia(createPinia());
  Object.values(api).forEach((f) => f.mockReset());
});

describe("migration store", () => {
  it("abre el asistente con el destino en la misma conexión y un nombre propuesto", () => {
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    expect(m).toMatchObject({ isOpen: true, step: "target", sourceSessionId: "s1", sourceDb: "sakila", targetSessionId: "s1", targetDb: "sakila_copia" });
  });

  it("paso 1 → 2: pide a Go un plan con todo (objetos nulos) y marca todo para elegir", async () => {
    api.planMigration.mockResolvedValue(plan());
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    expect(api.planMigration).toHaveBeenCalledWith(expect.objectContaining({ objects: { tables: null, structureOnly: [], views: null, routines: null, triggers: null }, target: { sessionId: "s1", database: "sakila_copia" } }));
    expect(m.step).toBe("content");
    expect(m.pickedTables).toEqual(["a", "b"]);
    expect(m.pickedRoutines).toEqual(["procedure:p"]);
  });

  it("paso 2 → 3: sin tocar la selección manda «todo» (nulos); si se desmarca algo, listas explícitas", async () => {
    api.planMigration.mockResolvedValue(plan());
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await m.loadPlan();
    expect(api.planMigration.mock.calls[1]![0].objects).toEqual({ tables: null, structureOnly: [], views: null, routines: null, triggers: null });
    expect(m.step).toBe("plan");

    m.back();
    m.pickedTables = ["a"];
    m.pickedRoutines = [];
    await m.loadPlan();
    expect(api.planMigration.mock.calls[2]![0].objects).toEqual({ tables: ["a"], structureOnly: [], views: ["v"], routines: [], triggers: ["t"] });
  });

  it("solo estructura: sin marcar manda listas vacías; al marcar una tabla viaja en `structureOnly` y desmarcarla de la selección la quita", async () => {
    api.planMigration.mockResolvedValue(plan());
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    expect(m.structureOnly).toEqual([]);

    m.structureOnly = ["a", "b"];
    m.pickedTables = ["a"]; // «b» ya no se migra: su marca no debe viajar
    await m.loadPlan();
    expect(api.planMigration.mock.calls[1]![0].objects).toEqual({ tables: ["a"], structureOnly: ["a"], views: ["v"], routines: [{ kind: "procedure", name: "p" }], triggers: ["t"] });

    m.back();
    m.pickedTables = ["a", "b"];
    m.structureOnly = ["b"];
    await m.loadPlan();
    expect(api.planMigration.mock.calls[2]![0].objects.structureOnly).toEqual(["b"]);
    expect(api.planMigration.mock.calls[2]![0].objects.tables).toEqual(["a", "b"]);
  });

  it("lo que ya existe en el destino no se elige ni rompe «todo»: sin tocar nada siguen yendo los nulos", async () => {
    api.planMigration.mockResolvedValue(plan({
      views: [{ name: "v", kind: "view", table: "", skipped: false, compare: "" }, { name: "v_old", kind: "view", table: "", skipped: true, compare: "different" }],
      routines: [{ name: "p", kind: "procedure", table: "", skipped: false, compare: "" }, { name: "p_old", kind: "procedure", table: "", skipped: true, compare: "same" }],
      triggers: [{ name: "t", kind: "trigger", table: "a", skipped: false, compare: "" }, { name: "t_old", kind: "trigger", table: "a", skipped: true, compare: "unknown" }],
    }));
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    expect(m.pickedViews).toEqual(["v"]);
    expect(m.pickedRoutines).toEqual(["procedure:p"]);
    expect(m.pickedTriggers).toEqual(["t"]);
    await m.loadPlan();
    expect(api.planMigration.mock.calls[1]![0].objects).toEqual({ tables: null, structureOnly: [], views: null, routines: null, triggers: null });
  });

  it("un error de Go se queda en el paso y conserva su detalle", async () => {
    api.planMigration.mockRejectedValue(apiError("exists", "El schema destino ya tiene objetos", "QUERY_FAILED"));
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    expect(m.step).toBe("target");
    expect(m.error).toMatchObject({ detail: "exists" });
    expect(m.busy).toBe(false);
  });

  it("start manda el hash del plan que se vio y muestra el progreso de los eventos", async () => {
    api.planMigration.mockResolvedValue(plan());
    api.startMigration.mockResolvedValue(job());
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await m.loadPlan();
    await m.start();
    expect(api.startMigration).toHaveBeenCalledWith(expect.anything(), "h1");
    expect(m).toMatchObject({ step: "run", running: true });

    m.handleProgress({ jobId: "j1", phase: "data", rows: 100, estRows: 200, bytes: 1200, elapsedMs: 900, tablesDone: 1, tablesTotal: 2, perTable: [{ name: "a", state: "done", rowsDone: 100, rowsEst: 100, chunksDone: 1, chunksAll: 1 }, { name: "b", state: "copying", rowsDone: 0, rowsEst: 100, chunksDone: 0, chunksAll: 1 }] });
    expect(m.job).toMatchObject({ rows: 100, tablesDone: 1 });
    expect(m.job?.tables?.[0]).toMatchObject({ name: "a", state: "done" });
    m.handleProgress({ jobId: "otro", phase: "x", rows: 999, estRows: 1, bytes: 1, elapsedMs: 1, tablesDone: 0, tablesTotal: 0, perTable: [] });
    expect(m.job?.rows).toBe(100);
  });

  it("si la migración termina antes de que Start devuelva el id, no se pierde el final", async () => {
    api.planMigration.mockResolvedValue(plan());
    api.migrationJob.mockResolvedValue({ job: job({ state: "done", rows: 200, tablesDone: 2 }), failed: [], created: [], warnings: [], verification: null });
    api.startMigration.mockImplementation(async () => {
      m.handleDone({ jobId: "j1", state: "done", failed: 0, elapsedMs: 5 }); // llega antes de la respuesta
      return job();
    });
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await m.loadPlan();
    await m.start();
    expect(m.job?.state).toBe("done");
    expect(m.finished).toBe(true);
    expect(useUiStore().toasts.some((x) => x.kind === "success")).toBe(true);
  });

  it("al terminar con fallos guarda el resultado y avisa; Reintentar repite lo fallido y Verificar guarda el informe", async () => {
    api.planMigration.mockResolvedValue(plan());
    api.startMigration.mockResolvedValue(job());
    const failed = { job: job({ state: "partial" }), failed: [{ object: "b", kind: "table", error: "boom" }], created: [{ name: "a", kind: "table" }], warnings: [], verification: null };
    api.migrationJob.mockResolvedValue(failed);
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await m.loadPlan();
    await m.start();
    await m.handleDone({ jobId: "j1", state: "partial", failed: 1, elapsedMs: 10 });
    expect(m.result?.failed).toHaveLength(1);
    expect(useUiStore().toasts.at(-1)).toMatchObject({ kind: "error" });

    api.retryMigration.mockResolvedValue(job());
    await m.retry();
    expect(api.retryMigration).toHaveBeenCalledWith("j1");
    expect(m.result).toBeNull();

    api.migrationJob.mockResolvedValue({ ...failed, job: job({ state: "done" }), failed: [] });
    await m.handleDone({ jobId: "j1", state: "done", failed: 0, elapsedMs: 20 });
    api.verifyMigration.mockResolvedValue({ orphans: [{ foreignKey: "fk", table: "b", count: 2, atLeast: false }], rowCounts: [], objects: [], objectsChecked: 0, checked: true });
    await m.verify();
    expect(api.verifyMigration).toHaveBeenCalledWith("j1", { foreignKeys: true, rowCounts: true, objects: true });
    expect(m.result?.verification?.orphans).toHaveLength(1);
  });

  it("limpiar destino pide el nombre y deja de ofrecer lo creado", async () => {
    api.planMigration.mockResolvedValue(plan());
    api.startMigration.mockResolvedValue(job());
    api.migrationJob.mockResolvedValue({ job: job({ state: "partial" }), failed: [], created: [{ name: "a", kind: "table" }], warnings: [], verification: null });
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await m.loadPlan();
    await m.start();
    await m.handleDone({ jobId: "j1", state: "partial", failed: 0, elapsedMs: 1 });
    api.cleanupMigration.mockRejectedValueOnce(apiError("confirm", "Escribe el nombre", "VALIDATION"));
    expect(await m.cleanup("otro")).toBe(false);
    expect(m.error).toMatchObject({ detail: "confirm" });
    api.cleanupMigration.mockResolvedValue(undefined);
    expect(await m.cleanup("sakila_copia")).toBe(true);
    expect(m.result?.created).toEqual([]);
  });

  it("si se cerró con la copia en curso y terminó sola, al reabrir se ve el resultado y no un asistente nuevo", async () => {
    api.planMigration.mockResolvedValue(plan());
    api.startMigration.mockResolvedValue(job());
    api.migrationJob.mockResolvedValue({ job: job({ state: "done", rows: 200 }), failed: [], created: [], warnings: [], verification: null });
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await m.loadPlan();
    await m.start();
    m.close();
    await m.handleDone({ jobId: "j1", state: "done", failed: 0, elapsedMs: 5 });
    m.openFor("s1", "sakila");
    expect(m).toMatchObject({ isOpen: true, step: "run" });
    expect(m.job?.state).toBe("done");
    m.close(); // ya visto: se descarta
    m.openFor("s1", "sakila");
    expect(m).toMatchObject({ step: "target", job: null });
  });

  it("cerrar con la migración en curso la deja seguir y reabrir vuelve al progreso", async () => {
    api.planMigration.mockResolvedValue(plan());
    api.startMigration.mockResolvedValue(job());
    const m = useMigrationStore();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await m.loadPlan();
    await m.start();
    m.close();
    expect(m.isOpen).toBe(false);
    expect(m.running).toBe(true);
    m.openFor("s1", "otra"); // no empieza otra: vuelve a la que corre
    expect(m).toMatchObject({ isOpen: true, step: "run", sourceDb: "sakila" });
  });
});
