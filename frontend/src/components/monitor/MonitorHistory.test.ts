import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { nextTick } from "vue";

// jsdom no tiene canvas: uPlot se sustituye por un doble (las gráficas se prueban en TimeSeriesChart.test.ts).
vi.mock("uplot", () => {
  class FakeUPlot {
    static pxRatio = 1;
    constructor(public opts: Record<string, unknown>, public data: unknown[][]) {}
    destroy() {}
    setSize() {}
    valToPos(v: number) { return v; }
  }
  return { default: FakeUPlot };
});

const api = vi.hoisted(() => ({
  monitorCapabilities: vi.fn(), refreshMonitorCapabilities: vi.fn(), startMonitor: vi.fn(), stopMonitor: vi.fn(), monitorRuns: vi.fn(),
  watchProcesses: vi.fn(), unwatchProcesses: vi.fn(), monitorTop: vi.fn(), monitorDigest: vi.fn(), monitorSeries: vi.fn(), monitorCompare: vi.fn(),
  annotateMonitor: vi.fn(), deleteMonitorAnnotation: vi.fn(), purgeMonitor: vi.fn(), monitorCopyKill: vi.fn(),
}));
vi.mock("../../api/monitor", () => api);

import { i18n } from "../../i18n";
import { METRIC_KEYS, useMonitorStore } from "../../stores/monitor";
import { useSessionsStore } from "../../stores/sessions";
import { useUiStore } from "../../stores/ui";
import type { DigestDetail, DigestStat, MonitorCaps, MonitorRun, Series } from "../../types";
import MonitorMetrics from "./MonitorMetrics.vue";
import MonitorRuns from "./MonitorRuns.vue";
import MonitorTop from "./MonitorTop.vue";

const ok = { state: "ok", reason: "ok", hint: "" };
const caps = (over: Partial<MonitorCaps> = {}): MonitorCaps => ({ serverVersion: "8.0", isMariaDB: false, serverId: "u", processlist: ok, digest: ok, status: ok, processDigest: ok, ...over });
const stat = (digest: string, over: Partial<DigestStat> = {}): DigestStat => ({
  digest, schema: "pedidos", text: `SELECT ${digest} FROM t WHERE a = ?`, execs: 1000, totalMs: 5000, avgMs: 5, maxObservedMs: 40, rowsExamined: 2000, rowsSent: 1000,
  noIndex: 0, tmpDisk: 0, errors: 0, prev: null, changePct: 0, isNew: false, regression: false, approx: false, actors: [], actorCount: 0, ...over,
});
const runOf = (id: string, over: Partial<MonitorRun> = {}): MonitorRun => ({ id, profileId: "p1", startedAt: "2026-11-04T10:00:00Z", endedAt: "2026-11-04T11:00:00Z", reason: "stopped", intervalSec: 30, serverVersion: "8.0", serverId: "u", samples: 120, gaps: 0, ...over });
const at = (min: number) => new Date(Date.UTC(2026, 10, 4, 12, min)).toISOString();
const serie = (metric: string, vals: number[] | null = [1, 2, 3], annotations: Series["annotations"] = []): Series => ({
  metric, level: "raw", points: vals ? vals.map((v, i) => ({ t: at(i), v })) : [], gaps: [], annotations,
});

const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) };
let w: VueWrapper | null = null;

function setup(c: MonitorCaps = caps()) {
  const pinia = createPinia();
  setActivePinia(pinia);
  const sessions = useSessionsStore();
  sessions.byId = { s1: { sessionId: "s1", profileId: "p1", serverVersion: "8.0", currentDatabase: "" } };
  sessions.activeSessionId = "s1";
  const m = useMonitorStore();
  m.caps = { s1: c };
  return { pinia, m, ui: useUiStore() };
}
const mountTab = (comp: typeof MonitorTop | typeof MonitorMetrics | typeof MonitorRuns, pinia: ReturnType<typeof createPinia>) => {
  w = mount(comp, { props: { active: true }, attachTo: document.body, global: { plugins: [pinia, i18n] } });
  return w;
};
const body = () => document.body;
const q = <T extends Element = HTMLElement>(sel: string) => body().querySelector<T>(sel);

