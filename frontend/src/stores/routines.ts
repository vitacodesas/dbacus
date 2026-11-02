import { computed, ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/routines";
import { asAppError } from "../api/errors";
import { i18n } from "../i18n";
import { useUiStore } from "./ui";
import type { RoutineChange, RoutineDetail, RoutinePreview, RoutineResult } from "../types";

const t = (k: string, p?: Record<string, unknown>) => i18n.global.t(k, p ?? {});

export type RoutineKind = "procedure" | "function";

/** Qué rutina muestra el modal; `name === null` es una rutina nueva. */
export interface RoutineTarget {
  sessionId: string;
  db: string;
  kind: RoutineKind;
  name: string | null;
}

/** Diálogo de revisión (siempre, sin ajuste para saltarlo: spec 21, D5) y resultado del intento. */
export interface RoutineReview {
  sessionId: string;
  change: RoutineChange;
  preview: RoutinePreview;
  /** Código actual de la rutina a borrar (solo `drop`): se enseña para poder copiarlo antes de confirmar. */
  body: string;
  applying: boolean;
  error: string | null;
  /** Presente cuando el intento no terminó bien: qué se aplicó, si se restauró la original y la copia. */
  failed: RoutineResult | null;
}

/**
 * Ver, editar, crear y borrar rutinas (spec 21). El texto del editor es estado de UI efímero; la verdad es el servidor:
 * `Preview` genera el SQL en Go y solo `Apply` con el hash de esa vista previa ejecuta algo.
 */
export const useRoutinesStore = defineStore("routines", () => {
  const target = ref<RoutineTarget | null>(null);
  const detail = ref<RoutineDetail | null>(null);
  const mode = ref<"view" | "edit" | "create">("view");
  const text = ref("");
  /** Texto de partida (el CREATE leído o la plantilla) para saber si hay cambios. */
  const base = ref("");
  const loading = ref(false);
  const loadError = ref<string | null>(null);
  const actionError = ref<string | null>(null);
  const checking = ref(false);
  const review = ref<RoutineReview | null>(null);
  const recreateDefiner = ref(false);

  const dirty = computed(() => mode.value !== "view" && text.value !== base.value);
  const editing = computed(() => mode.value !== "view");
  let seq = 0;

  function reset() {
    seq++;
    target.value = null;
    detail.value = null;
    mode.value = "view";
    text.value = "";
    base.value = "";
    loading.value = false;
    loadError.value = null;
    actionError.value = null;
    checking.value = false;
    review.value = null;
    recreateDefiner.value = false;
  }

  function show(d: RoutineDetail) {
    detail.value = d;
    base.value = d.createSql ?? "";
    text.value = base.value;
    mode.value = "view";
  }

  /** Abre una rutina existente (solo lectura hasta pulsar «Editar») o, con `name === null`, una nueva con la plantilla de Go. */
  async function open(tg: RoutineTarget) {
    reset();
    target.value = tg;
    const mine = ++seq;
    loading.value = true;
    try {
      if (tg.name === null) {
        base.value = await api.routineTemplate(tg.kind);
        if (mine !== seq) return;
        text.value = base.value;
        mode.value = "create";
      } else {
        const d = await api.getRoutine(tg.sessionId, tg.db, tg.kind, tg.name);
        if (mine !== seq) return;
        show(d);
      }
    } catch (e) {
      if (mine === seq) loadError.value = asAppError(e).message;
    } finally {
      if (mine === seq) loading.value = false;
    }
  }

  function startEdit() {
    if (detail.value && !detail.value.bodyHidden) mode.value = "edit";
  }

  function discard() {
    text.value = base.value;
    actionError.value = null;
    if (mode.value === "edit") mode.value = "view";
  }

  function changeFor(): RoutineChange | null {
    const tg = target.value;
    if (!tg) return null;
    return {
      database: tg.db, kind: tg.kind, name: tg.name ?? "", op: mode.value === "create" ? "create" : "replace",
      sql: text.value, fingerprint: detail.value?.fingerprint ?? "", recreateDefiner: recreateDefiner.value,
    };
  }

  /** Pide a Go el SQL y abre la revisión. Un error de validación (nombre cambiado, DELIMITER…) se queda en el modal. */
  async function requestReview() {
    const tg = target.value;
    const change = changeFor();
    if (!tg || !change || checking.value) return;
    actionError.value = null;
    checking.value = true;
    try {
      const preview = await api.previewRoutine(tg.sessionId, change);
      review.value = { sessionId: tg.sessionId, change, preview, body: "", applying: false, error: null, failed: null };
    } catch (e) {
      actionError.value = asAppError(e).message;
    } finally {
      checking.value = false;
    }
  }

  /** Borrar: lee el código actual (para enseñarlo) y pide la vista previa del DROP. */
  async function requestDelete(tg: { sessionId: string; db: string; kind: RoutineKind; name: string }) {
    const ui = useUiStore();
    try {
      const d = await api.getRoutine(tg.sessionId, tg.db, tg.kind, tg.name);
      const change: RoutineChange = { database: tg.db, kind: tg.kind, name: tg.name, op: "drop", sql: "", fingerprint: d.fingerprint, recreateDefiner: false };
      const preview = await api.previewRoutine(tg.sessionId, change);
      review.value = { sessionId: tg.sessionId, change, preview, body: d.createSql ?? "", applying: false, error: null, failed: null };
    } catch (e) {
      ui.toast("error", asAppError(e).message);
    }
  }

  /** Cambiar «Recrear con mi usuario» obliga a recalcular el SQL (cambia el hash). */
  async function setRecreateDefiner(v: boolean) {
    recreateDefiner.value = v;
    const r = review.value;
    if (!r) return;
    const change = { ...r.change, recreateDefiner: v };
    try {
      review.value = { ...r, change, preview: await api.previewRoutine(r.sessionId, change), error: null };
    } catch (e) {
      review.value = { ...r, error: asAppError(e).message };
    }
  }

  function closeReview() {
    review.value = null;
  }

  async function apply() {
    const r = review.value;
    if (!r || r.applying) return;
    const ui = useUiStore();
    review.value = { ...r, applying: true, error: null };
    try {
      const res = await api.applyRoutine(r.sessionId, r.change, r.preview.sqlHash);
      if (res.failed) {
        // La base falló a mitad: se enseña qué pasó y el modal queda con el estado real (spec 21).
        review.value = { ...r, applying: false, failed: res };
        if (res.detail && target.value) show(res.detail);
        return;
      }
      review.value = null;
      if (r.change.op === "drop") {
        ui.toast("success", t("routines.deleted"));
        // Si el modal mostraba justo esa rutina, ya no existe.
        if (target.value?.name === r.change.name && target.value.db === r.change.database) reset();
        return;
      }
      const tg = target.value;
      if (res.detail && tg) {
        target.value = { ...tg, name: res.detail.name };
        show(res.detail);
      } else mode.value = "view";
      ui.toast("success", t(r.change.op === "create" ? "routines.created" : "routines.saved"));
    } catch (e) {
      review.value = { ...r, applying: false, error: asAppError(e).message };
    }
  }

  return {
    target, detail, mode, text, base, loading, loadError, actionError, checking, review, recreateDefiner, dirty, editing,
    reset, open, startEdit, discard, requestReview, requestDelete, setRecreateDefiner, closeReview, apply,
  };
});
