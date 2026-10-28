import { ref, shallowRef } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/history";
import { asAppError } from "../api/errors";
import type { HistoryAdded, HistoryEntry } from "../types";

export type HistoryStatus = "" | "ok" | "error";
const PAGE = 50;
const REFRESH_DEBOUNCE_MS = 150;

/** Historial de la conexión que se está mirando. Go es la fuente de verdad: aquí solo hay la página visible y el filtro. */
export const useHistoryStore = defineStore("history", () => {
  const profileId = ref<string | null>(null);
  const entries = shallowRef<HistoryEntry[]>([]);
  const nextCursor = ref("");
  const search = ref("");
  const status = ref<HistoryStatus>("");
  const loading = ref(false);
  const error = ref<string | null>(null);

  let generation = 0; // descarta respuestas de peticiones superadas por un cambio de filtro o de conexión
  let timer: ReturnType<typeof setTimeout> | null = null;

  async function reload() {
    const gen = ++generation;
    const id = profileId.value;
    if (!id) {
      entries.value = [];
      nextCursor.value = "";
      error.value = null;
      loading.value = false;
      return;
    }
    loading.value = true;
    try {
      const page = await api.listHistory({ profileId: id, search: search.value, status: status.value, limit: PAGE, cursor: "" });
      if (gen !== generation) return;
      entries.value = page.entries;
      nextCursor.value = page.nextCursor;
      error.value = null;
    } catch (e) {
      if (gen === generation) error.value = asAppError(e).message;
    } finally {
      if (gen === generation) loading.value = false;
    }
  }

  async function more() {
    const id = profileId.value;
    if (!id || !nextCursor.value || loading.value) return;
    const gen = generation;
    loading.value = true;
    try {
      const page = await api.listHistory({ profileId: id, search: search.value, status: status.value, limit: PAGE, cursor: nextCursor.value });
      if (gen !== generation) return;
      entries.value = [...entries.value, ...page.entries];
      nextCursor.value = page.nextCursor;
    } catch (e) {
      if (gen === generation) error.value = asAppError(e).message;
    } finally {
      if (gen === generation) loading.value = false;
    }
  }

  function setProfile(id: string | null) {
    if (profileId.value === id) return;
    profileId.value = id;
    void reload();
  }
  function setFilters(f: { search?: string; status?: HistoryStatus }) {
    const s = f.search ?? search.value;
    const st = f.status ?? status.value;
    if (s === search.value && st === status.value) return;
    search.value = s;
    status.value = st;
    void reload();
  }

  async function remove(id: string) {
    await api.deleteHistory(id);
    entries.value = entries.value.filter((e) => e.id !== id);
  }
  async function clear() {
    const id = profileId.value;
    if (!id) return;
    await api.clearHistory(id);
    await reload();
  }

  /** Go escribe el historial en segundo plano: una ráfaga de sentencias se agrupa en una sola recarga. */
  function handleAdded(ev: HistoryAdded) {
    if (ev.profileId !== profileId.value) return;
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      void reload();
    }, REFRESH_DEBOUNCE_MS);
  }

  return { profileId, entries, nextCursor, search, status, loading, error, reload, more, setProfile, setFilters, remove, clear, handleAdded };
});
