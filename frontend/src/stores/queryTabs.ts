import { computed, ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/queries";
import { asAppError } from "../api/errors";
import { useResultsStore } from "./results";
import type { TabState } from "../types";

export interface QueryTab {
  id: string;
  /** Perfil de conexión al que pertenece. La sesión se resuelve al ejecutar (puede no estar conectada aún). */
  profileId: string;
  title: string;
  sql: string;
  database: string;
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
    const states: TabState[] = tabs.value.map((t, i) => ({ id: t.id, sessionProfileId: t.profileId, title: t.title, sql: t.sql, database: t.database, order: i }));
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
      tabs.value = saved.map((s) => ({ id: s.id, profileId: s.sessionProfileId, title: s.title, sql: s.sql, database: s.database }));
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

  function openTab(opts: { profileId: string; sql?: string; title?: string; database?: string }): string {
    const tab: QueryTab = { id: newId(), profileId: opts.profileId, title: opts.title ?? nextTitle(opts.profileId), sql: opts.sql ?? "", database: opts.database ?? "" };
    tabs.value.push(tab);
    activeTabId.value = tab.id;
    schedulePersist();
    return tab.id;
  }

  async function closeTab(id: string) {
    const i = tabs.value.findIndex((t) => t.id === id);
    if (i < 0) return;
    tabs.value.splice(i, 1);
    if (activeTabId.value === id) activeTabId.value = (tabs.value[i] ?? tabs.value[i - 1])?.id ?? null;
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

  return { tabs, activeTabId, active, restored, lastError, byId, restore, openTab, closeTab, setActive, setSql, setDatabase, rename, move, persistNow };
});
