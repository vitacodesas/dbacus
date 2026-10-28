<script setup lang="ts">
import { ref, useId } from "vue";
import { Eye, EyeOff } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import BaseButton from "../ui/BaseButton.vue";

const props = defineProps<{ label: string; saved: boolean; hint?: string; error?: string }>();
const model = defineModel<string>({ default: "" });
const clear = defineModel<boolean>("clear", { default: false });
const { t } = useI18n();
const id = useId();
const visible = ref(false);

function toggleClear() {
  clear.value = !clear.value;
  if (clear.value) model.value = "";
}
</script>

<template>
  <div class="flex flex-col gap-1">
    <label :for="id" class="text-xs font-medium text-muted">{{ label }}</label>
    <div class="flex gap-1">
      <input
        :id="id"
        v-model="model"
        :type="visible ? 'text' : 'password'"
        :disabled="clear"
        :placeholder="props.saved && !clear ? t('form.hints.passwordSaved') : ''"
        autocomplete="off"
        class="h-8 min-w-0 flex-1 rounded-md border bg-panel px-2.5 text-sm placeholder:text-muted/70 disabled:opacity-50"
        :class="error ? 'border-danger' : 'border-border'"
      >
      <BaseButton :aria-label="visible ? t('form.buttons.hide') : t('form.buttons.show')" :title="visible ? t('form.buttons.hide') : t('form.buttons.show')" @click="visible = !visible">
        <component :is="visible ? EyeOff : Eye" class="size-4" />
      </BaseButton>
    </div>
    <p v-if="error" class="text-xs text-danger">{{ error }}</p>
    <p v-else-if="hint" class="text-xs text-muted">{{ hint }}</p>
    <button v-if="saved" type="button" class="self-start text-xs text-muted underline hover:text-fg" @click="toggleClear">
      {{ clear ? t("form.buttons.keepPassword") : t("form.buttons.deletePassword") }}
    </button>
  </div>
</template>