beforeEach(() => {
  Object.values(api).forEach((f) => f.mockReset());
  api.monitorTop.mockResolvedValue([]);
  api.monitorSeries.mockResolvedValue([]);
  api.monitorRuns.mockResolvedValue([]);
  api.monitorCompare.mockResolvedValue({ regressions: [], new: [], gone: [] });
  clipboard.writeText.mockClear();
  Object.defineProperty(navigator, "clipboard", { value: clipboard, configurable: true });
});
afterEach(() => {
  w?.unmount();
  w = null;
  document.body.innerHTML = "";
});

describe("Top queries", () => {
  it("pide a Go el ranking de 24 h por tiempo total y con comparación, y lo pinta en el orden recibido", async () => {
    const { pinia, m } = setup();
    api.monitorTop.mockResolvedValue([stat("zeta"), stat("alfa")]); // un orden que Vue no tiene motivo de «arreglar»
    const wrap = mountTab(MonitorTop, pinia);
    await flushPromises();
    const req = api.monitorTop.mock.calls[0]![0];
    expect(req).toMatchObject({ profileId: "p1", orderBy: "total_time", compare: true, includeSystem: false, limit: 50 });
    expect(Date.parse(req.to) - Date.parse(req.from)).toBe(24 * 3_600_000);
    expect(m.range).toBe("24h");
    const rows = wrap.findAll("[data-testid^=top-row-]").map((r) => r.attributes("data-testid"));
    expect(rows).toEqual(["top-row-zeta", "top-row-alfa"]);
  });

  it("elegir otro criterio vuelve a pedir a Go con ese criterio", async () => {
    const { pinia } = setup();
    const wrap = mountTab(MonitorTop, pinia);
    await flushPromises();
    api.monitorTop.mockClear();
    await wrap.get("[data-testid=top-order-execs]").trigger("click");
    await flushPromises();
    expect(api.monitorTop.mock.calls[0]![0]).toMatchObject({ orderBy: "execs" });
    expect(wrap.get("[data-testid=top-order-execs]").attributes("aria-checked")).toBe("true");
    expect(wrap.findAll("[data-testid^=top-order-]")).toHaveLength(7);
  });

  it("muestra «nueva», la regresión con su porcentaje y el cambio normal", async () => {
    const { pinia } = setup();
    api.monitorTop.mockResolvedValue([
      stat("n", { isNew: true }), stat("r", { regression: true, changePct: 38 }), stat("c", { changePct: -4.2 }),
    ]);
    const wrap = mountTab(MonitorTop, pinia);
    await flushPromises();
    expect(wrap.get("[data-testid=top-row-n] [data-testid=chip-new]").text()).toBe("nueva");
    expect(wrap.get("[data-testid=top-row-r] [data-testid=chip-regression]").text()).toContain("+38.0 %");
    expect(wrap.get("[data-testid=top-row-c] [data-testid=chip-change]").text()).toBe("-4.2 %");
  });

  it("sin comparación no hay chips y no se pide la comparación", async () => {
    const { pinia, m } = setup();
    api.monitorTop.mockResolvedValue([stat("a", { regression: true, changePct: 50 })]);
    m.topCompare = false;
    const wrap = mountTab(MonitorTop, pinia);
    await flushPromises();
    expect(api.monitorTop.mock.calls[0]![0]).toMatchObject({ compare: false });
    expect(api.monitorCompare).not.toHaveBeenCalled();
    expect(wrap.find("[data-testid=chip-regression]").exists()).toBe(false);
  });

  it("el resumen sale de la comparación de Go: regresiones, nuevas y desaparecidas con su lista", async () => {
    const { pinia } = setup();
    api.monitorTop.mockResolvedValue([stat("a")]);
    api.monitorCompare.mockResolvedValue({ regressions: [stat("a"), stat("b")], new: [stat("c")], gone: [stat("g", { text: "SELECT viejo" })] });
    const wrap = mountTab(MonitorTop, pinia);
    await flushPromises();
    expect(wrap.get("[data-testid=top-summary-regressions]").text()).toBe("2 regresiones");
    expect(wrap.get("[data-testid=top-summary-new]").text()).toBe("1 nuevas");
    expect(wrap.get("[data-testid=top-summary-gone]").text()).toBe("1 desaparecidas");
    expect(wrap.get("[data-testid=top-gone]").text()).toContain("SELECT viejo");
    // periodo previo de igual duración, justo antes del actual
    const [, a, b] = api.monitorCompare.mock.calls[0]!;
    expect(a.to).toBe(b.from);
    expect(Date.parse(a.to) - Date.parse(a.from)).toBe(Date.parse(b.to) - Date.parse(b.from));
  });

  it("rotula los valores aproximados", async () => {
    const { pinia } = setup();
    api.monitorTop.mockResolvedValue([stat("a", { approx: true, execs: 1500 })]);
    const wrap = mountTab(MonitorTop, pinia);
    await flushPromises();
    expect(wrap.get("[data-testid=top-row-a]").text()).toContain("≈");
    expect(wrap.find("[data-testid=top-approx]").exists()).toBe(true);
  });

  it("spinner mientras llega el primer ranking y estado vacío si no hay datos", async () => {
    const { pinia } = setup();
    let release!: (v: DigestStat[]) => void;
    api.monitorTop.mockReturnValue(new Promise<DigestStat[]>((r) => (release = r)));
    const wrap = mountTab(MonitorTop, pinia);
    await nextTick();
    expect(wrap.find("[data-testid=top-loading]").exists()).toBe(true);
    release([]);
    await flushPromises();
    expect(wrap.find("[data-testid=top-loading]").exists()).toBe(false);
    expect(wrap.get("[data-testid=top-empty]").text()).toContain("Sin datos en este rango");
  });

  it("si el digest no está disponible muestra el motivo y el GRANT en vez de una tabla vacía, y no pide nada", async () => {
    const { pinia } = setup(caps({ digest: { state: "unavailable", reason: "no_privilege", hint: "GRANT SELECT ON performance_schema.* TO 'u'@'h';" } }));
    const wrap = mountTab(MonitorTop, pinia);
    await flushPromises();
    expect(api.monitorTop).not.toHaveBeenCalled();
    expect(wrap.get("[data-testid=source-empty]").text()).toContain("Falta un privilegio");
    expect(wrap.get("[data-testid=source-empty-hint]").text()).toContain("GRANT SELECT ON performance_schema");
    await wrap.get("[data-testid=source-empty-copy]").trigger("click");
    await flushPromises();
    expect(clipboard.writeText).toHaveBeenCalledWith("GRANT SELECT ON performance_schema.* TO 'u'@'h';");
  });

  it("clic en una fila abre el detalle con el texto normalizado y «Copiar texto»", async () => {
    const { pinia } = setup();
    api.monitorTop.mockResolvedValue([stat("a")]);
    const detail: DigestDetail = { digest: "a", text: "SELECT a FROM t WHERE a = ?", schemas: ["pedidos"], stat: stat("a"), points: [{ t: at(0), execs: 10, totalMs: 50, avgMs: 5 }, { t: at(1), execs: 20, totalMs: 100, avgMs: 5 }], gaps: [], actors: [] };
    api.monitorDigest.mockResolvedValue(detail);
    const wrap = mountTab(MonitorTop, pinia);
    await flushPromises();
    await wrap.get("[data-testid=top-row-a]").trigger("click");
    await flushPromises();
    expect(api.monitorDigest.mock.calls[0]![1]).toBe("a");
    expect(q("[data-testid=digest-text]")!.textContent).toBe("SELECT a FROM t WHERE a = ?");
    expect(body().querySelectorAll("[data-testid=time-series-chart]")).toHaveLength(2);
    q("[data-testid=digest-copy]")!.click();
    await flushPromises();
    expect(clipboard.writeText).toHaveBeenCalledWith("SELECT a FROM t WHERE a = ?");
  });
});

