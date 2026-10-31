<script setup lang="ts">
import { useUiStore } from "../../stores/ui";
import BaseSplitter from "../ui/BaseSplitter.vue";
import BaseToast from "../ui/BaseToast.vue";
import EditDiscardDialog from "../query/EditDiscardDialog.vue";
import EditReviewDialog from "../query/EditReviewDialog.vue";
import QueryEntryDialog from "../queries/QueryEntryDialog.vue";
import SaveQueryDialog from "../queries/SaveQueryDialog.vue";
import TabCloseDialog from "../queries/TabCloseDialog.vue";
import SettingsDialog from "../settings/SettingsDialog.vue";
import ConnectDialogs from "../connections/ConnectDialogs.vue";
import ConnectionFormDialog from "../connections/ConnectionFormDialog.vue";
import DeleteConnectionDialog from "../connections/DeleteConnectionDialog.vue";
import ActivityBar from "./ActivityBar.vue";
import SidebarPanel from "./SidebarPanel.vue";
import StatusBar from "./StatusBar.vue";
import { isMac } from "../../composables/useShortcuts";

const ui = useUiStore();
</script>

<template>
  <div class="flex h-full flex-col bg-bg text-fg">
    <!-- Con la barra de título oculta en macOS, esta franja deja espacio a los botones nativos y permite arrastrar la ventana. -->
    <header class="flex h-9 shrink-0 items-center border-b border-border bg-panel text-xs text-muted select-none" style="--wails-draggable: drag" :class="isMac ? 'pl-20' : 'pl-3'">
      <slot name="titlebar" />
    </header>
    <div class="flex min-h-0 flex-1">
      <ActivityBar />
      <template v-if="!ui.sidebarCollapsed">
        <aside class="min-w-0 shrink-0 overflow-auto bg-panel" :style="{ width: `${ui.sidebarWidth}px` }">
          <slot name="sidebar"><SidebarPanel /></slot>
        </aside>
        <BaseSplitter orientation="vertical" @drag="ui.resizeSidebar" />
      </template>
      <main class="min-w-0 flex-1 overflow-auto"><slot /></main>
    </div>
    <StatusBar />
    <ConnectionFormDialog />
    <DeleteConnectionDialog />
    <ConnectDialogs />
    <SettingsDialog />
    <EditReviewDialog />
    <EditDiscardDialog />
    <SaveQueryDialog />
    <QueryEntryDialog />
    <TabCloseDialog />
    <BaseToast />
  </div>
</template>
