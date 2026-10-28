<script setup lang="ts">
import { useId } from "vue";
import { useI18n } from "vue-i18n";
import { pickFile } from "../../api/connections";
import { asAppError } from "../../api/errors";
import { useUiStore } from "../../stores/ui";
import BaseButton from "../ui/BaseButton.vue";

const props = defineProps<{ label: string; error?: string; dialogTitle?: string }>();
const model = defineModel<string>({ default: "" });
const { t } = useI18n();
const ui = useUiStore();
const id = useId();

async function browse() {
  try {
    const path = await pickFile(props.dialogTitle ?? props.label);
    if (path) model.value = path;
  } catch (e) {
    ui.toast("error", asAppError(e).message);
  }
}
</script>

<template>
  <div class="flex flex-col gap-1">
    <label :for="id" class="text-xs font-medium text-muted">{{ label }}</label>
    <div class="flex gap-1">
      <input
        :id="id"
        v-model="model"
        :placeholder="t('form.hints.pathNone')"
        class="font-data h-8 min-w-0 flex-1 rounded-md border bg-panel px-2.5 text-xs placeholder:text-muted/70"
        :class="error ? 'border-danger' : 'border-border'"
      >
      <BaseButton @click="browse">{{ t("form.buttons.browse") }}</BaseButton>
      <BaseButton v-if="model" variant="ghost" @click="model = ''">{{ t("form.buttons.clear") }}</BaseButton>
    </div>
    <p v-if="error" class="text-xs text-danger">{{ error }}</p>
  </div>
</template>
