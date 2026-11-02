import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({ getRoutine: vi.fn(), routineTemplate: vi.fn(), previewRoutine: vi.fn(), applyRoutine: vi.fn(), listRoutines: vi.fn(), routineCallStatement: vi.fn() }));
vi.mock("../api/routines", () => api);

import { useRoutinesStore } from "./routines";
import { useUiStore } from "./ui";
import type { RoutineDetail } from "../types";

const detail = (over: Partial<RoutineDetail> = {}): RoutineDetail => ({
  name: "p", kind: "procedure", definer: "root@localhost", comment: "", securityType: "DEFINER", lastAltered: "", database: "app", params: [], returns: "",
  createSql: "CREATE PROCEDURE p() SELECT 1", sqlMode: "", charsetClient: "", collationConnection: "", databaseCollation: "", bodyHidden: false, fingerprint: "fp", ...over,
});
const TG = { sessionId: "s1", db: "app", kind: "procedure" as const, name: "p" };
const preview = (over = {}) => ({ statements: ["SET", "CREATE tmp", "DROP tmp", "DROP p", "CREATE p"], sqlHash: "h1", warnings: [], destructive: true, noRollback: true, ...over });
const apiError = (detailCode: string, message: string) => Object.assign(new Error(message), { cause: JSON.stringify({ code: "VALIDATION", message, detail: detailCode, retryable: false }) });

