import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({ getAlterModel: vi.fn(), previewAlter: vi.fn(), applyAlter: vi.fn() }));
vi.mock("../api/alter", () => api);

import { nextTick } from "vue";
import { PREVIEW_DELAY_MS, useAlterStore } from "./alter";
import { useUiStore } from "./ui";
import type { AlterModel } from "../types";

const base = (): AlterModel => ({
  database: "app", table: "t", fingerprint: "fp", createSql: "", tableCollation: "", rowEstimate: null, incomingFks: [], indexes: [], foreignKeys: [],
  columns: [{ name: "id", dataType: "int", columnType: "int", nullable: false, default: null, isPrimaryKey: true, isAutoInc: false, isGenerated: false, comment: "", ordinal: 1, defaultExpr: false, charset: "", collation: "", onUpdate: "", generationExpr: "" }],
});
const TARGET = { sessionId: "s1", db: "app", table: "t" };
const preview = (sql = "ALTER TABLE x") => ({ statements: [sql], sqlHash: "h1", warnings: [], destructive: false, noRollback: true, estimatedRows: null });

async function opened() {
  const s = useAlterStore();
  await s.open(TARGET);
  return s;
}
/** Deja pasar el debounce y la respuesta de la vista previa. */
async function settle() {
  await nextTick();
  await vi.advanceTimersByTimeAsync(PREVIEW_DELAY_MS + 1);
}

describe("alter store", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setActivePinia(createPinia());
    Object.values(api).forEach((f) => f.mockReset());
    api.getAlterModel.mockResolvedValue(base());
    api.previewAlter.mockResolvedValue(preview());
  });

  it("abre leyendo el modelo; un error de lectura (p. ej. solo lectura) queda a la vista y no hay borradores", async () => {
    api.getAlterModel.mockRejectedValueOnce(Object.assign(new Error("x"), { cause: JSON.stringify({ code: "QUERY_FAILED", message: "Conexión de solo lectura", detail: "readonly", retryable: false }) }));
    const s = await opened();
    expect(s.loadError).toBe("Conexión de solo lectura");
    expect(s.model).toBeNull();
  });

  it("sin cambios no pide vista previa y no se puede aplicar", async () => {
    const s = await opened();
    await settle();
    expect(api.previewAlter).not.toHaveBeenCalled();
    expect(s.dirty).toBe(false);
    expect(s.canApply).toBe(false);
  });

  it("cada cambio recalcula el SQL con debounce y solo la última respuesta vale", async () => {
    const s = await opened();
    s.addColumn();
    s.drafts.cols[1].name = "a";
    await nextTick();
    s.drafts.cols[1].name = "ab";
    await settle();
    expect(api.previewAlter).toHaveBeenCalledTimes(1);
    expect(api.previewAlter.mock.calls[0][1].ops[0]).toMatchObject({ kind: "addColumn", name: "ab" });
    expect(s.preview?.sqlHash).toBe("h1");
    expect(s.canApply).toBe(true);
  });

  it("un error de validación de Go se muestra y bloquea Aplicar", async () => {
    api.previewAlter.mockRejectedValueOnce(Object.assign(new Error("x"), { cause: JSON.stringify({ code: "VALIDATION", message: "Falta un nombre", detail: "ops[0].name", retryable: false }) }));
    const s = await opened();
    s.addColumn();
    await settle();
    expect(s.previewError).toBe("Falta un nombre");
    expect(s.canApply).toBe(false);
    s.requestApply();
    expect(s.review).toBeNull();
  });

  it("descartar todo vuelve al estado leído y limpia la vista previa", async () => {
    const s = await opened();
    s.addColumn();
    await settle();
    s.discardAll();
    await settle();
    expect(s.dirty).toBe(false);
    expect(s.preview).toBeNull();
  });

  it("quitar una columna nueva la elimina; quitar una existente la marca; deshacer la restaura", async () => {
    const s = await opened();
    s.addColumn();
    s.removeColumn(s.drafts.cols[1].key);
    expect(s.drafts.cols).toHaveLength(1);
    s.removeColumn(s.drafts.cols[0].key);
    expect(s.drafts.cols[0].deleted).toBe(true);
    s.revert("cols", s.drafts.cols[0].key);
    expect(s.drafts.cols[0].deleted).toBe(false);
  });

  it("aplicar siempre pasa por la revisión y envía el hash de la vista previa", async () => {
    const s = await opened();
    s.addColumn();
    s.drafts.cols[1].name = "z";
    await settle();
    expect(s.review).toBeNull();
    s.requestApply();
    expect(s.review).not.toBeNull();
    api.applyAlter.mockResolvedValue({ applied: ["ALTER TABLE x"], failed: null, model: base() });
    expect(await s.apply()).toBe(true);
    expect(api.applyAlter).toHaveBeenCalledWith("s1", expect.objectContaining({ fingerprint: "fp" }), "h1");
    expect(s.review).toBeNull();
    expect(s.target).toBeNull();
    expect(useUiStore().toasts.at(-1)?.kind).toBe("success");
  });

  it("si falla a mitad, el editor sigue abierto con el estado real y la revisión cuenta qué pasó", async () => {
    const s = await opened();
    s.addColumn();
    s.drafts.cols[1].name = "z";
    await settle();
    s.requestApply();
    const real = { ...base(), fingerprint: "fp2" };
    api.applyAlter.mockResolvedValue({ applied: ["uno"], failed: { index: 1, message: "Duplicate key" }, model: real });
    expect(await s.apply()).toBe(false);
    expect(s.review?.partial?.failed?.index).toBe(1);
    expect(s.target).not.toBeNull();
    expect(s.model?.fingerprint).toBe("fp2");
    expect(s.dirty).toBe(false);
  });

  it("un error de la llamada (solo lectura, busy, stale) se muestra en la revisión sin cerrar nada", async () => {
    const s = await opened();
    s.addColumn();
    s.drafts.cols[1].name = "z";
    await settle();
    s.requestApply();
    api.applyAlter.mockRejectedValue(Object.assign(new Error("x"), { cause: JSON.stringify({ code: "QUERY_FAILED", message: "Conexión de solo lectura", detail: "readonly", retryable: false }) }));
    expect(await s.apply()).toBe(false);
    expect(s.review?.error).toBe("Conexión de solo lectura");
    expect(s.target).not.toBeNull();
  });

  it("no se puede cerrar la revisión mientras se aplica", async () => {
    const s = await opened();
    s.addColumn();
    s.drafts.cols[1].name = "z";
    await settle();
    s.requestApply();
    s.review!.applying = true;
    s.closeReview();
    expect(s.review).not.toBeNull();
  });
});
