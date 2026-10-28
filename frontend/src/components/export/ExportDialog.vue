<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useExportsStore } from "../../stores/exports";
import { formatBytes } from "../../utils/time";
import BaseButton from "../ui/BaseButton.vue";
import BaseCheckbox from "../ui/BaseCheckbox.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSelect from "../ui/BaseSelect.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";

const props = defineProps<{ queryId: string; stmtIndex: number; truncated: boolean; rowCount: number }>();
const open = defineModel<boolean>("open", { default: false });

const { t, locale } = useI18n();
const exports = useExportsStore();
const opts = exports.options;

// Si el resultado está truncado, lo que hay en memoria no es todo: se propone volver a ejecutar.
const source = ref<"buffer" | "rerun">("buffer");
watch(() => [open.value, props.truncated] as const, ([o, truncated]) => {
  if (o) {
    source.value = truncated ? "rerun" : "buffer";
    exports.dismiss();
  }
}, { immediate: true });

const isCsv = computed(() => opts.format === "csv");
const nullAs = computed({
  get: () => (opts.nullAs ? "NULL" : ""),
  set: (v: string) => (opts.nullAs = v),
});
const formats = [{ value: "csv", label: "CSV" }, { value: "json", label: "JSON" }];
const delimiters = computed(() => [
  { value: ",", label: t("export.comma") }, { value: ";", label: t("export.semicolon") }, { value: "\t", label: t("export.tab") },
]);
const nulls = computed(() => [{ value: "", label: t("export.nullEmpty") }, { value: "NULL", label: "NULL" }]);

const job = computed(() => exports.job);
const busy = computed(() => exports.running);

// Al terminar (bien, mal o cancelada) el aviso lo da el toast del store y el diálogo se cierra.
watch(() => job.value?.status, (s) => {
  if (open.value && s && s !== "running") {
    open.value = false;
    exports.dismiss();
  }
});

async function submit() {
  await exports.start(props.queryId, props.stmtIndex, source.value);
}
</script>

<template>
  <BaseModal v-model:open="open" :title="t('export.title')" size="md">
    <div v-if="busy" class="flex flex-col items-center gap-3 py-4" role="status" data-testid="export-progress">
      <BaseSpinner class="size-6" />
      <p class="text-sm">{{ t("export.exporting") }}</p>
      <p v-if="job" class="font-data text-xs text-muted">{{ t("export.progress", { rows: job.rows.toLocaleString(), size: formatBytes(job.bytes, locale) }) }}</p>
    </div>

    <form v-else class="flex flex-col gap-3" @submit.prevent="submit">
      <BaseSelect v-model="opts.format" :label="t('export.format')" :options="formats" />

      <template v-if="isCsv">
        <div class="grid grid-cols-2 gap-3">
          <BaseSelect v-model="opts.delimiter" :label="t('export.delimiter')" :options="delimiters" />
          <BaseSelect v-model="nullAs" :label="t('export.nullAs')" :options="nulls" />
        </div>
        <BaseCheckbox v-model="opts.header" :label="t('export.header')" />
        <BaseCheckbox v-model="opts.bom" :label="t('export.bom')" />
        <BaseCheckbox v-model="opts.crlf" :label="t('export.crlf')" />
        <BaseCheckbox v-model="opts.quoteAll" :label="t('export.quoteAll')" />
        <BaseCheckbox v-model="opts.escapeFormulas" :label="t('export.escapeFormulas')" />
      </template>
      <BaseCheckbox v-else v-model="opts.ndjson" :label="t('export.ndjson')" />

      <fieldset v-if="truncated" class="flex flex-col gap-1 rounded-md border border-border p-2">
        <legend class="px-1 text-xs font-medium text-muted">{{ t("export.source") }}</legend>
        <p class="text-xs text-warning">{{ t("export.truncatedNote", { n: rowCount.toLocaleString() }) }}</p>
        <label class="flex items-center gap-2 text-sm"><input v-model="source" type="radio" value="rerun" class="accent-[var(--accent)]">{{ t("export.sourceRerun") }}</label>
        <label class="flex items-center gap-2 text-sm"><input v-model="source" type="radio" value="buffer" class="accent-[var(--accent)]">{{ t("export.sourceBuffer", { n: rowCount.toLocaleString() }) }}</label>
        <p v-if="source === 'rerun'" class="text-xs text-muted">{{ t("export.rerunHint") }}</p>
      </fieldset>
    </form>

    <template #footer>
      <template v-if="busy">
        <BaseButton variant="danger" :disabled="exports.starting" @click="exports.cancel()">{{ t("export.stop") }}</BaseButton>
      </template>
      <template v-else>
        <BaseButton @click="open = false">{{ t("common.cancel") }}</BaseButton>
        <BaseButton variant="primary" data-testid="export-start" @click="submit">{{ t("export.start") }}</BaseButton>
      </template>
    </template>
  </BaseModal>
</template>
