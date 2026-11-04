<script setup lang="ts">
// Pestaña «Ejecuciones» (spec 22 ⚑D15): las sesiones de monitoreo de la conexión, con el motivo de fin y los huecos. Si el
// servidor cambió entre ejecuciones (otro `serverId`), se avisa: los datos no son de la misma máquina.
import { computed, ref, watch } from "vue";
import { Trash2, TriangleAlert } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { useMonitorStore } from "../../stores/monitor";
import { useUiStore } from "../../stores/ui";
import BaseButton from "../ui/BaseButton.vue";
import BaseDialogConfirm from "../ui/BaseDialogConfirm.vue";
import BaseEmptyState from "../ui/BaseEmptyState.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";

const props = defineProps<{ active: boolean }>();
const { t } = useI18n();
const store = useMonitorStore();
const ui = useUiStore();

watch(
  () => [props.active, store.profileId, store.currentRun?.id ?? "", store.currentRun?.reason ?? ""] as const,
  ([active, pid]) => active && pid && void store.loadRuns(),
  { immediate: true },
);

const when = (iso?: string | null) => (iso ? new Date(iso).toLocaleString() : "—");
const dur = (a: string, b?: string | null) => {
  if (!b) return "—";
  const s = Math.max(0, Math.round((Date.parse(b) - Date.parse(a)) / 1000));
  return s >= 3600 ? `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min` : s >= 60 ? `${Math.floor(s / 60)} min` : `${s} s`;
};
const reasonLabel = (r: string) => t(`monitor.runs.reason.${r}`, r);
const isBad = (r: string) => r === "crashed" || r === "app_closed" || r === "disconnected";

const serverIds = computed(() => new Set(store.runList.map((r) => r.serverId).filter(Boolean)));
const mixed = computed(() => serverIds.value.size > 1);
// Etiqueta corta y estable de cada servidor (posición en el conjunto), para que la tabla muestre cuál es cuál.
const serverTag = (id: string) => `#${[...serverIds.value].indexOf(id) + 1}`;

const confirmOpen = ref(false);
async function purge() {
  if (await store.purge(null)) {
    ui.toast("success", t("monitor.runs.purged"));
    void store.loadRuns();
  }
}
</script>

<template>
  <div class="flex min-h-0 flex-col gap-3 overflow-auto pt-3" data-testid="monitor-runs">
    <div class="flex flex-wrap items-center gap-2">
      <p class="text-xs text-muted">{{ t("monitor.runs.hint") }}</p>
      <span class="flex-1" />
      <BaseButton size="sm" variant="danger" :disabled="store.running || !store.runList.length" data-testid="runs-purge" @click="confirmOpen = true"><Trash2 class="size-3.5" /> {{ t("monitor.runs.purge") }}</BaseButton>
    </div>
    <p v-if="mixed" class="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-sm" role="status" data-testid="runs-mixed">
      <TriangleAlert class="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />{{ t("monitor.runs.mixed") }}
    </p>

    <div v-if="!store.runsLoaded" class="flex items-center justify-center gap-2 py-10 text-sm text-muted"><BaseSpinner /> {{ t("monitor.top.loading") }}</div>
    <BaseEmptyState v-else-if="!store.runList.length" :title="t('monitor.runs.empty')" :description="t('monitor.runs.emptyHint')" data-testid="runs-empty" />
    <table v-else class="w-full text-sm" data-testid="runs-table">
      <thead class="text-left text-xs text-muted">
        <tr>
          <th scope="col" class="py-1.5 pr-3">{{ t("monitor.runs.started") }}</th><th scope="col" class="pr-3">{{ t("monitor.runs.ended") }}</th><th scope="col" class="pr-3">{{ t("monitor.runs.duration") }}</th>
          <th scope="col" class="pr-3">{{ t("monitor.runs.endReason") }}</th><th scope="col" class="pr-3 text-right">{{ t("monitor.runs.interval") }}</th><th scope="col" class="pr-3 text-right">{{ t("monitor.runs.samples") }}</th>
          <th scope="col" class="pr-3 text-right">{{ t("monitor.runs.gaps") }}</th><th scope="col" class="pr-3">{{ t("monitor.runs.server") }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in store.runList" :key="r.id" class="border-t border-border" :data-testid="`run-${r.id}`">
          <td class="py-1.5 pr-3">{{ when(r.startedAt) }}</td>
          <td class="pr-3">{{ when(r.endedAt) }}</td>
          <td class="pr-3">{{ dur(r.startedAt, r.endedAt) }}</td>
          <td class="pr-3"><span :class="r.reason === 'running' ? 'text-success' : isBad(r.reason) && 'text-warning'" data-testid="run-reason">{{ reasonLabel(r.reason) }}</span></td>
          <td class="pr-3 text-right">{{ r.intervalSec }} s</td>
          <td class="pr-3 text-right">{{ r.samples }}</td>
          <td class="pr-3 text-right" :class="r.gaps > 0 && 'text-warning'">{{ r.gaps }}</td>
          <td class="pr-3 text-xs"><span :title="r.serverId">{{ r.serverVersion }}<template v-if="mixed"> · <span class="rounded bg-hover px-1" data-testid="run-server">{{ serverTag(r.serverId) }}</span></template></span></td>
        </tr>
      </tbody>
    </table>

    <BaseDialogConfirm v-model:open="confirmOpen" :title="t('monitor.runs.purgeTitle')" :message="t('monitor.runs.purgeMessage')" :confirm-label="t('monitor.runs.purge')" danger @confirm="purge" />
  </div>
</template>
