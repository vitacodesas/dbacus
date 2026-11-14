import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({
  planMigration: vi.fn(), startMigration: vi.fn(), cancelMigration: vi.fn(), migrationJob: vi.fn(), retryMigration: vi.fn(), verifyMigration: vi.fn(), cleanupMigration: vi.fn(),
}));
vi.mock("../../api/migration", () => api);
// Monaco no arranca en jsdom: el editor de solo lectura se sustituye por un doble.
vi.mock("../ui/CodeEditor.vue", () => ({ default: { props: ["modelValue"], template: "<pre data-testid='code-stub'>{{ modelValue }}</pre>" } }));

import { i18n } from "../../i18n";
import MigrationDialog from "./MigrationDialog.vue";
import { useConnectionsStore } from "../../stores/connections";
import { useMigrationStore } from "../../stores/migration";
import { useSessionsStore } from "../../stores/sessions";
import type { MigrationJob, MigrationPlan } from "../../types";

const apiError = (detail: string, message = "x", code = "QUERY_FAILED") => Object.assign(new Error(message), { cause: JSON.stringify({ code, message, detail, retryable: false }) });
const table = (name: string) => ({ name, estRows: 1000, estBytes: 1, chunks: 1, chunkKey: "pk", columns: 2, skippedColumns: [], skipped: false, structureOnly: false, warnings: [] });
const plan = (): MigrationPlan => ({
  planHash: "h", route: "go", tables: [table("a"), table("b")], views: [], routines: [], triggers: [], workers: 4,
  warnings: [{ code: "no_index", level: "warning", object: "b", message: "No tiene ningún índice" }], statements: ["CREATE TABLE a"], serverCaps: { maxConnections: 100, freeConnections: 80 }, targetNew: true,
});
const job = (over: Partial<MigrationJob> = {}): MigrationJob => ({
  id: "j", state: "running", phase: "data", route: "go", startedAt: "", elapsedMs: 10_000, rows: 1000, estRows: 2000, bytes: 0, tablesDone: 0, tablesAll: 2,
  tables: [{ name: "a", state: "copying", structureOnly: false, rowsDone: 1000, rowsEst: 1000, chunksDone: 1, chunksAll: 1 }, { name: "b", state: "failed", structureOnly: false, rowsDone: 0, rowsEst: 1000, chunksDone: 0, chunksAll: 1, error: "boom" }], objects: [], ...over,
});

let w: VueWrapper | null = null;
const q = <T extends Element = HTMLElement>(id: string) => document.body.querySelector<T>(`[data-testid=${id}]`);

function setup() {
  const pinia = createPinia();
  setActivePinia(pinia);
  const sessions = useSessionsStore();
  sessions.byId = { s1: { sessionId: "s1", profileId: "p1", serverVersion: "8.0", currentDatabase: "" }, s2: { sessionId: "s2", profileId: "p2", serverVersion: "8.0", currentDatabase: "" } };
  useConnectionsStore().profiles = [{ id: "p1", name: "Local", readOnly: false } as never, { id: "p2", name: "Producción", readOnly: true } as never];
  const m = useMigrationStore();
  w = mount(MigrationDialog, { attachTo: document.body, global: { plugins: [pinia, i18n] } });
  return m;
}

beforeEach(() => Object.values(api).forEach((f) => f.mockReset()));
afterEach(() => {
  w?.unmount();
  w = null;
  document.body.innerHTML = "";
});

