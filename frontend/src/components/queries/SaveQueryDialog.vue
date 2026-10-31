<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { folderPaths } from "../../composables/queryTree";
import { useQueryFilesStore } from "../../stores/queryFiles";
import BaseButton from "../ui/BaseButton.vue";
import BaseCheckbox from "../ui/BaseCheckbox.vue";
import BaseInput from "../ui/BaseInput.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSelect from "../ui/BaseSelect.vue";

const { t } = useI18n();
const files = useQueryFilesStore();
const d = computed(() => files.saveDialog);
const open = computed({ get: () => !!files.saveDialog, set: (v) => { if (!v) files.closeSaveDialog(); } });

const folders = computed(() => [{ value: "", label: t("queries.rootFolder") }, ...folderPaths(files.nodes).map((p) => ({ value: p, label: p }))]);
// Ruta final completa, en vivo: el usuario sabe exactamente dónde quedará.
const finalPath = computed(() => {
  const root = files.root?.path ?? "";
  const name = d.value?.name.trim() ?? "";
  const leaf = name ? (/\.sql$/i.test(name) ? name : `${name}.sql`) : "…";
  return [root, d.value?.folder, leaf].filter(Boolean).join("/");
});
</script>

<template>
  <BaseModal v-model:open="open" :title="t('queries.saveTitle')" :description="t('queries.saveDescription')" size="md">
    <form v-if="d" class="flex flex-col gap-3" data-testid="save-query-dialog" @submit.prevent="files.submitSave()">
      <BaseInput v-model="d.name" :label="t('queries.name')" :error="d.exists ? undefined : (d.error ?? undefined)" autofocus data-testid="save-query-name" />
      <BaseSelect v-model="d.folder" :label="t('queries.saveIn')" :options="folders" data-testid="save-query-folder" />
      <BaseCheckbox v-model="d.remember" data-testid="save-query-remember">{{ t("queries.remember") }}</BaseCheckbox>
      <p class="text-xs text-muted">{{ t("queries.rememberHint") }}</p>
      <div>
        <p class="text-xs font-medium text-muted">{{ t("queries.finalPath") }}</p>
        <p class="font-data mt-0.5 rounded-md bg-bg px-2 py-1.5 text-xs break-all" data-testid="save-query-path">{{ finalPath }}</p>
      </div>
      <div v-if="d.exists" class="flex items-center justify-between gap-2 rounded-md border border-warning/50 bg-warning/10 p-2 text-sm" role="alert" data-testid="save-query-exists">
        <span>{{ d.error }}</span>
        <BaseButton size="sm" variant="danger" :disabled="d.busy" data-testid="save-query-overwrite" @click="files.submitSave(true)">{{ t("queries.overwrite") }}</BaseButton>
      </div>
      <button type="submit" class="hidden" />
    </form>
    <template #footer>
      <BaseButton @click="files.closeSaveDialog()">{{ t("common.cancel") }}</BaseButton>
      <BaseButton variant="primary" :disabled="!d || d.busy" data-testid="save-query-submit" @click="files.submitSave()">{{ t("common.save") }}</BaseButton>
    </template>
  </BaseModal>
</template>
