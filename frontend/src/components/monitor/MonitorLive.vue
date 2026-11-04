<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Copy, Pause, Play, Power } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { writeClipboard } from "../../composables/clipboard";
import { formatCompact } from "../../composables/timeSeries";
import { useMonitorStore } from "../../stores/monitor";
import { useUiStore } from "../../stores/ui";
import type { Process } from "../../types";
import BaseButton from "../ui/BaseButton.vue";
import BaseEmptyState from "../ui/BaseEmptyState.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";
import VirtualList from "../ui/VirtualList.vue";

const { t, locale } = useI18n();
const store = useMonitorStore();
const ui = useUiStore();

const LONG_SEC = 10;
const VERY_LONG_SEC = 30;
const snap = computed(() => store.currentProcesses);
// Go ya los entrega ordenados por antigüedad: aquí no se ordena ni se filtra.
const rows = computed(() => snap.value?.rows ?? []);
const unavailable = computed(() => store.currentCaps?.processlist.state === "unavailable");
const selectedId = ref<number | null>(null);
// Otra conexión, otros procesos: la selección no se arrastra.
watch(() => store.sessionId, () => (selectedId.value = null));
const selected = computed(() => rows.value.find((p) => p.id === selectedId.value) ?? null);

const age = (p: Process) => (p.command === "Sleep" ? "text-muted" : p.timeSec >= VERY_LONG_SEC ? "text-danger font-medium" : p.timeSec >= LONG_SEC ? "text-warning font-medium" : "");
const dur = (s: number) => (s >= 3600 ? `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min` : s >= 60 ? `${Math.floor(s / 60)} min ${s % 60} s` : `${s} s`);
const num = (v: number | undefined) => (v === undefined ? "—" : formatCompact(v, locale.value));

