import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({ getEditInfo: vi.fn(), stageEdit: vi.fn(), unstageEdit: vi.fn(), discardEdits: vi.fn(), previewEdits: vi.fn(), applyEdits: vi.fn() }));
vi.mock("../api/edits", () => api);
const queries = vi.hoisted(() => ({ execute: vi.fn(), cancel: vi.fn(), getRows: vi.fn() }));
vi.mock("../api/queries", () => queries);

import { useEditsStore } from "./edits";
import { useResultsStore } from "./results";
import { useUiStore } from "./ui";
import type { EditInfo } from "../types";

const info = (over: Partial<EditInfo> = {}): EditInfo => ({
  editable: true, reason: "", database: "app", table: "t", keyColumns: [0], pending: 0,
  columns: [{ editable: false, reason: "key", dataType: "int", nullable: false }, { editable: true, reason: "", dataType: "varchar", nullable: true }], ...over,
});
const apiError = (message: string, code = "VALIDATION") => Object.assign(new Error(message), { cause: JSON.stringify({ code, message, detail: "cell", retryable: false }) });

async function openResult(tabId = "t1", queryId = "q1") {
  const results = useResultsStore();
  queries.execute.mockResolvedValue(queryId);
  await results.execute({ tabId, sessionId: "s", sql: "SELECT * FROM t", baseOffset: 0, database: "", autoLimit: 0 });
  results.handleStarted({ queryId, tabId, sessionId: "s", startedAt: "" });
  return results;
}

beforeEach(() => {
  setActivePinia(createPinia());
  [api, queries].forEach((a) => Object.values(a).forEach((f) => f.mockReset()));
  queries.getRows.mockResolvedValue({ offset: 0, total: 0, complete: true, rows: [] });
});

