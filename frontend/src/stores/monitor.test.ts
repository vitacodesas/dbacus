import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({
  monitorCapabilities: vi.fn(), refreshMonitorCapabilities: vi.fn(), startMonitor: vi.fn(), stopMonitor: vi.fn(), monitorRuns: vi.fn(),
  watchProcesses: vi.fn(), unwatchProcesses: vi.fn(), monitorTop: vi.fn(), monitorDigest: vi.fn(), monitorSeries: vi.fn(), monitorCompare: vi.fn(),
  annotateMonitor: vi.fn(), deleteMonitorAnnotation: vi.fn(), purgeMonitor: vi.fn(), monitorCopyKill: vi.fn(), killProcess: vi.fn(),
}));
vi.mock("../api/monitor", () => api);

import { LEASE_RENEW_MS, useMonitorStore } from "./monitor";
import { useSessionsStore } from "./sessions";
import { useUiStore } from "./ui";
import type { MonitorCaps, MonitorRun, ProcessSnapshot } from "../types";

const ok = { state: "ok", reason: "ok", hint: "" };
const caps = (over: Partial<MonitorCaps> = {}): MonitorCaps => ({ serverVersion: "8.0", isMariaDB: false, serverId: "u", processlist: ok, digest: ok, status: ok, processDigest: ok, ...over });
const runOf = (over: Partial<MonitorRun> = {}): MonitorRun => ({ id: "7", profileId: "p1", startedAt: new Date().toISOString(), endedAt: null, reason: "running", intervalSec: 30, serverVersion: "8.0", serverId: "u", samples: 0, gaps: 0, ...over });
const snap = (n = 1): ProcessSnapshot => ({ at: "2026-11-04T12:00:00Z", rows: Array.from({ length: n }, (_, i) => ({ id: i + 1, user: "u", host: "h", db: "d", command: "Query", timeSec: i, state: "", info: "", infoTruncated: false, digest: "" })), summary: { total: n, running: n, sleeping: 0, waitingLock: 0, longestSec: 0, byCommand: null, byUser: null }, maxConnections: 151 });
const apiError = (detail: string, message = "x", code = "QUERY_FAILED") => Object.assign(new Error(message), { cause: JSON.stringify({ code, message, detail, retryable: false }) });

function setup() {
  const sessions = useSessionsStore();
  sessions.byId = { s1: { sessionId: "s1", profileId: "p1", serverVersion: "8.0", currentDatabase: "" } };
  sessions.activeSessionId = "s1";
  return { m: useMonitorStore(), sessions };
}

beforeEach(() => {
  setActivePinia(createPinia());
  Object.values(api).forEach((f) => f.mockReset());
  api.monitorCapabilities.mockResolvedValue(caps());
  api.watchProcesses.mockResolvedValue(undefined);
  api.unwatchProcesses.mockResolvedValue(undefined);
  vi.useFakeTimers();
});
afterEach(() => vi.useRealTimers());

