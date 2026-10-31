<script setup lang="ts">
import { TriangleAlert } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { useQueryFilesStore } from "../../stores/queryFiles";
import type { QueryTab } from "../../stores/queryTabs";
import BaseButton from "../ui/BaseButton.vue";

// Aviso sobre el editor cuando el archivo de la pestaña cambió o desapareció del disco (spec 20 ⚑D9).
defineProps<{ tab: QueryTab }>();
const { t } = useI18n();
const files = useQueryFilesStore();
</script>

<template>
  <div v-if="tab.filePath && tab.diskState !== 'ok'" class="flex flex-wrap items-center gap-2 border-b border-warning/40 bg-warning/10 px-3 py-1.5 text-sm" role="alert" data-testid="file-banner" :data-state="tab.diskState">
    <TriangleAlert class="size-4 shrink-0 text-warning" />
    <span class="min-w-0 flex-1">{{ t(tab.diskState === "changed" ? "queries.changedOnDisk" : "queries.missingOnDisk", { path: tab.filePath }) }}</span>
    <template v-if="tab.diskState === 'changed'">
      <BaseButton size="sm" data-testid="banner-reload" @click="files.reload(tab.id)">{{ t("queries.reload") }}</BaseButton>
      <BaseButton size="sm" data-testid="banner-keep" @click="files.keepMine(tab.id)">{{ t("queries.keepMine") }}</BaseButton>
      <BaseButton size="sm" variant="danger" data-testid="banner-overwrite" @click="files.overwrite(tab.id)">{{ t("queries.overwrite") }}</BaseButton>
    </template>
    <BaseButton v-else size="sm" variant="primary" data-testid="banner-save-again" @click="files.saveAgain(tab.id)">{{ t("queries.saveAgain") }}</BaseButton>
  </div>
</template>
