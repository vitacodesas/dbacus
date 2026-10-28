<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { asAppError } from "../../api/errors";
import { useConnectionsStore } from "../../stores/connections";
import { useConnectionUiStore } from "../../stores/connectionUi";
import { useSessionsStore } from "../../stores/sessions";
import { useUiStore } from "../../stores/ui";
import BaseButton from "../ui/BaseButton.vue";
import BaseCheckbox from "../ui/BaseCheckbox.vue";
import BaseModal from "../ui/BaseModal.vue";

const { t } = useI18n();
const connections = useConnectionsStore();
const sessions = useSessionsStore();
const uiConn = useConnectionUiStore();
const ui = useUiStore();

const target = computed(() => (uiConn.deleteTargetId ? connections.byId.get(uiConn.deleteTargetId) : undefined));
const open = computed({
  get: () => !!target.value,
  set: (v) => {
    if (!v) uiConn.deleteTargetId = null;
  },
});
const withHistory = ref(false);
const busy = ref(false);
watch(open, () => (withHistory.value = false));

async function confirm() {
  if (!target.value) return;
  busy.value = true;
  try {
    await connections.remove(target.value.id, withHistory.value);
    await sessions.refresh(); // Go cerró las sesiones del perfil
    ui.toast("success", t("connections.toast.deleted"));
    uiConn.deleteTargetId = null;
  } catch (e) {
    ui.toast("error", asAppError(e).message);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <BaseModal v-model:open="open" :title="t('delete.title')" size="sm">
    <p class="text-sm">{{ t("delete.message", { name: target?.name ?? "" }) }}</p>
    <BaseCheckbox v-model="withHistory" class="mt-3" :label="t('delete.withHistory')" />
    <template #footer>
      <BaseButton @click="open = false">{{ t("common.cancel") }}</BaseButton>
      <BaseButton variant="danger" :loading="busy" @click="confirm">{{ t("delete.confirm") }}</BaseButton>
    </template>
  </BaseModal>
</template>
