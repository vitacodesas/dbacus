<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, onUnmounted, ref, watch } from "vue";
import AppLayout from "./components/layout/AppLayout.vue";
import MigrationDialog from "./components/schema/MigrationDialog.vue";
import QueryWorkspace from "./components/query/QueryWorkspace.vue";
import WelcomeView from "./views/WelcomeView.vue";
import MonitorView from "./views/MonitorView.vue";
import { useConnectFlow } from "./composables/useConnectFlow";
import { useShortcuts } from "./composables/useShortcuts";
import { useTheme } from "./composables/useTheme";
import { startEventDispatch } from "./events";
import { useConnectionsStore } from "./stores/connections";
import { useQueryTabsStore } from "./stores/queryTabs";
import { useQueryFilesStore } from "./stores/queryFiles";
import { useSessionsStore } from "./stores/sessions";
import { useUiStore } from "./stores/ui";

useTheme();
useShortcuts();
const queryTabs = useQueryTabsStore();
const ui = useUiStore();
// El Monitor se monta la primera vez que se abre y luego se queda (su estado y gráficas no se pierden al volver a consultas).
const monitorSeen = ref(false);
watch(() => ui.mainView, (v) => { if (v === "monitor") monitorSeen.value = true; }, { immediate: true });
// Abrir o activar una consulta (sidebar, historial, ⌘T…) con el Monitor delante la dejaba oculta: se vuelve a las consultas (spec 22 ⚑D12).
watch(() => queryTabs.focusSeq, () => { ui.mainView = "queries"; });

// Arranque: perfiles, sesiones vivas en Go y suscripción a eventos.
let stopEvents: (() => void) | null = null;
onMounted(() => {
  stopEvents = startEventDispatch();
  const flow = useConnectFlow();
  useSessionsStore().reconnect = (profileId) => void flow.connect(profileId);
  void useUiStore().load();
  void useConnectionsStore().load();
  void useSessionsStore().refresh();
  void queryTabs.restore().then(() => queryFiles.syncTabs());
  window.addEventListener("focus", onFocus);
});
onUnmounted(() => {
  stopEvents?.();
  window.removeEventListener("focus", onFocus);
});

// Sin vigilante de disco (spec 20 ⚑D10): al volver a la ventana se relee la carpeta de consultas.
const queryFiles = useQueryFilesStore();
function onFocus() {
  if (ui.sidebarView === "queries" && !ui.sidebarCollapsed) void queryFiles.load().then(() => queryFiles.syncTabs());
  else void queryFiles.syncTabs();
}

// Galería de componentes solo en dev: http://localhost:9245/#/dev/components
const DevComponentsView = import.meta.env.DEV ? defineAsyncComponent(() => import("./views/DevComponentsView.vue")) : null;
const showDev = computed(() => import.meta.env.DEV && window.location.hash === "#/dev/components");
</script>

<template>
  <component :is="DevComponentsView" v-if="showDev && DevComponentsView" />
  <AppLayout v-else>
    <!-- v-show y no v-if: el Monitor no desmonta el workspace (Monaco, resultados y ediciones pendientes siguen vivos). -->
    <div v-show="ui.mainView === 'queries'" class="h-full" data-testid="queries-area">
      <QueryWorkspace v-if="queryTabs.tabs.length" />
      <WelcomeView v-else />
    </div>
    <MonitorView v-if="monitorSeen" v-show="ui.mainView === 'monitor'" class="h-full" />
  </AppLayout>
  <!-- A nivel de App: una migración larga no depende de qué panel del sidebar esté abierto (spec 23). -->
  <MigrationDialog />
</template>
