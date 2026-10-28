import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { i18n } from "../../i18n";

const api = vi.hoisted(() => ({ startExport: vi.fn(), cancelExport: vi.fn() }));
vi.mock("../../api/exports", () => api);

import { useExportsStore } from "../../stores/exports";
import ExportDialog from "./ExportDialog.vue";

let w: VueWrapper | null = null;
const mountDialog = (props: Partial<{ truncated: boolean; rowCount: number }> = {}) => {
  w = mount(ExportDialog, { props: { queryId: "q1", stmtIndex: 2, truncated: false, rowCount: 100, open: true, ...props }, attachTo: document.body, global: { plugins: [i18n] } });
  return w;
};
const body = () => document.body;
const button = (label: string) => Array.from(body().querySelectorAll("button")).find((b) => b.textContent?.trim() === label) as HTMLButtonElement | undefined;

beforeEach(() => {
  setActivePinia(createPinia());
  Object.values(api).forEach((f) => f.mockReset());
});
afterEach(() => {
  w?.unmount();
  w = null;
  document.body.innerHTML = "";
});

describe("ExportDialog", () => {
  it("exporta lo cargado con las opciones elegidas cuando el resultado está completo", async () => {
    api.startExport.mockResolvedValue({ jobId: "j1", path: "/tmp/r.csv", canceled: false });
    mountDialog();
    await flushPromises();
    expect(body().textContent).not.toContain("truncado"); // no hay elección de origen
    const bom = Array.from(body().querySelectorAll("label")).find((l) => l.textContent?.includes("BOM"))!.querySelector("input")!;
    bom.click();
    button("Exportar…")!.click();
    await flushPromises();
    expect(api.startExport).toHaveBeenCalledWith({
      queryId: "q1", stmtIndex: 2, source: "buffer", options: expect.objectContaining({ format: "csv", bom: true, header: true, delimiter: "," }),
    });
    // exportando: se ve el progreso y el botón Detener
    expect(body().querySelector('[data-testid="export-progress"]')).toBeTruthy();
    useExportsStore().handleProgress({ jobId: "j1", rows: 1234, bytes: 2048 });
    await flushPromises();
    expect(body().textContent).toMatch(/1[.,]234 filas/);
    expect(button("Detener")).toBeTruthy();
  });

  it("si está truncado propone re-ejecutar por defecto y avisa de que puede diferir", async () => {
    api.startExport.mockResolvedValue({ jobId: "j1", path: "p", canceled: false });
    mountDialog({ truncated: true, rowCount: 100000 });
    await flushPromises();
    expect(body().textContent).toMatch(/truncado a 100[.,]000 filas/);
    expect(body().textContent).toContain("puede diferir");
    button("Exportar…")!.click();
    await flushPromises();
    expect(api.startExport).toHaveBeenCalledWith(expect.objectContaining({ source: "rerun" }));
  });

  it("elegir 'solo las filas cargadas' exporta el buffer", async () => {
    api.startExport.mockResolvedValue({ jobId: "j1", path: "p", canceled: false });
    mountDialog({ truncated: true, rowCount: 500 });
    await flushPromises();
    (body().querySelector('input[type="radio"][value="buffer"]') as HTMLInputElement).click();
    button("Exportar…")!.click();
    await flushPromises();
    expect(api.startExport).toHaveBeenCalledWith(expect.objectContaining({ source: "buffer" }));
  });

  it("JSON oculta las opciones de CSV y muestra NDJSON", async () => {
    mountDialog();
    await flushPromises();
    const ex = useExportsStore();
    ex.options.format = "json";
    await flushPromises();
    expect(body().textContent).toContain("NDJSON");
    expect(body().textContent).not.toContain("BOM");
  });

  it("al terminar la exportación el diálogo se cierra", async () => {
    api.startExport.mockResolvedValue({ jobId: "j1", path: "/tmp/r.csv", canceled: false });
    const wrapper = mountDialog();
    await flushPromises();
    button("Exportar…")!.click();
    await flushPromises();
    useExportsStore().handleDone({ jobId: "j1", path: "/tmp/r.csv", rows: 100, bytes: 10, canceled: false } as never);
    await flushPromises();
    expect(wrapper.emitted("update:open")?.at(-1)).toEqual([false]);
  });

  it("Detener cancela el trabajo en curso", async () => {
    api.startExport.mockResolvedValue({ jobId: "j9", path: "p", canceled: false });
    api.cancelExport.mockResolvedValue(undefined);
    mountDialog();
    await flushPromises();
    button("Exportar…")!.click();
    await flushPromises();
    button("Detener")!.click();
    await flushPromises();
    expect(api.cancelExport).toHaveBeenCalledWith("j9");
  });
});