describe("routines store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    Object.values(api).forEach((f) => f.mockReset());
    api.getRoutine.mockResolvedValue(detail());
    api.previewRoutine.mockResolvedValue(preview());
    api.routineTemplate.mockResolvedValue("CREATE PROCEDURE nueva_rutina()\nBEGIN\nEND");
  });

  it("abre en modo lectura con el CREATE de Go; no hay cambios hasta editar", async () => {
    const s = useRoutinesStore();
    await s.open(TG);
    expect(api.getRoutine).toHaveBeenCalledWith("s1", "app", "procedure", "p");
    expect(s.mode).toBe("view");
    expect(s.text).toBe("CREATE PROCEDURE p() SELECT 1");
    expect(s.editing).toBe(false);
    expect(s.dirty).toBe(false);
  });

  it("una rutina nueva sale de la plantilla de Go (Vue no arma SQL) y ya es modo creación", async () => {
    const s = useRoutinesStore();
    await s.open({ ...TG, name: null });
    expect(api.routineTemplate).toHaveBeenCalledWith("procedure");
    expect(s.mode).toBe("create");
    expect(s.text).toContain("nueva_rutina");
    expect(s.dirty).toBe(false);
  });

  it("un error de lectura queda a la vista", async () => {
    api.getRoutine.mockRejectedValueOnce(apiError("x", "La rutina ya no existe"));
    const s = useRoutinesStore();
    await s.open(TG);
    expect(s.loadError).toBe("La rutina ya no existe");
    expect(s.detail).toBeNull();
  });

  it("con el cuerpo oculto no se puede editar", async () => {
    api.getRoutine.mockResolvedValueOnce(detail({ bodyHidden: true, createSql: "" }));
    const s = useRoutinesStore();
    await s.open(TG);
    s.startEdit();
    expect(s.mode).toBe("view");
  });

  it("editar → revisar: manda el texto, el fingerprint y la operación «replace»; nada se ejecuta", async () => {
    const s = useRoutinesStore();
    await s.open(TG);
    s.startEdit();
    s.text = "CREATE PROCEDURE p() SELECT 2";
    expect(s.dirty).toBe(true);
    await s.requestReview();
    expect(api.previewRoutine).toHaveBeenCalledWith("s1", { database: "app", kind: "procedure", name: "p", op: "replace", sql: "CREATE PROCEDURE p() SELECT 2", fingerprint: "fp", recreateDefiner: false });
    expect(s.review?.preview.statements).toHaveLength(5);
    expect(api.applyRoutine).not.toHaveBeenCalled();
  });

  it("un error de validación de Go se queda en el modal y no abre la revisión", async () => {
    api.previewRoutine.mockRejectedValueOnce(apiError("name_changed", "El nombre de la rutina no puede cambiar"));
    const s = useRoutinesStore();
    await s.open(TG);
    s.startEdit();
    s.text = "CREATE PROCEDURE otro() SELECT 1";
    await s.requestReview();
    expect(s.review).toBeNull();
    expect(s.actionError).toBe("El nombre de la rutina no puede cambiar");
  });

  it("aplicar envía el hash de la revisión; al acabar bien vuelve a modo lectura con el estado real", async () => {
    api.applyRoutine.mockResolvedValue({ applied: ["DROP", "CREATE"], failed: null, restored: false, backupSql: "", detail: detail({ createSql: "CREATE PROCEDURE p() SELECT 2", fingerprint: "fp2" }) });
    const s = useRoutinesStore();
    await s.open(TG);
    s.startEdit();
    s.text = "CREATE PROCEDURE p() SELECT 2";
    await s.requestReview();
    await s.apply();
    expect(api.applyRoutine).toHaveBeenCalledWith("s1", expect.objectContaining({ op: "replace" }), "h1");
    expect(s.review).toBeNull();
    expect(s.mode).toBe("view");
    expect(s.detail?.fingerprint).toBe("fp2");
    expect(s.dirty).toBe(false);
    expect(useUiStore().toasts.at(-1)?.kind).toBe("success");
  });

  it("crear: tras aplicar el modal pasa a mostrar la rutina con su nombre real", async () => {
    api.applyRoutine.mockResolvedValue({ applied: ["CREATE"], failed: null, restored: false, backupSql: "", detail: detail({ name: "nueva" }) });
    const s = useRoutinesStore();
    await s.open({ ...TG, name: null });
    s.text = "CREATE PROCEDURE nueva() SELECT 1";
    await s.requestReview();
    expect(api.previewRoutine).toHaveBeenCalledWith("s1", expect.objectContaining({ op: "create", name: "" }));
    await s.apply();
    expect(s.target?.name).toBe("nueva");
    expect(s.mode).toBe("view");
  });

  it("un CREATE que falla: la revisión queda abierta con qué se aplicó y si se restauró la original", async () => {
    api.applyRoutine.mockResolvedValue({ applied: ["DROP p"], failed: { index: 4, message: "syntax" }, restored: true, backupSql: "", detail: detail() });
    const s = useRoutinesStore();
    await s.open(TG);
    s.startEdit();
    s.text = "CREATE PROCEDURE p() BROKEN";
    await s.requestReview();
    await s.apply();
    expect(s.review?.failed?.restored).toBe(true);
    expect(s.review?.failed?.failed?.index).toBe(4);
    expect(s.review?.applying).toBe(false);
    expect(s.mode).toBe("view"); // el modal se recargó con la rutina real
  });

  it("si ni la restauración funcionó, la copia llega a la revisión", async () => {
    api.applyRoutine.mockResolvedValue({ applied: ["DROP p"], failed: { index: 4, message: "x" }, restored: false, backupSql: "CREATE PROCEDURE p() SELECT 1", detail: null });
    const s = useRoutinesStore();
    await s.open(TG);
    s.startEdit();
    s.text = "CREATE PROCEDURE p() BROKEN";
    await s.requestReview();
    await s.apply();
    expect(s.review?.failed?.backupSql).toBe("CREATE PROCEDURE p() SELECT 1");
  });

  it("stale_preview o busy (errores de la llamada) se muestran en la revisión sin cerrarla", async () => {
    api.applyRoutine.mockRejectedValueOnce(apiError("stale_preview", "El SQL cambió desde la revisión"));
    const s = useRoutinesStore();
    await s.open(TG);
    s.startEdit();
    s.text = "x";
    await s.requestReview();
    await s.apply();
    expect(s.review?.error).toBe("El SQL cambió desde la revisión");
    expect(s.review?.applying).toBe(false);
  });

  it("«Recrear con mi usuario» recalcula la vista previa con la opción activada", async () => {
    const s = useRoutinesStore();
    await s.open(TG);
    s.startEdit();
    s.text = "CREATE PROCEDURE p() SELECT 2";
    await s.requestReview();
    api.previewRoutine.mockResolvedValueOnce(preview({ sqlHash: "h2" }));
    await s.setRecreateDefiner(true);
    expect(api.previewRoutine).toHaveBeenLastCalledWith("s1", expect.objectContaining({ recreateDefiner: true }));
    expect(s.review?.preview.sqlHash).toBe("h2");
  });

  it("borrar: enseña el código actual, no abre el modal y, al confirmar, avisa con un toast", async () => {
    api.previewRoutine.mockResolvedValue(preview({ statements: ["DROP PROCEDURE `app`.`p`"] }));
    api.applyRoutine.mockResolvedValue({ applied: ["DROP"], failed: null, restored: false, backupSql: "", detail: null });
    const s = useRoutinesStore();
    await s.requestDelete(TG);
    expect(s.target).toBeNull();
    expect(s.review?.body).toBe("CREATE PROCEDURE p() SELECT 1");
    expect(api.previewRoutine).toHaveBeenCalledWith("s1", expect.objectContaining({ op: "drop", name: "p", fingerprint: "fp" }));
    await s.apply();
    expect(api.applyRoutine).toHaveBeenCalledWith("s1", expect.objectContaining({ op: "drop" }), "h1");
    expect(s.review).toBeNull();
    expect(useUiStore().toasts.at(-1)?.kind).toBe("success");
  });

  it("borrar la rutina que muestra el modal lo cierra", async () => {
    api.applyRoutine.mockResolvedValue({ applied: ["DROP"], failed: null, restored: false, backupSql: "", detail: null });
    const s = useRoutinesStore();
    await s.open(TG);
    await s.requestDelete(TG);
    await s.apply();
    expect(s.target).toBeNull();
  });

  it("descartar vuelve al texto original", async () => {
    const s = useRoutinesStore();
    await s.open(TG);
    s.startEdit();
    s.text = "otra cosa";
    s.discard();
    expect(s.text).toBe("CREATE PROCEDURE p() SELECT 1");
    expect(s.mode).toBe("view");
  });
});
