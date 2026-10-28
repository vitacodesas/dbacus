<script setup lang="ts">
import { useI18n } from "vue-i18n";
import BaseButton from "./BaseButton.vue";
import BaseModal from "./BaseModal.vue";

defineProps<{ title: string; message: string; confirmLabel?: string; danger?: boolean }>();
const open = defineModel<boolean>("open", { default: false });
const emit = defineEmits<{ confirm: []; cancel: [] }>();
const { t } = useI18n();

function cancel() {
  open.value = false;
  emit("cancel");
}
function confirm() {
  open.value = false;
  emit("confirm");
}
</script>

<template>
  <BaseModal v-model:open="open" :title="title" size="sm">
    <p class="text-sm">{{ message }}</p>
    <template #footer>
      <BaseButton @click="cancel">{{ t("common.cancel") }}</BaseButton>
      <BaseButton :variant="danger ? 'danger' : 'primary'" @click="confirm">{{ confirmLabel ?? t("common.confirm") }}</BaseButton>
    </template>
  </BaseModal>
</template>
