<script setup lang="ts">
// Único importador de uPlot (ADR-006). Pinta lo que Go entrega: no agrega, ordena ni muestrea (spec 22 ⚑D11).
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import type { Annotation, Series, TimeGap } from "../../types";
import { buildData, formatCompact, seriesDomain } from "../../composables/timeSeries";
import { useUiStore } from "../../stores/ui";

const props = withDefaults(defineProps<{ series: Series; label?: string; height?: number; unit?: string; annotatable?: boolean }>(), { height: 180 });
// Clic sobre la gráfica = «anotar aquí» (spec 22 ⚑D14): se emite la hora del punto pulsado; la nota la crea Go.
const emit = defineEmits<{ annotate: [at: string] }>();
const { t, locale } = useI18n();
const ui = useUiStore();
const root = ref<HTMLElement | null>(null);
const showData = ref(false);
let plot: uPlot | null = null;
let observer: ResizeObserver | null = null;

const sec = (iso: string) => Date.parse(iso) / 1000;
const points = computed(() => props.series.points ?? []);
const gaps = computed<TimeGap[]>(() => props.series.gaps ?? []);
const annotations = computed<Annotation[]>(() => props.series.annotations ?? []);

const xDomain = computed(() => seriesDomain(props.series));

const token = (name: string, fallback: string) => {
  try {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
  } catch {
    return fallback;
  }
};

function build() {
  destroy();
  const el = root.value;
  if (!el || !points.value.length) return;
  const accent = token("--accent", "#4f46e5");
  const muted = token("--muted", "#64748b");
  const border = token("--border", "#e2e8f0");
  const warning = token("--warning", "#d97706");
  const [xs, ys] = buildData(props.series.points, props.series.gaps);
  const dom = xDomain.value;
  const noData = t("monitor.chart.noData");
  const axisCommon = { stroke: muted, grid: { stroke: border, width: 1 }, ticks: { stroke: border, width: 1 } };
  const opts: uPlot.Options = {
    width: el.clientWidth || 600,
    height: props.height,
    legend: { show: false },
    cursor: { drag: { x: false, y: false }, points: { size: 7 } },
    scales: { x: { time: true, range: dom ? () => dom : undefined } },
    axes: [{ ...axisCommon, grid: { show: false } }, { ...axisCommon, size: 52, values: (_u, vals) => vals.map((v) => formatCompact(v, locale.value)) }],
    series: [{}, { label: props.label ?? props.series.metric, stroke: accent, width: 2, spanGaps: false, points: { show: false }, fill: `${accent}1f` }],
    hooks: {
      // Bandas «sin datos» bajo la línea y anotaciones encima.
      drawClear: [
        (u) => {
          const { ctx, bbox } = u;
          ctx.save();
          ctx.fillStyle = `${muted}26`;
          ctx.font = `${11 * uPlot.pxRatio}px system-ui, sans-serif`;
          ctx.textAlign = "center";
          for (const g of gaps.value) {
            const x0 = Math.max(bbox.left, u.valToPos(sec(g.from), "x", true));
            const x1 = Math.min(bbox.left + bbox.width, u.valToPos(sec(g.to), "x", true));
            if (x1 <= x0) continue;
            ctx.fillRect(x0, bbox.top, x1 - x0, bbox.height);
            if (x1 - x0 > 64 * uPlot.pxRatio) {
              ctx.fillStyle = muted;
              ctx.fillText(noData, (x0 + x1) / 2, bbox.top + bbox.height / 2);
              ctx.fillStyle = `${muted}26`;
            }
          }
          ctx.restore();
        },
      ],
      draw: [
        (u) => {
          const { ctx, bbox } = u;
          ctx.save();
          ctx.strokeStyle = warning;
          ctx.fillStyle = muted;
          ctx.setLineDash([4 * uPlot.pxRatio, 4 * uPlot.pxRatio]);
          ctx.lineWidth = uPlot.pxRatio;
          ctx.font = `${11 * uPlot.pxRatio}px system-ui, sans-serif`;
          for (const a of annotations.value) {
            const x = u.valToPos(sec(a.at), "x", true);
            if (x < bbox.left || x > bbox.left + bbox.width) continue;
            ctx.beginPath();
            ctx.moveTo(x, bbox.top);
            ctx.lineTo(x, bbox.top + bbox.height);
            ctx.stroke();
            ctx.fillText(a.text, x + 4 * uPlot.pxRatio, bbox.top + 12 * uPlot.pxRatio);
          }
          ctx.restore();
        },
      ],
    },
  };
  plot = new uPlot(opts, [xs, ys] as uPlot.AlignedData, el);
  const over = plot.over as HTMLElement | undefined; // el doble de uPlot de las pruebas no lo trae
  if (props.annotatable && over) {
    over.style.cursor = "copy";
    over.addEventListener("click", (ev: MouseEvent) => {
      const u = plot;
      if (!u) return;
      const x = u.posToVal(ev.clientX - over.getBoundingClientRect().left, "x");
      if (Number.isFinite(x)) emit("annotate", new Date(x * 1000).toISOString());
    });
  }
}
function destroy() {
  plot?.destroy();
  plot = null;
}

