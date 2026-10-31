<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useQueryFilesStore } from "../../stores/queryFiles";
import { useQueryTabsStore } from "../../stores/queryTabs";
import BaseButton from "../ui/BaseButton.vue";
import BaseModal from "../ui/BaseModal.vue";

// «Guardar / No guardar / Cancelar» al cerrar una pestaña con texto sin guardar (spec 20).
const { t } = useI18n();
const tabs = useQueryTabsStore();
const files = useQueryFilesStore();
const tab = computed(() => (tabs.closePrompt ? tabs.byId(tabs.closePrompt) : undefined));
const open = computed({ get: () => !!tab.value, set: (v) => { if (!v) tabs.closePrompt = null; } });

async function save() {
  const id = tabs.closePrompt;
  tabs.closePrompt = null;
  if (id) await files.save(id, { thenClose: true }); // sin archivo, abre el diálogo y cierra al terminar
}
async function discard() {
  const id = tabs.closePrompt;
  tabs.closePrompt = null;
  if (id) await tabs.closeTab(id, true);
}
</script>

<template>
  <BaseModal v-model:open="open" :title="t('queries.closeTitle')" size="sm">
    <p class="text-sm" data-testid="tab-close-message">{{ t(tab?.filePath ? "queries.closeMessageFile" : "queries.closeMessageNew", { name: tab?.title ?? "" }) }}</p>
    <template #footer>
      <BaseButton data-testid="tab-close-cancel" @click="tabs.closePrompt = null">{{ t("common.cancel") }}</BaseButton>
      <BaseButton variant="danger" data-testid="tab-close-discard" @click="discard">{{ t("queries.dontSave") }}</BaseButton>
      <BaseButton variant="primary" data-testid="tab-close-save" @click="save">{{ t("common.save") }}</BaseButton>
    </template>
  </BaseModal>
</template>