describe("monitor store · estado y ejecución", () => {
  it("elige la sesión activa y guarda las capacidades por sesión", async () => {
    const { m } = setup();
    expect(m.sessionId).toBe("s1");
    expect(m.profileId).toBe("p1");
    await m.loadCaps();
    expect(api.monitorCapabilities).toHaveBeenCalledWith("s1");
    expect(m.currentCaps?.serverVersion).toBe("8.0");
    expect(m.canStart).toBe(true);
  });

  it("sin ninguna fuente utilizable no se puede iniciar", async () => {
    const { m } = setup();
    const off = { state: "unavailable", reason: "perf_schema_off", hint: "" };
    api.monitorCapabilities.mockResolvedValue(caps({ processlist: off, digest: off, status: off }));
    await m.loadCaps();
    expect(m.canStart).toBe(false);
  });

  it("Start deja que Go aplique los valores por defecto y guarda la ejecución por perfil", async () => {
    const { m } = setup();
    api.startMonitor.mockResolvedValue(runOf());
    await m.start();
    expect(api.startMonitor).toHaveBeenCalledWith("s1", { intervalSec: 0, topK: 0, processes: false, status: false, digest: false });
    expect(m.running).toBe(true);
    expect(m.currentRun?.id).toBe("7");
  });

  it("un busy de Go queda en `error` y no crea ejecución", async () => {
    const { m } = setup();
    api.startMonitor.mockRejectedValue(apiError("busy", "Ya hay un monitoreo en marcha"));
    expect(await m.start()).toBeNull();
    expect(m.error?.detail).toBe("busy");
    expect(m.running).toBe(false);
  });

  it("Stop llama a Go y limpia la ejecución", async () => {
    const { m } = setup();
    api.startMonitor.mockResolvedValue(runOf());
    api.stopMonitor.mockResolvedValue(undefined);
    await m.start();
    await m.stop();
    expect(api.stopMonitor).toHaveBeenCalledWith("7");
    expect(m.running).toBe(false);
  });

  it("monitor:sample cuenta muestras y guarda la última; de otra ejecución se ignora", async () => {
    const { m } = setup();
    api.startMonitor.mockResolvedValue(runOf());
    await m.start();
    m.handleSample({ runId: "7", at: "t", qps: 1200, threadsRunning: 4, threadsConnected: 40, longestSec: 3 });
    m.handleSample({ runId: "99", at: "t", qps: 1, threadsRunning: 1, threadsConnected: 1, longestSec: 1 });
    expect(m.currentSample?.qps).toBe(1200);
    expect(m.currentRun?.samples).toBe(1);
  });

  it("monitor:state degraded avisa una sola vez y la recuperación quita el aviso", async () => {
    const { m } = setup();
    api.startMonitor.mockResolvedValue(runOf());
    await m.start();
    const ev = { runId: "7", profileId: "p1", state: "degraded", source: "digest", reason: "no_privilege" };
    m.handleState(ev);
    m.handleState(ev);
    expect(m.currentNotices).toEqual([{ source: "digest", reason: "no_privilege" }]);
    m.handleState({ runId: "7", profileId: "p1", state: "running", source: "digest", reason: "recovered" });
    expect(m.currentNotices).toEqual([]);
    expect(m.running).toBe(true);
  });

  it("monitor:state stopped y disconnected terminan la ejecución; disconnected avisa con un toast", async () => {
    const { m } = setup();
    api.startMonitor.mockResolvedValue(runOf());
    await m.start();
    m.handleState({ runId: "7", profileId: "p1", state: "disconnected" });
    expect(m.running).toBe(false);
    expect(useUiStore().toasts.some((t) => t.kind === "info")).toBe(true);
  });

  it("un corte por cambio de servidor es un error visible", async () => {
    const { m } = setup();
    api.startMonitor.mockResolvedValue(runOf());
    await m.start();
    m.handleState({ runId: "7", profileId: "p1", state: "stopped", source: "server" });
    expect(m.running).toBe(false);
    expect(useUiStore().toasts.some((t) => t.kind === "error")).toBe(true);
  });
});

