<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useAlterStore } from "../../stores/alter";
import BaseButton from "../ui/BaseButton.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";
import BaseTabs from "../ui/BaseTabs.vue";
import AlterColumnsTab from "./AlterColumnsTab.vue";
import AlterFksTab from "./AlterFksTab.vue";
import AlterIndexesTab from "./AlterIndexesTab.vue";

const { t } = useI18n();
const alter = useAlterStore();

const open = computed({ get: () => alter.target !== null, set: (v: boolean) => !v && alter.reset() });
const tab = ref("columns");
const tabs = computed(() => [
  { value: "columns", label: t("alter.tabs.columns") },
  { value: "indexes", label: t("alter.tabs.indexes") },
  { value: "fks", label: t("alter.tabs.fks") },
]);
const title = computed(() => t("alter.title", { name: alter.target ? `${alter.target.db}.${alter.target.table}` : "" }));

// El panel enseña exactamente lo que Go devolvió en Preview: lo mismo que luego se revisa y se ejecuta.
const sql = computed(() => (alter.preview?.statements ?? []).map((s) => `${s};`).join("\n\n"));
const warnings = computed(() => alter.preview?.warnings ?? []);
</script>

<template>
  <BaseModal v-model:open="open" :title="title" :description="t('alter.intro')" size="xl">
    <div v-if="alter.loading" class="flex items-center gap-2 py-8 text-sm text-muted"><BaseSpinner class="size-4" />{{ t("alter.loading") }}</div>
    <p v-else-if="alter.loadError" class="rounded-md border border-danger p-2 text-sm" role="alert" data-testid="alter-load-error">{{ alter.loadError }}</p>
    <div v-else class="flex flex-col gap-3" data-testid="alter-editor">
      <BaseTabs v-model="tab" :items="tabs">
        <template #columns><div class="pt-3"><AlterColumnsTab /></div></template>
        <template #indexes><div class="pt-3"><AlterIndexesTab /></div></template>
        <template #fks><div class="pt-3"><AlterFksTab /></div></template>
      </BaseTabs>

      <section :aria-label="t('alter.sqlTitle')">
        <h3 class="mb-1 flex items-center gap-2 text-[11px] font-medium text-muted uppercase">
          {{ t("alter.sqlTitle") }}<BaseSpinner v-if="alter.previewing" class="size-3" />
        </h3>
        <pre v-if="sql" class="font-data max-h-48 overflow-auto rounded-md border border-border bg-bg p-3 text-xs whitespace-pre-wrap" data-testid="alter-sql">{{ sql }}</pre>
        <p v-else-if="alter.previewError" class="rounded-md border border-danger p-2 text-sm" role="alert" data-testid="alter-preview-error">{{ alter.previewError }}</p>
        <p v-else class="rounded-md border border-dashed border-border p-3 text-xs text-muted" data-testid="alter-sql-empty">{{ alter.dirty ? t("alter.previewing") : t("alter.noChanges") }}</p>
        <ul v-if="sql && warnings.length" class="mt-2 flex flex-col gap-1" role="note">
          <li v-for="w in warnings" :key="w.code + w.message" class="text-xs" :class="w.severity === 'danger' ? 'text-danger' : w.severity === 'warning' ? 'text-warning' : 'text-muted'" data-testid="alter-warning">{{ w.message }}</li>
        </ul>
      </section>
    </div>
    <template #footer>
      <BaseButton v-if="alter.dirty" class="mr-auto" data-testid="alter-discard" @click="alter.discardAll()">{{ t("alter.discardAll") }}</BaseButton>
      <BaseButton @click="open = false">{{ t("common.close") }}</BaseButton>
      <BaseButton variant="primary" :disabled="!alter.canApply" data-testid="alter-apply" @click="alter.requestApply()">{{ t("alter.apply") }}</BaseButton>
    </template>
  </BaseModal>
</template>
