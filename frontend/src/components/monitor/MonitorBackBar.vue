<script setup lang="ts">
import { ArrowLeft } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { useQueryTabsStore } from "../../stores/queryTabs";
import { useUiStore } from "../../stores/ui";

// Franja para volver a las consultas: las pestañas siguen vivas detrás del Monitor, pero su barra no se ve (spec 22 ⚑D12).
const { t } = useI18n();
const tabs = useQueryTabsStore();
const ui = useUiStore();
</script>

<template>
  <div class="flex min-w-0 items-center gap-2" data-testid="monitor-back">
    <button
      class="flex shrink-0 items-center gap-1 rounded-md border border-border px-2 py-1 text-sm hover:bg-hover"
      :title="t('monitor.back.none')"
      data-testid="monitor-back-btn"
      @click="ui.mainView = 'queries'"
    >
      <ArrowLeft class="size-4" aria-hidden="true" />
      {{ tabs.tabs.length ? t("monitor.back.open", { n: tabs.tabs.length }) : t("monitor.back.none") }}
    </button>
    <div v-if="tabs.tabs.length" class="flex min-w-0 items-center gap-1 overflow-x-auto" role="list" :aria-label="t('monitor.back.aria')">
      <button
        v-for="tab in tabs.tabs"
        :key="tab.id"
        role="listitem"
        class="max-w-44 shrink-0 truncate rounded-md px-2 py-1 text-xs text-muted hover:bg-hover hover:text-fg"
        :class="tab.id === tabs.activeTabId && 'bg-hover text-fg'"
        :title="tab.filePath || tab.title"
        :data-testid="`monitor-back-tab-${tab.id}`"
        @click="tabs.setActive(tab.id)"
      >
        <span v-if="tabs.isDirty(tab)" class="mr-1" :title="t('monitor.back.unsaved')">●</span>{{ tab.title }}
      </button>
    </div>
  </div>
</template>
