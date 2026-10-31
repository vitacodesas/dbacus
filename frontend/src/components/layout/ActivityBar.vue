<script setup lang="ts">
import { Database, FileCode, FolderTree, History, Settings } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { useUiStore, type SidebarView } from "../../stores/ui";
import BaseTooltip from "../ui/BaseTooltip.vue";

const ui = useUiStore();
const { t } = useI18n();
const items: { view: SidebarView; icon: typeof Database; label: string }[] = [
  { view: "connections", icon: Database, label: "activity.connections" },
  { view: "schema", icon: FolderTree, label: "activity.schema" },
  { view: "queries", icon: FileCode, label: "activity.queries" },
  { view: "history", icon: History, label: "activity.history" },
];
</script>

<template>
  <nav class="flex w-11 shrink-0 flex-col items-center gap-1 border-r border-border bg-panel py-2" :aria-label="t('app.name')">
    <BaseTooltip v-for="item in items" :key="item.view" :text="t(item.label)">
      <button
        class="grid size-9 place-items-center rounded-md text-muted hover:bg-hover hover:text-fg"
        :class="ui.sidebarView === item.view && !ui.sidebarCollapsed && 'bg-hover text-fg'"
        :aria-label="t(item.label)"
        :aria-pressed="ui.sidebarView === item.view && !ui.sidebarCollapsed"
        @click="ui.selectSidebarView(item.view)"
      >
        <component :is="item.icon" class="size-5" />
      </button>
    </BaseTooltip>
    <div class="mt-auto">
      <BaseTooltip :text="t('activity.settings')">
        <button class="grid size-9 place-items-center rounded-md text-muted hover:bg-hover hover:text-fg" :aria-label="t('activity.settings')" data-testid="open-settings" @click="ui.settingsOpen = true">
          <Settings class="size-5" />
        </button>
      </BaseTooltip>
    </div>
  </nav>
</template>
