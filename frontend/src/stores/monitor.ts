import { computed, ref, watch as vueWatch } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/monitor";
import { asAppError } from "../api/errors";
import { i18n } from "../i18n";
import type { Annotation, AppError, Comparison, DigestDetail, DigestStat, MonitorCaps, MonitorConfig, MonitorProcessesEvent, MonitorRun, MonitorSampleEvent, MonitorStateEvent, ProcessSnapshot, Series, TimeRange } from "../types";
import { useSessionsStore } from "./sessions";
import { useUiStore } from "./ui";

const t = (k: string, p?: Record<string, unknown>) => i18n.global.t(k, p ?? {});

export type MonitorTab = "live" | "top" | "metrics" | "runs";
export type RangeKey = "1h" | "6h" | "24h" | "7d";
const RANGE_MS: Record<RangeKey, number> = { "1h": 3_600_000, "6h": 21_600_000, "24h": 86_400_000, "7d": 604_800_000 };
export const RANGE_KEYS = Object.keys(RANGE_MS) as RangeKey[];

/** Criterios del ranking (spec 22): Go ordena; la vista solo elige cuál pedir. */
export const ORDER_KEYS = ["total_time", "execs", "avg_time", "rows_ratio", "no_index", "tmp_disk", "errors"] as const;
export type OrderKey = (typeof ORDER_KEYS)[number];
/** Métricas de la pestaña Métricas. `bp_hit_ratio` la calcula Go (aciertos del buffer pool, %). */
export const METRIC_KEYS = ["qps", "threads_running", "threads_connected", "slow_queries", "aborted_connects", "row_lock_waits", "bp_hit_ratio", "tmp_disk_tables", "bytes_received", "bytes_sent"] as const;
export type MetricKey = (typeof METRIC_KEYS)[number];

/** Renovación del arrendamiento de la vista en vivo: Go lo suelta a los 30 s sin renovar (spec 22). */
export const LEASE_RENEW_MS = 15_000;
export const LIVE_INTERVAL_MS = 2_000;

export interface SourceNotice {
  source: string;
  reason: string;
}

/**
 * Proyección del monitoreo (spec 22, spec 09). Todo lo calcula Go: este store no agrega, ordena ni filtra; guarda lo que
 * llega (`caps`, `processes`, `top`, `series`…) y el estado visual (pestaña, rango, conexión elegida).
 */
