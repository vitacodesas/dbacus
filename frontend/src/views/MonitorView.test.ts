import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";

// jsdom no tiene canvas ni matchMedia: uPlot se sustituye por un doble (las gráficas se prueban en sus propios archivos).
vi.mock("uplot", () => ({ default: class { static pxRatio = 1; destroy() {} setSize() {} valToPos() { return 0; } } }));

const api = vi.hoisted(() => ({
  monitorCapabilities: vi.fn(), refreshMonitorCapabilities: vi.fn(), startMonitor: vi.fn(), stopMonitor: vi.fn(), monitorRuns: vi.fn(),
  watchProcesses: vi.fn(), unwatchProcesses: vi.fn(), monitorTop: vi.fn(), monitorDigest: vi.fn(), monitorSeries: vi.fn(), monitorCompare: vi.fn(),
  annotateMonitor: vi.fn(), deleteMonitorAnnotation: vi.fn(), purgeMonitor: vi.fn(), monitorCopyKill: vi.fn(), killProcess: vi.fn(),
}));
vi.mock("../api/monitor", () => api);

import { i18n } from "../i18n";
import MonitorView from "./MonitorView.vue";
import { useMonitorStore } from "../stores/monitor";
import { useSchemaStore } from "../stores/schema";
import { useSessionsStore } from "../stores/sessions";
import { useConnectionsStore } from "../stores/connections";
import { useUiStore } from "../stores/ui";
import type { MonitorCaps, ProcessSnapshot } from "../types";

const ok = { state: "ok", reason: "ok", hint: "" };
const caps = (over: Partial<MonitorCaps> = {}): MonitorCaps => ({ serverVersion: "8.0", isMariaDB: false, serverId: "u", processlist: ok, digest: ok, status: ok, ...over });
const proc = (id: number, over = {}) => ({ id, user: "app", host: "h", db: "pedidos", command: "Query", timeSec: 3, state: "", info: `SELECT ${id}`, infoTruncated: false, ...over });
const snap = (rows = [proc(1), proc(2, { timeSec: 45 })]): ProcessSnapshot => ({ at: "t", rows, summary: { total: rows.length, running: rows.length, sleeping: 0, waitingLock: 0, longestSec: 45, byCommand: null, byUser: null }, maxConnections: 151 });

const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) };
function mountView(withSession = true, readOnly = false) {
  const pinia = createPinia();
  setActivePinia(pinia);
  const sessions = useSessionsStore();
  if (withSession) {
    sessions.byId = { s1: { sessionId: "s1", profileId: "p1", serverVersion: "8.0", currentDatabase: "" } };
    sessions.activeSessionId = "s1";
  }
  useConnectionsStore().profiles = [{ id: "p1", name: "P", readOnly } as never];
  useSchemaStore().loadDatabases = vi.fn().mockResolvedValue(undefined);
  useUiStore().mainView = "monitor";
  const wrapper = mount(MonitorView, { global: { plugins: [pinia, i18n] } });
  return { wrapper, m: useMonitorStore(), ui: useUiStore(), sessions };
}

beforeEach(() => {
  Object.values(api).forEach((f) => f.mockReset());
  api.monitorCapabilities.mockResolvedValue(caps());
  api.watchProcesses.mockResolvedValue(undefined);
  api.unwatchProcesses.mockResolvedValue(undefined);
  api.monitorTop.mockResolvedValue([]);
  api.monitorSeries.mockResolvedValue([]);
  api.monitorRuns.mockResolvedValue([]);
  api.monitorCompare.mockResolvedValue({ regressions: [], new: [], gone: [] });
  api.monitorCopyKill.mockImplementation(async (_s: string, id: number, q: boolean) => (q ? `KILL QUERY ${id};` : `KILL ${id};`));
  clipboard.writeText.mockClear();
  Object.defineProperty(navigator, "clipboard", { value: clipboard, configurable: true });
});
afterEach(() => vi.restoreAllMocks());

