<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, onUnmounted } from "vue";
import AppLayout from "./components/layout/AppLayout.vue";
import QueryWorkspace from "./components/query/QueryWorkspace.vue";
import WelcomeView from "./views/WelcomeView.vue";
import { useConnectFlow } from "./composables/useConnectFlow";
import { useShortcuts } from "./composables/useShortcuts";
import { useTheme } from "./composables/useTheme";
import { startEventDispatch } from "./events";
import { useConnectionsStore } from "./stores/connections";
import { useQueryTabsStore } from "./stores/queryTabs";
import { useSessionsStore } from "./stores/sessions";
import { useUiStore } from "./stores/ui";

useTheme();
useShortcuts();
const queryTabs = useQueryTabsStore();

// Arranque: perfiles, sesiones vivas en Go y suscripción a eventos.
let stopEvents: (() => void) | null = null;
onMounted(() => {
  stopEvents = startEventDispatch();
  const flow = useConnectFlow();
  useSessionsStore().reconnect = (profileId) => void flow.connect(profileId);
  void useUiStore().load();
  void useConnectionsStore().load();
  void useSessionsStore().refresh();
  void queryTabs.restore();
});
onUnmounted(() => stopEvents?.());

// Galería de componentes solo en dev: http://localhost:9245/#/dev/components
const DevComponentsView = import.meta.env.DEV ? defineAsyncComponent(() => import("./views/DevComponentsView.vue")) : null;
const showDev = computed(() => import.meta.env.DEV && window.location.hash === "#/dev/components");
</script>

<template>
  <component :is="DevComponentsView" v-if="showDev && DevComponentsView" />
  <AppLayout v-else>
    <QueryWorkspace v-if="queryTabs.tabs.length" />
    <WelcomeView v-else />
  </AppLayout>
</template>
