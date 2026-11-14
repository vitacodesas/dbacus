import { computed, ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/migration";
import { asAppError } from "../api/errors";
import { i18n } from "../i18n";
import type { MigrateDoneEvent, MigrateProgressEvent, MigrateStateEvent, MigrationJob, MigrationPlan, MigrationRequest, MigrationResult, MigrationVerification } from "../types";
import { useUiStore } from "./ui";

const t = (k: string, p?: Record<string, unknown>) => i18n.global.t(k, p ?? {}) as string;

export type MigrationStep = "target" | "content" | "plan" | "run";

/** Lo que el asistente muestra de un error de Go (el mensaje y el detalle que lo distingue). */
export interface MigrationError {
  message: string;
  detail: string;
}

/** Los estados en los que el job aún se mueve. */
const ACTIVE = new Set(["planning", "running", "cancelling"]);

/**
 * Asistente de migración de schema (spec 23). Go inspecciona, planifica, copia y cuenta; aquí solo se elige, se pinta y se
 * reciben los eventos `migrate:*`. El store no agrega ni ordena datos: lo único calculado es lo que se ve (porcentaje, velocidad).
 */
export const useMigrationStore = defineStore("migration", () => {
  const isOpen = ref(false);
  const step = ref<MigrationStep>("target");
  const sourceSessionId = ref("");
  const sourceDb = ref("");
  const targetSessionId = ref("");
  const targetDb = ref("");
  const skipExisting = ref(false);
  const workers = ref(0);
  const chunkRows = ref(0);
  const verifyForeignKeys = ref(false);
  const verifyRowCounts = ref(false);

  /** Lo que hay en el origen (primer plan, con todo marcado) para elegir qué migrar. */
  const catalog = ref<MigrationPlan | null>(null);
  const pickedTables = ref<string[]>([]);
  /** Tablas de `pickedTables` que se crean vacías (sin copiar sus filas, spec 23 ⚑D20). */
  const structureOnly = ref<string[]>([]);
  const pickedViews = ref<string[]>([]);
  const pickedRoutines = ref<string[]>([]); // «kind:name»
  const pickedTriggers = ref<string[]>([]);

  const plan = ref<MigrationPlan | null>(null);
  const busy = ref(false);
  const error = ref<MigrationError | null>(null);
  const job = ref<MigrationJob | null>(null);
  const result = ref<MigrationResult | null>(null);
  const verifying = ref(false);
  const cleaning = ref(false);
  /** Se cerró el asistente con la copia en curso: al reabrir se vuelve a ella (o a su resultado) en vez de empezar otra. */
  const resumable = ref(false);

  // Un job pequeño puede terminar antes de que Start devuelva su id: los eventos se guardan hasta entonces.
  const early = new Map<string, { progress?: MigrateProgressEvent; state?: MigrateStateEvent; done?: MigrateDoneEvent }>();

  const running = computed(() => !!job.value && ACTIVE.has(job.value.state));
  const finished = computed(() => !!job.value && !ACTIVE.has(job.value.state));

  function failure(e: unknown): MigrationError {
    const a = asAppError(e);
    return { message: a.message, detail: a.detail ?? "" };
  }

  function reset() {
    resumable.value = false;
    step.value = "target";
    plan.value = catalog.value = null;
    pickedTables.value = pickedViews.value = pickedRoutines.value = pickedTriggers.value = structureOnly.value = [];
    error.value = null;
    job.value = result.value = null;
    busy.value = verifying.value = cleaning.value = false;
    skipExisting.value = false;
    workers.value = chunkRows.value = 0;
    verifyForeignKeys.value = verifyRowCounts.value = false;
  }

  /** Abre el asistente sobre una base. Si ya hay una migración en curso, vuelve a ella en vez de empezar otra. */
  function openFor(sessionId: string, db: string) {
    if (running.value || (resumable.value && job.value)) {
      isOpen.value = true;
      step.value = "run";
      return;
    }
    reset();
    sourceSessionId.value = sessionId;
    sourceDb.value = db;
    targetSessionId.value = sessionId;
    targetDb.value = `${db}_copia`;
    isOpen.value = true;
  }

  function close() {
    isOpen.value = false; // un job en curso sigue: al terminar avisa con un aviso y se puede volver a él
    if (running.value) resumable.value = true;
    else if (step.value === "run") {
      resumable.value = false;
      reset();
    }
  }

  const request = (full: boolean): MigrationRequest => {
    const all = !full && !!catalog.value;
    const c = catalog.value;
    const same = <T extends string>(picked: T[], universe: T[]) => picked.length === universe.length;
    const routineKeys = (c?.routines ?? []).filter((x) => !x.skipped).map((r) => `${r.kind}:${r.name}`);
    const empty = structureOnly.value.filter((n) => pickedTables.value.includes(n));
    const useAll =
      !all ||
      (empty.length === 0 &&
        same(pickedTables.value, (c?.tables ?? []).filter((x) => !x.skipped).map((x) => x.name)) &&
        same(pickedViews.value, (c?.views ?? []).filter((x) => !x.skipped).map((x) => x.name)) &&
        same(pickedRoutines.value, routineKeys) &&
        same(pickedTriggers.value, (c?.triggers ?? []).filter((x) => !x.skipped).map((x) => x.name)));
    return {
      source: { sessionId: sourceSessionId.value, database: sourceDb.value },
      target: { sessionId: targetSessionId.value, database: targetDb.value.trim() },
      objects: useAll
        ? { tables: null, structureOnly: [], views: null, routines: null, triggers: null }
        : {
            tables: pickedTables.value,
            structureOnly: empty,
            views: pickedViews.value,
            routines: pickedRoutines.value.map((k) => ({ kind: k.slice(0, k.indexOf(":")), name: k.slice(k.indexOf(":") + 1) })),
            triggers: pickedTriggers.value,
          },
      options: { workers: workers.value, chunkRows: chunkRows.value, skipExisting: skipExisting.value, verifyForeignKeys: verifyForeignKeys.value, verifyRowCounts: verifyRowCounts.value },
    };
  };

  /** Paso 1 → 2: un primer plan con todo para saber qué hay en el origen. */
  async function loadCatalog() {
    busy.value = true;
    error.value = null;
    try {
      const p = await api.planMigration(request(true));
      catalog.value = p;
      pickedTables.value = (p.tables ?? []).filter((x) => !x.skipped).map((x) => x.name);
      structureOnly.value = [];
      pickedViews.value = (p.views ?? []).filter((x) => !x.skipped).map((x) => x.name);
      pickedRoutines.value = (p.routines ?? []).filter((x) => !x.skipped).map((x) => `${x.kind}:${x.name}`);
      pickedTriggers.value = (p.triggers ?? []).filter((x) => !x.skipped).map((x) => x.name);
      step.value = "content";
    } catch (e) {
      error.value = failure(e);
    } finally {
      busy.value = false;
    }
  }

  /** Paso 2 → 3: el plan con lo elegido. */
  async function loadPlan() {
    busy.value = true;
    error.value = null;
    try {
      plan.value = await api.planMigration(request(false));
      step.value = "plan";
    } catch (e) {
      error.value = failure(e);
    } finally {
      busy.value = false;
    }
  }

  async function start() {
    if (!plan.value) return;
    busy.value = true;
    error.value = null;
    try {
      const j = await api.startMigration(request(false), plan.value.planHash);
      job.value = j;
      result.value = null;
      step.value = "run";
      const e = early.get(j.id);
      early.delete(j.id);
      if (e?.progress) handleProgress(e.progress);
      if (e?.state) handleState(e.state);
      if (e?.done) await handleDone(e.done);
    } catch (e) {
      error.value = failure(e);
    } finally {
      busy.value = false;
    }
  }

  async function cancel() {
    if (!job.value) return;
    try {
      await api.cancelMigration(job.value.id);
    } catch (e) {
      error.value = failure(e);
    }
  }

  async function retry() {
    if (!job.value) return;
    error.value = null;
    try {
      job.value = await api.retryMigration(job.value.id);
      result.value = null;
    } catch (e) {
      error.value = failure(e);
    }
  }

  async function verify() {
    if (!job.value) return;
    verifying.value = true;
    error.value = null;
    try {
      const v: MigrationVerification = await api.verifyMigration(job.value.id, { foreignKeys: true, rowCounts: true, objects: true });
      result.value = { ...(result.value ?? ({ job: job.value, failed: [], created: [], warnings: [], verification: null } as MigrationResult)), verification: v };
    } catch (e) {
      error.value = failure(e);
    } finally {
      verifying.value = false;
    }
  }

  async function cleanup(confirmName: string): Promise<boolean> {
    if (!job.value) return false;
    cleaning.value = true;
    error.value = null;
    try {
      await api.cleanupMigration(job.value.id, confirmName);
      useUiStore().toast("success", t("migration.cleaned"));
      result.value = result.value ? { ...result.value, created: [] } : result.value;
      return true;
    } catch (e) {
      error.value = failure(e);
      return false;
    } finally {
      cleaning.value = false;
    }
  }

  function back() {
    error.value = null;
    if (step.value === "plan") step.value = "content";
    else if (step.value === "content") step.value = "target";
  }

  // ---- eventos de Go ----
  const mine = (id: string) => job.value?.id === id;
  const buffer = (id: string) => {
    let e = early.get(id);
    if (!e) early.set(id, (e = {}));
    return e;
  };

  function handleProgress(ev: MigrateProgressEvent) {
    if (!mine(ev.jobId)) {
      buffer(ev.jobId).progress = ev;
      return;
    }
    const j = job.value!;
    const prev = new Map((j.tables ?? []).map((x) => [x.name, x]));
    job.value = {
      ...j, phase: ev.phase, rows: ev.rows, estRows: ev.estRows, bytes: ev.bytes, elapsedMs: ev.elapsedMs, tablesDone: ev.tablesDone, tablesAll: ev.tablesTotal,
      tables: (ev.perTable ?? []).map((p) => ({ name: p.name, state: p.state, structureOnly: prev.get(p.name)?.structureOnly ?? false, rowsDone: p.rowsDone, rowsEst: p.rowsEst, chunksDone: p.chunksDone, chunksAll: p.chunksAll, error: prev.get(p.name)?.error })),
    };
  }

  function handleState(ev: MigrateStateEvent) {
    if (!mine(ev.jobId)) {
      buffer(ev.jobId).state = ev;
      return;
    }
    job.value = { ...job.value!, state: ev.state, phase: ev.phase };
  }

  async function handleDone(ev: MigrateDoneEvent) {
    if (!mine(ev.jobId)) {
      buffer(ev.jobId).done = ev;
      return;
    }
    try {
      result.value = await api.migrationJob(ev.jobId);
      job.value = result.value.job;
    } catch {
      job.value = { ...job.value!, state: ev.state };
    }
    const ui = useUiStore();
    if (ev.state === "done") ui.toast("success", t("migration.done", { target: targetDb.value }));
    else if (ev.state === "canceled") ui.toast("info", t("migration.canceledToast"));
    else ui.toast("error", t("migration.failedToast", { n: ev.failed }));
  }

  return {
    isOpen, step, sourceSessionId, sourceDb, targetSessionId, targetDb, skipExisting, workers, chunkRows, verifyForeignKeys, verifyRowCounts,
    catalog, pickedTables, structureOnly, pickedViews, pickedRoutines, pickedTriggers, plan, busy, error, job, result, verifying, cleaning, running, finished,
    openFor, close, reset, request, loadCatalog, loadPlan, start, cancel, retry, verify, cleanup, back, handleProgress, handleState, handleDone,
  };
});
