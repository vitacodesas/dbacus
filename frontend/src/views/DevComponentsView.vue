<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref } from "vue";
import BaseButton from "../components/ui/BaseButton.vue";
import BaseCheckbox from "../components/ui/BaseCheckbox.vue";
import BaseContextMenu, { type MenuItem } from "../components/ui/BaseContextMenu.vue";
import BaseDialogConfirm from "../components/ui/BaseDialogConfirm.vue";
import BaseEmptyState from "../components/ui/BaseEmptyState.vue";
import BaseInput from "../components/ui/BaseInput.vue";
import BaseKbd from "../components/ui/BaseKbd.vue";
import BaseModal from "../components/ui/BaseModal.vue";
import BaseSelect from "../components/ui/BaseSelect.vue";
import BaseSpinner from "../components/ui/BaseSpinner.vue";
import BaseSplitter from "../components/ui/BaseSplitter.vue";
import BaseTabs from "../components/ui/BaseTabs.vue";
import BaseToast from "../components/ui/BaseToast.vue";
import BaseToggle from "../components/ui/BaseToggle.vue";
import BaseTooltip from "../components/ui/BaseTooltip.vue";
import ColorSwatches from "../components/ui/ColorSwatches.vue";
import TimeSeriesChart from "../components/monitor/TimeSeriesChart.vue";
import { monitorSeries } from "../api/monitor";
import { useUiStore, type Theme } from "../stores/ui";
import type { Series } from "../types";
import { usePingStore } from "../stores/ping";

const ui = useUiStore();
const ping = usePingStore();
onMounted(() => ping.listen());
onUnmounted(() => ping.stop());

const text = ref("");
const select = ref("mysql");
const check = ref(true);
const toggle = ref(false);
const swatch = ref("#06b6d4");
const tab = ref("a");
const modal = ref(false);
const confirm = ref(false);
const paneWidth = ref(240);
const menu: MenuItem[] = [
  { id: "copy", label: "Copiar" },
  { id: "del", label: "Eliminar", danger: true, separatorBefore: true },
];
const themes: Theme[] = ["light", "dark", "system"];

// Gráfica del Monitor (ADR-006): con el backend simulado (VITE_MOCK_API=1) pide 7 días sobre 5 000 muestras con huecos y
// mide cuánto tarda Go (mock) en entregar ≤ 300 puntos y cuánto en pintarlos. Sin mock, la llamada falla y se muestra el error.
const chart = ref<Series | null>(null);
const chartInfo = ref("");
async function loadChart(range: "6h" | "7d") {
  const to = new Date();
  const from = new Date(to.getTime() - (range === "7d" ? 7 * 86_400_000 : 6 * 3_600_000));
  const t0 = performance.now();
  try {
    const [s] = await monitorSeries({ profileId: "p1", from: from.toISOString(), to: to.toISOString(), metrics: ["qps"], maxPoints: 300 });
    const t1 = performance.now();
    chart.value = s ?? null;
    await nextTick();
    chartInfo.value = `${s?.points?.length ?? 0} puntos, ${s?.gaps?.length ?? 0} huecos, ${s?.annotations?.length ?? 0} anotaciones · datos ${(t1 - t0).toFixed(0)} ms · pintado ${(performance.now() - t1).toFixed(0)} ms`;
  } catch (e) {
    chartInfo.value = `Error: ${(e as Error).message}`;
  }
}
</script>

