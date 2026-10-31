import { onBeforeUnmount, onMounted } from "vue";
import { useQueryFilesStore } from "../stores/queryFiles";
import { useQueryTabsStore } from "../stores/queryTabs";
import { useUiStore } from "../stores/ui";
import { useQueryActions } from "./useQueryActions";

export const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
export const modKey = isMac ? "⌘" : "Ctrl";

/** Atajos globales de la app (spec 08). Los de editor/consulta se registran en sus componentes. */
export function useShortcuts() {
  const ui = useUiStore();
  const actions = useQueryActions();
  const tabs = useQueryTabsStore();
  const files = useQueryFilesStore();
  const onKey = (e: KeyboardEvent) => {
    const mod = isMac ? e.metaKey : e.ctrlKey;
    if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "b") {
      e.preventDefault();
      ui.toggleSidebar();
    } else if (mod && !e.shiftKey && !e.altKey && e.key === ",") {
      e.preventDefault();
      ui.settingsOpen = true;
    } else if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "t") {
      e.preventDefault();
      actions.newQuery();
    } else if (mod && !e.altKey && e.key.toLowerCase() === "s" && !document.querySelector('[role="dialog"]')) {
      // ⌘S guarda la consulta de la pestaña activa; dentro de un diálogo (p. ej. el formulario de conexión) no actúa.
      e.preventDefault();
      const id = tabs.activeTabId;
      if (id) void (e.shiftKey ? files.saveAs(id) : files.save(id));
    } else if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "w") {
      e.preventDefault();
      void actions.closeActive();
    }
  };
  onMounted(() => window.addEventListener("keydown", onKey));
  onBeforeUnmount(() => window.removeEventListener("keydown", onKey));
}
