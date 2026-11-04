<script setup lang="ts">
import { computed } from "vue";
import { Copy, TriangleAlert } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { useMonitorStore } from "../../stores/monitor";
import { useUiStore } from "../../stores/ui";
import BaseButton from "../ui/BaseButton.vue";

const { t, te } = useI18n();
const store = useMonitorStore();
const ui = useUiStore();

const SOURCES = ["processlist", "digest", "status"] as const;
// Una banda por fuente que no está del todo bien: el motivo y el texto fijo que Go dice para habilitarla (no se ejecuta nada).
const items = computed(() => {
  const c = store.currentCaps;
  if (!c) return [];
  return SOURCES.filter((s) => c[s].state !== "ok").map((s) => ({ source: s, state: c[s].state, reason: c[s].reason, hint: c[s].hint }));
});
const reasonText = (reason: string) => (te(`monitor.reason.${reason}`) ? t(`monitor.reason.${reason}`) : t("monitor.reason.error"));

async function copy(text: string) {
  await navigator.clipboard.writeText(text);
  ui.toast("success", t("monitor.copied"));
}
</script>

<template>
  <div v-if="items.length || store.currentNotices.length" class="flex flex-col gap-2" data-testid="monitor-banners">
    <div v-for="i in items" :key="i.source" class="flex items-start gap-2 rounded-md border px-3 py-2 text-sm" :class="i.state === 'unavailable' ? 'border-danger/40 bg-danger/10' : 'border-warning/40 bg-warning/10'" :data-testid="`banner-${i.source}`" role="status">
      <TriangleAlert class="mt-0.5 size-4 shrink-0" :class="i.state === 'unavailable' ? 'text-danger' : 'text-warning'" />
      <div class="min-w-0 flex-1">
        <p class="font-medium">{{ t(`monitor.source.${i.source}`) }} · {{ t(`monitor.state.${i.state}`) }}</p>
        <p class="text-muted">{{ reasonText(i.reason) }}</p>
        <div v-if="i.hint" class="mt-1 flex items-start gap-2">
          <code class="font-data min-w-0 flex-1 rounded bg-hover px-2 py-1 text-xs break-words whitespace-pre-wrap">{{ i.hint }}</code>
          <BaseButton size="sm" variant="ghost" :aria-label="t('monitor.copyHint')" data-testid="copy-hint" @click="copy(i.hint)"><Copy class="size-3.5" /></BaseButton>
        </div>
      </div>
    </div>
    <div v-for="n in store.currentNotices" :key="`${n.source}:${n.reason}`" class="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-sm" data-testid="banner-notice" role="status">
      <TriangleAlert class="mt-0.5 size-4 shrink-0 text-warning" />
      <p>{{ t("monitor.notice", { source: n.source, reason: n.reason }) }}</p>
    </div>
  </div>
</template>