<template>
  <div class="h-full overflow-auto bg-bg p-6 text-fg">
    <div class="mx-auto flex max-w-3xl flex-col gap-8">
      <header class="flex items-center justify-between">
        <h1 class="text-xl font-semibold">Componentes</h1>
        <div class="flex gap-1">
          <BaseButton v-for="th in themes" :key="th" size="sm" :variant="ui.theme === th ? 'primary' : 'secondary'" @click="ui.theme = th">{{ th }}</BaseButton>
        </div>
      </header>

      <section class="flex flex-col gap-3">
        <h2 class="text-sm font-semibold text-muted">Botones</h2>
        <div class="flex flex-wrap gap-2">
          <BaseButton variant="primary">Primario</BaseButton>
          <BaseButton>Secundario</BaseButton>
          <BaseButton variant="danger">Peligro</BaseButton>
          <BaseButton variant="ghost">Ghost</BaseButton>
          <BaseButton variant="primary" size="sm">Pequeño</BaseButton>
          <BaseButton variant="primary" loading>Cargando</BaseButton>
          <BaseButton variant="primary" disabled>Deshabilitado</BaseButton>
          <BaseSpinner />
        </div>
      </section>

      <section class="grid grid-cols-2 gap-4">
        <h2 class="col-span-2 text-sm font-semibold text-muted">Formulario</h2>
        <BaseInput v-model="text" label="Host" placeholder="127.0.0.1" />
        <BaseInput v-model="text" label="Con error" error="Campo requerido" />
        <BaseInput v-model="text" label="Deshabilitado" disabled />
        <BaseInput v-model="text" label="Monoespaciado" mono />
        <BaseSelect v-model="select" label="Driver" :options="[{ value: 'mysql', label: 'MySQL / MariaDB' }, { value: 'pg', label: 'PostgreSQL (próximamente)', disabled: true }]" />
        <div class="flex flex-col justify-end gap-2">
          <BaseCheckbox v-model="check" label="Guardar contraseña en el llavero" />
          <BaseToggle v-model="toggle" label="Túnel SSH" />
          <ColorSwatches v-model="swatch" label="Color" none-label="Sin color" />
        </div>
      </section>

      <section class="flex flex-col gap-3">
        <h2 class="text-sm font-semibold text-muted">Overlays</h2>
        <div class="flex flex-wrap items-center gap-2">
          <BaseButton @click="modal = true">Modal</BaseButton>
          <BaseButton variant="danger" @click="confirm = true">Confirmar</BaseButton>
          <BaseTooltip text="Un tooltip"><BaseButton>Hover</BaseButton></BaseTooltip>
          <BaseContextMenu :items="menu" @select="(id) => ui.toast('info', `Menú: ${id}`)">
            <div class="rounded-md border border-dashed border-border px-4 py-2 text-sm text-muted">Clic derecho aquí</div>
          </BaseContextMenu>
          <BaseButton @click="ui.toast('success', 'Guardado')">Toast ok</BaseButton>
          <BaseButton @click="ui.toast('error', 'No se pudo conectar')">Toast error</BaseButton>
          <span class="text-sm text-muted">Atajo: <BaseKbd>⌘</BaseKbd> <BaseKbd>B</BaseKbd></span>
        </div>
      </section>

      <section class="flex flex-col gap-3">
        <h2 class="text-sm font-semibold text-muted">Tabs y splitter</h2>
        <BaseTabs v-model="tab" :items="[{ value: 'a', label: 'Resultado 1' }, { value: 'b', label: 'Mensajes' }]">
          <template #a><p class="p-3 text-sm">Contenido A</p></template>
          <template #b><p class="p-3 text-sm">Contenido B</p></template>
        </BaseTabs>
        <div class="flex h-24 rounded-md border border-border">
          <div class="grid place-items-center text-sm text-muted" :style="{ width: `${paneWidth}px` }">{{ paneWidth }} px</div>
          <BaseSplitter orientation="vertical" @drag="(d) => (paneWidth = Math.max(80, paneWidth + d))" />
          <div class="grid flex-1 place-items-center text-sm text-muted">Arrastra o usa ← →</div>
        </div>
      </section>

      <section class="flex flex-col gap-2" data-testid="gallery-chart">
        <h2 class="text-sm font-semibold text-muted">TimeSeriesChart (ADR-006, solo con backend simulado)</h2>
        <div class="flex gap-2"><BaseButton @click="loadChart('6h')">6 h</BaseButton><BaseButton @click="loadChart('7d')">7 d (5 000 muestras)</BaseButton></div>
        <p data-testid="chart-info" class="font-data text-xs">{{ chartInfo }}</p>
        <TimeSeriesChart v-if="chart" :series="chart" label="Consultas por segundo" />
      </section>

      <section class="rounded-md border border-border"><BaseEmptyState title="Estado vacío" description="Descripción de apoyo." /></section>

      <section class="flex flex-col gap-2">
        <h2 class="text-sm font-semibold text-muted">Puente Wails (spike T0.1)</h2>
        <div class="flex gap-2">
          <BaseButton variant="primary" @click="ping.greet('Wails')">Saludar (binding)</BaseButton>
          <BaseButton @click="ping.pick()">Elegir archivo (diálogo)</BaseButton>
        </div>
        <p data-testid="greeting" class="text-sm">{{ ping.greeting }}</p>
        <p data-testid="tick" class="text-sm">Ticks de Go: {{ ping.lastTick ?? "—" }}</p>
        <p data-testid="file" class="font-data text-sm">{{ ping.pickedFile }}</p>
      </section>
    </div>

    <BaseModal v-model:open="modal" title="Título del modal" description="Descripción accesible">
      Contenido del modal.
      <template #footer><BaseButton @click="modal = false">Cerrar</BaseButton></template>
    </BaseModal>
    <BaseDialogConfirm v-model:open="confirm" title="Eliminar conexión" message="Esta acción no se puede deshacer." danger confirm-label="Eliminar" />
    <BaseToast />
  </div>
</template>
