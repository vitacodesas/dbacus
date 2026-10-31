<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useQueryFilesStore } from "../../stores/queryFiles";
import { useUiStore } from "../../stores/ui";
import type { Settings } from "../../types";
import BaseButton from "../ui/BaseButton.vue";
import BaseInput from "../ui/BaseInput.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSelect from "../ui/BaseSelect.vue";
import BaseToggle from "../ui/BaseToggle.vue";

const { t } = useI18n();
const queryFiles = useQueryFilesStore();
const ui = useUiStore();
// La carpeta de consultas se cambia con el diálogo nativo (spec 20 ⚑D3): no es un campo de texto.
watch(() => ui.settingsOpen, (o) => o && !queryFiles.root && void queryFiles.load());

// Se edita una copia: nada cambia hasta pulsar Guardar y Go valida los límites.
const draft = reactive<Settings>(currentSettings());
const field = ref("");
const message = ref("");
const saving = ref(false);

function currentSettings(): Settings {
  return {
    theme: ui.theme, locale: ui.locale, maxRows: ui.maxRows, maxResultMemoryMiB: ui.maxResultMemoryMiB,
    historyRetention: ui.historyRetention, showSystemDatabases: ui.showSystemDatabases, confirmEdits: ui.confirmEdits,
    sidebarWidth: ui.sidebarWidth, splitRatio: ui.splitRatio, collapsedGroups: [...ui.collapsedGroups],
  };
}
watch(
  () => ui.settingsOpen,
  (open) => {
    if (!open) return;
    Object.assign(draft, currentSettings());
    field.value = message.value = "";
  },
  { immediate: true },
);

const themes = computed(() => (["system", "light", "dark"] as const).map((v) => ({ value: v, label: t(`theme.${v}`) })));
const locales = [{ value: "es", label: "Español" }, { value: "en", label: "English" }];
const errorFor = (name: string) => (field.value === name ? message.value : "");

// Los campos numéricos llegan como string desde el input.
const num = (v: string | number) => Number(v);

async function save() {
  saving.value = true;
  const err = await ui.saveSettings({ ...draft });
  saving.value = false;
  if (!err) {
    ui.settingsOpen = false;
    return;
  }
  field.value = err.detail ?? "";
  message.value = t(`settings.invalid.${err.detail}`, err.message);
}
</script>

<template>
  <BaseModal v-model:open="ui.settingsOpen" :title="t('settings.title')" :description="t('settings.description')">
    <form class="flex flex-col gap-4" data-testid="settings-form" @submit.prevent="save">
      <div class="grid grid-cols-2 gap-3">
        <BaseSelect v-model="draft.theme" :label="t('settings.theme')" :options="themes" :error="errorFor('theme')" />
        <BaseSelect v-model="draft.locale" :label="t('settings.locale')" :options="locales" :error="errorFor('locale')" />
      </div>
      <BaseInput :model-value="draft.maxRows" type="number" mono :label="t('settings.maxRows')" :error="errorFor('maxRows')" @update:model-value="draft.maxRows = num($event)" />
      <BaseInput :model-value="draft.maxResultMemoryMiB" type="number" mono :label="t('settings.maxMemory')" :error="errorFor('maxResultMemoryMiB')" @update:model-value="draft.maxResultMemoryMiB = num($event)" />
      <BaseInput :model-value="draft.historyRetention" type="number" mono :label="t('settings.retention')" :error="errorFor('historyRetention')" @update:model-value="draft.historyRetention = num($event)" />
      <p class="-mt-2 text-xs text-muted">{{ t("settings.hint") }}</p>
      <BaseToggle v-model="draft.showSystemDatabases" :label="t('settings.showSystem')" />
      <BaseToggle v-model="draft.confirmEdits" :label="t('settings.confirmEdits')" />
      <fieldset class="flex flex-col gap-1.5 rounded-md border border-border p-3" data-testid="settings-queries">
        <legend class="px-1 text-xs font-medium text-muted">{{ t("queries.settings.title") }}</legend>
        <p class="font-data text-xs break-all" data-testid="settings-queries-path">{{ queryFiles.root?.path || "—" }}<span v-if="queryFiles.root?.isDefault" class="font-sans text-muted"> · {{ t("queries.settings.defaultHint") }}</span></p>
        <div class="flex flex-wrap gap-2">
          <BaseButton size="sm" type="button" data-testid="settings-queries-change" @click="queryFiles.pickRoot()">{{ t("queries.settings.change") }}</BaseButton>
          <BaseButton size="sm" type="button" :disabled="queryFiles.root?.isDefault" @click="queryFiles.resetRoot()">{{ t("queries.settings.reset") }}</BaseButton>
          <BaseButton size="sm" type="button" @click="queryFiles.reveal('')">{{ t("queries.settings.reveal") }}</BaseButton>
        </div>
        <p class="text-xs text-muted">{{ t("queries.settings.hint") }}</p>
      </fieldset>
      <p v-if="message && !field" class="text-xs text-danger" role="alert">{{ message }}</p>
    </form>
    <template #footer>
      <BaseButton variant="ghost" @click="ui.settingsOpen = false">{{ t("common.cancel") }}</BaseButton>
      <BaseButton variant="primary" :disabled="saving" data-testid="settings-save" @click="save">{{ t("common.save") }}</BaseButton>
    </template>
  </BaseModal>
</template>
