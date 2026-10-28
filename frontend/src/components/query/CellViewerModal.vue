<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { getCell } from "../../api/queries";
import { asAppError } from "../../api/errors";
import { copyText } from "../../composables/gridModel";
import { useUiStore } from "../../stores/ui";
import type { Cell } from "../../types";
import BaseButton from "../ui/BaseButton.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";

const props = defineProps<{ target: { queryId: string; stmt: number; row: number; col: number; name: string; cell: Cell } | null }>();
const emit = defineEmits<{ close: [] }>();
const { t } = useI18n();
const ui = useUiStore();

const open = computed({ get: () => !!props.target, set: (v) => !v && emit("close") });
const full = ref<string | null>(null);
const loading = ref(false);

// Las celdas recortadas (texto muy largo, binarios) piden su valor completo a Go.
watch(() => props.target, async (tg) => {
  full.value = null;
  if (!tg) return;
  if (!tg.cell.trunc) return void (full.value = copyText(tg.cell));
  loading.value = true;
  try {
    const c = await getCell(tg.queryId, tg.stmt, tg.row, tg.col);
    full.value = c.v === null || c.v === undefined ? "NULL" : String(c.v);
  } catch (e) {
    ui.toast("error", asAppError(e).message);
    full.value = copyText(tg.cell);
  } finally {
    loading.value = false;
  }
}, { immediate: true });

const pretty = computed(() => {
  const v = full.value ?? "";
  if (props.target?.cell.t !== "json") return v;
  try {
    return JSON.stringify(JSON.parse(v), null, 2);
  } catch {
    return v;
  }
});

async function copy() {
  try {
    await navigator.clipboard.writeText(full.value ?? "");
    ui.toast("success", t("query.copied"));
  } catch {
    ui.toast("error", t("query.cannotCopy"));
  }
}
</script>

<template>
  <BaseModal v-model:open="open" :title="`${t('query.cell.title')} · ${target?.name ?? ''}`" size="lg">
    <div v-if="loading" class="flex items-center gap-2 text-sm text-muted"><BaseSpinner /> {{ t("query.cell.loading") }}</div>
    <pre v-else class="font-data max-h-[60vh] overflow-auto rounded-md border border-border bg-bg p-3 text-xs break-words whitespace-pre-wrap" data-testid="cell-value">{{ pretty }}</pre>
    <template #footer>
      <BaseButton :disabled="loading" @click="copy">{{ t("query.cell.copy") }}</BaseButton>
      <BaseButton variant="primary" @click="open = false">{{ t("common.close") }}</BaseButton>
    </template>
  </BaseModal>
</template>
