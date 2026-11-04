<script setup lang="ts">
// Pestaña «Métricas» (spec 22): series de tiempo con huecos rotulados y anotaciones. Go entrega ≤ 300 puntos por serie ya
// agregados; aquí solo se pintan. El clic en una gráfica (o el botón «Añadir nota») abre el diálogo de anotación.
import { computed, ref, watch } from "vue";
import { NotebookPen, RefreshCw, X } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { formatCompact } from "../../composables/timeSeries";
import { METRIC_KEYS, useMonitorStore, type MetricKey } from "../../stores/monitor";
import type { Series } from "../../types";
import BaseButton from "../ui/BaseButton.vue";
import BaseEmptyState from "../ui/BaseEmptyState.vue";
import BaseInput from "../ui/BaseInput.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";
import SourceEmpty from "./SourceEmpty.vue";
import TimeSeriesChart from "./TimeSeriesChart.vue";

const props = defineProps<{ active: boolean }>();
const { t, locale } = useI18n();
const store = useMonitorStore();

const unavailable = computed(() => store.currentCaps?.status.state === "unavailable");
const reload = () => void store.loadSeries([...METRIC_KEYS]);

watch(
  () => [props.active, store.profileId, store.range, unavailable.value] as const,
  ([active, pid, , off]) => active && pid && !off && reload(),
  { immediate: true },
);
// Con el monitoreo en marcha cada muestra nueva refresca las gráficas (Series cuesta ms y viene ya reducida por Go).
watch(() => store.currentRun?.samples, () => props.active && !unavailable.value && reload());

const byMetric = computed(() => new Map(store.series.map((s) => [s.metric, s])));
const serie = (m: MetricKey): Series => byMetric.value.get(m) ?? { metric: m, level: "raw", points: [], gaps: [], annotations: [] };
const hasData = computed(() => store.series.some((s) => (s.points?.length ?? 0) > 0));
// Las anotaciones llegan iguales en cada serie: se leen de la primera.
const annotations = computed(() => store.series[0]?.annotations ?? []);

const unit: Partial<Record<MetricKey, string>> = { bp_hit_ratio: "%", bytes_received: "B/s", bytes_sent: "B/s", qps: "/s" };
const last = (m: MetricKey) => {
  const p = serie(m).points;
  return p && p.length ? p[p.length - 1]!.v : null;
};
const fmtLast = (m: MetricKey) => {
  const v = last(m);
  if (v === null) return "—";
  return m === "bp_hit_ratio" ? `${v.toFixed(2)} %` : formatCompact(v, locale.value);
};
const QUICK: MetricKey[] = ["qps", "bp_hit_ratio", "tmp_disk_tables", "slow_queries"];

// --- anotaciones ---
const dialog = ref(false);
const noteAt = ref("");
const noteText = ref("");
const noteError = ref("");
const saving = ref(false);
function askNote(at?: string) {
  noteAt.value = at ?? new Date().toISOString();
  noteText.value = "";
  noteError.value = "";
  dialog.value = true;
}
async function saveNote() {
  saving.value = true;
  const a = await store.annotate(noteText.value.trim(), noteAt.value);
  saving.value = false;
  if (!a) {
    noteError.value = store.error?.detail === "text" ? t("monitor.metrics.noteInvalid") : (store.error?.message ?? "");
    return;
  }
  dialog.value = false;
  reload();
}
async function removeNote(id: string) {
  if (await store.deleteAnnotation(id)) reload();
}
const when = (iso: string) => new Date(iso).toLocaleString();
</script>

<template>
  <div class="flex min-h-0 flex-col gap-3 overflow-auto pt-3" data-testid="monitor-metrics">
    <SourceEmpty v-if="unavailable" source="status" />
    <template v-else>
      <div class="flex flex-wrap items-center gap-2">
        <p class="text-xs text-muted">{{ t("monitor.metrics.hint") }}</p>
        <span class="flex-1" />
        <BaseButton size="sm" data-testid="metrics-add-note" @click="askNote()"><NotebookPen class="size-3.5" /> {{ t("monitor.metrics.addNote") }}</BaseButton>
        <BaseButton size="sm" :loading="store.loadingSeries" :aria-label="t('monitor.metrics.refresh')" data-testid="metrics-refresh" @click="reload"><RefreshCw class="size-3.5" /></BaseButton>
      </div>

      <div v-if="!store.seriesLoaded" class="flex items-center justify-center gap-2 py-10 text-sm text-muted" data-testid="metrics-loading"><BaseSpinner /> {{ t("monitor.top.loading") }}</div>
      <BaseEmptyState v-else-if="!hasData" :title="t('monitor.metrics.empty')" :description="t('monitor.metrics.emptyHint')" data-testid="metrics-empty" />
      <template v-else>
        <div class="grid grid-cols-2 gap-3 lg:grid-cols-4" data-testid="metrics-quick">
          <div v-for="m in QUICK" :key="m" class="rounded-md bg-hover px-3 py-2" :data-testid="`quick-${m}`"><p class="text-xs text-muted">{{ t(`monitor.metrics.names.${m}`) }}</p><p class="text-xl font-medium">{{ fmtLast(m) }}</p></div>
        </div>
        <div class="grid grid-cols-1 gap-4 xl:grid-cols-2" data-testid="metrics-charts">
          <section v-for="m in METRIC_KEYS" :key="m" class="rounded-md border border-border p-3" :data-testid="`chart-${m}`" :aria-label="t(`monitor.metrics.names.${m}`)">
            <h3 class="mb-1 text-sm font-medium">{{ t(`monitor.metrics.names.${m}`) }}</h3>
            <TimeSeriesChart :series="serie(m)" :label="t(`monitor.metrics.names.${m}`)" :unit="unit[m]" :height="150" annotatable @annotate="askNote" />
          </section>
        </div>
        <section v-if="annotations.length" class="rounded-md border border-border p-3" data-testid="metrics-annotations">
          <h3 class="mb-1 text-sm font-medium">{{ t("monitor.metrics.notes") }}</h3>
          <ul class="flex flex-col gap-1">
            <li v-for="a in annotations" :key="a.id" class="flex items-center gap-2 text-sm" :data-testid="`note-${a.id}`">
              <span class="font-data text-xs text-muted">{{ when(a.at) }}</span>
              <span class="min-w-0 flex-1 truncate">{{ a.text }}</span>
              <BaseButton size="sm" variant="ghost" :aria-label="t('monitor.metrics.deleteNote')" data-testid="note-delete" @click="removeNote(a.id)"><X class="size-3.5" /></BaseButton>
            </li>
          </ul>
        </section>
      </template>
    </template>

    <BaseModal v-model:open="dialog" :title="t('monitor.metrics.noteTitle')" :description="when(noteAt)" size="sm">
      <form @submit.prevent="saveNote">
        <BaseInput v-model="noteText" :label="t('monitor.metrics.noteLabel')" :placeholder="t('monitor.metrics.notePlaceholder')" maxlength="200" :error="noteError" data-testid="note-input" />
      </form>
      <template #footer>
        <BaseButton variant="ghost" @click="dialog = false">{{ t("common.cancel") }}</BaseButton>
        <BaseButton variant="primary" :disabled="!noteText.trim()" :loading="saving" data-testid="note-save" @click="saveNote">{{ t("common.save") }}</BaseButton>
      </template>
    </BaseModal>
  </div>
</template>