describe("Top queries: quién la ejecutó (spec 22 ⚑D17)", () => {
  const actor = (user: string, host: string, seen: number) => ({ user, host, seen, lastSeen: Date.UTC(2026, 10, 4, 12, 0) });

  it("la columna «Quién» muestra el principal con «+N», o «—» sin datos, y no ordena ni agrega nada", async () => {
    const { pinia } = setup();
    api.monitorTop.mockResolvedValue([
      stat("a", { actors: [actor("app", "10.0.0.4", 9), actor("etl", "10.0.0.9", 2), actor("bi", "10.0.0.7", 1)], actorCount: 4 }),
      stat("b", { actors: [actor("report", "10.0.0.9", 3)], actorCount: 1 }),
      stat("c"),
    ]);
    const wrap = mountTab(MonitorTop, pinia);
    await flushPromises();
    expect(wrap.get("[data-testid=top-who-header]").text()).toBe("Quién");
    const cells = wrap.findAll("[data-testid=top-actors]");
    expect(cells.map((c) => c.text().replace(/\s+/g, " "))).toEqual(["app · 10.0.0.4 +3", "report · 10.0.0.9", "—"]);
    // El tooltip lista a los recibidos y recuerda que es por muestreo; sin datos explica por qué.
    expect(cells[0]!.attributes("title")).toContain("etl · 10.0.0.9 · visto en 2 muestras");
    expect(cells[0]!.attributes("title")).toContain("Visto por muestreo");
    expect(cells[2]!.attributes("title")).toContain("Sin datos");
  });

  it("si el servidor no ofrece STATEMENT_DIGEST lo dice en el tooltip y en el detalle", async () => {
    const { pinia } = setup(caps({ processDigest: { state: "unavailable", reason: "unsupported", hint: "" } }));
    api.monitorTop.mockResolvedValue([stat("a")]);
    api.monitorDigest.mockResolvedValue({ digest: "a", text: "SELECT ?", schemas: ["pedidos"], stat: stat("a"), points: [], gaps: [], actors: [] } satisfies DigestDetail);
    const wrap = mountTab(MonitorTop, pinia);
    await flushPromises();
    expect(wrap.get("[data-testid=top-actors]").attributes("title")).toContain("STATEMENT_DIGEST");
    await wrap.get("[data-testid=top-row-a]").trigger("click");
    await flushPromises();
    expect(q("[data-testid=digest-actors-empty]")!.textContent).toContain("STATEMENT_DIGEST");
  });

  it("el detalle lista a todos con las muestras y deja la nota de que es aproximado", async () => {
    const { pinia } = setup();
    api.monitorTop.mockResolvedValue([stat("a", { actors: [actor("app", "10.0.0.4", 9)], actorCount: 2 })]);
    api.monitorDigest.mockResolvedValue({
      digest: "a", text: "SELECT ?", schemas: ["pedidos"], stat: stat("a"), points: [], gaps: [],
      actors: [actor("app", "10.0.0.4", 9), actor("etl", "10.0.0.9", 2)],
    } satisfies DigestDetail);
    const wrap = mountTab(MonitorTop, pinia);
    await flushPromises();
    await wrap.get("[data-testid=top-row-a]").trigger("click");
    await flushPromises();
    const items = Array.from(body().querySelectorAll("[data-testid=digest-actor]")).map((li) => li.textContent!.replace(/\s+/g, " ").trim());
    expect(items).toHaveLength(2);
    expect(items[0]).toContain("app · 10.0.0.4");
    expect(items[0]).toContain("visto en 9 muestras");
    expect(items[1]).toContain("etl · 10.0.0.9");
    expect(q("[data-testid=digest-actors]")!.textContent).toContain("aproximado");
  });
});

