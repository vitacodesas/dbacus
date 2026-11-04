<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, onUnmounted, ref, watch } from "vue";
import { Activity, Play, RefreshCw, Square } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import MonitorLive from "../components/monitor/MonitorLive.vue";
import MonitorMetrics from "../components/monitor/MonitorMetrics.vue";
import MonitorRuns from "../components/monitor/MonitorRuns.vue";
import MonitorTop from "../components/monitor/MonitorTop.vue";
import SourceBanners from "../components/monitor/SourceBanners.vue";
import BaseButton from "../components/ui/BaseButton.vue";
import BaseEmptyState from "../components/ui/BaseEmptyState.vue";
import BaseSelect from "../components/ui/BaseSelect.vue";
import BaseTabs from "../components/ui/BaseTabs.vue";
import { RANGE_KEYS, useMonitorStore, type MonitorTab, type RangeKey } from "../stores/monitor";
import { useConnectionsStore } from "../stores/connections";
import { useSchemaStore } from "../stores/schema";
import { useSessionsStore } from "../stores/sessions";
import { useUiStore } from "../stores/ui";

const { t } = useI18n();
const store = useMonitorStore();
const sessions = useSessionsStore();
const connections = useConnectionsStore();
const schema = useSchemaStore();
const ui = useUiStore();

const sessionOptions = computed(() => sessions.list.map((s) => ({ value: s.sessionId, label: connections.byId.get(s.profileId)?.name ?? s.profileId })));
const dbOptions = computed(() => [{ value: "", label: t("monitor.header.allDatabases") }, ...(store.sessionId ? (schema.databasesOf(store.sessionId) ?? []) : []).map((d) => ({ value: d.name, label: d.name }))]);
const rangeOptions = RANGE_KEYS.map((k) => ({ value: k, label: t(`monitor.range.${k}`) }));
const tabs = computed(() => (["live", "top", "metrics", "runs"] as MonitorTab[]).map((v) => ({ value: v, label: t(`monitor.tabs.${v}`) })));

// El monitor se queda montado al volver a consultas (v-show): la vigilancia solo corre mientras se ve la pestaña En vivo.
const visible = computed(() => ui.mainView === "monitor");
// Solo se pide arrendamiento cuando Capabilities ya dijo que la lista de procesos se puede leer: lo contrario sería una llamada inútil.
const live = computed(() => visible.value && store.tab === "live" && !!store.sessionId && !!store.currentCaps && store.currentCaps.processlist.state !== "unavailable");
watch([live, () => store.sessionId], ([on, sid]) => void (on && sid ? store.watch(sid) : store.unwatch()), { immediate: true });
watch(() => store.sessionId, (sid) => {
  if (!sid) return;
  if (!store.caps[sid]) void store.loadCaps(sid);
  void schema.loadDatabases(sid);
}, { immediate: true });
onBeforeUnmount(() => void store.unwatch());

// Cronómetro de la ejecución: solo corre mientras hay una.
const now = ref(Date.now());
let tick: ReturnType<typeof setInterval> | null = null;
onMounted(() => { tick = setInterval(() => (now.value = Date.now()), 1000); });
onUnmounted(() => { if (tick) clearInterval(tick); });
const elapsed = computed(() => {
  const r = store.currentRun;
  if (!r) return "";
  const s = Math.max(0, Math.floor((now.value - Date.parse(r.startedAt)) / 1000));
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(Math.floor(s / 3600))}:${p(Math.floor((s % 3600) / 60))}:${p(s % 60)}`;
});

const sessionModel = computed({ get: () => store.sessionId ?? "", set: (v: string) => store.selectSession(v || null) });
const rangeModel = computed({ get: () => store.range, set: (v: string) => (store.range = v as RangeKey) });
const tabModel = computed({ get: () => store.tab, set: (v: string) => (store.tab = v as MonitorTab) });
</script>

<template>
  <section class="flex min-h-0 flex-col gap-3 p-4" :aria-label="t('monitor.title')" data-testid="monitor-view">
    <BaseEmptyState v-if="!sessions.list.length" :title="t('monitor.noSession.title')" :description="t('monitor.noSession.description')">
      <template #icon><Activity class="size-8" /></template>
    </BaseEmptyState>
    <template v-else>
      <header class="flex flex-wrap items-end gap-3">
        <div class="w-56"><BaseSelect v-model="sessionModel" :label="t('monitor.header.connection')" :options="sessionOptions" data-testid="monitor-session" /></div>
        <div class="w-44"><BaseSelect v-model="store.schema" :label="t('monitor.header.database')" :options="dbOptions" data-testid="monitor-db" /></div>
        <div class="w-32"><BaseSelect v-model="rangeModel" :label="t('monitor.header.range')" :options="rangeOptions" data-testid="monitor-range" /></div>
        <div class="flex h-8 items-center gap-2 text-sm" data-testid="monitor-run-state" :aria-live="'polite'">
          <template v-if="store.running">
            <span class="inline-block size-2 animate-pulse rounded-full bg-success" aria-hidden="true" />
            <span>{{ t("monitor.header.running") }}</span>
            <span class="font-data text-muted" data-testid="monitor-elapsed">{{ elapsed }}</span>
          </template>
          <span v-else class="text-muted">{{ t("monitor.header.idle") }}</span>
        </div>
        <span class="flex-1" />
        <BaseButton size="md" :loading="store.loadingCaps" data-testid="monitor-refresh-caps" :aria-label="t('monitor.header.refreshCaps')" @click="store.loadCaps(store.sessionId, true)"><RefreshCw class="size-4" /></BaseButton>
        <BaseButton v-if="store.running" variant="danger" data-testid="monitor-stop" @click="store.stop()"><Square class="size-4" /> {{ t("monitor.header.stop") }}</BaseButton>
        <BaseButton v-else variant="primary" :disabled="!store.canStart" :loading="store.starting" data-testid="monitor-start" @click="store.start()"><Play class="size-4" /> {{ t("monitor.header.start") }}</BaseButton>
      </header>

      <p v-if="store.error" class="rounded-md border border-danger/40 bg-danger/10 px-3 py-2 text-sm text-danger" role="alert" data-testid="monitor-error">{{ store.error.message }}</p>
      <SourceBanners />

      <BaseTabs v-model="tabModel" :items="tabs" class="min-h-0 flex-1">
        <template #live><div class="flex h-full min-h-0 flex-col pt-3"><MonitorLive /></div></template>
        <template #top><MonitorTop :active="visible && store.tab === 'top'" /></template>
        <template #metrics><MonitorMetrics :active="visible && store.tab === 'metrics'" /></template>
        <template #runs><MonitorRuns :active="visible && store.tab === 'runs'" /></template>
      </BaseTabs>
    </template>
  </section>
</template>
