import { computed, ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/queryFiles";
import { asAppError } from "../api/errors";
import { i18n } from "../i18n";
import { baseOf, copyName, flattenQueries, joinPath, parentOf, type QuerySort } from "../composables/queryTree";
import { useConnectionsStore } from "./connections";
import { useQueryTabsStore, type QueryTab } from "./queryTabs";
import { useSessionsStore } from "./sessions";
import { useUiStore } from "./ui";
import type { AppError, QueryHit, QueryNode, QueryRoot, QueryTree } from "../types";

const t = (k: string, p?: Record<string, unknown>) => i18n.global.t(k, p ?? {});
const stripExt = (n: string) => n.replace(/\.sql$/i, "");

/** Diálogo «Guardar como consulta» (spec 20). `tabId` es la pestaña que queda vinculada; sin él (historial) solo se guarda. */
export interface SaveDialogState {
  tabId: string | null;
  sql: string;
  profileId: string;
  database: string;
  name: string;
  folder: string;
  remember: boolean;
  /** Cerrar la pestaña al terminar de guardar (venía de «Guardar» en la pregunta de cierre). */
  thenClose: boolean;
  /** El nombre ya existe: el diálogo ofrece sobrescribir. */
  exists: boolean;
  error: string | null;
  busy: boolean;
}

/** Diálogo de carpeta nueva, renombrar o mover. */
export interface EntryDialogState {
  kind: "newFolder" | "rename" | "move";
  path: string; // origen (rename/move) o carpeta padre (newFolder)
  isFolder: boolean;
  name: string;
  folder: string;
  error: string | null;
  busy: boolean;
}

/**
 * Consultas guardadas como archivos .sql (spec 20). Go es la verdad (listado, versiones, rutas); aquí solo viven la
 * proyección del árbol, la búsqueda, los diálogos y el vínculo de las pestañas con sus archivos.
 */
export const useQueryFilesStore = defineStore("queryFiles", () => {
  const tabs = useQueryTabsStore();
  const ui = useUiStore();

  const tree = ref<QueryTree | null>(null);
  const loading = ref(false);
  const error = ref<AppError | null>(null);
  const collapsed = ref<Set<string>>(new Set());
  const sort = ref<QuerySort>("name");

  const query = ref("");
  const searchContent = ref(false);
  const hits = ref<QueryHit[] | null>(null);
  const searching = ref(false);
  let searchSeq = 0;

  const saveDialog = ref<SaveDialogState | null>(null);
  const entryDialog = ref<EntryDialogState | null>(null);
  const deletePrompt = ref<{ path: string; isFolder: boolean } | null>(null);
  /** Última carpeta elegida al guardar, durante la sesión. */
  const lastFolder = ref("");

  const nodes = computed<QueryNode[]>(() => tree.value?.nodes ?? []);
  const root = computed<QueryRoot | null>(() => tree.value?.root ?? null);
  const rows = computed(() => flattenQueries(nodes.value, collapsed.value, sort.value));
  const byPath = computed(() => new Map(nodes.value.map((n) => [n.path, n])));

  function toggle(path: string) {
    const s = new Set(collapsed.value);
    if (!s.delete(path)) s.add(path);
    collapsed.value = s;
  }

  async function load() {
    loading.value = true;
    try {
      tree.value = await api.listQueries();
      error.value = null;
    } catch (e) {
      error.value = asAppError(e);
      // La raíz se muestra aunque no se pueda abrir: la UI explica dónde debería estar y ofrece elegir otra.
      try {
        tree.value = { root: await api.getRoot(), nodes: [], truncated: false };
      } catch {
        tree.value = null;
      }
    } finally {
      loading.value = false;
    }
  }

  // ---- búsqueda ----------------------------------------------------------------------------------------------------
  async function search(text: string, content: boolean) {
    query.value = text;
    searchContent.value = content;
    const q = text.trim();
    const seq = ++searchSeq;
    if (!q) {
      hits.value = null;
      searching.value = false;
      return;
    }
    searching.value = true;
    try {
      const r = await api.searchQueries({ text: q, content, limit: 50 });
      if (seq === searchSeq) hits.value = r;
    } catch (e) {
      if (seq === searchSeq) {
        hits.value = [];
        ui.toast("error", asAppError(e).message);
      }
    } finally {
      if (seq === searchSeq) searching.value = false;
    }
  }

  // ---- abrir -------------------------------------------------------------------------------------------------------
  /** Conexión para una consulta sin conexión propia: la de la sesión activa o la primera guardada. */
  const fallbackProfile = () => useSessionsStore().active?.profileId ?? tabs.active?.profileId ?? useConnectionsStore().profiles[0]?.id ?? null;

  /** Abre una consulta guardada en una pestaña (o enfoca la que ya la tiene). Nunca ejecuta nada. */
  async function openFile(path: string): Promise<string | null> {
    const existing = tabs.tabs.find((x) => x.filePath === path);
    if (existing) {
      tabs.setActive(existing.id);
      return existing.id;
    }
    try {
      const f = await api.openQuery(path);
      const known = f.profileId && !f.profileMissing && useConnectionsStore().profiles.some((p) => p.id === f.profileId);
      const profileId = known ? f.profileId : fallbackProfile();
      if (!profileId) {
        ui.toast("info", t("query.needSession"));
        return null;
      }
      if (f.profileId && !known) ui.toast("info", t("queries.profileMissing", { name: f.profileName || f.profileId }));
      return tabs.openTab({ profileId, sql: f.sql, title: f.name, database: f.database, file: { path: f.path, version: f.version, remember: !!f.profileId } });
    } catch (e) {
      ui.toast("error", asAppError(e).message);
      if (asAppError(e).code === "NOT_FOUND") void load();
      return null;
    }
  }

  // ---- guardar -----------------------------------------------------------------------------------------------------
  type Flags = { expectedVersion: string; overwrite: boolean };

  async function persist(tab: QueryTab, path: string, flags: Flags, remember: boolean): Promise<boolean> {
    const sql = tab.sql; // lo enviado: si se sigue escribiendo mientras guarda, la pestaña queda «sin guardar» con razón
    try {
      const ref = await api.saveQuery({ path, sql, profileId: tab.profileId, database: tab.database, rememberConnection: remember, ...flags });
      tabs.bindFile(tab.id, { path: ref.path, title: ref.name, sql, version: ref.version, remember });
      ui.toast("success", t("queries.saved", { name: ref.name }));
      void load();
      return true;
    } catch (e) {
      const err = asAppError(e);
      if (err.detail === "changed_on_disk") tabs.setDiskState(tab.id, "changed");
      else if (err.code === "NOT_FOUND") tabs.setDiskState(tab.id, "missing");
      else ui.toast("error", err.message);
      return false;
    }
  }

  /** `⌘S`: guarda en su archivo; si la pestaña aún no tiene, abre el diálogo. Devuelve si quedó guardada. */
  async function save(tabId: string, opts: { thenClose?: boolean } = {}): Promise<boolean> {
    const tab = tabs.byId(tabId);
    if (!tab) return false;
    if (!tab.filePath) {
      openSaveDialog({ tabId, sql: tab.sql, profileId: tab.profileId, database: tab.database, thenClose: opts.thenClose });
      return false;
    }
    const ok = await persist(tab, tab.filePath, { expectedVersion: tab.fileVersion, overwrite: false }, tab.remember);
    if (ok && opts.thenClose) await tabs.closeTab(tabId, true);
    return ok;
  }
  /** «Sobrescribir» tras detectar un cambio externo. */
  async function overwrite(tabId: string) {
    const tab = tabs.byId(tabId);
    if (tab?.filePath) await persist(tab, tab.filePath, { expectedVersion: "", overwrite: true }, tab.remember);
  }
  /** «Guardar de nuevo» cuando el archivo se borró fuera de la app. */
  async function saveAgain(tabId: string) {
    const tab = tabs.byId(tabId);
    if (tab?.filePath) await persist(tab, tab.filePath, { expectedVersion: "", overwrite: false }, tab.remember);
  }
  /** Recarga el texto desde el disco (descarta lo que había en la pestaña). */
  async function reload(tabId: string, silent = false) {
    const tab = tabs.byId(tabId);
    if (!tab?.filePath) return;
    try {
      const f = await api.openQuery(tab.filePath);
      tabs.replaceFromDisk(tabId, f.sql, f.version);
      if (silent) ui.toast("info", t("queries.reloaded", { name: tab.title }));
    } catch (e) {
      if (asAppError(e).code === "NOT_FOUND") tabs.setDiskState(tabId, "missing");
      else ui.toast("error", asAppError(e).message);
    }
  }
  /** «Mantener lo mío»: acepta la versión del disco como base y conserva el texto de la pestaña (queda sin guardar). */
  async function keepMine(tabId: string) {
    const tab = tabs.byId(tabId);
    if (!tab?.filePath) return;
    try {
      const f = await api.openQuery(tab.filePath);
      tabs.rebase(tabId, f.sql, f.version);
    } catch (e) {
      if (asAppError(e).code === "NOT_FOUND") tabs.setDiskState(tabId, "missing");
      else ui.toast("error", asAppError(e).message);
    }
  }

  function openSaveDialog(o: { tabId?: string | null; sql: string; profileId: string; database: string; thenClose?: boolean; name?: string }) {
    const tab = o.tabId ? tabs.byId(o.tabId) : null;
    const existing = tab?.filePath ? stripExt(baseOf(tab.filePath)) : "";
    const stamp = new Date();
    const pad = (n: number) => String(n).padStart(2, "0");
    saveDialog.value = {
      tabId: o.tabId ?? null, sql: o.sql, profileId: o.profileId, database: o.database,
      name: o.name ?? (existing || `consulta-${stamp.getFullYear()}${pad(stamp.getMonth() + 1)}${pad(stamp.getDate())}-${pad(stamp.getHours())}${pad(stamp.getMinutes())}`),
      folder: tab?.filePath ? parentOf(tab.filePath) : lastFolder.value,
      remember: tab ? tab.remember || !tab.filePath : true,
      thenClose: !!o.thenClose, exists: false, error: null, busy: false,
    };
    if (!tree.value) void load();
  }
  /** `⌘⇧S`: guardar como, aunque la pestaña ya tenga archivo. */
  function saveAs(tabId: string) {
    const tab = tabs.byId(tabId);
    if (tab) openSaveDialog({ tabId, sql: tab.sql, profileId: tab.profileId, database: tab.database });
  }
  function closeSaveDialog() {
    saveDialog.value = null;
  }

  async function submitSave(overwriteExisting = false): Promise<boolean> {
    const d = saveDialog.value;
    if (!d || d.busy) return false;
    const name = d.name.trim();
    if (!name) {
      d.error = t("queries.nameRequired");
      return false;
    }
    d.busy = true;
    d.error = null;
    d.exists = false;
    const tab = d.tabId ? tabs.byId(d.tabId) : null;
    const sql = tab ? tab.sql : d.sql;
    try {
      const ref = await api.saveQuery({
        path: joinPath(d.folder, name), sql, profileId: d.profileId, database: d.database,
        rememberConnection: d.remember, expectedVersion: "", overwrite: overwriteExisting,
      });
      lastFolder.value = d.folder;
      if (tab) tabs.bindFile(tab.id, { path: ref.path, title: ref.name, sql, version: ref.version, remember: d.remember });
      ui.toast("success", t("queries.savedAt", { path: ref.path }));
      const close = d.thenClose && !!tab;
      saveDialog.value = null;
      void load();
      if (close) await tabs.closeTab(tab!.id, true);
      return true;
    } catch (e) {
      const err = asAppError(e);
      d.busy = false;
      d.exists = err.detail === "exists";
      d.error = err.message;
      return false;
    }
  }

  // ---- carpetas, mover, duplicar, borrar -------------------------------------------------------------------------
  function openEntryDialog(kind: EntryDialogState["kind"], path: string) {
    const node = byPath.value.get(path);
    const isFolder = kind === "newFolder" ? true : node?.kind === "folder";
    entryDialog.value = {
      kind, path, isFolder, error: null, busy: false,
      name: kind === "rename" ? (node?.name ?? baseOf(path)) : "",
      folder: kind === "move" ? parentOf(path) : "",
    };
    if (kind === "newFolder") entryDialog.value.folder = path;
  }
  function closeEntryDialog() {
    entryDialog.value = null;
  }

  async function submitEntry(): Promise<boolean> {
    const d = entryDialog.value;
    if (!d || d.busy) return false;
    d.busy = true;
    d.error = null;
    try {
      if (d.kind === "newFolder") {
        if (!d.name.trim()) throw Object.assign(new Error(t("queries.nameRequired")), { local: true });
        await api.createFolder(joinPath(d.folder, d.name.trim()));
      } else {
        const leaf = d.kind === "rename" ? d.name.trim() : baseOf(d.path);
        if (!leaf) throw Object.assign(new Error(t("queries.nameRequired")), { local: true });
        const to = joinPath(d.kind === "move" ? d.folder : parentOf(d.path), leaf);
        await moveEntry(d.path, to);
      }
      entryDialog.value = null;
      void load();
      return true;
    } catch (e) {
      d.busy = false;
      d.error = (e as { local?: boolean }).local ? (e as Error).message : asAppError(e).message;
      return false;
    }
  }

  /** Mueve o renombra y actualiza las pestañas que apuntaban a archivos afectados. */
  async function moveEntry(from: string, to: string) {
    const changes = await api.moveQuery(from, to);
    tabs.applyPathChanges(changes);
  }

  async function duplicate(path: string) {
    try {
      const f = await api.openQuery(path);
      const folder = parentOf(f.path);
      const name = copyName(nodes.value, folder, f.name);
      await api.saveQuery({ path: joinPath(folder, name), sql: f.sql, profileId: f.profileId, database: f.database, rememberConnection: !!f.profileId, expectedVersion: "", overwrite: false });
      await load();
    } catch (e) {
      ui.toast("error", asAppError(e).message);
    }
  }

  function askDelete(path: string) {
    deletePrompt.value = { path, isFolder: byPath.value.get(path)?.kind === "folder" };
  }
  async function confirmDelete(prompt = deletePrompt.value) {
    const p = prompt;
    deletePrompt.value = null;
    if (!p) return;
    try {
      await api.deleteQuery(p.path);
      // Las pestañas conservan su texto: pasan a «el archivo ya no existe» y se pueden guardar de nuevo.
      for (const tab of tabs.tabs) if (tab.filePath === p.path || tab.filePath.startsWith(`${p.path}/`)) tabs.setDiskState(tab.id, "missing");
      ui.toast("info", t("queries.deleted"));
      await load();
    } catch (e) {
      ui.toast("error", asAppError(e).message);
    }
  }

  // ---- carpeta raíz ------------------------------------------------------------------------------------------------
  async function pickRoot() {
    try {
      const r = await api.pickRoot();
      if (r) {
        collapsed.value = new Set();
        await load();
        await syncTabs();
      }
      return r;
    } catch (e) {
      ui.toast("error", asAppError(e).message);
      return null;
    }
  }
  async function resetRoot() {
    try {
      await api.resetRoot();
      collapsed.value = new Set();
      await load();
      await syncTabs();
    } catch (e) {
      ui.toast("error", asAppError(e).message);
    }
  }
  async function reveal(path = "") {
    try {
      await api.reveal(path);
    } catch (e) {
      ui.toast("error", asAppError(e).message);
    }
  }

  // ---- cambios en el disco -----------------------------------------------------------------------------------------
  /**
   * Compara cada pestaña vinculada con el listado del disco (al abrir el panel, al recuperar el foco y al arrancar):
   * sin cambios locales se recarga sola; con cambios locales se avisa (spec 20 ⚑D10).
   */
  async function syncTabs() {
    const bound = tabs.tabs.filter((x) => x.filePath);
    if (!bound.length) return;
    await load();
    if (!tree.value || error.value) return; // sin listado no se afirma nada (la carpeta puede estar desmontada)
    const map = byPath.value;
    for (const tab of bound) {
      const node = map.get(tab.filePath);
      if (!node) tabs.setDiskState(tab.id, "missing");
      else if (tab.fileVersion && node.version !== tab.fileVersion) {
        if (tabs.isDirty(tab)) tabs.setDiskState(tab.id, "changed");
        else await reload(tab.id, true);
      } else if (tab.diskState === "missing") tabs.setDiskState(tab.id, "ok"); // el archivo volvió
    }
  }

  return {
    tree, loading, error, collapsed, sort, query, searchContent, hits, searching, saveDialog, entryDialog, deletePrompt, lastFolder,
    nodes, root, rows, byPath, toggle, load, search, openFile, save, saveAs, overwrite, saveAgain, reload, keepMine, openSaveDialog,
    closeSaveDialog, submitSave, openEntryDialog, closeEntryDialog, submitEntry, moveEntry, duplicate, askDelete, confirmDelete,
    pickRoot, resetRoot, reveal, syncTabs,
  };
});
