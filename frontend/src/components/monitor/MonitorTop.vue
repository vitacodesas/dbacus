<script setup lang="ts">
// Pestaña «Top queries» (spec 22): Go calcula y ordena el ranking; aquí solo se elige el criterio y se pinta.
import { computed, watch } from "vue";
import { Copy, TriangleAlert } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { digestSeries, formatCompact } from "../../composables/timeSeries";
import { ORDER_KEYS, useMonitorStore, type OrderKey } from "../../stores/monitor";
import { useUiStore } from "../../stores/ui";
import type { DigestStat } from "../../types";
import BaseButton from "../ui/BaseButton.vue";
import BaseCheckbox from "../ui/BaseCheckbox.vue";
import BaseEmptyState from "../ui/BaseEmptyState.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";
import SourceEmpty from "./SourceEmpty.vue";
import TimeSeriesChart from "./TimeSeriesChart.vue";

const props = defineProps<{ active: boolean }>();
const { t, locale } = useI18n();
const store = useMonitorStore();
const ui = useUiStore();

const unavailable = computed(() => store.currentCaps?.digest.state === "unavailable");
const compare = computed(() => store.topCompare);

// Se pide solo con la pestaña a la vista y cuando cambia algo que afecta al resultado: el ranking del rango de 7 días
// tarda ~0,7 s en Go, así que no se recarga a ciegas.
watch(
  () => [props.active, store.profileId, store.range, store.schema, store.topOrder, store.topCompare, unavailable.value] as const,
  ([active, pid, , , , cmp, off]) => {
    if (!active || !pid || off) return;
    void store.loadTop();
    if (cmp) void store.loadComparison();
  },
  { immediate: true },
);

const orderOptions = computed(() => ORDER_KEYS.map((k) => ({ value: k, label: t(`monitor.top.order.${k}`) })));
const ms = (v: number) => (v >= 60_000 ? `${(v / 60_000).toFixed(1)} min` : v >= 1000 ? `${(v / 1000).toFixed(1)} s` : `${v >= 100 ? Math.round(v) : v.toFixed(1)} ms`);
const num = (v: number) => formatCompact(v, locale.value);
const pct = (v: number) => `${v > 0 ? "+" : ""}${v.toFixed(1)} %`;
const anyApprox = computed(() => store.top.some((s) => s.approx));

const openDigest = (s: DigestStat) => void store.loadDigest(s.digest);
const detailOpen = computed({ get: () => !!store.digestDetail || store.loadingDigest, set: (v: boolean) => !v && store.closeDigest() });

const detailStat = computed(() => store.digestDetail?.stat ?? null);
async function copyText(text: string) {
  await navigator.clipboard.writeText(text);
  ui.toast("success", t("monitor.copied"));
}
// La consulta pide al menos 150 px: con menos ancho la tabla se desplaza en horizontal en vez de cortar el texto a «SELECT…».
const GRID = "grid grid-cols-[24px_minmax(150px,1fr)_72px_64px_68px_64px_96px_56px_60px_44px_92px] items-center gap-2 px-3";
const selectedOrder = computed({ get: () => store.topOrder, set: (v: string) => (store.topOrder = v as OrderKey) });
const regressionCount = computed(() => store.comparison?.regressions?.length ?? 0);
const newCount = computed(() => store.comparison?.new?.length ?? 0);
const goneList = computed(() => store.comparison?.gone ?? []);
</script>

