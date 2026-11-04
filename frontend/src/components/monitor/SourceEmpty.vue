<script setup lang="ts">
// Estado de una fuente no disponible dentro de su pestaña: el motivo y el texto fijo que Go dice para habilitarla, en vez
// de una tabla vacía (spec 22 ⚑D9). La app nunca ejecuta ese texto.
import { computed } from "vue";
import { Copy } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { writeClipboard } from "../../composables/clipboard";
import { useMonitorStore } from "../../stores/monitor";
import { useUiStore } from "../../stores/ui";
import BaseButton from "../ui/BaseButton.vue";
import BaseEmptyState from "../ui/BaseEmptyState.vue";

const props = defineProps<{ source: "processlist" | "digest" | "status" }>();
const { t, te } = useI18n();
const store = useMonitorStore();
const ui = useUiStore();

const state = computed(() => store.currentCaps?.[props.source] ?? null);
const reasonText = computed(() => {
  const r = state.value?.reason ?? "error";
  return te(`monitor.reason.${r}`) ? t(`monitor.reason.${r}`) : t("monitor.reason.error");
});

async function copy(text: string) {
  if (await writeClipboard(text)) ui.toast("success", t("monitor.copied"));
  else ui.toast("error", t("monitor.cannotCopy"));
}
</script>

<template>
  <BaseEmptyState :title="t('monitor.unavailable.title', { source: t(`monitor.source.${source}`) })" :description="reasonText" data-testid="source-empty">
    <div v-if="state?.hint" class="flex max-w-lg items-start gap-2 text-left">
      <code class="font-data min-w-0 flex-1 rounded bg-hover px-2 py-1 text-xs break-words whitespace-pre-wrap" data-testid="source-empty-hint">{{ state.hint }}</code>
      <BaseButton size="sm" variant="ghost" :aria-label="t('monitor.copyHint')" data-testid="source-empty-copy" @click="copy(state.hint)"><Copy class="size-3.5" /></BaseButton>
    </div>
  </BaseEmptyState>
</template>