export const useMonitorStore = defineStore("monitor", () => {
  const sessions = useSessionsStore();

  const tab = ref<MonitorTab>("live");
  const selectedSessionId = ref<string | null>(null);
  const schema = ref("");
  const range = ref<RangeKey>("24h");

  /** Por sesión: lo que el servidor deja leer. */
  const caps = ref<Record<string, MonitorCaps>>({});
  const processes = ref<Record<string, ProcessSnapshot>>({});
  /** Por perfil: una ejecución a la vez (spec 22). `null` = sin ejecución activa. */
  const run = ref<Record<string, MonitorRun | null>>({});
  const lastSample = ref<Record<string, MonitorSampleEvent>>({});
  const notices = ref<Record<string, SourceNotice[]>>({});
  const starting = ref(false);
  const error = ref<AppError | null>(null);
  const loadingCaps = ref(false);

  // Lecturas históricas (las pinta T16.7); aquí solo se guardan tal como las devuelve Go.
  const top = ref<DigestStat[]>([]);
  const series = ref<Series[]>([]);
  const runList = ref<MonitorRun[]>([]);
  const comparison = ref<Comparison | null>(null);
  const digestDetail = ref<DigestDetail | null>(null);
  const loadingTop = ref(false);
  /** Criterio y comparación del ranking; `topLoaded` distingue «aún no se pidió» de «sin datos». */
  const topOrder = ref<OrderKey>("total_time");
  const topCompare = ref(true);
  const topLoaded = ref(false);
  const loadingSeries = ref(false);
  const seriesLoaded = ref(false);
  const loadingRuns = ref(false);
  const runsLoaded = ref(false);
  const loadingDigest = ref(false);

  // Vista en vivo: una sola a la vez, con arrendamiento que se renueva mientras está visible.
  const watching = ref<string | null>(null);
  let renewTimer: ReturnType<typeof setInterval> | null = null;

  const sessionId = computed(() => {
    const id = selectedSessionId.value;
    if (id && sessions.byId[id]) return id;
    return sessions.activeSessionId ?? sessions.list[0]?.sessionId ?? null;
  });
  const session = computed(() => (sessionId.value ? (sessions.byId[sessionId.value] ?? null) : null));
  const profileId = computed(() => session.value?.profileId ?? null);
  const currentCaps = computed(() => (sessionId.value ? (caps.value[sessionId.value] ?? null) : null));
  const currentRun = computed(() => (profileId.value ? (run.value[profileId.value] ?? null) : null));
  const currentSample = computed(() => (profileId.value ? (lastSample.value[profileId.value] ?? null) : null));
  const currentProcesses = computed(() => (sessionId.value ? (processes.value[sessionId.value] ?? null) : null));
  const currentNotices = computed(() => (profileId.value ? (notices.value[profileId.value] ?? []) : []));
  const running = computed(() => !!currentRun.value && currentRun.value.reason === "running");
  /** Hay algo que muestrear: alguna fuente utilizable (si no, Iniciar no tiene sentido). */
  const canStart = computed(() => {
    const c = currentCaps.value;
    return !!c && [c.processlist, c.digest, c.status].some((s) => s.state !== "unavailable");
  });

  const rangeWindow = (): TimeRange => {
    const to = new Date();
    return { from: new Date(to.getTime() - RANGE_MS[range.value]).toISOString(), to: to.toISOString() };
  };

  /** El periodo previo, de igual duración, justo antes del rango elegido (para comparar). */
  const previousWindow = (): TimeRange => {
    const cur = rangeWindow();
    const span = Date.parse(cur.to) - Date.parse(cur.from);
    return { from: new Date(Date.parse(cur.from) - span).toISOString(), to: cur.from };
  };

  function fail(e: unknown) {
    error.value = asAppError(e);
    return error.value;
  }

  /** Otra conexión, otros datos: lo histórico de la anterior no se arrastra. */
  function resetHistorical() {
    top.value = [];
    series.value = [];
    runList.value = [];
    comparison.value = null;
    digestDetail.value = null;
    topLoaded.value = seriesLoaded.value = runsLoaded.value = false;
  }

  function selectSession(id: string | null) {
    if (id === sessionId.value) return;
    void unwatch();
    selectedSessionId.value = id;
    schema.value = "";
    error.value = null;
    resetHistorical();
  }

  async function loadCaps(id = sessionId.value, refresh = false) {
    if (!id) return;
    loadingCaps.value = true;
    try {
      const c = await (refresh ? api.refreshMonitorCapabilities(id) : api.monitorCapabilities(id));
      caps.value = { ...caps.value, [id]: c };
    } catch (e) {
      fail(e);
    } finally {
      loadingCaps.value = false;
    }
  }

  async function start(cfg?: Partial<MonitorConfig>) {
    const id = sessionId.value;
    const pid = profileId.value;
    if (!id || !pid || starting.value) return null;
    starting.value = true;
    try {
      // IntervalSec 0 y fuentes sin marcar: Go aplica los valores por defecto de Ajustes y las fuentes disponibles.
      const r = await api.startMonitor(id, { intervalSec: 0, topK: 0, processes: false, status: false, digest: false, ...cfg });
      run.value = { ...run.value, [pid]: r };
      notices.value = { ...notices.value, [pid]: [] };
      error.value = null;
      return r;
    } catch (e) {
      fail(e);
      return null;
    } finally {
      starting.value = false;
    }
  }

  async function stop() {
    const pid = profileId.value;
    const r = currentRun.value;
    if (!pid || !r) return;
    try {
      await api.stopMonitor(r.id);
      run.value = { ...run.value, [pid]: null };
    } catch (e) {
      fail(e);
    }
  }

  // --- vista en vivo (arrendamiento) ---
  async function watch_(id: string) {
    try {
      await api.watchProcesses(id, LIVE_INTERVAL_MS);
      return true;
    } catch (e) {
      fail(e);
      stopRenewing();
      watching.value = null;
      return false;
    }
  }
  function stopRenewing() {
    if (renewTimer) clearInterval(renewTimer);
    renewTimer = null;
  }
  /** Empieza (o cambia de sesión) la vista en vivo y renueva el arrendamiento mientras siga visible. */
  async function watch(id = sessionId.value) {
    if (!id) return;
    if (watching.value && watching.value !== id) await unwatch();
    if (watching.value === id) return;
    watching.value = id;
    if (!(await watch_(id))) return;
    stopRenewing();
    renewTimer = setInterval(() => {
      const cur = watching.value;
      if (cur) void watch_(cur);
    }, LEASE_RENEW_MS);
  }
  /** Suelta el arrendamiento (vista oculta o sesión cerrada); Go detiene la vigilancia. */
  async function unwatch() {
    const id = watching.value;
    stopRenewing();
    watching.value = null;
    if (!id) return;
    try {
      await api.unwatchProcesses(id);
    } catch {
      // La sesión pudo cerrarse ya: Go detuvo la vigilancia por su cuenta.
    }
  }

  // Si la sesión vigilada se cierra, no hay nada que renovar.
  vueWatch(
    () => (watching.value ? sessions.byId[watching.value] : true),
    (s) => {
      if (!s) {
        stopRenewing();
        if (watching.value) delete processes.value[watching.value];
        watching.value = null;
      }
    },
  );

  async function copyKill(processId: number, queryOnly: boolean) {
    const id = sessionId.value;
    if (!id) return null;
    try {
      return await api.monitorCopyKill(id, processId, queryOnly);
    } catch (e) {
      useUiStore().toast("error", fail(e).message);
      return null;
    }
  }

  // --- lecturas históricas: esqueleto para T16.7 (Go calcula; aquí solo se guarda) ---
  async function loadTop(req: Partial<Parameters<typeof api.monitorTop>[0]> = {}) {
    const pid = profileId.value;
    if (!pid) return;
    loadingTop.value = true;
    try {
      const w = rangeWindow();
      top.value = (await api.monitorTop({ profileId: pid, from: w.from, to: w.to, schema: schema.value, orderBy: topOrder.value, limit: 50, compare: topCompare.value, includeSystem: false, ...req })) ?? [];
      topLoaded.value = true;
      error.value = null;
    } catch (e) {
      fail(e);
    } finally {
      loadingTop.value = false;
    }
  }
  /** Regresiones, nuevas y desaparecidas: el periodo elegido frente al previo de igual duración. Lo decide Go. */
  async function loadComparison() {
    const pid = profileId.value;
    if (!pid) return;
    try {
      comparison.value = await api.monitorCompare(pid, previousWindow(), rangeWindow());
    } catch (e) {
      fail(e);
    }
  }
  async function loadSeries(metrics: string[], maxPoints = 300) {
    const pid = profileId.value;
    if (!pid) return;
    loadingSeries.value = true;
    try {
      const w = rangeWindow();
      series.value = (await api.monitorSeries({ profileId: pid, from: w.from, to: w.to, metrics, maxPoints })) ?? [];
      seriesLoaded.value = true;
      error.value = null;
    } catch (e) {
      fail(e);
    } finally {
      loadingSeries.value = false;
    }
  }
  async function loadRuns() {
    const pid = profileId.value;
    if (!pid) return;
    loadingRuns.value = true;
    try {
      runList.value = (await api.monitorRuns(pid)) ?? [];
      runsLoaded.value = true;
    } catch (e) {
      fail(e);
    } finally {
      loadingRuns.value = false;
    }
  }
  async function compare(a: TimeRange, b: TimeRange) {
    const pid = profileId.value;
    if (!pid) return;
    try {
      comparison.value = await api.monitorCompare(pid, a, b);
    } catch (e) {
      fail(e);
    }
  }
  async function loadDigest(digest: string) {
    const pid = profileId.value;
    if (!pid) return;
    loadingDigest.value = true;
    digestDetail.value = null;
    try {
      const w = rangeWindow();
      digestDetail.value = await api.monitorDigest(pid, digest, w.from, w.to);
    } catch (e) {
      fail(e);
    } finally {
      loadingDigest.value = false;
    }
  }
  function closeDigest() {
    digestDetail.value = null;
  }
  async function annotate(text: string, at = new Date().toISOString()): Promise<Annotation | null> {
    const pid = profileId.value;
    if (!pid) return null;
    try {
      return await api.annotateMonitor(pid, at, text);
    } catch (e) {
      fail(e);
      return null;
    }
  }
  async function deleteAnnotation(id: string): Promise<boolean> {
    try {
      await api.deleteMonitorAnnotation(id);
      return true;
    } catch (e) {
      fail(e);
      return false;
    }
  }
  /** Borra los datos del perfil (o los anteriores a `before`). Devuelve si se pudo; Go rechaza con `busy` si hay una ejecución. */
  async function purge(before: string | null = null): Promise<boolean> {
    const pid = profileId.value;
    if (!pid) return false;
    try {
      await api.purgeMonitor(pid, before);
      error.value = null;
      return true;
    } catch (e) {
      fail(e);
      return false;
    }
  }

  // --- eventos de Go (se despachan desde src/events.ts) ---
  function handleProcesses(ev: MonitorProcessesEvent) {
    // Solo la sesión que se vigila: un evento tardío tras `unwatch` no repinta ni reabre nada.
    if (watching.value !== ev.sessionId) return;
    processes.value = { ...processes.value, [ev.sessionId]: ev.snapshot };
  }
  function handleSample(ev: MonitorSampleEvent) {
    const pid = Object.keys(run.value).find((p) => run.value[p]?.id === ev.runId);
    if (!pid) return;
    lastSample.value = { ...lastSample.value, [pid]: ev };
    const r = run.value[pid];
    if (r) run.value = { ...run.value, [pid]: { ...r, samples: r.samples + 1 } };
  }
  function handleState(ev: MonitorStateEvent) {
    const pid = ev.profileId;
    if (ev.state === "degraded") {
      const list = notices.value[pid] ?? [];
      const src = ev.source ?? "";
      if (!list.some((n) => n.source === src && n.reason === (ev.reason ?? ""))) notices.value = { ...notices.value, [pid]: [...list, { source: src, reason: ev.reason ?? "" }] };
      return;
    }
    if (ev.state === "running") {
      // «running» con motivo = una fuente se recuperó; sin motivo = la ejecución arrancó (la tenemos de Start o de otra ventana).
      if (ev.reason) notices.value = { ...notices.value, [pid]: (notices.value[pid] ?? []).filter((n) => n.source !== ev.source) };
      return;
    }
    // stopped | disconnected: la ejecución terminó (también cuando Go la corta por cambio de servidor).
    const r = run.value[pid];
    if (r && r.id === ev.runId) run.value = { ...run.value, [pid]: null };
    if (ev.state === "disconnected") delete lastSample.value[pid];
    if (ev.state === "stopped" && ev.source === "server") useUiStore().toast("error", t("monitor.toast.serverChanged"));
    else if (ev.state === "disconnected") useUiStore().toast("info", t("monitor.toast.disconnected"));
  }

  return {
    tab, selectedSessionId, schema, range, caps, processes, run, lastSample, notices, starting, error, loadingCaps,
    top, series, runList, comparison, digestDetail, loadingTop, watching,
    topOrder, topCompare, topLoaded, loadingSeries, seriesLoaded, loadingRuns, runsLoaded, loadingDigest,
    sessionId, session, profileId, currentCaps, currentRun, currentSample, currentProcesses, currentNotices, running, canStart,
    rangeWindow, previousWindow, selectSession, loadCaps, start, stop, watch, unwatch, copyKill,
    loadTop, loadComparison, loadSeries, loadRuns, compare, loadDigest, closeDigest, annotate, deleteAnnotation, purge,
    handleProcesses, handleSample, handleState,
  };
});