<template>
  <div class="flex min-h-0 flex-col gap-3 pt-3" data-testid="monitor-top">
    <SourceEmpty v-if="unavailable" source="digest" />
    <template v-else>
      <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div role="radiogroup" :aria-label="t('monitor.top.criterion')" class="flex flex-wrap gap-1.5" data-testid="top-criteria">
          <button
            v-for="o in orderOptions" :key="o.value" type="button" role="radio" :aria-checked="selectedOrder === o.value"
            class="rounded-md border px-2.5 py-1 text-xs" :class="selectedOrder === o.value ? 'border-accent bg-accent/10 text-fg' : 'border-border text-muted hover:text-fg'"
            :data-testid="`top-order-${o.value}`" @click="selectedOrder = o.value"
          >
            {{ o.label }}
          </button>
        </div>
        <span class="flex-1" />
        <BaseCheckbox v-model="store.topCompare" :label="t('monitor.top.compare')" data-testid="top-compare" />
      </div>

      <p v-if="compare && (regressionCount || newCount || goneList.length)" class="flex flex-wrap items-center gap-2 text-xs" data-testid="top-summary">
        <span class="rounded bg-danger/10 px-2 py-0.5 text-danger" data-testid="top-summary-regressions">{{ t("monitor.top.regressions", { n: regressionCount }) }}</span>
        <span class="rounded bg-success/10 px-2 py-0.5 text-success" data-testid="top-summary-new">{{ t("monitor.top.newOnes", { n: newCount }) }}</span>
        <span class="rounded bg-hover px-2 py-0.5 text-muted" data-testid="top-summary-gone">{{ t("monitor.top.gone", { n: goneList.length }) }}</span>
      </p>
      <details v-if="compare && goneList.length" class="text-xs" data-testid="top-gone">
        <summary class="cursor-pointer text-muted">{{ t("monitor.top.goneTitle") }}</summary>
        <ul class="mt-1 flex flex-col gap-1">
          <li v-for="g in goneList" :key="g.schema + g.digest" class="font-data truncate" :title="g.text">{{ g.text }}</li>
        </ul>
      </details>

      <div v-if="!store.topLoaded && store.loadingTop" class="flex items-center justify-center gap-2 py-10 text-sm text-muted" data-testid="top-loading"><BaseSpinner /> {{ t("monitor.top.loading") }}</div>
      <BaseEmptyState v-else-if="store.topLoaded && !store.top.length" :title="t('monitor.top.empty')" :description="t('monitor.top.emptyHint')" data-testid="top-empty" />
      <div v-else-if="store.top.length" class="min-h-0 overflow-x-auto rounded-md border border-border" :aria-busy="store.loadingTop || undefined">
        <div class="flex min-w-[894px] flex-col">
          <div class="border-b border-border py-1.5 text-xs text-muted" :class="GRID" role="row">
            <span>#</span><span>{{ t("monitor.top.query") }}</span><span>{{ t("monitor.live.db") }}</span><span class="text-right">{{ t("monitor.top.execs") }}</span>
            <span class="text-right">{{ t("monitor.top.total") }}</span><span class="text-right">{{ t("monitor.top.avg") }}</span><span class="text-right">{{ t("monitor.top.rows") }}</span>
            <span class="text-right">{{ t("monitor.top.noIndex") }}</span><span class="text-right">{{ t("monitor.top.tmpDisk") }}</span><span class="text-right">{{ t("monitor.top.errors") }}</span>
            <span class="text-right">{{ t("monitor.top.change") }}</span>
          </div>
          <div class="min-h-0 overflow-auto" :class="store.loadingTop && 'opacity-60'" data-testid="top-list">
            <div
              v-for="(s, i) in store.top" :key="s.schema + '/' + s.digest" class="h-8 cursor-pointer text-sm hover:bg-hover" :class="GRID"
              role="row" tabindex="0" :data-testid="`top-row-${s.digest}`" @click="openDigest(s)" @keydown.enter="openDigest(s)"
            >
              <span class="text-xs text-muted">{{ i + 1 }}</span>
              <span class="font-data truncate text-xs" :title="s.text">{{ s.text }}</span>
              <span class="truncate">{{ s.schema || "—" }}</span>
              <span class="text-right" :title="String(s.execs)">{{ s.approx ? "≈ " : "" }}{{ num(s.execs) }}</span>
              <span class="text-right">{{ ms(s.totalMs) }}</span>
              <span class="text-right">{{ ms(s.avgMs) }}</span>
              <span class="text-right text-xs" :title="t('monitor.top.rowsHint')">{{ num(s.rowsExamined) }} / {{ num(s.rowsSent) }}</span>
              <span class="text-right" :class="s.noIndex > 0 && 'text-warning'">{{ s.noIndex ? num(s.noIndex) : "—" }}</span>
              <span class="text-right" :class="s.tmpDisk > 0 && 'text-warning'">{{ s.tmpDisk ? num(s.tmpDisk) : "—" }}</span>
              <span class="text-right" :class="s.errors > 0 && 'text-danger'">{{ s.errors ? num(s.errors) : "—" }}</span>
              <span class="text-right text-xs" data-testid="top-change">
                <template v-if="compare">
                  <span v-if="s.isNew" class="rounded bg-success/10 px-1.5 py-0.5 text-success" data-testid="chip-new">{{ t("monitor.top.new") }}</span>
                  <span v-else-if="s.regression" class="inline-flex items-center gap-1 rounded bg-danger/10 px-1.5 py-0.5 text-danger" data-testid="chip-regression"><TriangleAlert class="size-3" aria-hidden="true" />{{ pct(s.changePct) }}</span>
                  <span v-else class="rounded bg-hover px-1.5 py-0.5 text-muted" data-testid="chip-change">{{ pct(s.changePct) }}</span>
                </template>
                <span v-else class="text-muted">—</span>
              </span>
            </div>
          </div>
          <p v-if="anyApprox" class="border-t border-border px-3 py-1.5 text-xs text-muted" data-testid="top-approx">{{ t("monitor.top.approxNote") }}</p>
        </div>
      </div>
    </template>

    <BaseModal v-model:open="detailOpen" :title="t('monitor.top.detail')" size="lg">
      <div v-if="store.loadingDigest" class="flex items-center justify-center gap-2 py-8 text-sm text-muted"><BaseSpinner /> {{ t("monitor.top.loading") }}</div>
      <div v-else-if="store.digestDetail" class="flex flex-col gap-3" data-testid="digest-detail">
        <div class="flex items-start gap-2">
          <pre class="font-data max-h-40 min-w-0 flex-1 overflow-auto rounded bg-hover p-2 text-xs break-words whitespace-pre-wrap" data-testid="digest-text">{{ store.digestDetail.text }}</pre>
          <BaseButton size="sm" data-testid="digest-copy" @click="copyText(store.digestDetail.text)"><Copy class="size-3.5" /> {{ t("monitor.top.copyText") }}</BaseButton>
        </div>
        <p class="text-xs text-muted">{{ t("monitor.top.normalizedNote") }}</p>
        <dl v-if="detailStat" class="grid grid-cols-2 gap-x-6 gap-y-1 text-sm sm:grid-cols-4">
          <div><dt class="text-xs text-muted">{{ t("monitor.top.execs") }}</dt><dd>{{ num(detailStat.execs) }}</dd></div>
          <div><dt class="text-xs text-muted">{{ t("monitor.top.total") }}</dt><dd>{{ ms(detailStat.totalMs) }}</dd></div>
          <div><dt class="text-xs text-muted">{{ t("monitor.top.avg") }}</dt><dd>{{ ms(detailStat.avgMs) }}</dd></div>
          <div><dt class="text-xs text-muted">{{ t("monitor.top.maxObserved") }}</dt><dd>{{ ms(detailStat.maxObservedMs) }}</dd></div>
        </dl>
        <TimeSeriesChart :series="digestSeries(store.digestDetail, 'execs')" :label="t('monitor.top.execs')" :height="140" />
        <TimeSeriesChart :series="digestSeries(store.digestDetail, 'avgMs')" :label="t('monitor.top.avg')" unit="ms" :height="140" />
      </div>
      <template #footer><BaseButton @click="store.closeDigest()">{{ t("common.close") }}</BaseButton></template>
    </BaseModal>
  </div>
</template>
