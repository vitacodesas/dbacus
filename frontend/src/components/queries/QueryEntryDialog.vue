<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { folderPaths } from "../../composables/queryTree";
import { useQueryFilesStore } from "../../stores/queryFiles";
import BaseButton from "../ui/BaseButton.vue";
import BaseInput from "../ui/BaseInput.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSelect from "../ui/BaseSelect.vue";

const { t } = useI18n();
const files = useQueryFilesStore();
const d = computed(() => files.entryDialog);
const open = computed({ get: () => !!files.entryDialog, set: (v) => { if (!v) files.closeEntryDialog(); } });
const title = computed(() => (d.value ? t(`queries.dialog.${d.value.kind}`) : ""));

// Al mover una carpeta no se ofrece ella misma ni sus descendientes como destino.
const folders = computed(() => {
  const self = d.value?.kind === "move" && d.value.isFolder ? d.value.path : null;
  const list = folderPaths(files.nodes).filter((p) => !self || (p !== self && !p.startsWith(`${self}/`)));
  return [{ value: "", label: t("queries.rootFolder") }, ...list.map((p) => ({ value: p, label: p }))];
});
</script>

<template>
  <BaseModal v-model:open="open" :title="title" size="sm">
    <form v-if="d" class="flex flex-col gap-3" data-testid="query-entry-dialog" @submit.prevent="files.submitEntry()">
      <BaseInput v-if="d.kind !== 'move'" v-model="d.name" :label="t('queries.name')" :error="d.error ?? undefined" autofocus data-testid="query-entry-name" />
      <BaseSelect v-if="d.kind !== 'rename'" v-model="d.folder" :label="d.kind === 'move' ? t('queries.moveDestination') : t('queries.parentFolder')" :options="folders" :error="d.kind === 'move' ? (d.error ?? undefined) : undefined" data-testid="query-entry-folder" />
      <button type="submit" class="hidden" />
    </form>
    <template #footer>
      <BaseButton @click="files.closeEntryDialog()">{{ t("common.cancel") }}</BaseButton>
      <BaseButton variant="primary" :disabled="!d || d.busy" data-testid="query-entry-submit" @click="files.submitEntry()">{{ t("common.confirm") }}</BaseButton>
    </template>
  </BaseModal>
</template>
