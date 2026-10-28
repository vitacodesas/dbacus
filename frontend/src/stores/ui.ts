import { computed, ref, watch } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/settings";
import { asAppError } from "../api/errors";
import { setLocale, type Locale } from "../i18n";
import type { AppError, Settings } from "../types";

export type Theme = "light" | "dark" | "system";
export type SidebarView = "connections" | "schema" | "history";
export interface Toast {
  id: number;
  kind: "info" | "success" | "error";
  message: string;
  action?: { label: string; run: () => void };
}

export const SIDEBAR_MIN = 240;
export const SIDEBAR_MAX = 480;
const TOAST_MS = 4000;
const PERSIST_MS = 500;
const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

// Estado visual y ajustes de usuario. Go (SettingsService) es la fuente de verdad: aquí se proyectan y los cambios de
// tema, idioma, anchos y límites se guardan con debounce, pero solo después de haber cargado los guardados.
export const useUiStore = defineStore("ui", () => {
  const theme = ref<Theme>("system");
  const systemDark = ref(false);
  const sidebarWidth = ref(300);
  const sidebarCollapsed = ref(false);
  const splitRatio = ref(0.5);
  const sidebarView = ref<SidebarView>("connections");
  const locale = ref<Locale>("es");
  const showSystemDatabases = ref(false);
  const maxRows = ref(100_000);
  const maxResultMemoryMiB = ref(512);
  const historyRetention = ref(5000);
  const settingsOpen = ref(false);
  /** LIMIT automático para SELECT simples (0 = sin límite; el tope real es MaxRows de Go). */
  const autoLimit = ref(1000);
  const toasts = ref<Toast[]>([]);
  let toastSeq = 0;

  const snapshotKey = () => JSON.stringify(snapshot());
  const resolvedTheme = computed<"light" | "dark">(() => (theme.value === "system" ? (systemDark.value ? "dark" : "light") : theme.value));

  function setSidebarWidth(px: number) {
    sidebarWidth.value = clamp(Math.round(px), SIDEBAR_MIN, SIDEBAR_MAX);
  }
  function resizeSidebar(deltaPx: number) {
    setSidebarWidth(sidebarWidth.value + deltaPx);
  }
  function setSplitRatio(r: number) {
    splitRatio.value = clamp(r, 0.1, 0.9);
  }
  function toggleSidebar() {
    sidebarCollapsed.value = !sidebarCollapsed.value;
  }
  function selectSidebarView(view: SidebarView) {
    // Pulsar la vista activa pliega/despliega, como en un IDE.
    if (sidebarView.value === view && !sidebarCollapsed.value) sidebarCollapsed.value = true;
    else {
      sidebarView.value = view;
      sidebarCollapsed.value = false;
    }
  }

  function toast(kind: Toast["kind"], message: string, action?: Toast["action"]) {
    const id = ++toastSeq;
    toasts.value.push({ id, kind, message, action });
    // Los errores quedan hasta cerrarse manualmente.
    if (kind !== "error") setTimeout(() => dismissToast(id), TOAST_MS);
    return id;
  }
  function dismissToast(id: number) {
    toasts.value = toasts.value.filter((t) => t.id !== id);
  }

  watch(locale, setLocale, { immediate: true });

  const snapshot = (): Settings => ({
    theme: theme.value, locale: locale.value, maxRows: maxRows.value, maxResultMemoryMiB: maxResultMemoryMiB.value,
    historyRetention: historyRetention.value, showSystemDatabases: showSystemDatabases.value,
    sidebarWidth: sidebarWidth.value, splitRatio: splitRatio.value,
  });
  function apply(s: Settings) {
    theme.value = s.theme as Theme;
    locale.value = s.locale as Locale;
    maxRows.value = s.maxRows;
    maxResultMemoryMiB.value = s.maxResultMemoryMiB;
    historyRetention.value = s.historyRetention;
    showSystemDatabases.value = s.showSystemDatabases;
    sidebarWidth.value = s.sidebarWidth;
    splitRatio.value = s.splitRatio;
  }

  // Sin esto, el primer guardado (p. ej. al redimensionar) pisaría lo guardado con los valores por defecto.
  let loaded = false;
  let lastSaved = "";
  let persistTimer: ReturnType<typeof setTimeout> | null = null;
  async function load() {
    try {
      apply(await api.getSettings());
      lastSaved = snapshotKey();
      loaded = true;
    } catch (e) {
      toast("error", asAppError(e).message);
    }
  }
  function schedulePersist() {
    if (!loaded || snapshotKey() === lastSaved) return;
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = setTimeout(async () => {
      persistTimer = null;
      try {
        const sent = snapshot();
        await api.setSettings(sent);
        lastSaved = JSON.stringify(sent);
      } catch (e) {
        toast("error", asAppError(e).message);
      }
    }, PERSIST_MS);
  }
  watch(snapshotKey, schedulePersist);

  /** Guarda de inmediato los ajustes del diálogo; si Go los rechaza, devuelve el error y no cambia nada. */
  async function saveSettings(s: Settings): Promise<AppError | null> {
    try {
      apply(await api.setSettings(s));
      lastSaved = snapshotKey();
      return null;
    } catch (e) {
      return asAppError(e);
    }
  }

  return {
    theme, systemDark, resolvedTheme, sidebarWidth, sidebarCollapsed, splitRatio, sidebarView, locale, autoLimit, toasts,
    showSystemDatabases, maxRows, maxResultMemoryMiB, historyRetention, settingsOpen, load, saveSettings,
    setSidebarWidth, resizeSidebar, setSplitRatio, toggleSidebar, selectSidebarView, toast, dismissToast,
  };
});
