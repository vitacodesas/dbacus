import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({ startExport: vi.fn(), cancelExport: vi.fn() }));
vi.mock("../api/exports", () => api);

import { useExportsStore } from "./exports";
import { useUiStore } from "./ui";

const done = (over: Record<string, unknown> = {}) => ({ jobId: "j1", path: "/tmp/x.csv", rows: 10, bytes: 100, canceled: false, ...over }) as never;

beforeEach(() => {
  setActivePinia(createPinia());
  Object.values(api).forEach((f) => f.mockReset());
});

describe("exports store", () => {
  it("envía las opciones y sigue progreso hasta el final con un aviso de éxito", async () => {
    api.startExport.mockResolvedValue({ jobId: "j1", path: "/tmp/x.csv", canceled: false });
    const ex = useExportsStore();
    ex.options.format = "json";
    expect(await ex.start("q1", 0, "rerun")).toBe(true);
    expect(api.startExport).toHaveBeenCalledWith({ queryId: "q1", stmtIndex: 0, source: "rerun", options: expect.objectContaining({ format: "json" }) });
    expect(ex.running).toBe(true);

    ex.handleProgress({ jobId: "j1", rows: 5, bytes: 50 });
    expect(ex.job).toMatchObject({ rows: 5, bytes: 50, status: "running" });
    ex.handleProgress({ jobId: "otro", rows: 99, bytes: 1 });
    expect(ex.job?.rows).toBe(5);

    ex.handleDone(done());
    expect(ex.job).toMatchObject({ status: "done", rows: 10 });
    expect(ex.running).toBe(false);
    expect(useUiStore().toasts.at(-1)).toMatchObject({ kind: "success" });
  });

  it("si el usuario cierra el diálogo de guardado no hay trabajo", async () => {
    api.startExport.mockResolvedValue({ jobId: "", path: "", canceled: true });
    const ex = useExportsStore();
    expect(await ex.start("q1", 0, "buffer")).toBe(false);
    expect(ex.job).toBeNull();
    expect(ex.running).toBe(false);
  });

  it("un export:done que llega antes de que Export devuelva el id no se pierde", async () => {
    let resolve!: (v: unknown) => void;
    api.startExport.mockReturnValue(new Promise((r) => (resolve = r)));
    const ex = useExportsStore();
    const p = ex.start("q1", 0, "buffer");
    ex.handleDone(done());
    resolve({ jobId: "j1", path: "/tmp/x.csv", canceled: false });
    await p;
    expect(ex.job?.status).toBe("done");
  });

  it("errores y cancelaciones quedan reflejados y avisan", async () => {
    api.startExport.mockResolvedValue({ jobId: "j1", path: "p", canceled: false });
    const ex = useExportsStore();
    await ex.start("q1", 0, "buffer");
    ex.handleDone(done({ error: { code: "QUERY_FAILED", message: "tabla borrada" } }));
    expect(ex.job).toMatchObject({ status: "error", error: "tabla borrada" });
    expect(useUiStore().toasts.at(-1)?.kind).toBe("error");

    await ex.start("q1", 0, "buffer");
    ex.handleDone(done({ canceled: true }));
    expect(ex.job?.status).toBe("canceled");
  });

  it("solo una exportación a la vez; cancelar llama a Go con el id", async () => {
    api.startExport.mockResolvedValue({ jobId: "j1", path: "p", canceled: false });
    api.cancelExport.mockResolvedValue(undefined);
    const ex = useExportsStore();
    await ex.start("q1", 0, "buffer");
    expect(await ex.start("q2", 0, "buffer")).toBe(false);
    expect(api.startExport).toHaveBeenCalledTimes(1);
    await ex.cancel();
    expect(api.cancelExport).toHaveBeenCalledWith("j1");
  });

  it("si Go rechaza la petición se avisa y no queda nada corriendo", async () => {
    api.startExport.mockRejectedValue({ message: "Resultado expirado" });
    const ex = useExportsStore();
    expect(await ex.start("q1", 0, "buffer")).toBe(false);
    expect(ex.running).toBe(false);
    expect(useUiStore().toasts.at(-1)?.kind).toBe("error");
  });
});
