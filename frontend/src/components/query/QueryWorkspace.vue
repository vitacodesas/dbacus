<script setup lang="ts">
import { computed, defineAsyncComponent, ref, useTemplateRef, watch } from "vue";
import { SquareTerminal } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { errorMarker } from "../../composables/queryMarkers";
import { useQueryActions } from "../../composables/useQueryActions";
import { useQueryTabsStore } from "../../stores/queryTabs";
import { useResultsStore } from "../../stores/results";
import { useSessionsStore } from "../../stores/sessions";
import { useUiStore } from "../../stores/ui";
import BaseButton from "../ui/BaseButton.vue";
import BaseEmptyState from "../ui/BaseEmptyState.vue";
import BaseSplitter from "../ui/BaseSplitter.vue";
import FileBanner from "../queries/FileBanner.vue";
import QueryTabsBar from "./QueryTabsBar.vue";
import QueryToolbar from "./QueryToolbar.vue";
import ResultPane from "./ResultPane.vue";
import type SqlEditorType from "./SqlEditor.vue";

// Monaco pesa ~3,8 MB: se carga al abrir la primera pestaña de consulta, no al arrancar la app.
const SqlEditor = defineAsyncComponent(() => import("./SqlEditor.vue"));

const { t } = useI18n();
const tabs = useQueryTabsStore();
const results = useResultsStore();
const sessions = useSessionsStore();
const ui = useUiStore();
const { newQuery, runner } = useQueryActions();

const active = computed(() => tabs.active);
const editor = useTemplateRef<InstanceType<typeof SqlEditorType>>("editor");
const session = computed(() => (active.value ? sessions.sessionFor(active.value.profileId) : null));
const database = computed(() => active.value?.database || session.value?.currentDatabase || "");

// Libera el modelo del editor de las pestañas que se cierran.
watch(() => tabs.tabs.map((x) => x.id), (now, before = []) => before.filter((id) => !now.includes(id)).forEach((id) => editor.value?.dispose(id)));

// El marcador de error se retira en cuanto el usuario edita el texto (ya no corresponde a lo ejecutado).
const dismissed = ref<Record<string, boolean>>({});
watch(() => active.value?.sql, () => {
  const id = active.value?.id;
  if (id && ["error", "canceled"].includes(results.of(id).status)) dismissed.value[id] = true;
});
watch(() => (active.value ? results.of(active.value.id).status : "idle"), (s) => {
  if (s === "running" && active.value) dismissed.value[active.value.id] = false;
});
const marker = computed(() => {
  const tab = active.value;
  if (!tab || dismissed.value[tab.id]) return null;
  const r = results.of(tab.id);
  if (r.status !== "error" || !r.errorRange || !r.error) return null;
  return errorMarker(tab.sql, { baseOffset: r.baseOffset, ...r.errorRange, message: r.error.message, detail: r.error.detail });
});

// Divisor editor/resultados
const split = useTemplateRef<HTMLElement>("split");
function onDrag(dy: number) {
  const h = split.value?.clientHeight;
  if (h) ui.setSplitRatio(ui.splitRatio + dy / h);
}

const runAll = () => active.value && runner.runAll(active.value.id);
const runSelection = () => active.value && editor.value && runner.runSelection(active.value.id, editor.value.selection());
const format = () => active.value && runner.format(active.value.id);
const stop = () => active.value && runner.cancel(active.value.id);
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <QueryTabsBar @new="newQuery" />

    <template v-if="active">
      <FileBanner :tab="active" />
      <QueryToolbar :tab="active" @run="runAll" @run-selection="runSelection" @stop="stop" @format="format" />
      <div ref="split" class="flex min-h-0 flex-1 flex-col">
        <div class="min-h-12 overflow-hidden" :style="{ flexBasis: `${ui.splitRatio * 100}%`, flexGrow: 0, flexShrink: 0 }">
          <SqlEditor
            ref="editor"
            :tab-id="active.id"
            :model-value="active.sql"
            :session-id="session?.sessionId ?? null"
            :database="database"
            :marker="marker"
            @update:model-value="tabs.setSql(active.id, $event)"
            @run="runAll"
            @run-selection="runSelection"
            @format="format"
          />
        </div>
        <BaseSplitter orientation="horizontal" @drag="onDrag" />
        <div class="min-h-0 flex-1"><ResultPane :tab-id="active.id" @cancel="stop" @rerun="runAll" /></div>
      </div>
    </template>

    <div v-else class="grid flex-1 place-items-center">
      <BaseEmptyState :title="t('query.emptyTitle')" :description="t('query.emptyDescription')">
        <template #icon><SquareTerminal class="size-10" /></template>
        <BaseButton variant="primary" @click="newQuery">{{ t("query.newTab") }}</BaseButton>
      </BaseEmptyState>
    </div>
  </div>
</template>
