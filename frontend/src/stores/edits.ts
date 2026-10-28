import { computed, ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/edits";
import { asAppError } from "../api/errors";
import { i18n } from "../i18n";
import { useResultsStore } from "./results";
import { useUiStore } from "./ui";
import type { AppError, EditApplied, EditConflict, EditInfo, EditPreview, EditValue } from "../types";

const t = (k: string, p?: Record<string, unknown>) => i18n.global.t(k, p ?? {});
const keyOf = (queryId: string, stmt: number) => `${queryId}:${stmt}`;

/** Diálogo de revisión del SQL antes de aplicar (y de resultado del intento). */
export interface ReviewState {
  tabId: string;
  queryId: string;
  stmt: number;
  statements: EditPreview[];
  applying: boolean;
  conflict: EditConflict | null;
  error: string | null;
}

/**
 * Proyección en cliente de la edición de celdas (spec 13). Los valores pendientes, la validación y el SQL viven en Go: aquí
 * solo está el veredicto por resultado (`EditInfo`, con el número de cambios pendientes) y el estado de los diálogos.
 */
export const useEditsStore = defineStore("edits", () => {
  const infos = ref<Record<string, EditInfo>>({});
  const review = ref<ReviewState | null>(null);
  const discardPrompt = ref<{ tabId: string; count: number; resolve: (ok: boolean) => void } | null>(null);

  /** Petición al grid de seleccionar una fila (y abrir su editor) tras crearla; el grid la consume (spec 14). */
  const focusRequest = ref<{ queryId: string; stmt: number; row: number } | null>(null);

  const infoOf = (queryId: string, stmt: number): EditInfo | null => infos.value[keyOf(queryId, stmt)] ?? null;
  const isEditable = (queryId: string, stmt: number, col: number) => {
    const i = infoOf(queryId, stmt);
    return !!i?.editable && !!i.columns?.[col]?.editable;
  };

  /** Cambios pendientes de la ejecución actual de una pestaña (todas sus sentencias). */
  function pendingFor(tabId: string): number {
    const q = useResultsStore().of(tabId).queryId;
    if (!q) return 0;
    return Object.entries(infos.value).reduce((n, [k, i]) => (k.startsWith(`${q}:`) ? n + i.pending : n), 0);
  }

  async function load(queryId: string, stmt: number) {
    try {
      infos.value = { ...infos.value, [keyOf(queryId, stmt)]: await api.getEditInfo(queryId, stmt) };
    } catch {
      // Sin veredicto (resultado liberado, sesión cerrada…): el grid simplemente no ofrece edición.
      const rest = { ...infos.value };
      delete rest[keyOf(queryId, stmt)];
      infos.value = rest;
    }
  }

  function setInfo(queryId: string, stmt: number, info: EditInfo) {
    infos.value = { ...infos.value, [keyOf(queryId, stmt)]: info };
  }

  /** Deja un cambio pendiente. Devuelve el error de validación de Go (para mostrarlo en la celda) o null. */
  async function stage(tabId: string, queryId: string, stmt: number, row: number, col: number, value: EditValue): Promise<AppError | null> {
    try {
      setInfo(queryId, stmt, await api.stageEdit(queryId, stmt, row, col, value));
      await useResultsStore().reloadCached(tabId);
      return null;
    } catch (e) {
      return asAppError(e);
    }
  }

  async function unstage(tabId: string, queryId: string, stmt: number, row: number, col: number) {
    try {
      setInfo(queryId, stmt, await api.unstageEdit(queryId, stmt, row, col));
      await useResultsStore().reloadCached(tabId);
    } catch (e) {
      useUiStore().toast("error", asAppError(e).message);
    }
  }

  async function discard(tabId: string, queryId: string, stmt: number) {
    try {
      setInfo(queryId, stmt, await api.discardEdits(queryId, stmt));
      await useResultsStore().reloadCached(tabId);
    } catch (e) {
      useUiStore().toast("error", asAppError(e).message);
    }
  }

  // ---- filas nuevas y borrados (spec 14) -------------------------------------------------------------------------------

  /** Añade una fila nueva vacía al final (o copias de las filas indicadas) y pide al grid que se coloque en la primera. */
  async function addRow(tabId: string, queryId: string, stmt: number, copyOf: number[] = []): Promise<AppError | null> {
    try {
      const res = copyOf.length ? await api.duplicateEditRows(queryId, stmt, copyOf) : await api.addEditRow(queryId, stmt);
      setInfo(queryId, stmt, res.info);
      await useResultsStore().reloadCached(tabId);
      if (res.rows?.length) focusRequest.value = { queryId, stmt, row: res.rows[0] };
      return null;
    } catch (e) {
      return asAppError(e);
    }
  }

  /** Marca filas para borrar (en una fila nueva, la descarta). */
  async function markDelete(tabId: string, queryId: string, stmt: number, rows: number[]): Promise<AppError | null> {
    try {
      setInfo(queryId, stmt, await api.markDeleteRows(queryId, stmt, rows));
      await useResultsStore().reloadCached(tabId);
      return null;
    } catch (e) {
      return asAppError(e);
    }
  }

  async function unmarkDelete(tabId: string, queryId: string, stmt: number, rows: number[]) {
    try {
      setInfo(queryId, stmt, await api.unmarkDeleteRows(queryId, stmt, rows));
      await useResultsStore().reloadCached(tabId);
    } catch (e) {
      useUiStore().toast("error", asAppError(e).message);
    }
  }

  /** «Aplicar»: con confirmación activada muestra el SQL; si no, aplica directamente. */
  async function requestApply(tabId: string, queryId: string, stmt: number, opts: { viewOnly?: boolean } = {}) {
    const ui = useUiStore();
    try {
      const statements = await api.previewEdits(queryId, stmt);
      if (!statements.length) return;
      review.value = { tabId, queryId, stmt, statements, applying: false, conflict: null, error: null };
      if (!ui.confirmEdits && !opts.viewOnly) await apply();
    } catch (e) {
      ui.toast("error", asAppError(e).message);
    }
  }

  async function apply() {
    const r = review.value;
    if (!r || r.applying) return;
    r.applying = true;
    r.error = null;
    try {
      const res = await api.applyEdits(r.queryId, r.stmt);
      if (res.conflict) {
        r.conflict = res.conflict;
        return;
      }
      const parts = [
        res.applied ? t("edit.appliedUpdated", { n: res.applied }) : "",
        res.inserted ? t("edit.appliedInserted", { n: res.inserted }) : "",
        res.deleted ? t("edit.appliedDeleted", { n: res.deleted }) : "",
      ].filter(Boolean);
      useUiStore().toast("success", `${t("edit.appliedTitle")}: ${parts.join(", ")}`);
      review.value = null;
      await load(r.queryId, r.stmt);
      await useResultsStore().reloadCached(r.tabId);
    } catch (e) {
      r.error = asAppError(e).message;
    } finally {
      if (review.value) review.value.applying = false;
    }
  }

  function closeReview() {
    review.value = null;
  }

  /** Evento `edit:applied` (p. ej. aplicado desde otra ventana): se refresca el veredicto y las ventanas visibles. */
  async function handleApplied(ev: EditApplied) {
    const results = useResultsStore();
    await load(ev.queryId, ev.stmtIndex);
    const tabId = results.tabOfQuery(ev.queryId);
    if (tabId) await results.reloadCached(tabId);
  }

  /**
   * Antes de reemplazar o cerrar un resultado con cambios sin aplicar, pide confirmación. true = se puede seguir (y los
   * cambios ya están descartados en Go).
   */
  async function confirmDiscard(tabId: string): Promise<boolean> {
    const count = pendingFor(tabId);
    if (count === 0) return true;
    const ok = await new Promise<boolean>((resolve) => (discardPrompt.value = { tabId, count, resolve }));
    if (ok) {
      const q = useResultsStore().of(tabId).queryId;
      for (const k of Object.keys(infos.value).filter((k) => q && k.startsWith(`${q}:`))) {
        const stmt = Number(k.slice(k.lastIndexOf(":") + 1));
        try {
          await api.discardEdits(q!, stmt);
        } catch {
          /* el resultado puede haberse liberado ya: no hay nada que descartar */
        }
        const rest = { ...infos.value };
        delete rest[k];
        infos.value = rest;
      }
    }
    return ok;
  }

  function answerDiscard(ok: boolean) {
    const p = discardPrompt.value;
    discardPrompt.value = null;
    p?.resolve(ok);
  }

  const reviewing = computed(() => review.value !== null);

  return {
    infos, review, discardPrompt, reviewing, focusRequest, addRow, markDelete, unmarkDelete, infoOf, isEditable, pendingFor, load, stage, unstage, discard, requestApply, apply, closeReview,
    handleApplied, confirmDiscard, answerDiscard,
  };
});
