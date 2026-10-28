import { computed, ref, watch } from "vue";
import { defineStore } from "pinia";
import { setLocale, type Locale } from "../i18n";

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
const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

// Estado visual. La persistencia en Go (SettingsService/ui_state) se conecta en T6.1/T4.6 (spec 09).
export const useUiStore = defineStore("ui", () => {
  const theme = ref<Theme>("system");
  const systemDark = ref(false);
  const sidebarWidth = ref(300);
  const sidebarCollapsed = ref(false);
  const splitRatio = ref(0.5);
  const sidebarView = ref<SidebarView>("connections");
  const locale = ref<Locale>("es");
  /** LIMIT automático para SELECT simples (0 = sin límite; el tope real es MaxRows de Go). */
  const autoLimit = ref(1000);
  const toasts = ref<Toast[]>([]);
  let toastSeq = 0;

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

  return {
    theme, systemDark, resolvedTheme, sidebarWidth, sidebarCollapsed, splitRatio, sidebarView, locale, autoLimit, toasts,
    setSidebarWidth, resizeSidebar, setSplitRatio, toggleSidebar, selectSidebarView, toast, dismissToast,
  };
});