describe("MigrationDialog", () => {
  it("paso 1: lista las conexiones (la de solo lectura, marcada y no elegible) y no avanza con el nombre vacío", async () => {
    const m = setup();
    m.openFor("s1", "sakila");
    await flushPromises();
    const opts = Array.from(document.body.querySelectorAll<HTMLOptionElement>("[data-testid=migration-target-session] option"));
    expect(opts.map((o) => o.textContent?.trim())).toEqual(["Local", "Producción (solo lectura)"]);
    expect(opts[1]!.disabled).toBe(true);
    expect(q("migration-route-hint")!.textContent).toContain("dentro del servidor");
    expect(q<HTMLButtonElement>("migration-next")!.disabled).toBe(false);
    m.targetDb = "  ";
    await flushPromises();
    expect(q<HTMLButtonElement>("migration-next")!.disabled).toBe(true);
  });

  it("traduce el error por su detalle y añade el mensaje de Go cuando trae lo concreto", async () => {
    const m = setup();
    m.openFor("s1", "sakila");
    api.planMigration.mockRejectedValueOnce(apiError("same_schema", "dup"));
    m.targetDb = "sakila";
    await m.loadCatalog();
    await flushPromises();
    expect(q("migration-error")!.textContent).toBe("El origen y el destino son el mismo schema.");
    api.planMigration.mockRejectedValueOnce(apiError("exists", "tablas que chocan: actor"));
    await m.loadCatalog();
    await flushPromises();
    expect(q("migration-error")!.textContent).toContain("ya tiene objetos");
    expect(q("migration-error")!.textContent).toContain("tablas que chocan: actor");
    api.planMigration.mockRejectedValueOnce(apiError("desconocido", "Mensaje de Go"));
    await m.loadCatalog();
    await flushPromises();
    expect(q("migration-error")!.textContent).toBe("Mensaje de Go");
  });

  it("paso 2 → 3: con todo marcado sigue; sin nada marcado avisa y bloquea; el plan trae avisos y SQL", async () => {
    api.planMigration.mockResolvedValue(plan());
    const m = setup();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await flushPromises();
    expect(q("migration-step-content-body")).not.toBeNull();
    expect(q("migration-pick-table-a")!.textContent).toContain("1000"); // el español agrupa miles solo desde 5 cifras
    m.pickedTables = [];
    await flushPromises();
    expect(q("migration-empty")).not.toBeNull();
    expect(q<HTMLButtonElement>("migration-next")!.disabled).toBe(true);
    m.pickedTables = ["a", "b"];
    await m.loadPlan();
    await flushPromises();
    expect(q("migration-plan-route")!.textContent).toContain("Por la app");
    expect(q("migration-warning-no_index")!.textContent).toContain("No tiene ningún índice");
    expect(q("code-stub")).not.toBeNull();
    expect(q("migration-start")).not.toBeNull();
  });

  it("solo estructura: casilla por tabla (apagada si la tabla no se migra), «todas/ninguna», tachado de las filas y resumen en el plan", async () => {
    api.planMigration.mockResolvedValue(plan());
    const m = setup();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await flushPromises();
    const box = (n: string) => q(`migration-structure-${n}`)!.querySelector("input") as HTMLInputElement;
    expect(box("a").checked).toBe(false);
    box("a").click();
    await flushPromises();
    expect(m.structureOnly).toEqual(["a"]);
    expect(q("migration-pick-table-a")!.querySelector(".line-through")).not.toBeNull();
    expect(q("migration-pick-table-b")!.querySelector(".line-through")).toBeNull();

    m.pickedTables = ["a"];
    await flushPromises();
    expect(box("b").disabled).toBe(true);
    q<HTMLButtonElement>("migration-structure-all")!.click();
    await flushPromises();
    expect(m.structureOnly).toEqual(["a"]); // «todas» solo marca las tablas que se migran
    q<HTMLButtonElement>("migration-structure-none")!.click();
    await flushPromises();
    expect(m.structureOnly).toEqual([]);

    api.planMigration.mockResolvedValue({ ...plan(), tables: [{ ...table("a"), structureOnly: true, estRows: 0, chunks: 0 }, table("b")] });
    m.pickedTables = ["a", "b"];
    await m.loadPlan();
    await flushPromises();
    expect(q("migration-plan-structure")!.textContent).toContain("1 solo estructura");
  });

  it("vistas, rutinas y triggers que ya existen en el destino: no se eligen y dicen si su código es igual", async () => {
    api.planMigration.mockResolvedValue({
      ...plan(),
      views: [{ name: "v_old", kind: "view", table: "", skipped: true, compare: "different" }],
      routines: [{ name: "p", kind: "procedure", table: "", skipped: false, compare: "" }, { name: "p_old", kind: "procedure", table: "", skipped: true, compare: "same" }],
      triggers: [{ name: "t_old", kind: "trigger", table: "a", skipped: true, compare: "unknown" }],
    });
    const m = setup();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await flushPromises();
    expect(q("migration-pick-routine-p")).not.toBeNull();
    expect(q("migration-pick-routine-p_old")).toBeNull();
    expect(q("migration-pick-view-v_old")).toBeNull();
    expect(q("migration-existing-p_old")!.textContent).toContain("mismo código");
    expect(q("migration-existing-v_old")!.textContent).toContain("código distinto");
    expect(q("migration-existing-v_old")!.textContent).toContain("no se reemplaza");
    expect(q("migration-existing-t_old")!.textContent).toContain("no se pudo comparar");
    expect(m.pickedRoutines).toEqual(["procedure:p"]);
  });

  it("rutinas que el usuario no puede leer: salen en el paso de contenido con el motivo y, si es todo lo que queda, el aviso lo explica", async () => {
    api.planMigration.mockResolvedValue({
      ...plan(),
      routines: [{ name: "lsp_get_schools", kind: "procedure", table: "", skipped: true, compare: "unreadable" }],
      views: [],
      triggers: [],
    });
    const m = setup();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await flushPromises();
    expect(q("migration-pick-routine-lsp_get_schools")).toBeNull();
    expect(q("migration-existing-lsp_get_schools")!.textContent).toContain("no se puede leer su código");
    expect(q("migration-existing-lsp_get_schools")!.textContent).toContain("SHOW_ROUTINE");
    m.pickedTables = [];
    await flushPromises();
    expect(q("migration-empty")!.textContent).toContain("1 rutinas, vistas o triggers no se pueden leer");
  });

  it("el plan y el progreso listan las rutinas, vistas y triggers", async () => {
    api.planMigration.mockResolvedValue({
      ...plan(),
      routines: [{ name: "p", kind: "procedure", table: "", skipped: false, compare: "" }],
      views: [{ name: "v_old", kind: "view", table: "", skipped: true, compare: "different" }],
    });
    api.startMigration.mockResolvedValue(job({ objects: [{ name: "p", kind: "procedure", state: "done", error: "" }] }));
    const m = setup();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await m.loadPlan();
    await flushPromises();
    expect(q("migration-plan-object-p")!.textContent).toContain("Procedimiento");
    expect(q("migration-plan-object-p")!.textContent).toContain("se crea");
    expect(q("migration-plan-object-v_old")!.textContent).toContain("código distinto");
    await m.start();
    await flushPromises();
    expect(q("migration-job-object-state-p")!.textContent).toBe("creada");
  });

  it("paso 4: porcentaje, velocidad y tiempo restante salen de lo que Go informa; en curso solo se puede cancelar o cerrar", async () => {
    api.planMigration.mockResolvedValue(plan());
    api.startMigration.mockResolvedValue(job());
    const m = setup();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await m.loadPlan();
    await m.start();
    await flushPromises();
    expect(q("migration-progress")!.getAttribute("aria-valuenow")).toBe("50");
    expect(q("migration-numbers")!.textContent).toContain("100 filas/s");
    expect(q("migration-numbers")!.textContent).toContain("00:00:10"); // tiempo transcurrido
    expect(q("migration-numbers")!.textContent).toContain("restantes");
    expect(q("migration-phase")!.textContent).toContain("Copiando datos");
    expect(q("migration-cancel")).not.toBeNull();
    expect(q("migration-retry")).toBeNull();
    expect(q("migration-cleanup")).toBeNull();
    await q<HTMLButtonElement>("migration-cancel")!.click();
    expect(api.cancelMigration).toHaveBeenCalledWith("j");
  });

  it("al terminar con fallos: lista el error, ofrece Reintentar y exige escribir el nombre para limpiar", async () => {
    api.planMigration.mockResolvedValue(plan());
    api.startMigration.mockResolvedValue(job());
    api.migrationJob.mockResolvedValue({ job: job({ state: "partial" }), failed: [{ object: "b", kind: "table", error: "boom" }], created: [{ name: "a", kind: "table" }], warnings: [], verification: null });
    api.cleanupMigration.mockResolvedValue(undefined);
    const m = setup();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await m.loadPlan();
    await m.start();
    await m.handleDone({ jobId: "j", state: "partial", failed: 1, elapsedMs: 5 });
    await flushPromises();
    expect(q("migration-failed")!.textContent).toContain("boom");
    expect(q("migration-table-state-b")!.textContent).toBe("falló");
    expect(q("migration-retry")).not.toBeNull();
    expect(q("migration-cancel")).toBeNull();

    q<HTMLButtonElement>("migration-cleanup")!.click();
    await flushPromises();
    const go = () => q<HTMLButtonElement>("migration-cleanup-go")!;
    expect(go().disabled).toBe(true);
    const input = q<HTMLInputElement>("migration-cleanup-confirm")!;
    input.value = "otro";
    input.dispatchEvent(new Event("input"));
    await flushPromises();
    expect(go().disabled).toBe(true);
    input.value = "sakila_copia";
    input.dispatchEvent(new Event("input"));
    await flushPromises();
    expect(go().disabled).toBe(false);
    go().click();
    await flushPromises();
    expect(api.cleanupMigration).toHaveBeenCalledWith("j", "sakila_copia");
  });

  it("la verificación muestra huérfanos y recuentos distintos, o «sin huérfanos» si todo cuadra", async () => {
    api.planMigration.mockResolvedValue(plan());
    api.startMigration.mockResolvedValue(job());
    api.migrationJob.mockResolvedValue({ job: job({ state: "done" }), failed: [], created: [], warnings: [], verification: null });
    const m = setup();
    m.openFor("s1", "sakila");
    await m.loadCatalog();
    await m.loadPlan();
    await m.start();
    await m.handleDone({ jobId: "j", state: "done", failed: 0, elapsedMs: 5 });
    api.verifyMigration.mockResolvedValueOnce({ orphans: [{ foreignKey: "fk", table: "b", count: 1000, atLeast: true }], rowCounts: [{ table: "a", source: 5, target: 4 }], objects: [{ kind: "procedure", name: "lsp_get_schools", status: "different" }, { kind: "view", name: "v", status: "missing" }, { kind: "trigger", name: "t", status: "unreadable" }], objectsChecked: 5, checked: true });
    await m.verify();
    await flushPromises();
    expect(q("migration-orphans")!.textContent).toContain("b.fk: 1000 filas");
    expect(q("migration-orphans")!.textContent).toContain("al menos");
    expect(q("migration-rowcounts")!.textContent).toContain("origen 5, destino 4");
    expect(q("migration-object-lsp_get_schools")!.textContent).toContain("Procedimiento lsp_get_schools: código distinto");
    expect(q("migration-object-v")!.textContent).toContain("no está en el destino");
    expect(q("migration-object-t")!.textContent).toContain("no se pudo leer");
    expect(q("migration-objects-checked")!.textContent).toContain("5 rutinas, vistas y triggers comparados");
    expect(q("migration-verified-ok")).toBeNull();
    api.verifyMigration.mockResolvedValueOnce({ orphans: [], rowCounts: [], objects: [{ kind: "procedure", name: "lsp_get_schools", status: "same" }], objectsChecked: 1, checked: true });
    await m.verify();
    await flushPromises();
    expect(q("migration-verified-ok")).not.toBeNull();
    expect(q("migration-object-lsp_get_schools")!.textContent).toContain("Procedimiento lsp_get_schools: igual"); // los iguales también se ven
    expect(q("migration-objects-checked")!.textContent).toContain("1");
    api.verifyMigration.mockResolvedValueOnce({ orphans: [], rowCounts: [], objects: [{ kind: "function", name: "f", status: "different" }], objectsChecked: 1, checked: true });
    await m.verify();
    await flushPromises();
    expect(q("migration-verified-ok")).toBeNull(); // una rutina distinta ya no cuenta como «todo bien»
  });
});
