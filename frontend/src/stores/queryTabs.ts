import { computed, ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/queries";
import { asAppError } from "../api/errors";
import { useEditsStore } from "./edits";
import { useResultsStore } from "./results";
import type { TabState } from "../types";

export interface QueryTab {
  id: string;
  /** Perfil de conexión al que pertenece. La sesión se resuelve al ejecutar (puede no estar conectada aún). */
  profileId: string;
  title: string;
  sql: string;
  database: string;
  /** Texto de la última vez que se guardó o abrió: `sql !== baseSql` es «sin guardar» (spec 20). */
  baseSql: string;
  /** Consulta guardada vinculada (ruta relativa a la carpeta de consultas) o `""`. */
  filePath: string;
  /** Versión del archivo cuando se guardó/abrió; Go la compara al guardar para no pisar cambios externos. */
  fileVersion: string;
  /** La cabecera `-- @dbacus` (conexión y base) se escribe al guardar. */
  remember: boolean;
  /** Estado del archivo respecto al disco, tras comparar su versión (spec 20 ⚑D9/⚑D10). */
  diskState: "ok" | "changed" | "missing";
}

const PERSIST_DEBOUNCE_MS = 500;
const newId = () => (globalThis.crypto?.randomUUID ? globalThis.crypto.randomUUID() : `t${Date.now()}${Math.random().toString(16).slice(2)}`);

/** Pestañas de consulta. El texto vive aquí para el editor; Go lo persiste (debounce) y lo restaura al arrancar. */
export const useQueryTabsStore = defineStore("queryTabs", () => {
  const tabs = ref<QueryTab[]>([]);
  const activeTabId = ref<string | null>(null);
  const restored = ref(false);
  const lastError = ref<string | null>(null);

  const active = computed(() => tabs.value.find((t) => t.id === activeTabId.value) ?? null);
  const byId = (id: string) => tabs.value.find((t) => t.id === id);

  let timer: ReturnType<typeof setTimeout> | null = null;
  function schedulePersist() {
    if (!restored.value) return; // no pisar lo guardado antes de haberlo leído
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => void persistNow(), PERSIST_DEBOUNCE_MS);
  }
  async function persistNow() {
    if (timer) clearTimeout(timer);
    timer = null;
    const states: TabState[] = tabs.value.map((t, i) => ({
      id: t.id, sessionProfileId: t.profileId, title: t.title, sql: t.sql, database: t.database, order: i,
      filePath: t.filePath, baseSql: t.baseSql, fileVersion: t.fileVersion,
    }));
    try {
      await api.saveTabs(states);
      lastError.value = null;
    } catch (e) {
      lastError.value = asAppError(e).message;
    }
  }

  async function restore() {
    try {
      const saved = (await api.loadTabs()).slice().sort((a, b) => a.order - b.order);
      tabs.value = saved.map((s) => ({
        id: s.id, profileId: s.sessionProfileId, title: s.title, sql: s.sql, database: s.database,
        baseSql: s.baseSql ?? "", filePath: s.filePath ?? "", fileVersion: s.fileVersion ?? "", remember: !!s.filePath, diskState: "ok" as const,
      }));
      activeTabId.value = tabs.value[0]?.id ?? null;
    } catch (e) {
      lastError.value = asAppError(e).message;
    } finally {
      restored.value = true;
    }
  }

  function nextTitle(profileId: string) {
    const used = new Set(tabs.value.filter((t) => t.profileId === profileId).map((t) => t.title));
    for (let n = 1; ; n++) if (!used.has(`Consulta ${n}`)) return `Consulta ${n}`;
  }

  /**
   * `file` vincula la pestaña a una consulta guardada. Sin él, una pestaña con SQL de partida (una tabla del árbol, el
   * historial) no cuenta como «sin guardar» hasta que se edita: lo generado se puede regenerar; lo escrito, no.
   */
  function openTab(opts: { profileId: string; sql?: string; title?: string; database?: string; file?: { path: string; version: string; remember: boolean } }): string {
    const sql = opts.sql ?? "";
    const tab: QueryTab = {
      id: newId(), profileId: opts.profileId, title: opts.title ?? nextTitle(opts.profileId), sql, database: opts.database ?? "",
      baseSql: sql, filePath: opts.file?.path ?? "", fileVersion: opts.file?.version ?? "", remember: opts.file?.remember ?? false, diskState: "ok",
    };
    tabs.value.push(tab);
    activeTabId.value = tab.id;
    schedulePersist();
    return tab.id;
  }

  /** Pestañas con texto sin guardar (editado desde el último guardado/apertura). */
  const isDirty = (t: QueryTab) => t.sql !== t.baseSql;
  const unsaved = computed(() => tabs.value.filter((t) => isDirty(t) && (t.filePath || t.sql.trim() !== "")));

  /** Pestaña pendiente de la pregunta «Guardar / No guardar / Cancelar» al cerrar (la resuelve `TabCloseDialog`). */
  const closePrompt = ref<string | null>(null);

  /** `force` cierra sin preguntar por el texto sin guardar (el usuario ya contestó). */
  async function closeTab(id: string, force = false) {
    const i = tabs.value.findIndex((t) => t.id === id);
    if (i < 0) return;
    const edits = useEditsStore();
    if (edits.pendingFor(id) > 0 && !(await edits.confirmDiscard(id))) return; // ediciones de celdas sin aplicar (spec 13)
    const cur = byId(id);
    if (!force && cur && isDirty(cur) && (cur.filePath || cur.sql.trim() !== "")) {
      closePrompt.value = id;
      return;
    }
    const at = tabs.value.findIndex((t) => t.id === id);
    if (at < 0) return;
    tabs.value.splice(at, 1);
    if (activeTabId.value === id) activeTabId.value = (tabs.value[at] ?? tabs.value[at - 1])?.id ?? null;
    useResultsStore().forgetTab(id);
    schedulePersist();
    try {
      await api.closeTab(id); // cancela lo que corra, libera resultados y la conexión dedicada
    } catch {
      /* la sesión pudo haberse cerrado ya: no hay nada que liberar */
    }
  }

  function setActive(id: string) {
    if (byId(id)) activeTabId.value = id;
  }
  function setSql(id: string, sql: string) {
    const t = byId(id);
    if (!t || t.sql === sql) return;
    t.sql = sql;
    schedulePersist();
  }
  /** Vincula (o actualiza) la pestaña con su consulta guardada tras guardar, abrir o recargar. */
  function bindFile(id: string, f: { path: string; title: string; sql: string; version: string; remember: boolean }) {
    const t = byId(id);
    if (!t) return;
    t.filePath = f.path;
    t.title = f.title;
    t.baseSql = f.sql;
    t.fileVersion = f.version;
    t.remember = f.remember;
    t.diskState = "ok";
    schedulePersist();
  }
  /** Tras una recarga desde el disco: el texto del editor pasa a ser el del archivo. */
  function replaceFromDisk(id: string, sql: string, version: string) {
    const t = byId(id);
    if (!t) return;
    t.sql = sql;
    t.baseSql = sql;
    t.fileVersion = version;
    t.diskState = "ok";
    schedulePersist();
  }
  /** Acepta la versión del disco como base y conserva el texto de la pestaña (queda «sin guardar»). */
  function rebase(id: string, baseSql: string, version: string) {
    const t = byId(id);
    if (!t) return;
    t.baseSql = baseSql;
    t.fileVersion = version;
    t.diskState = "ok";
    schedulePersist();
  }
  function setDiskState(id: string, state: QueryTab["diskState"]) {
    const t = byId(id);
    if (t && t.diskState !== state) t.diskState = state;
  }
  function setProfile(id: string, profileId: string) {
    const t = byId(id);
    if (t) {
      t.profileId = profileId;
      schedulePersist();
    }
  }
  /** Una carpeta o archivo cambió de ruta: las pestañas vinculadas siguen apuntando a su archivo. */
  function applyPathChanges(changes: { from: string; to: string }[]) {
    for (const c of changes) {
      const t = tabs.value.find((x) => x.filePath === c.from);
      if (!t) continue;
      t.filePath = c.to;
      t.title = c.to.slice(c.to.lastIndexOf("/") + 1).replace(/\.sql$/i, "");
    }
    if (changes.length) schedulePersist();
  }
  function setDatabase(id: string, database: string) {
    const t = byId(id);
    if (!t || t.database === database) return;
    t.database = database;
    schedulePersist();
  }
  function rename(id: string, title: string) {
    const t = byId(id);
    const clean = title.trim();
    if (!t || !clean) return;
    t.title = clean;
    schedulePersist();
  }
  function move(from: number, to: number) {
    if (from === to || from < 0 || to < 0 || from >= tabs.value.length || to >= tabs.value.length) return;
    const [t] = tabs.value.splice(from, 1);
    tabs.value.splice(to, 0, t);
    schedulePersist();
  }

  return {
    tabs, activeTabId, active, restored, lastError, closePrompt, unsaved, byId, isDirty, restore, openTab, closeTab, setActive, setSql, setDatabase, setProfile, rename, move, persistNow,
    bindFile, replaceFromDisk, rebase, setDiskState, applyPathChanges,
  };
});
