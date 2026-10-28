import { computed, ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/exports";
import { asAppError } from "../api/errors";
import { i18n } from "../i18n";
import type { ExportDone, ExportOptions, ExportProgress } from "../types";
import { useUiStore } from "./ui";

export type ExportFormat = "csv" | "json";
export interface ExportJob {
  jobId: string;
  rows: number;
  bytes: number;
  status: "running" | "done" | "error" | "canceled";
  path: string;
  error: string | null;
}

export const defaultOptions = (): ExportOptions => ({
  format: "csv", delimiter: ",", header: true, nullAs: "", bom: false, crlf: false, quoteAll: false, escapeFormulas: false, ndjson: false,
});

/** Exportación en curso (una a la vez). Las opciones se recuerdan entre usos durante la sesión. */
export const useExportsStore = defineStore("exports", () => {
  const options = ref<ExportOptions>(defaultOptions());
  const job = ref<ExportJob | null>(null);
  const starting = ref(false);
  const running = computed(() => starting.value || job.value?.status === "running");

  // Una exportación pequeña puede terminar antes de que `Export` devuelva el id: el evento se guarda hasta entonces.
  const early = new Map<string, ExportDone>();
  const t = (key: string, named?: Record<string, unknown>) => i18n.global.t(key, named ?? {}) as string;

  function finish(ev: ExportDone) {
    const ui = useUiStore();
    if (!job.value || job.value.jobId !== ev.jobId) return;
    job.value = { ...job.value, rows: ev.rows, bytes: ev.bytes, path: ev.path, status: ev.canceled ? "canceled" : ev.error ? "error" : "done", error: ev.error?.message ?? null };
    if (ev.canceled) ui.toast("info", t("export.canceled"));
    else if (ev.error) ui.toast("error", `${t("export.failed")}: ${ev.error.message}`);
    else ui.toast("success", t("export.saved", { rows: ev.rows.toLocaleString(), path: ev.path }));
  }

  async function start(queryId: string, stmtIndex: number, source: "buffer" | "rerun"): Promise<boolean> {
    if (running.value) return false;
    starting.value = true;
    job.value = null;
    try {
      const res = await api.startExport({ queryId, stmtIndex, source, options: { ...options.value } });
      if (res.canceled) return false; // el usuario cerró el diálogo de guardado
      job.value = { jobId: res.jobId, rows: 0, bytes: 0, status: "running", path: res.path, error: null };
      const done = early.get(res.jobId);
      if (done) {
        early.delete(res.jobId);
        finish(done);
      }
      return true;
    } catch (e) {
      useUiStore().toast("error", `${t("export.failed")}: ${asAppError(e).message}`);
      return false;
    } finally {
      starting.value = false;
    }
  }

  async function cancel() {
    if (job.value?.status !== "running") return;
    try {
      await api.cancelExport(job.value.jobId);
    } catch (e) {
      useUiStore().toast("error", asAppError(e).message);
    }
  }

  function handleProgress(ev: ExportProgress) {
    if (job.value?.jobId === ev.jobId && job.value.status === "running") job.value = { ...job.value, rows: ev.rows, bytes: ev.bytes };
  }
  function handleDone(ev: ExportDone) {
    if (!job.value || job.value.jobId !== ev.jobId) {
      if (starting.value) early.set(ev.jobId, ev); // aún no conocemos el id
      return;
    }
    finish(ev);
  }
  function dismiss() {
    if (job.value && job.value.status !== "running") job.value = null;
  }

  return { options, job, starting, running, start, cancel, handleProgress, handleDone, dismiss };
});