describe("monitor store · arrendamiento de la vista en vivo", () => {
  it("watch renueva el arrendamiento cada 15 s mientras siga visible", async () => {
    const { m } = setup();
    await m.watch();
    expect(api.watchProcesses).toHaveBeenCalledTimes(1);
    expect(api.watchProcesses).toHaveBeenCalledWith("s1", 2000);
    await vi.advanceTimersByTimeAsync(LEASE_RENEW_MS);
    expect(api.watchProcesses).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(LEASE_RENEW_MS);
    expect(api.watchProcesses).toHaveBeenCalledTimes(3);
  });

  it("el intervalo de la vista en vivo sale de los ajustes", async () => {
    const { m } = setup();
    useUiStore().monitor.monitorLiveIntervalSec = 5;
    await m.watch();
    expect(api.watchProcesses).toHaveBeenCalledWith("s1", 5000);
  });

  it("liveEnabled sigue el ajuste automático hasta que el usuario pausa o reanuda", () => {
    const { m } = setup();
    const ui = useUiStore();
    expect(m.liveEnabled).toBe(true);
    ui.monitor.monitorLiveAuto = false;
    expect(m.liveEnabled).toBe(false);
    m.setLive(true);
    expect(m.liveEnabled).toBe(true);
    ui.monitor.monitorLiveAuto = true;
    m.setLive(false);
    expect(m.liveEnabled).toBe(false);
  });

  it("watch repetido sobre la misma sesión no abre otro arrendamiento ni otro temporizador", async () => {
    const { m } = setup();
    await m.watch();
    await m.watch();
    expect(api.watchProcesses).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(LEASE_RENEW_MS);
    expect(api.watchProcesses).toHaveBeenCalledTimes(2);
  });

  it("unwatch suelta el arrendamiento y deja de renovar", async () => {
    const { m } = setup();
    await m.watch();
    await m.unwatch();
    expect(api.unwatchProcesses).toHaveBeenCalledWith("s1");
    await vi.advanceTimersByTimeAsync(LEASE_RENEW_MS * 3);
    expect(api.watchProcesses).toHaveBeenCalledTimes(1);
    expect(m.watching).toBeNull();
  });

  it("los procesos solo se guardan de la sesión vigilada (un evento tardío no repinta)", async () => {
    const { m } = setup();
    m.handleProcesses({ sessionId: "s1", snapshot: snap(2) });
    expect(m.currentProcesses).toBeNull();
    await m.watch();
    m.handleProcesses({ sessionId: "s1", snapshot: snap(2) });
    expect(m.currentProcesses?.rows).toHaveLength(2);
    await m.unwatch();
    m.handleProcesses({ sessionId: "s1", snapshot: snap(5) });
    expect(m.currentProcesses?.rows).toHaveLength(2);
  });

  it("si Go rechaza el arrendamiento (UNSUPPORTED) se guarda el error y no se renueva nada", async () => {
    const { m } = setup();
    api.watchProcesses.mockRejectedValue(apiError("", "La lista de procesos no está disponible", "UNSUPPORTED"));
    await m.watch();
    expect(m.error?.code).toBe("UNSUPPORTED");
    expect(m.watching).toBeNull();
    await vi.advanceTimersByTimeAsync(LEASE_RENEW_MS * 2);
    expect(api.watchProcesses).toHaveBeenCalledTimes(1);
  });

  it("si la sesión vigilada se cierra, se olvida la vigilancia y sus procesos", async () => {
    const { m, sessions } = setup();
    await m.watch();
    m.handleProcesses({ sessionId: "s1", snapshot: snap() });
    sessions.byId = {};
    await vi.advanceTimersByTimeAsync(0);
    await Promise.resolve();
    expect(m.watching).toBeNull();
    await vi.advanceTimersByTimeAsync(LEASE_RENEW_MS * 2);
    expect(api.watchProcesses).toHaveBeenCalledTimes(1);
  });

  it("cambiar de conexión suelta la vigilancia de la anterior", async () => {
    const { m, sessions } = setup();
    sessions.byId = { ...sessions.byId, s2: { sessionId: "s2", profileId: "p2", serverVersion: "8.0", currentDatabase: "" } };
    await m.watch("s1");
    m.selectSession("s2");
    await Promise.resolve();
    expect(api.unwatchProcesses).toHaveBeenCalledWith("s1");
    expect(m.sessionId).toBe("s2");
  });
});