describe("Métricas", () => {
  it("pide todas las métricas a Go en una sola llamada y pinta una gráfica por métrica", async () => {
    const { pinia } = setup();
    api.monitorSeries.mockResolvedValue(METRIC_KEYS.map((k) => serie(k)));
    const wrap = mountTab(MonitorMetrics, pinia);
    await flushPromises();
    expect(api.monitorSeries).toHaveBeenCalledTimes(1);
    expect(api.monitorSeries.mock.calls[0]![0]).toMatchObject({ profileId: "p1", metrics: [...METRIC_KEYS], maxPoints: 300 });
    expect(wrap.findAll("[data-testid^=chart-]").filter((e) => METRIC_KEYS.some((k) => e.attributes("data-testid") === `chart-${k}`))).toHaveLength(METRIC_KEYS.length);
    expect(wrap.find("[data-testid=chart-bp_hit_ratio]").exists()).toBe(true);
  });

  it("sin datos en ninguna serie muestra el estado vacío (los huecos no son ceros)", async () => {
    const { pinia } = setup();
    api.monitorSeries.mockResolvedValue(METRIC_KEYS.map((k) => serie(k, null)));
    const wrap = mountTab(MonitorMetrics, pinia);
    await flushPromises();
    expect(wrap.get("[data-testid=metrics-empty]").text()).toContain("no ceros");
    expect(wrap.find("[data-testid=metrics-charts]").exists()).toBe(false);
  });

  it("con la fuente de estado no disponible muestra el motivo y no pide series", async () => {
    const { pinia } = setup(caps({ status: { state: "unavailable", reason: "no_privilege", hint: "" } }));
    const wrap = mountTab(MonitorMetrics, pinia);
    await flushPromises();
    expect(api.monitorSeries).not.toHaveBeenCalled();
    expect(wrap.find("[data-testid=source-empty]").exists()).toBe(true);
  });

  it("«Añadir nota» crea la anotación con Go y recarga las series; el texto inválido se explica", async () => {
    const { pinia } = setup();
    api.monitorSeries.mockResolvedValue(METRIC_KEYS.map((k) => serie(k)));
    api.annotateMonitor.mockResolvedValue({ id: "1", profileId: "p1", at: at(1), text: "deploy" });
    const wrap = mountTab(MonitorMetrics, pinia);
    await flushPromises();
    await wrap.get("[data-testid=metrics-add-note]").trigger("click");
    await flushPromises();
    const input = q<HTMLInputElement>("[data-testid=note-input]")!;
    expect((q("[data-testid=note-save]") as HTMLButtonElement).disabled).toBe(true); // sin texto no se guarda
    input.value = "  deploy v2.14  ";
    input.dispatchEvent(new Event("input"));
    await flushPromises();
    q("[data-testid=note-save]")!.click();
    await flushPromises();
    expect(api.annotateMonitor).toHaveBeenCalledTimes(1);
    expect(api.annotateMonitor.mock.calls[0]![0]).toBe("p1");
    expect(api.annotateMonitor.mock.calls[0]![2]).toBe("deploy v2.14");
    expect(api.monitorSeries).toHaveBeenCalledTimes(2); // recarga
    expect(q("[data-testid=note-input]")).toBeNull(); // el diálogo se cerró
  });

  it("si Go rechaza la nota (text) el diálogo sigue abierto con el motivo", async () => {
    const { pinia } = setup();
    api.monitorSeries.mockResolvedValue(METRIC_KEYS.map((k) => serie(k)));
    api.annotateMonitor.mockRejectedValue(Object.assign(new Error("x"), { cause: JSON.stringify({ code: "VALIDATION", message: "x", detail: "text", retryable: false }) }));
    const wrap = mountTab(MonitorMetrics, pinia);
    await flushPromises();
    await wrap.get("[data-testid=metrics-add-note]").trigger("click");
    await flushPromises();
    const input = q<HTMLInputElement>("[data-testid=note-input]")!;
    input.value = "nota";
    input.dispatchEvent(new Event("input"));
    await flushPromises();
    q("[data-testid=note-save]")!.click();
    await flushPromises();
    expect(body().textContent).toContain("Entre 1 y 200 caracteres");
    expect(q("[data-testid=note-input]")).not.toBeNull();
  });

  it("lista las notas y permite borrarlas (recarga después)", async () => {
    const { pinia } = setup();
    const note = { id: "7", profileId: "p1", at: at(1), text: "deploy v2.14" };
    api.monitorSeries.mockResolvedValue(METRIC_KEYS.map((k) => serie(k, [1, 2], [note])));
    api.deleteMonitorAnnotation.mockResolvedValue(undefined);
    const wrap = mountTab(MonitorMetrics, pinia);
    await flushPromises();
    expect(wrap.get("[data-testid=note-7]").text()).toContain("deploy v2.14");
    await wrap.get("[data-testid=note-7] [data-testid=note-delete]").trigger("click");
    await flushPromises();
    expect(api.deleteMonitorAnnotation).toHaveBeenCalledWith("7");
    expect(api.monitorSeries).toHaveBeenCalledTimes(2);
  });

  it("con el monitoreo en marcha, cada muestra nueva refresca las gráficas", async () => {
    const { pinia, m } = setup();
    api.monitorSeries.mockResolvedValue(METRIC_KEYS.map((k) => serie(k)));
    m.run = { p1: runOf("7", { reason: "running", endedAt: null, samples: 1 }) };
    mountTab(MonitorMetrics, pinia);
    await flushPromises();
    api.monitorSeries.mockClear();
    m.handleSample({ runId: "7", at: at(2), qps: 1, threadsRunning: 1, threadsConnected: 1, longestSec: 0 });
    await flushPromises();
    expect(api.monitorSeries).toHaveBeenCalledTimes(1);
  });
});

