<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { formatJson } from "../../api/edits";
import { asAppError } from "../../api/errors";
import { useEditsStore } from "../../stores/edits";
import BaseButton from "../ui/BaseButton.vue";
import BaseModal from "../ui/BaseModal.vue";

export interface CellEditTarget {
  tabId: string;
  queryId: string;
  stmt: number;
  row: number;
  col: number;
  name: string;
  dataType: string;
  nullable: boolean;
  text: string;
}

const props = defineProps<{ target: CellEditTarget | null }>();
const emit = defineEmits<{ close: [] }>();

const { t } = useI18n();
const edits = useEditsStore();

const text = ref("");
const error = ref<string | null>(null);
const saving = ref(false);
const area = ref<HTMLTextAreaElement | null>(null);

const open = computed({
  get: () => props.target !== null,
  set: (v: boolean) => !v && emit("close"),
});
const isJson = computed(() => props.target?.dataType.toLowerCase() === "json");

watch(
  () => props.target,
  (tg) => {
    if (!tg) return;
    text.value = tg.text;
    error.value = null;
    saving.value = false;
    void nextTick(() => area.value?.focus());
  },
  { immediate: true },
);

// Go valida (y convierte) el valor; si lo rechaza, el motivo se ve aquí y el diálogo sigue abierto.
async function save(value: { null: boolean; text: string }) {
  const tg = props.target;
  if (!tg || saving.value) return;
  saving.value = true;
  const err = await edits.stage(tg.tabId, tg.queryId, tg.stmt, tg.row, tg.col, value);
  saving.value = false;
  if (err) error.value = err.message;
  else emit("close");
}

async function format() {
  try {
    text.value = await formatJson(text.value);
    error.value = null;
  } catch (e) {
    error.value = asAppError(e).message;
  }
}

function onKey(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
    e.preventDefault();
    void save({ null: false, text: text.value });
  }
}
</script>

<template>
  <BaseModal v-model:open="open" :title="t('edit.expandedTitle', { name: target?.name ?? '' })" :description="target?.dataType" size="lg">
    <div class="flex flex-col gap-2" data-testid="cell-editor-modal">
      <textarea
        ref="area"
        v-model="text"
        class="font-data h-72 w-full resize-y rounded-md border border-border bg-bg p-2 text-sm"
        :class="error && 'border-danger'"
        spellcheck="false"
        autocomplete="off"
        autocorrect="off"
        autocapitalize="off"
        wrap="soft"
        data-testid="cell-editor-textarea"
        :aria-label="t('edit.editCell')"
        @keydown="onKey"
      />
      <p v-if="error" class="rounded-md border border-danger p-2 text-sm" role="alert" data-testid="cell-editor-modal-error">{{ error }}</p>
      <p class="text-xs text-muted">{{ t("edit.expandedHint") }}</p>
    </div>
    <template #footer>
      <BaseButton v-if="isJson" class="mr-auto" data-testid="cell-editor-format" @click="format">{{ t("edit.format") }}</BaseButton>
      <BaseButton v-if="target?.nullable" :disabled="saving" @click="save({ null: true, text: '' })">{{ t("edit.setNull") }}</BaseButton>
      <BaseButton :disabled="saving" @click="emit('close')">{{ t("common.cancel") }}</BaseButton>
      <BaseButton variant="primary" :loading="saving" data-testid="cell-editor-save" @click="save({ null: false, text })">{{ t("edit.saveChange") }}</BaseButton>
    </template>
  </BaseModal>
</template>
