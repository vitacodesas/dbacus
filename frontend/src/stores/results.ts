import { ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/queries";
import { asAppError, ErrorCode } from "../api/errors";
import { omit } from "../utils/record";
import type { AppError, Cell, QueryDone, QueryError, QueryProgress, QueryStarted, QueryStatement, StatementResult } from "../types";

export type RunStatus = "idle" | "running" | "done" | "error" | "canceled";

export interface TabResult {
  queryId: string | null;
  sessionId: string;
  status: RunStatus;
  startedAt: number;
  elapsedMs: number;
  statements: StatementResult[];
  /** Filas recibidas por sentencia (en vivo mientras corre). */
  rowCounts: Record<number, number>;
  activeStmt: number;
  error: AppError | null;
  errorStmt: number;
  /** Posición del error en el SQL enviado (UTF-16), relativa al texto enviado; sumar baseOffset para el editor. */
  errorRange: { start: number; end: number } | null;
  /** Dónde empieza en el editor el texto que se envió (0 si fue todo el contenido). */
  baseOffset: number;
  database: string;
  /** El resultado ya no está en memoria en Go (RESULT_EXPIRED). */
  expired: boolean;
}

/** Tamaño de ventana de filas que se pide a Go y cuántas ventanas se conservan por pestaña (spec 06). */
export const WINDOW = 200;
export const MAX_CACHED_WINDOWS = 5;
/** Tope de filas que se traen de una vez para copiar/exportar desde la UI. */
export const MAX_COPY_ROWS = 100_000;

interface CachedWindow {
  rows: Cell[][];
  /** Ventana incompleta mientras la consulta sigue leyendo: se vuelve a pedir cuando llegan más filas. */
  partial: boolean;
  used: number;
}

const fresh = (over: Partial<TabResult> = {}): TabResult => ({
  queryId: null, sessionId: "", status: "idle", startedAt: 0, elapsedMs: 0, statements: [], rowCounts: {}, activeStmt: 0,
  error: null, errorStmt: -1, errorRange: null, baseOffset: 0, database: "", expired: false, ...over,
});

/**
 * Estado de los resultados por pestaña. Las filas NO viven en estado reactivo profundo (ADR-002, spec 09): se piden por
 * ventanas a Go y se guardan en una caché pequeña fuera de la reactividad; `version` avisa al grid de que cambió.
 */
export const useResultsStore = defineStore("results", () => {
  const byTab = ref<Record<string, TabResult>>({});
  const version = ref(0);
  const queryToTab = new Map<string, string>();
  // tabId → ("queryId:stmt:ventana" → ventana)
  const windows = new Map<string, Map<string, CachedWindow>>();
  const inFlight = new Set<string>();
  let tick = 0;

  const of = (tabId: string): TabResult => byTab.value[tabId] ?? fresh();
  const tabOfQuery = (queryId: string) => queryToTab.get(queryId);
  const bump = () => void version.value++;

  function dropWindows(tabId: string) {
    windows.delete(tabId);
    for (const k of [...inFlight]) if (k.startsWith(`${tabId}\u0001`)) inFlight.delete(k);
    bump();
  }

  function releaseCurrent(tabId: string) {
    const prev = byTab.value[tabId];
    if (prev?.queryId) queryToTab.delete(prev.queryId);
    dropWindows(tabId);
  }

  /** Lanza una ejecución. Lo que llegue por eventos se asocia a la pestaña por `tabId`/`queryId`. */
  async function execute(p: { tabId: string; sessionId: string; sql: string; baseOffset: number; database: string; autoLimit: number }) {
    releaseCurrent(p.tabId);
    byTab.value = { ...byTab.value, [p.tabId]: fresh({ status: "running", startedAt: Date.now(), sessionId: p.sessionId, baseOffset: p.baseOffset, database: p.database }) };
    try {
      const queryId = await api.execute({ sessionId: p.sessionId, tabId: p.tabId, sql: p.sql, database: p.database, maxRows: 0, autoLimit: p.autoLimit });
      // Los eventos pueden llegar antes que esta respuesta: query:started ya habrá fijado el queryId.
      const r = byTab.value[p.tabId];
      if (r && !r.queryId) r.queryId = queryId;
      queryToTab.set(queryId, p.tabId);
    } catch (e) {
      const err = asAppError(e);
      byTab.value[p.tabId] = fresh({ ...byTab.value[p.tabId], status: "error", error: err, errorStmt: -1, elapsedMs: Date.now() - byTab.value[p.tabId].startedAt });
    }
  }

  async function cancel(tabId: string) {
    const r = byTab.value[tabId];
    if (r?.status === "running" && r.queryId) await api.cancel(r.queryId);
  }

  const running = (tabId: string) => byTab.value[tabId]?.status === "running";

  // ---- eventos de Go -------------------------------------------------------------------------------------------------

  function handleStarted(ev: QueryStarted) {
    const r = byTab.value[ev.tabId];
    if (!r || r.status !== "running") return;
    r.queryId = ev.queryId;
    queryToTab.set(ev.queryId, ev.tabId);
  }

  function resultFor(queryId: string): TabResult | undefined {
    const tabId = queryToTab.get(queryId);
    const r = tabId ? byTab.value[tabId] : undefined;
    return r && r.queryId === queryId ? r : undefined;
  }

  function handleStatement(ev: QueryStatement) {
    const r = resultFor(ev.queryId);
    if (!r) return;
    const s = ev.result;
    r.statements[s.index] = s;
    if (s.kind === "rows") {
      r.rowCounts[s.index] = s.rowCount;
      // Al primer resultado con filas se le da el foco; las siguientes sentencias no lo roban.
      if (r.statements.slice(0, s.index).every((x) => !x || x.kind !== "rows")) r.activeStmt = s.index;
    }
  }

  function handleProgress(ev: QueryProgress) {
    const r = resultFor(ev.queryId);
    if (!r) return;
    r.rowCounts[ev.stmtIndex] = ev.rowCount;
    r.elapsedMs = Date.now() - r.startedAt;
    refreshPartial(queryToTab.get(ev.queryId)!);
  }

  function handleDone(ev: QueryDone) {
    const r = resultFor(ev.queryId);
    if (!r) return;
    r.status = "done";
    const done = ev.results ?? [];
    r.statements = done;
    done.forEach((s) => s.kind === "rows" && (r.rowCounts[s.index] = s.rowCount));
    r.elapsedMs = ev.elapsedMs;
    r.database = ev.database;
    refreshPartial(queryToTab.get(ev.queryId)!);
  }

  function handleError(ev: QueryError) {
    const r = resultFor(ev.queryId);
    if (!r) return;
    const canceled = ev.error?.code === ErrorCode.QueryCanceled;
    r.status = canceled ? "canceled" : "error";
    r.error = ev.error;
    r.errorStmt = ev.stmtIndex;
    r.errorRange = ev.stmtIndex >= 0 ? { start: ev.start, end: ev.end } : null;
    r.elapsedMs = Date.now() - r.startedAt;
    refreshPartial(queryToTab.get(ev.queryId)!);
  }

  // ---- ventanas de filas ---------------------------------------------------------------------------------------------

  /** Descarta ventanas incompletas (llegaron más filas desde que se pidieron) para que el grid las vuelva a pedir. */
  function refreshPartial(tabId: string) {
    const cache = windows.get(tabId);
    if (!cache) return;
    let changed = false;
    for (const [k, w] of cache) {
      if (w.partial) {
        cache.delete(k);
        changed = true;
      }
    }
    if (changed) bump();
  }

  /**
   * Fila `index` del resultado `stmt`. Devuelve undefined si su ventana aún no está en memoria (y la pide, junto con
   * la siguiente: lo normal es seguir bajando). Lee `version` para que el componente se vuelva a pintar al llegar.
   */
  function rowAt(tabId: string, stmt: number, index: number): Cell[] | undefined {
    void version.value;
    const r = byTab.value[tabId];
    if (!r?.queryId || index < 0) return undefined;
    const win = Math.floor(index / WINDOW);
    const w = windows.get(tabId)?.get(`${r.queryId}:${stmt}:${win}`);
    if (w) {
      w.used = ++tick;
      return w.rows[index - win * WINDOW];
    }
    void ensureWindow(tabId, stmt, win);
    if ((index % WINDOW) / WINDOW > 0.6) void ensureWindow(tabId, stmt, win + 1);
    return undefined;
  }

  async function ensureWindow(tabId: string, stmt: number, win: number) {
    const r = byTab.value[tabId];
    if (!r?.queryId || r.expired) return;
    const queryId = r.queryId;
    const key = `${queryId}:${stmt}:${win}`;
    const flight = `${tabId}\u0001${key}`;
    const total = r.rowCounts[stmt] ?? 0;
    if (win < 0 || win * WINDOW >= Math.max(total, 1) && total > 0 || inFlight.has(flight) || windows.get(tabId)?.has(key)) return;
    inFlight.add(flight);
    try {
      const res = await api.getRows(queryId, stmt, win * WINDOW, WINDOW);
      // Si mientras tanto se lanzó otra ejecución en la pestaña, esta respuesta ya no sirve.
      if (byTab.value[tabId]?.queryId !== queryId) return;
      let cache = windows.get(tabId);
      if (!cache) windows.set(tabId, (cache = new Map()));
      const rows = (res.rows ?? []).map((row) => row ?? []);
      cache.set(key, { rows, partial: !res.complete && rows.length < WINDOW, used: ++tick });
      evict(cache);
      bump();
    } catch (e) {
      if (asAppError(e).code === ErrorCode.ResultExpired && byTab.value[tabId]?.queryId === queryId) {
        byTab.value[tabId].expired = true;
        dropWindows(tabId);
      }
    } finally {
      inFlight.delete(flight);
    }
  }

  function evict(cache: Map<string, CachedWindow>) {
    while (cache.size > MAX_CACHED_WINDOWS) {
      let oldest: string | null = null;
      let min = Infinity;
      for (const [k, w] of cache) if (w.used < min) [oldest, min] = [k, w.used];
      if (oldest) cache.delete(oldest);
    }
  }

  /** Trae filas [from, to) directamente (sin la caché del grid) para copiar; en bloques de 1000, máx. MAX_COPY_ROWS. */
  async function fetchRange(tabId: string, stmt: number, from: number, to: number): Promise<Cell[][]> {
    const r = byTab.value[tabId];
    if (!r?.queryId) return [];
    const end = Math.min(to, from + MAX_COPY_ROWS);
    const out: Cell[][] = [];
    for (let off = from; off < end; off += 1000) {
      const res = await api.getRows(r.queryId, stmt, off, Math.min(1000, end - off));
      const rows = (res.rows ?? []).map((row) => row ?? []);
      out.push(...rows);
      if (rows.length === 0) break;
    }
    return out;
  }

  function setActiveStatement(tabId: string, index: number) {
    if (byTab.value[tabId]) byTab.value[tabId].activeStmt = index;
  }

  /** Olvida lo de una pestaña cerrada. Go libera la memoria con CloseTab. */
  function forgetTab(tabId: string) {
    releaseCurrent(tabId);
    byTab.value = omit(byTab.value, tabId);
  }

  return {
    byTab, version, of, tabOfQuery, running, execute, cancel,
    handleStarted, handleStatement, handleProgress, handleDone, handleError,
    rowAt, ensureWindow, fetchRange, setActiveStatement, forgetTab,
  };
});
