<script setup lang="ts">
import { nextTick, ref } from "vue";
import { Plus, X } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { asAppError } from "../../api/errors";
import { useQueryTabsStore } from "../../stores/queryTabs";
import { useQueryFilesStore } from "../../stores/queryFiles";
import { useResultsStore } from "../../stores/results";
import BaseSpinner from "../ui/BaseSpinner.vue";
import { useUiStore } from "../../stores/ui";
import { modKey } from "../../composables/useShortcuts";

const emit = defineEmits<{ new: [] }>();
const { t } = useI18n();
const tabs = useQueryTabsStore();
const results = useResultsStore();
const files = useQueryFilesStore();
const ui = useUiStore();

const editing = ref<string | null>(null);
const draft = ref("");
const input = ref<HTMLInputElement[] | HTMLInputElement | null>(null);

async function startRename(id: string, title: string) {
  editing.value = id;
  draft.value = title;
  await nextTick();
  const el = Array.isArray(input.value) ? input.value[0] : input.value;
  el?.focus();
  el?.select();
}
async function commit() {
  const id = editing.value;
  editing.value = null;
  if (!id) return;
  const tab = tabs.byId(id);
  const name = draft.value.trim();
  // Renombrar una pestaña vinculada renombra su archivo (spec 20); si Go lo rechaza se avisa y el título no cambia.
  if (tab?.filePath && name && name !== tab.title) {
    const dir = tab.filePath.includes("/") ? tab.filePath.slice(0, tab.filePath.lastIndexOf("/") + 1) : "";
    try {
      await files.moveEntry(tab.filePath, `${dir}${name}`);
      void files.load();
    } catch (e) {
      ui.toast("error", asAppError(e).message);
    }
    return;
  }
  tabs.rename(id, draft.value);
}

// Reordenar arrastrando
let dragFrom = -1;
const onDragStart = (i: number, e: DragEvent) => {
  dragFrom = i;
  e.dataTransfer?.setData("text/plain", String(i));
};
const onDrop = (i: number) => {
  tabs.move(dragFrom, i);
  dragFrom = -1;
};
</script>

<template>
  <div class="flex h-9 shrink-0 items-end border-b border-border bg-bg" role="tablist" :aria-label="t('query.newTab')">
    <div class="flex min-w-0 flex-1 items-end overflow-x-auto">
      <div
        v-for="(tab, i) in tabs.tabs"
        :key="tab.id"
        role="tab"
        :aria-selected="tab.id === tabs.activeTabId"
        :tabindex="tab.id === tabs.activeTabId ? 0 : -1"
        draggable="true"
        class="group flex h-8 max-w-48 shrink-0 cursor-default items-center gap-1.5 border-r border-border px-3 text-sm"
        :class="tab.id === tabs.activeTabId ? 'bg-panel text-fg' : 'text-muted hover:bg-hover'"
        :data-testid="`tab-${tab.title}`"
        @click="tabs.setActive(tab.id)"
        @auxclick.middle.prevent="tabs.closeTab(tab.id)"
        @dblclick="startRename(tab.id, tab.title)"
        @dragstart="onDragStart(i, $event)"
        @dragover.prevent
        @drop.prevent="onDrop(i)"
        @keydown.f2.prevent="startRename(tab.id, tab.title)"
      >
        <BaseSpinner v-if="results.running(tab.id)" class="size-3 shrink-0" />
        <input
          v-if="editing === tab.id"
          ref="input"
          v-model="draft"
          class="w-28 rounded border border-accent bg-bg px-1 text-sm"
          :aria-label="t('query.rename')"
          @keydown.enter.prevent="commit"
          @keydown.esc.prevent="editing = null"
          @blur="commit"
          @click.stop
        >
        <span v-else class="truncate" :title="tab.filePath || undefined">{{ tab.title }}</span>
        <span
          v-if="editing !== tab.id && tabs.isDirty(tab) && (tab.filePath || tab.sql.trim())"
          class="shrink-0 text-accent"
          :title="t('queries.unsavedDot')"
          role="img"
          :aria-label="t('queries.unsavedDot')"
          data-testid="tab-dirty"
        >●</span>
        <button
          type="button"
          class="grid size-4 shrink-0 place-items-center rounded text-muted opacity-0 group-hover:opacity-100 hover:bg-border hover:text-fg focus-visible:opacity-100"
          :class="tab.id === tabs.activeTabId && 'opacity-100'"
          :aria-label="`${t('query.closeTab')}: ${tab.title}`"
          @click.stop="tabs.closeTab(tab.id)"
        >
          <X class="size-3" />
        </button>
      </div>
    </div>
    <button
      type="button"
      class="grid size-8 shrink-0 place-items-center text-muted hover:bg-hover hover:text-fg"
      :aria-label="`${t('query.newTab')} (${modKey}T)`"
      :title="`${t('query.newTab')} (${modKey}T)`"
      @click="emit('new')"
    >
      <Plus class="size-4" />
    </button>
  </div>
</template>
