import { onBeforeUnmount, onMounted, watchEffect } from "vue";
import { useUiStore } from "../stores/ui";

/** Aplica la clase `dark` según el tema resuelto y sigue los cambios del sistema. */
export function useTheme() {
  const ui = useUiStore();
  const media = window.matchMedia("(prefers-color-scheme: dark)");
  const onChange = (e: MediaQueryListEvent) => (ui.systemDark = e.matches);

  ui.systemDark = media.matches;
  watchEffect(() => document.documentElement.classList.toggle("dark", ui.resolvedTheme === "dark"));

  onMounted(() => media.addEventListener("change", onChange));
  onBeforeUnmount(() => media.removeEventListener("change", onChange));
}
