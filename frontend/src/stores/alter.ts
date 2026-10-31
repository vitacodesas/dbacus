import { computed, ref, watch } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/alter";
import { asAppError } from "../api/errors";
import { blankColumn, blankFk, blankIndex, buildChangeSet, draftsFromModel, type Drafts } from "../composables/alterOps";
import { i18n } from "../i18n";
import { useUiStore } from "./ui";
import type { AlterModel, AlterPreview, AlterResult } from "../types";

const t = (k: string, p?: Record<string, unknown>) => i18n.global.t(k, p ?? {});

/** Debounce del SQL en vivo (D4 del spec 19). */
export const PREVIEW_DELAY_MS = 250;

export interface AlterTarget {
  sessionId: string;
  db: string;
  table: string;
}

/** Diálogo de revisión (siempre, D5) y resultado del intento. */
export interface AlterReview {
  applying: boolean;
  error: string | null;
  /** Presente si el intento aplicó algo y luego falló (D8): qué se aplicó y qué falló. */
  partial: AlterResult | null;
}

/**
 * Editor de estructura de tabla (spec 19). Los borradores son estado de UI efímero; la verdad es el servidor: cada cambio
 * recalcula en Go el SQL (`Preview`) y solo `Apply` con el hash de esa vista previa ejecuta algo.
 */
export const useAlterStore = defineStore("alter", () => {
  const target = ref<AlterTarget | null>(null);
  const model = ref<AlterModel | null>(null);
  const drafts = ref<Drafts>({ cols: [], idxs: [], fks: [] });
  const loading = ref(false);
  const loadError = ref<string | null>(null);

  const preview = ref<AlterPreview | null>(null);
  const previewError = ref<string | null>(null);
  const previewing = ref(false);
  const review = ref<AlterReview | null>(null);

  const changeSet = computed(() => (model.value && target.value ? buildChangeSet(model.value, drafts.value, target.value.db, target.value.table) : null));
  const dirty = computed(() => (changeSet.value?.ops?.length ?? 0) > 0);
  /** Hay SQL revisado y válido que se puede aplicar. */
  const canApply = computed(() => dirty.value && !previewing.value && !previewError.value && (preview.value?.statements?.length ?? 0) > 0);

  let timer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;

  function schedule() {
    clearTimeout(timer);
    const cs = changeSet.value;
    const tg = target.value;
    seq++;
    if (!cs || !tg || !cs.ops?.length) {
      preview.value = null;
      previewError.value = null;
      previewing.value = false;
      return;
    }
    previewing.value = true;
    const mine = seq;
    timer = setTimeout(async () => {
      try {
        const p = await api.previewAlter(tg.sessionId, cs);
        if (mine !== seq) return;
        preview.value = p;
        previewError.value = null;
      } catch (e) {
        if (mine !== seq) return;
        preview.value = null;
        previewError.value = asAppError(e).message;
      } finally {
        if (mine === seq) previewing.value = false;
      }
    }, PREVIEW_DELAY_MS);
  }
  watch(changeSet, schedule, { deep: true });

  function load(m: AlterModel) {
    model.value = m;
    drafts.value = draftsFromModel(m);
  }

  async function open(tg: AlterTarget) {
    reset();
    target.value = tg;
    loading.value = true;
    try {
      load(await api.getAlterModel(tg.sessionId, tg.db, tg.table));
    } catch (e) {
      loadError.value = asAppError(e).message;
    } finally {
      loading.value = false;
    }
  }

  function reset() {
    clearTimeout(timer);
    seq++;
    target.value = null;
    model.value = null;
    drafts.value = { cols: [], idxs: [], fks: [] };
    loading.value = false;
    loadError.value = null;
    preview.value = null;
    previewError.value = null;
    previewing.value = false;
    review.value = null;
  }

  /** Descarta todos los cambios y vuelve al estado leído. */
  function discardAll() {
    if (model.value) load(model.value);
  }

  const addColumn = () => drafts.value.cols.push(blankColumn());
  const addIndex = () => drafts.value.idxs.push(blankIndex());
  const addFk = () => drafts.value.fks.push(blankFk());

  /** Mueve una columna (índice dentro de TODOS los borradores, incluidos los borrados). */
  function moveColumn(index: number, delta: -1 | 1) {
    const cols = drafts.value.cols;
    const j = index + delta;
    if (j < 0 || j >= cols.length) return;
    [cols[index], cols[j]] = [cols[j], cols[index]];
  }

  /** Quita un borrador nuevo; los que ya existen se marcan para borrar. */
  function removeColumn(key: string) {
    const i = drafts.value.cols.findIndex((c) => c.key === key);
    if (i < 0) return;
    if (drafts.value.cols[i].orig === null) drafts.value.cols.splice(i, 1);
    else drafts.value.cols[i].deleted = true;
  }
  function removeIndex(key: string) {
    const i = drafts.value.idxs.findIndex((c) => c.key === key);
    if (i < 0) return;
    if (drafts.value.idxs[i].orig === null) drafts.value.idxs.splice(i, 1);
    else drafts.value.idxs[i].deleted = true;
  }
  function removeFk(key: string) {
    const i = drafts.value.fks.findIndex((c) => c.key === key);
    if (i < 0) return;
    if (drafts.value.fks[i].orig === null) drafts.value.fks.splice(i, 1);
    else drafts.value.fks[i].deleted = true;
  }

  /** Devuelve una fila a lo que se leyó del servidor (conserva su posición en la lista). */
  function revert(kind: "cols" | "idxs" | "fks", key: string) {
    if (!model.value) return;
    const list = drafts.value[kind] as { key: string; orig: string | null }[];
    const i = list.findIndex((d) => d.key === key);
    const fresh = (draftsFromModel(model.value)[kind] as { key: string; orig: string | null }[]).find((d) => d.orig === list[i]?.orig);
    if (i >= 0 && fresh) list[i] = { ...fresh, key };
  }

  function requestApply() {
    if (canApply.value) review.value = { applying: false, error: null, partial: null };
  }
  const closeReview = () => {
    if (!review.value?.applying) review.value = null;
  };

  async function apply(): Promise<boolean> {
    const tg = target.value;
    const cs = changeSet.value;
    const rv = review.value;
    if (!tg || !cs || !rv || !preview.value) return false;
    rv.applying = true;
    rv.error = null;
    try {
      const res = await api.applyAlter(tg.sessionId, cs, preview.value.sqlHash);
      if (res.model) load(res.model);
      if (res.failed) {
        // La primera sentencia ya se confirmó: no hay vuelta atrás. Se enseña qué pasó y el modal queda con el estado real.
        rv.partial = res;
        return false;
      }
      useUiStore().toast("success", t("alter.applied", { n: res.applied?.length ?? 0 }));
      review.value = null;
      target.value = null; // cierra el editor
      return true;
    } catch (e) {
      rv.error = asAppError(e).message;
      return false;
    } finally {
      rv.applying = false;
    }
  }

  return {
    target, model, drafts, loading, loadError, preview, previewError, previewing, review, changeSet, dirty, canApply,
    open, reset, discardAll, addColumn, addIndex, addFk, moveColumn, revert, removeColumn, removeIndex, removeFk, requestApply, closeReview, apply,
  };
});