describe("edits store", () => {
  it("guarda el veredicto de Go y dice qué columnas se pueden editar", async () => {
    api.getEditInfo.mockResolvedValue(info());
    const edits = useEditsStore();
    await edits.load("q1", 0);
    expect(edits.isEditable("q1", 0, 1)).toBe(true);
    expect(edits.isEditable("q1", 0, 0)).toBe(false); // clave
    expect(edits.isEditable("q1", 0, 9)).toBe(false); // fuera de rango
    expect(edits.isEditable("otro", 0, 1)).toBe(false); // sin veredicto
  });

  it("un resultado sin veredicto (liberado, sesión cerrada) no ofrece edición", async () => {
    api.getEditInfo.mockRejectedValue(apiError("expiró", "RESULT_EXPIRED"));
    const edits = useEditsStore();
    await edits.load("q1", 0);
    expect(edits.infoOf("q1", 0)).toBeNull();
  });

  it("stage envía el texto a Go, actualiza el contador y devuelve el error de validación sin tocar nada", async () => {
    await openResult();
    const edits = useEditsStore();
    api.stageEdit.mockResolvedValueOnce(info({ pending: 1 }));
    expect(await edits.stage("t1", "q1", 0, 3, 1, { null: false, text: "x" })).toBeNull();
    expect(api.stageEdit).toHaveBeenCalledWith("q1", 0, 3, 1, { null: false, text: "x" });
    expect(edits.infoOf("q1", 0)?.pending).toBe(1);
    expect(edits.pendingFor("t1")).toBe(1);

    api.stageEdit.mockRejectedValueOnce(apiError("«abc» no es un entero válido"));
    const err = await edits.stage("t1", "q1", 0, 3, 1, { null: false, text: "abc" });
    expect(err?.message).toContain("no es un entero");
    expect(edits.infoOf("q1", 0)?.pending).toBe(1); // lo rechazado no cambia el contador
  });

  it("stage vuelve a pedir las ventanas ya cargadas para ver el valor superpuesto por Go", async () => {
    const results = await openResult();
    results.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount: 5 });
    queries.getRows.mockResolvedValue({ offset: 0, total: 5, complete: true, rows: [[{ v: 1 }, { v: "a" }]] });
    results.rowAt("t1", 0, 0); // pide la ventana 0
    await vi.waitFor(() => expect(queries.getRows).toHaveBeenCalledTimes(1));
    queries.getRows.mockClear();

    api.stageEdit.mockResolvedValue(info({ pending: 1 }));
    await useEditsStore().stage("t1", "q1", 0, 0, 1, { null: false, text: "x" });
    expect(queries.getRows).toHaveBeenCalledWith("q1", 0, 0, expect.any(Number));
  });

  it("aplicar: revisa el SQL, aplica y avisa; con ajuste desactivado aplica directamente", async () => {
    await openResult();
    const edits = useEditsStore();
    const ui = useUiStore();
    api.previewEdits.mockResolvedValue([{ row: 0, cols: [1], sql: "UPDATE `app`.`t` SET `n` = 'x' WHERE `id` = 1" }]);
    api.applyEdits.mockResolvedValue({ applied: 1, conflict: null });
    api.getEditInfo.mockResolvedValue(info({ pending: 0 }));

    await edits.requestApply("t1", "q1", 0);
    expect(edits.review?.statements).toHaveLength(1);
    expect(api.applyEdits).not.toHaveBeenCalled(); // confirmEdits = true por defecto: primero se ve el SQL
    await edits.apply();
    expect(api.applyEdits).toHaveBeenCalledWith("q1", 0);
    expect(edits.review).toBeNull();
    expect(ui.toasts.at(-1)).toMatchObject({ kind: "success" });

    ui.confirmEdits = false;
    await edits.requestApply("t1", "q1", 0);
    expect(api.applyEdits).toHaveBeenCalledTimes(2);
    expect(edits.review).toBeNull();

    api.applyEdits.mockClear();
    await edits.requestApply("t1", "q1", 0, { viewOnly: true }); // «Ver SQL» nunca aplica
    expect(api.applyEdits).not.toHaveBeenCalled();
    expect(edits.review).not.toBeNull();
  });

  it("un conflicto deja la revisión abierta con la fila y no cierra nada", async () => {
    await openResult();
    const edits = useEditsStore();
    api.previewEdits.mockResolvedValue([{ row: 2, cols: [1], sql: "UPDATE …" }]);
    api.applyEdits.mockResolvedValue({ applied: 0, conflict: { row: 2, reason: "changed" } });
    await edits.requestApply("t1", "q1", 0);
    await edits.apply();
    expect(edits.review?.conflict).toEqual({ row: 2, reason: "changed" });
    expect(edits.review?.applying).toBe(false);
  });

  it("un error del servidor se muestra en el diálogo y se puede reintentar", async () => {
    await openResult();
    const edits = useEditsStore();
    api.previewEdits.mockResolvedValue([{ row: 0, cols: [1], sql: "UPDATE …" }]);
    api.applyEdits.mockRejectedValueOnce(apiError("Fila 1: Data too long", "QUERY_FAILED"));
    await edits.requestApply("t1", "q1", 0);
    await edits.apply();
    expect(edits.review?.error).toContain("Data too long");
    api.applyEdits.mockResolvedValueOnce({ applied: 1, conflict: null });
    api.getEditInfo.mockResolvedValue(info());
    await edits.apply();
    expect(edits.review).toBeNull();
  });

  it("confirmDiscard: sin cambios sigue sin preguntar; con cambios espera la respuesta y descarta en Go", async () => {
    await openResult();
    const edits = useEditsStore();
    expect(await edits.confirmDiscard("t1")).toBe(true);
    expect(edits.discardPrompt).toBeNull();

    api.stageEdit.mockResolvedValue(info({ pending: 2 }));
    await edits.stage("t1", "q1", 0, 0, 1, { null: false, text: "x" });
    api.discardEdits.mockResolvedValue(info({ pending: 0 }));

    const cancel = edits.confirmDiscard("t1");
    expect(edits.discardPrompt).toMatchObject({ tabId: "t1", count: 2 });
    edits.answerDiscard(false);
    expect(await cancel).toBe(false);
    expect(api.discardEdits).not.toHaveBeenCalled();
    expect(edits.pendingFor("t1")).toBe(2);

    const ok = edits.confirmDiscard("t1");
    edits.answerDiscard(true);
    expect(await ok).toBe(true);
    expect(api.discardEdits).toHaveBeenCalledWith("q1", 0);
    expect(edits.pendingFor("t1")).toBe(0);
  });

  it("volver a ejecutar con cambios pendientes pregunta antes de reemplazar el resultado", async () => {
    const results = await openResult();
    const edits = useEditsStore();
    api.stageEdit.mockResolvedValue(info({ pending: 1 }));
    await edits.stage("t1", "q1", 0, 0, 1, { null: false, text: "x" });
    queries.execute.mockClear();

    const run = results.execute({ tabId: "t1", sessionId: "s", sql: "SELECT 1", baseOffset: 0, database: "", autoLimit: 0 });
    await vi.waitFor(() => expect(edits.discardPrompt).not.toBeNull());
    edits.answerDiscard(false);
    await run;
    expect(queries.execute).not.toHaveBeenCalled(); // se quedó como estaba
    expect(results.of("t1").queryId).toBe("q1");
  });
});