describe("Ejecuciones", () => {
  it("lista las ejecuciones con su motivo de fin, intervalo, muestras y huecos", async () => {
    const { pinia } = setup();
    api.monitorRuns.mockResolvedValue([runOf("3", { reason: "app_closed", gaps: 2, samples: 80 }), runOf("2", { reason: "stopped" })]);
    const wrap = mountTab(MonitorRuns, pinia);
    await flushPromises();
    expect(wrap.get("[data-testid=run-3] [data-testid=run-reason]").text()).toBe("App cerrada");
    expect(wrap.get("[data-testid=run-2] [data-testid=run-reason]").text()).toBe("Detenida");
    expect(wrap.get("[data-testid=run-3]").text()).toContain("80");
    expect(wrap.get("[data-testid=run-3]").text()).toContain("1 h 0 min");
    expect(wrap.find("[data-testid=runs-mixed]").exists()).toBe(false);
  });

  it("avisa cuando las ejecuciones vienen de servidores distintos y etiqueta cada una", async () => {
    const { pinia } = setup();
    api.monitorRuns.mockResolvedValue([runOf("2", { serverId: "b" }), runOf("1", { serverId: "a" })]);
    const wrap = mountTab(MonitorRuns, pinia);
    await flushPromises();
    expect(wrap.get("[data-testid=runs-mixed]").text()).toContain("servidores distintos");
    expect(wrap.get("[data-testid=run-2] [data-testid=run-server]").text()).not.toBe(wrap.get("[data-testid=run-1] [data-testid=run-server]").text());
  });

  it("estado vacío sin ejecuciones", async () => {
    const { pinia } = setup();
    const wrap = mountTab(MonitorRuns, pinia);
    await flushPromises();
    expect(wrap.get("[data-testid=runs-empty]").text()).toContain("Aún no hay ejecuciones");
    expect((wrap.get("[data-testid=runs-purge]").element as HTMLButtonElement).disabled).toBe(true);
  });

  it("«Borrar datos» pide confirmación, borra con Go y recarga; con una ejecución activa está deshabilitado", async () => {
    const { pinia, m } = setup();
    api.monitorRuns.mockResolvedValue([runOf("1")]);
    api.purgeMonitor.mockResolvedValue(undefined);
    const wrap = mountTab(MonitorRuns, pinia);
    await flushPromises();
    await wrap.get("[data-testid=runs-purge]").trigger("click");
    await flushPromises();
    expect(api.purgeMonitor).not.toHaveBeenCalled(); // primero la confirmación
    const confirm = Array.from(body().querySelectorAll("button")).find((b) => b.textContent?.trim() === "Borrar datos" && b.closest("[role=dialog]"))!;
    confirm.click();
    await flushPromises();
    expect(api.purgeMonitor).toHaveBeenCalledWith("p1", null);
    expect(api.monitorRuns).toHaveBeenCalledTimes(2);

    m.run = { p1: runOf("9", { reason: "running", endedAt: null }) };
    await flushPromises();
    expect((wrap.get("[data-testid=runs-purge]").element as HTMLButtonElement).disabled).toBe(true);
  });
});
