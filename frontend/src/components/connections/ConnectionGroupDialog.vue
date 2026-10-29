<script setup lang="ts">
import { ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { asAppError, ErrorCode } from "../../api/errors";
import { useConnectionsStore } from "../../stores/connections";
import { useUiStore } from "../../stores/ui";
import type { ConnectionGroup } from "../../types";
import BaseButton from "../ui/BaseButton.vue";
import BaseInput from "../ui/BaseInput.vue";
import BaseModal from "../ui/BaseModal.vue";
import ColorSwatches from "../ui/ColorSwatches.vue";

// Edita nombre y color de un grupo existente. Los grupos nuevos se crean desde el formulario de la conexión.
const props = defineProps<{ group: ConnectionGroup | null }>();
const open = defineModel<boolean>("open", { default: false });
const emit = defineEmits<{ delete: [group: ConnectionGroup] }>();

const { t } = useI18n();
const connections = useConnectionsStore();
const ui = useUiStore();

const name = ref("");
const color = ref("");
const errors = ref<Record<string, string>>({});
const saving = ref(false);

watch(open, (isOpen) => {
  if (!isOpen || !props.group) return;
  name.value = props.group.name;
  color.value = props.group.color ?? "";
  errors.value = {};
});

async function save() {
  if (!props.group || saving.value) return;
  if (!name.value.trim()) {
    errors.value = { name: t("form.errors.required") };
    return;
  }
  saving.value = true;
  try {
    await connections.saveGroup({ ...props.group, name: name.value, color: color.value });
    ui.toast("success", t("groups.toast.saved"));
    open.value = false;
  } catch (e) {
    const err = asAppError(e);
    // Go repite la validación: su error viaja con el campo en `detail` y se pinta junto a él.
    if (err.code === ErrorCode.Validation && err.detail) errors.value = { [err.detail]: err.message };
    else ui.toast("error", err.message);
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <BaseModal v-model:open="open" :title="t('groups.dialog.title')" size="sm">
    <form class="flex flex-col gap-3" @submit.prevent="save">
      <BaseInput v-model="name" :label="t('groups.dialog.name')" :error="errors.name" />
      <ColorSwatches v-model="color" :label="t('groups.dialog.color')" :none-label="t('groups.dialog.colorNone')" :error="errors.color" />
    </form>
    <template #footer>
      <BaseButton variant="danger" class="mr-auto" data-testid="group-delete" @click="group && emit('delete', group)">{{ t("groups.delete") }}</BaseButton>
      <BaseButton @click="open = false">{{ t("common.cancel") }}</BaseButton>
      <BaseButton variant="primary" :disabled="saving" data-testid="group-save" @click="save">{{ t("groups.dialog.save") }}</BaseButton>
    </template>
  </BaseModal>
</template>