async function copyKill(p: Process, queryOnly: boolean) {
  const text = await store.copyKill(p.id, queryOnly);
  if (text === null) return;
  if (await writeClipboard(text)) ui.toast("success", t("monitor.copied"));
  else ui.toast("error", t("monitor.cannotCopy"));
}
// Terminar un proceso (spec 22 ⚑D8): confirmación con elección explícita; Go vuelve a comprobar identidad y solo lectura.
const killTarget = ref<Process | null>(null);
const killMode = ref<"query" | "conn">("query");
const killOpen = computed({ get: () => killTarget.value !== null, set: (v: boolean) => !v && (killTarget.value = null) });
function askKill(p: Process) {
  killMode.value = "query";
  killTarget.value = p;
}
const killing = ref(false);
async function confirmKill() {
  const p = killTarget.value;
  if (!p) return;
  killing.value = true;
  const ok = await store.killProcess(p, killMode.value === "query");
  killing.value = false;
  killTarget.value = null;
  if (ok && killMode.value === "conn" && selectedId.value === p.id) selectedId.value = null;
}
const GRID = "grid grid-cols-[56px_96px_96px_72px_88px_minmax(0,1fr)_72px] items-center gap-2 px-3";
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col gap-3" data-testid="monitor-live">
    <div class="flex flex-wrap items-center gap-2 text-xs" data-testid="live-banner">
      <span class="inline-flex items-center gap-1.5 rounded-full border border-border px-2 py-0.5" :class="store.running ? 'text-success' : 'text-muted'" data-testid="live-badge">
        <span v-if="store.liveEnabled" class="inline-block size-1.5 animate-pulse rounded-full bg-current" aria-hidden="true" />
        {{ store.running ? t("monitor.live.recordingBadge") : t("monitor.live.badge") }}
      </span>
      <span v-if="!store.liveEnabled" class="text-muted" data-testid="live-paused">{{ t("monitor.live.paused") }} · {{ t("monitor.live.pausedHint") }}</span>
      <span class="flex-1" />
      <BaseButton v-if="store.liveEnabled" size="sm" data-testid="live-pause" @click="store.setLive(false)"><Pause class="size-3.5" /> {{ t("monitor.live.pause") }}</BaseButton>
      <BaseButton v-else size="sm" data-testid="live-resume" @click="store.setLive(true)"><Play class="size-3.5" /> {{ t("monitor.live.resume") }}</BaseButton>
    </div>
    <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <div class="rounded-md bg-hover px-3 py-2" data-testid="card-running"><p class="text-xs text-muted">{{ t("monitor.live.running") }}</p><p class="text-xl font-medium">{{ snap ? snap.summary.running : "—" }}</p></div>
      <div class="rounded-md bg-hover px-3 py-2" data-testid="card-connections"><p class="text-xs text-muted">{{ t("monitor.live.connections") }}</p><p class="text-xl font-medium">{{ snap ? snap.summary.total : "—" }}<span v-if="snap && snap.maxConnections" class="text-sm font-normal text-muted" data-testid="card-max-connections"> / {{ snap.maxConnections }}</span></p></div>
      <div class="rounded-md bg-hover px-3 py-2" data-testid="card-longest"><p class="text-xs text-muted">{{ t("monitor.live.longest") }}</p><p class="text-xl font-medium" :class="snap && snap.summary.longestSec >= VERY_LONG_SEC && 'text-danger'">{{ snap ? dur(snap.summary.longestSec) : "—" }}</p></div>
      <div class="rounded-md bg-hover px-3 py-2" data-testid="card-qps"><p class="text-xs text-muted">{{ t("monitor.live.qps") }}</p><p class="text-xl font-medium">{{ num(store.currentSample?.qps) }}</p><p v-if="!store.running" class="text-xs text-muted">{{ t("monitor.live.qpsHint") }}</p></div>
    </div>

    <BaseEmptyState v-if="unavailable" :title="t('monitor.live.unavailable')" :description="t('monitor.live.unavailableHint')" />
    <BaseEmptyState v-else-if="!snap && !store.liveEnabled" :title="t('monitor.live.paused')" :description="t('monitor.live.pausedHint')" data-testid="live-idle" />
    <div v-else-if="!snap" class="flex flex-1 items-center justify-center gap-2 text-sm text-muted" data-testid="live-waiting"><BaseSpinner /> {{ t("monitor.live.waiting") }}</div>
    <BaseEmptyState v-else-if="!rows.length" :title="t('monitor.live.empty')" />
    <div v-else class="flex min-h-0 flex-1 flex-col rounded-md border border-border">
      <div class="border-b border-border py-1.5 text-xs text-muted" :class="GRID"><span>ID</span><span>{{ t("monitor.live.user") }}</span><span>{{ t("monitor.live.db") }}</span><span>{{ t("monitor.live.command") }}</span><span>{{ t("monitor.live.time") }}</span><span>{{ t("monitor.live.query") }}</span><span /></div>
      <VirtualList :items="rows" :row-height="32" :item-key="(p: Process) => p.id" class="min-h-0 flex-1" data-testid="process-list">
        <template #default="{ item: p }">
          <div class="h-8 cursor-pointer text-sm hover:bg-hover" :class="[GRID, selectedId === p.id && 'bg-hover']" :data-testid="`process-${p.id}`" @click="selectedId = selectedId === p.id ? null : p.id">
            <span class="font-data text-xs">{{ p.id }}</span>
            <span class="truncate">{{ p.user }}</span>
            <span class="truncate">{{ p.db || "—" }}</span>
            <span class="truncate">{{ p.command }}</span>
            <span :class="age(p)" data-testid="process-time">{{ dur(p.timeSec) }}</span>
            <span class="font-data truncate text-xs" :class="!p.info && 'text-muted'">{{ p.info || p.state || "—" }}</span>
            <span class="flex items-center justify-end gap-0.5">
              <BaseButton size="sm" variant="ghost" :aria-label="t('monitor.live.copyKill')" :title="t('monitor.live.copyKill')" data-testid="copy-kill" @click.stop="copyKill(p, false)"><Copy class="size-3.5" /></BaseButton>
              <BaseButton v-if="!store.readOnly" size="sm" variant="ghost" class="text-danger" :aria-label="t('monitor.kill.action')" :title="t('monitor.kill.action')" data-testid="kill-open" @click.stop="askKill(p)"><Power class="size-3.5" /></BaseButton>
            </span>
          </div>
        </template>
      </VirtualList>
    </div>

    <!-- «Ver completo»: el INFO vive solo en memoria (spec 22 ⚑D7), truncado por Go a 2 KiB. -->
    <div v-if="selected" class="rounded-md border border-border p-3" data-testid="process-detail">
      <div class="mb-2 flex flex-wrap items-center gap-2 text-sm">
        <span class="font-medium">#{{ selected.id }} · {{ selected.user }}@{{ selected.host }}</span>
        <span class="text-muted">{{ selected.state }}</span>
        <span class="flex-1" />
        <BaseButton size="sm" data-testid="copy-kill-query" @click="copyKill(selected, true)"><Copy class="size-3.5" /> KILL QUERY</BaseButton>
        <BaseButton size="sm" data-testid="copy-kill-conn" @click="copyKill(selected, false)"><Copy class="size-3.5" /> KILL</BaseButton>
        <BaseButton v-if="!store.readOnly" size="sm" variant="danger" data-testid="kill-open-detail" @click="askKill(selected)"><Power class="size-3.5" /> {{ t("monitor.kill.action") }}</BaseButton>
        <span v-else class="text-xs text-muted" data-testid="kill-readonly">{{ t("monitor.kill.readOnlyHint") }}</span>
      </div>
      <pre class="font-data max-h-48 overflow-auto text-xs break-words whitespace-pre-wrap" data-testid="process-info">{{ selected.info || t("monitor.live.noInfo") }}</pre>
      <p v-if="selected.infoTruncated" class="mt-1 text-xs text-muted">{{ t("monitor.live.truncated") }}</p>
    </div>
    <BaseModal v-model:open="killOpen" :title="t('monitor.kill.title', { id: killTarget?.id ?? '' })" :description="killTarget ? t('monitor.kill.who', { user: killTarget.user, host: killTarget.host, command: killTarget.command }) : ''" size="sm">
      <fieldset class="flex flex-col gap-3" data-testid="kill-dialog">
        <label class="flex cursor-pointer items-start gap-2 text-sm"><input v-model="killMode" type="radio" value="query" class="mt-1 accent-[var(--accent)]" data-testid="kill-mode-query"><span><span class="font-medium">{{ t("monitor.kill.queryLabel") }}</span><span class="block text-xs text-muted">{{ t("monitor.kill.queryHint") }}</span></span></label>
        <label class="flex cursor-pointer items-start gap-2 text-sm"><input v-model="killMode" type="radio" value="conn" class="mt-1 accent-[var(--accent)]" data-testid="kill-mode-conn"><span><span class="font-medium">{{ t("monitor.kill.connLabel") }}</span><span class="block text-xs text-muted">{{ t("monitor.kill.connHint") }}</span></span></label>
        <p class="text-xs text-warning">{{ t("monitor.kill.warnOwn") }}</p>
      </fieldset>
      <template #footer>
        <BaseButton data-testid="kill-cancel" @click="killTarget = null">{{ t("common.cancel") }}</BaseButton>
        <BaseButton variant="danger" :loading="killing" data-testid="kill-confirm" @click="confirmKill">{{ t("monitor.kill.confirm") }}</BaseButton>
      </template>
    </BaseModal>
  </div>
</template>
