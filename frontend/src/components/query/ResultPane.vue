<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useResultsStore } from "../../stores/results";
import { useUiStore } from "../../stores/ui";
import { Download } from "lucide-vue-next";
import ExportDialog from "../export/ExportDialog.vue";
import BaseButton from "../ui/BaseButton.vue";
import BaseEmptyState from "../ui/BaseEmptyState.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";
import ResultGrid from "./ResultGrid.vue";

const props = defineProps<{ tabId: string }>();
const emit = defineEmits<{ cancel: []; rerun: [] }>();

const { t } = useI18n();
const results = useResultsStore();
const ui = useUiStore();

const res = computed(() => results.of(props.tabId));
const MESSAGES = -1; // "índice" de la pestaña Mensajes

// Pestañas de resultado: una por sentencia que devolvió filas.
const rowStatements = computed(() => res.value.statements.map((s, i) => (s?.kind === "rows" ? i : -1)).filter((i) => i >= 0));
const hasMessages = computed(() => res.value.statements.some((s) => s) || !!res.value.error);
const picked = ref(0); // lo que eligió el usuario: índice de sentencia o MESSAGES
watch(() => [props.tabId, res.value.queryId], () => (picked.value = res.value.activeStmt), { immediate: true });
watch(() => res.value.activeStmt, (i) => (picked.value = i));
// Una ejecución fallida o cancelada sin ningún resultado con filas solo tiene qué mostrar en Mensajes (también al volver a la pestaña).
const current = computed(() =>
  (res.value.status === "error" || res.value.status === "canceled") && rowStatements.value.length === 0 ? MESSAGES : picked.value,
);

const stmt = computed(() => res.value.statements[current.value]);
const rowCount = computed(() => res.value.rowCounts[current.value] ?? stmt.value?.rowCount ?? 0);
const showGrid = computed(() => current.value !== MESSAGES && stmt.value?.kind === "rows" && (stmt.value.columns?.length ?? 0) > 0);

// Cronómetro mientras corre
const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | null = null;
watch(() => res.value.status === "running", (on) => {
  if (timer) clearInterval(timer);
  timer = on ? setInterval(() => (now.value = Date.now()), 200) : null;
}, { immediate: true });
onBeforeUnmount(() => timer && clearInterval(timer));
const elapsed = computed(() => (res.value.status === "running" ? now.value - res.value.startedAt : res.value.elapsedMs));

const received = computed(() => Object.values(res.value.rowCounts).reduce((a, b) => a + b, 0));

// Rango visible que informa el grid
const exportOpen = ref(false);
const range = ref<[number, number]>([0, 0]);
const fmt = (n: number) => n.toLocaleString();
const rangeText = computed(() => {
  const total = rowCount.value;
  if (!total) return t("query.noRows");
  const [a, b] = range.value;
  const base = t("query.rowsRange", { from: fmt(a + 1), to: fmt(Math.min(b, total)), total: fmt(total) });
  return stmt.value?.truncated ? `${base} · ${t("query.truncated", { total: fmt(total) })}` : base;
});

const errorText = computed(() => [res.value.error?.message, res.value.error?.detail].filter(Boolean).join("\n"));
async function copyError() {
  try {
    await navigator.clipboard.writeText(errorText.value);
    ui.toast("success", t("query.copied"));
  } catch {
    ui.toast("error", t("query.cannotCopy"));
  }
}
const summary = (i: number) => {
  const s = res.value.statements[i];
  if (!s) return "";
  if (s.kind === "rows") return t("query.rowsTotal", { total: fmt(res.value.rowCounts[i] ?? s.rowCount) });
  const parts = [t("query.affected", { n: fmt(s.affectedRows) })];
  if (s.lastInsertId) parts.push(t("query.lastInsert", { id: s.lastInsertId }));
  if (s.warnings) parts.push(t("query.warnings", { n: s.warnings }));
  return parts.join(" · ");
};
</script>