describe("monitor store · lecturas y KILL", () => {
  it("copyKill devuelve el texto armado por Go (Vue no arma SQL)", async () => {
    const { m } = setup();
    api.monitorCopyKill.mockResolvedValue("KILL QUERY 9;");
    expect(await m.copyKill(9, true)).toBe("KILL QUERY 9;");
    expect(api.monitorCopyKill).toHaveBeenCalledWith("s1", 9, true);
  });

  it("copyKill con error de Go muestra un toast y devuelve null", async () => {
    const { m } = setup();
    api.monitorCopyKill.mockRejectedValue(apiError("processId", "Identificador de proceso no válido", "VALIDATION"));
    expect(await m.copyKill(0, false)).toBeNull();
    expect(useUiStore().toasts.some((t) => t.kind === "error")).toBe(true);
  });

  it("killProcess manda id, usuario y host a Go y avisa", async () => {
    const { m } = setup();
    api.killProcess.mockResolvedValue(undefined);
    const p = { id: 9, user: "app", host: "h:1", db: "", command: "Query", timeSec: 1, state: "", info: "", infoTruncated: false, digest: "" };
    expect(await m.killProcess(p, true)).toBe(true);
    expect(api.killProcess).toHaveBeenCalledWith("s1", p, true);
    expect(useUiStore().toasts.some((t) => t.kind === "success")).toBe(true);
  });

  it("killProcess con error de Go (p. ej. sin privilegio) muestra el motivo y devuelve false", async () => {
    const { m } = setup();
    api.killProcess.mockRejectedValue(apiError("kill_denied", "Falta el privilegio", "QUERY_FAILED"));
    const p = { id: 9, user: "app", host: "h:1", db: "", command: "Query", timeSec: 1, state: "", info: "", infoTruncated: false, digest: "" };
    expect(await m.killProcess(p, false)).toBe(false);
    expect(useUiStore().toasts.some((t) => t.kind === "error")).toBe(true);
  });

  it("loadTop y loadSeries piden a Go el rango elegido y guardan el resultado tal cual (sin ordenar ni filtrar)", async () => {
    const { m } = setup();
    const stats = [{ digest: "b" }, { digest: "a" }] as never;
    api.monitorTop.mockResolvedValue(stats);
    api.monitorSeries.mockResolvedValue([{ metric: "qps" }] as never);
    m.range = "6h";
    m.schema = "pedidos";
    await m.loadTop({ orderBy: "execs" });
    const req = api.monitorTop.mock.calls[0]![0];
    expect(req).toMatchObject({ profileId: "p1", schema: "pedidos", orderBy: "execs" });
    expect(Date.parse(req.to) - Date.parse(req.from)).toBe(6 * 3_600_000);
    expect(m.top).toStrictEqual(stats);
    await m.loadSeries(["qps"]);
    expect(api.monitorSeries.mock.calls[0]![0]).toMatchObject({ profileId: "p1", metrics: ["qps"], maxPoints: 300 });
    expect(m.series).toHaveLength(1);
  });

  it("el código del store no ordena, filtra ni agrega (regla de oro 1)", async () => {
    const src = (await import("./monitor?raw")).default as string;
    expect(src).not.toMatch(/\.sort\(|\.filter\(\s*\(?\s*\w+\s*\)?\s*=>\s*\w+\.(execs|totalMs)|\.reduce\(/);
  });

  it("previousWindow es el periodo de igual duración justo antes del rango elegido", () => {
    const { m } = setup();
    m.range = "6h";
    const cur = m.rangeWindow();
    const prev = m.previousWindow();
    expect(Date.parse(prev.to)).toBe(Date.parse(cur.from));
    expect(Date.parse(prev.to) - Date.parse(prev.from)).toBe(6 * 3_600_000);
  });

  it("loadTop usa el criterio y la comparación elegidos; loadComparison pide previo contra actual", async () => {
    const { m } = setup();
    api.monitorTop.mockResolvedValue([]);
    api.monitorCompare.mockResolvedValue({ regressions: [], new: [], gone: [] });
    m.topOrder = "avg_time";
    m.topCompare = false;
    await m.loadTop();
    expect(api.monitorTop.mock.calls[0]![0]).toMatchObject({ orderBy: "avg_time", compare: false });
    expect(m.topLoaded).toBe(true);
    await m.loadComparison();
    const [pid, a, b] = api.monitorCompare.mock.calls[0]!;
    expect(pid).toBe("p1");
    expect(a.to).toBe(b.from);
  });

  it("una respuesta vacía (null de Go) no rompe las listas", async () => {
    const { m } = setup();
    api.monitorTop.mockResolvedValue(undefined);
    api.monitorSeries.mockResolvedValue(undefined);
    api.monitorRuns.mockResolvedValue(undefined);
    await m.loadTop();
    await m.loadSeries(["qps"]);
    await m.loadRuns();
    expect([m.top, m.series, m.runList]).toEqual([[], [], []]);
    expect([m.topLoaded, m.seriesLoaded, m.runsLoaded]).toEqual([true, true, true]);
  });

  it("cambiar de conexión descarta lo histórico de la anterior", async () => {
    const { m, sessions } = setup();
    sessions.byId = { ...sessions.byId, s2: { sessionId: "s2", profileId: "p2", serverVersion: "8.0", currentDatabase: "" } };
    api.monitorTop.mockResolvedValue([{ digest: "a" }] as never);
    await m.loadTop();
    expect(m.top).toHaveLength(1);
    m.selectSession("s2");
    expect(m.top).toEqual([]);
    expect(m.topLoaded).toBe(false);
  });

  it("purge y deleteAnnotation devuelven si Go pudo; un fallo queda en error", async () => {
    const { m } = setup();
    api.purgeMonitor.mockResolvedValue(undefined);
    expect(await m.purge()).toBe(true);
    expect(api.purgeMonitor).toHaveBeenCalledWith("p1", null);
    api.purgeMonitor.mockRejectedValue(apiError("busy", "Detén el monitoreo primero"));
    expect(await m.purge()).toBe(false);
    expect(m.error?.detail).toBe("busy");
    api.deleteMonitorAnnotation.mockRejectedValue(apiError("id", "x", "VALIDATION"));
    expect(await m.deleteAnnotation("x")).toBe(false);
  });
});