describe("MonitorView", () => {
  it("sin conexiones abiertas pide abrir una", () => {
    const { wrapper } = mountView(false);
    expect(wrapper.text()).toContain("Sin conexión para monitorear");
    expect(api.watchProcesses).not.toHaveBeenCalled();
  });

  it("al mostrarse sondea permisos y empieza la vista en vivo (arrendamiento)", async () => {
    mountView();
    await flushPromises();
    expect(api.monitorCapabilities).toHaveBeenCalledWith("s1");
    expect(api.watchProcesses).toHaveBeenCalledWith("s1", 2000);
  });

  it("se pausa al ocultarse (volver a consultas) y se reanuda al volver", async () => {
    const { ui } = mountView();
    await flushPromises();
    ui.mainView = "queries";
    await flushPromises();
    expect(api.unwatchProcesses).toHaveBeenCalledWith("s1");
    api.watchProcesses.mockClear();
    ui.mainView = "monitor";
    await flushPromises();
    expect(api.watchProcesses).toHaveBeenCalledTimes(1);
  });

  it("al desmontarse suelta el arrendamiento", async () => {
    const { wrapper } = mountView();
    await flushPromises();
    wrapper.unmount();
    expect(api.unwatchProcesses).toHaveBeenCalledWith("s1");
  });

  it("cambiar de pestaña fuera de «En vivo» también suelta el arrendamiento", async () => {
    const { m } = mountView();
    await flushPromises();
    m.tab = "metrics";
    await flushPromises();
    expect(api.unwatchProcesses).toHaveBeenCalledWith("s1");
  });

  it("En vivo: tarjetas y tabla con lo que entrega Go, con color por antigüedad", async () => {
    const { wrapper, m } = mountView();
    await flushPromises();
    m.handleProcesses({ sessionId: "s1", snapshot: snap() });
    await flushPromises();
    expect(wrapper.get("[data-testid=card-connections]").text()).toContain("2");
    expect(wrapper.get("[data-testid=card-longest]").text()).toContain("45 s");
    expect(wrapper.findAll("[data-testid^=process-]").filter((e) => /^process-\d+$/.test(e.attributes("data-testid") ?? ""))).toHaveLength(2);
    expect(wrapper.get("[data-testid=process-2] [data-testid=process-time]").classes()).toContain("text-danger");
    expect(wrapper.get("[data-testid=process-1] [data-testid=process-time]").classes()).not.toContain("text-danger");
  });

  it("«Copiar KILL» pide el texto a Go y lo copia (Vue no arma el KILL)", async () => {
    const { wrapper, m } = mountView();
    await flushPromises();
    m.handleProcesses({ sessionId: "s1", snapshot: snap() });
    await flushPromises();
    await wrapper.get("[data-testid=process-1] [data-testid=copy-kill]").trigger("click");
    await flushPromises();
    expect(api.monitorCopyKill).toHaveBeenCalledWith("s1", 1, false);
    expect(clipboard.writeText).toHaveBeenCalledWith("KILL 1;");
  });

  it("«Terminar…» pide confirmación, por defecto cancela solo la consulta y manda id+usuario+host a Go", async () => {
    api.killProcess.mockResolvedValue(undefined);
    const { wrapper, m } = mountView();
    await flushPromises();
    m.handleProcesses({ sessionId: "s1", snapshot: snap() });
    await flushPromises();
    await wrapper.get("[data-testid=process-1] [data-testid=kill-open]").trigger("click");
    await flushPromises();
    expect(api.killProcess).not.toHaveBeenCalled();
    const body = () => document.body;
    expect((body().querySelector("[data-testid=kill-mode-query]") as HTMLInputElement).checked).toBe(true);
    (body().querySelector("[data-testid=kill-confirm]") as HTMLButtonElement).click();
    await flushPromises();
    expect(api.killProcess).toHaveBeenCalledWith("s1", expect.objectContaining({ id: 1, user: "app", host: "h" }), true);
  });

  it("elegir «Cerrar la conexión» manda queryOnly=false; cancelar no manda nada", async () => {
    api.killProcess.mockResolvedValue(undefined);
    const { wrapper, m } = mountView();
    await flushPromises();
    m.handleProcesses({ sessionId: "s1", snapshot: snap() });
    await flushPromises();
    await wrapper.get("[data-testid=process-2] [data-testid=kill-open]").trigger("click");
    await flushPromises();
    (document.body.querySelector("[data-testid=kill-cancel]") as HTMLButtonElement).click();
    await flushPromises();
    expect(api.killProcess).not.toHaveBeenCalled();
    await wrapper.get("[data-testid=process-2] [data-testid=kill-open]").trigger("click");
    await flushPromises();
    (document.body.querySelector("[data-testid=kill-mode-conn]") as HTMLInputElement).click();
    await flushPromises();
    (document.body.querySelector("[data-testid=kill-confirm]") as HTMLButtonElement).click();
    await flushPromises();
    expect(api.killProcess).toHaveBeenCalledWith("s1", expect.objectContaining({ id: 2 }), false);
  });

  it("en un perfil de solo lectura no se ofrece «Terminar…» pero sí copiar el KILL", async () => {
    const { wrapper, m } = mountView(true, true);
    await flushPromises();
    m.handleProcesses({ sessionId: "s1", snapshot: snap() });
    await flushPromises();
    expect(wrapper.find("[data-testid=kill-open]").exists()).toBe(false);
    expect(wrapper.find("[data-testid=copy-kill]").exists()).toBe(true);
    await wrapper.get("[data-testid=process-1]").trigger("click");
    expect(wrapper.find("[data-testid=kill-readonly]").exists()).toBe(true);
    expect(wrapper.find("[data-testid=kill-open-detail]").exists()).toBe(false);
  });

  it("clic en una fila muestra el INFO completo y KILL QUERY", async () => {
    const { wrapper, m } = mountView();
    await flushPromises();
    m.handleProcesses({ sessionId: "s1", snapshot: snap([proc(1, { info: "SELECT largo", infoTruncated: true })]) });
    await flushPromises();
    await wrapper.get("[data-testid=process-1]").trigger("click");
    expect(wrapper.get("[data-testid=process-info]").text()).toBe("SELECT largo");
    expect(wrapper.get("[data-testid=process-detail]").text()).toContain("2 KiB");
    await wrapper.get("[data-testid=copy-kill-query]").trigger("click");
    await flushPromises();
    expect(clipboard.writeText).toHaveBeenCalledWith("KILL QUERY 1;");
  });

  it("cientos de procesos: la lista virtualizada monta solo unas decenas de filas", async () => {
    const { wrapper, m } = mountView();
    await flushPromises();
    m.handleProcesses({ sessionId: "s1", snapshot: snap(Array.from({ length: 500 }, (_, i) => proc(i + 1))) });
    await flushPromises();
    const rows = wrapper.findAll("[data-testid^=process-]").filter((e) => /^process-\d+$/.test(e.attributes("data-testid") ?? ""));
    expect(rows.length).toBeGreaterThan(0);
    expect(rows.length).toBeLessThan(60);
  });

  it("fuente con permiso parcial: banda con el motivo y el GRANT copiable", async () => {
    api.monitorCapabilities.mockResolvedValue(caps({ processlist: { state: "degraded", reason: "own_only", hint: "GRANT PROCESS ON *.* TO 'u'@'h';" } }));
    const { wrapper } = mountView();
    await flushPromises();
    const b = wrapper.get("[data-testid=banner-processlist]");
    expect(b.text()).toContain("PROCESS");
    expect(b.text()).toContain("GRANT PROCESS ON *.* TO 'u'@'h';");
    await wrapper.get("[data-testid=copy-hint]").trigger("click");
    await flushPromises();
    expect(clipboard.writeText).toHaveBeenCalledWith("GRANT PROCESS ON *.* TO 'u'@'h';");
  });

  it("performance_schema apagado: aviso sin botón de activar (requiere reiniciar el servidor)", async () => {
    api.monitorCapabilities.mockResolvedValue(caps({ digest: { state: "unavailable", reason: "perf_schema_off", hint: "" } }));
    const { wrapper } = mountView();
    await flushPromises();
    const b = wrapper.get("[data-testid=banner-digest]");
    expect(b.text()).toContain("reiniciarlo");
    expect(b.find("[data-testid=copy-hint]").exists()).toBe(false);
  });

  it("procesos no disponibles: estado vacío en lugar de la tabla y sin pedir arrendamiento", async () => {
    const off = { state: "unavailable", reason: "no_privilege", hint: "" };
    api.monitorCapabilities.mockResolvedValue(caps({ processlist: off }));
    const { wrapper } = mountView();
    await flushPromises();
    expect(wrapper.text()).toContain("No se pueden leer los procesos");
    expect(api.watchProcesses).not.toHaveBeenCalled();
  });

  it("si Go rechaza el arrendamiento (UNSUPPORTED) el error se muestra y no queda vigilancia", async () => {
    api.watchProcesses.mockRejectedValue(Object.assign(new Error("x"), { cause: JSON.stringify({ code: "UNSUPPORTED", message: "La lista de procesos no está disponible", detail: "", retryable: false }) }));
    const { wrapper, m } = mountView();
    await flushPromises();
    expect(wrapper.get("[data-testid=monitor-error]").text()).toContain("no está disponible");
    expect(m.watching).toBeNull();
  });

  it("Iniciar llama a Go y el cronómetro aparece; Detener lo para", async () => {
    api.startMonitor.mockResolvedValue({ id: "7", profileId: "p1", startedAt: new Date().toISOString(), endedAt: null, reason: "running", intervalSec: 30, serverVersion: "8.0", serverId: "u", samples: 0, gaps: 0 });
    api.stopMonitor.mockResolvedValue(undefined);
    const { wrapper } = mountView();
    await flushPromises();
    await wrapper.get("[data-testid=monitor-start]").trigger("click");
    await flushPromises();
    expect(api.startMonitor).toHaveBeenCalled();
    expect(wrapper.get("[data-testid=monitor-elapsed]").text()).toMatch(/^\d\d:\d\d:\d\d$/);
    await wrapper.get("[data-testid=monitor-stop]").trigger("click");
    await flushPromises();
    expect(api.stopMonitor).toHaveBeenCalledWith("7");
    expect(wrapper.find("[data-testid=monitor-elapsed]").exists()).toBe(false);
  });

  it("sin ninguna fuente utilizable «Iniciar» queda deshabilitado", async () => {
    const off = { state: "unavailable", reason: "unsupported", hint: "" };
    api.monitorCapabilities.mockResolvedValue(caps({ processlist: off, digest: off, status: off }));
    const { wrapper } = mountView();
    await flushPromises();
    expect(wrapper.get("[data-testid=monitor-start]").attributes("disabled")).toBeDefined();
  });

  it("un busy de Go al iniciar se muestra como error", async () => {
    api.startMonitor.mockRejectedValue(Object.assign(new Error("Ya hay un monitoreo en marcha"), { cause: JSON.stringify({ code: "QUERY_FAILED", message: "Ya hay un monitoreo en marcha", detail: "busy", retryable: false }) }));
    const { wrapper } = mountView();
    await flushPromises();
    await wrapper.get("[data-testid=monitor-start]").trigger("click");
    await flushPromises();
    expect(wrapper.get("[data-testid=monitor-error]").text()).toContain("Ya hay un monitoreo");
  });

  it("la tarjeta de conexiones muestra el máximo del servidor cuando Go lo entrega", async () => {
    const { wrapper, m } = mountView();
    await flushPromises();
    m.handleProcesses({ sessionId: "s1", snapshot: snap() });
    await flushPromises();
    expect(wrapper.get("[data-testid=card-connections]").text().replace(/\s+/g, " ")).toContain("2 / 151");
    m.handleProcesses({ sessionId: "s1", snapshot: { ...snap(), maxConnections: 0 } });
    await flushPromises();
    expect(wrapper.find("[data-testid=card-max-connections]").exists()).toBe(false);
  });
});