onMounted(() => {
  build();
  if (typeof ResizeObserver !== "undefined" && root.value) {
    observer = new ResizeObserver(() => {
      const w = root.value?.clientWidth;
      if (plot && w) plot.setSize({ width: w, height: props.height });
    });
    observer.observe(root.value);
  }
});
onBeforeUnmount(() => {
  observer?.disconnect();
  destroy();
});
// Con ≤ 300 puntos reconstruir es barato y evita que las bandas y anotaciones lean datos viejos.
watch(() => props.series, build);
// Canvas no resuelve variables CSS: al cambiar de tema se relee y se repinta (ADR-006).
watch(() => ui.resolvedTheme, () => void nextTick(build), { flush: "post" });

const fmt = (v: number) => formatCompact(v, locale.value);
const timeLabel = (iso: string) => new Date(iso).toLocaleString();
const first = computed(() => points.value[0]);
const last = computed(() => points.value[points.value.length - 1]);
const summary = computed(() =>
  first.value && last.value
    ? t("monitor.chart.summary", { label: props.label ?? props.series.metric, from: timeLabel(first.value.t), to: timeLabel(last.value.t), count: points.value.length, last: fmt(last.value.v), gaps: gaps.value.length })
    : t("monitor.chart.empty"),
);
</script>

<template>
  <figure class="m-0" data-testid="time-series-chart">
    <div ref="root" class="w-full" role="img" :aria-label="summary" :style="{ minHeight: `${height}px` }">
      <p v-if="!points.length" class="grid place-items-center text-sm text-muted" :style="{ height: `${height}px` }" data-testid="chart-empty">{{ t("monitor.chart.empty") }}</p>
    </div>
    <figcaption class="mt-1 flex items-center justify-between text-xs text-muted">
      <span>{{ label ?? series.metric }}<template v-if="unit"> · {{ unit }}</template></span>
      <button v-if="points.length" type="button" class="underline hover:text-fg" :aria-expanded="showData" data-testid="chart-toggle-data" @click="showData = !showData">{{ showData ? t("monitor.chart.hideData") : t("monitor.chart.showData") }}</button>
    </figcaption>
    <!-- Tabla alternativa accesible (el canvas no es legible por lectores de pantalla); se monta solo al abrirla. -->
    <div v-if="showData" class="mt-1 max-h-48 overflow-auto rounded-md border border-border">
      <table class="w-full text-xs" data-testid="chart-data">
        <caption class="sr-only">{{ summary }}</caption>
        <thead class="sticky top-0 bg-panel text-left text-muted"><tr><th scope="col" class="px-2 py-1">{{ t("monitor.chart.time") }}</th><th scope="col" class="px-2 py-1 text-right">{{ t("monitor.chart.value") }}</th></tr></thead>
        <tbody>
          <tr v-for="p in points" :key="p.t" class="border-t border-border"><td class="px-2 py-0.5 font-data">{{ timeLabel(p.t) }}</td><td class="px-2 py-0.5 text-right font-data">{{ fmt(p.v) }}</td></tr>
        </tbody>
      </table>
    </div>
  </figure>
</template>