<template>
  <section class="flex h-full min-h-0 flex-col bg-panel" :aria-label="t('query.messages')" data-testid="result-pane">
    <div v-if="res.status === 'idle'" class="grid flex-1 place-items-center"><BaseEmptyState :title="t('query.idle')" /></div>

    <template v-else>
      <!-- Pestañas de resultados -->
      <div class="flex shrink-0 items-center gap-1 border-b border-border px-2" role="tablist">
        <button
          v-for="(si, n) in rowStatements"
          :key="si"
          role="tab"
          :aria-selected="current === si"
          class="-mb-px border-b-2 px-3 py-1.5 text-xs"
          :class="current === si ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg'"
          @click="picked = si; results.setActiveStatement(tabId, si)"
        >
          {{ t("query.result", { n: n + 1 }) }}
        </button>
        <button
          v-if="hasMessages"
          role="tab"
          :aria-selected="current === MESSAGES"
          class="-mb-px border-b-2 px-3 py-1.5 text-xs"
          :class="current === MESSAGES ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg'"
          @click="picked = MESSAGES"
        >
          {{ t("query.messages") }}
        </button>
        <span class="ml-auto flex items-center gap-2 pr-1 text-xs text-muted">
          <template v-if="res.status === 'running'">
            <BaseSpinner class="size-3.5" />
            {{ t("query.running") }} {{ t("query.rowsReceived", { n: fmt(received) }) }}
          </template>
          <span class="font-data">{{ t("query.took", { ms: fmt(elapsed) }) }}</span>
        </span>
        <BaseButton v-if="showGrid && res.status !== 'running' && res.queryId" size="sm" variant="ghost" data-testid="export-open" :title="t('export.button')" @click="exportOpen = true">
          <Download class="size-3.5" />{{ t("export.button") }}
        </BaseButton>
        <BaseButton v-if="res.status === 'running'" size="sm" variant="danger" @click="emit('cancel')">{{ t("query.stop") }}</BaseButton>
      </div>

      <!-- Avisos -->
      <div v-if="res.expired" class="flex items-center gap-3 border-b border-border bg-warning/10 px-3 py-1.5 text-sm" role="status">
        {{ t("query.expired") }}
        <BaseButton size="sm" @click="emit('rerun')">{{ t("query.rerun") }}</BaseButton>
      </div>
      <div v-if="res.status === 'canceled'" class="border-b border-border bg-warning/10 px-3 py-1.5 text-sm" role="status">{{ t("query.canceled") }}</div>

      <!-- Contenido -->
      <div class="min-h-0 flex-1">
        <ResultGrid
          v-if="showGrid && res.queryId && stmt"
          :key="`${res.queryId}:${current}`"
          :tab-id="tabId"
          :query-id="res.queryId"
          :stmt-index="current"
          :columns="stmt.columns ?? []"
          :row-count="rowCount"
          @visible-range="(a, b) => (range = [a, b])"
          @cancel="emit('cancel')"
        />
        <div v-else-if="current !== MESSAGES && res.status === 'done'" class="grid h-full place-items-center">
          <BaseEmptyState :title="stmt?.kind === 'exec' ? summary(current) : t('query.noRows')" />
        </div>
        <div v-else-if="current === MESSAGES" class="h-full overflow-auto p-3 text-sm" data-testid="messages">
          <ul class="flex flex-col gap-2">
            <li v-for="(s, i) in res.statements" :key="i" class="rounded-md border border-border p-2">
              <div class="font-data truncate text-xs text-muted" :title="s?.sql">{{ s?.sql }}</div>
              <div class="mt-1 flex flex-wrap items-center gap-x-3 text-sm">
                <span class="text-success">✓</span>
                <span>{{ summary(i) }}</span>
                <span class="font-data text-xs text-muted">{{ t("query.took", { ms: fmt(s?.elapsedMs ?? 0) }) }}</span>
                <span v-if="s?.limitApplied" class="text-xs text-muted">{{ t("query.limitApplied", { n: s.limitApplied }) }}</span>
                <span v-if="s?.truncated" class="text-xs text-warning">{{ t("query.truncated", { total: fmt(s.rowCount) }) }}</span>
              </div>
            </li>
          </ul>
          <div v-if="res.error" class="mt-2 rounded-md border border-danger p-3" role="alert">
            <div class="flex items-center justify-between gap-2">
              <strong class="text-danger">{{ t("query.errorTitle") }}</strong>
              <BaseButton size="sm" @click="copyError">{{ t("query.copyError") }}</BaseButton>
            </div>
            <p class="mt-1 break-words whitespace-pre-wrap">{{ res.error.message }}</p>
            <p v-if="res.error.detail" class="font-data mt-1 text-xs break-words whitespace-pre-wrap text-muted">{{ res.error.detail }}</p>
            <p class="font-data mt-1 text-[10px] text-muted">{{ res.error.code }}</p>
          </div>
        </div>
      </div>

      <!-- Estado del grid -->
      <footer v-if="showGrid" class="flex shrink-0 items-center gap-4 border-t border-border px-3 py-1 text-xs text-muted">
        <span data-testid="range">{{ rangeText }}</span>
        <span v-if="stmt?.limitApplied" class="text-warning">{{ t("query.limitApplied", { n: stmt.limitApplied }) }}</span>
        <span v-if="res.database" class="font-data ml-auto">{{ res.database }}</span>
      </footer>
    </template>
    <ExportDialog
      v-if="res.queryId && stmt"
      v-model:open="exportOpen"
      :query-id="res.queryId"
      :stmt-index="current"
      :truncated="!!stmt.truncated"
      :limit-applied="stmt.limitApplied ?? 0"
      :row-count="rowCount"
    />
  </section>
</template>
