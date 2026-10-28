<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useEditsStore } from "../../stores/edits";
import BaseDialogConfirm from "../ui/BaseDialogConfirm.vue";

const { t } = useI18n();
const edits = useEditsStore();

const prompt = computed(() => edits.discardPrompt);
const open = computed({
  get: () => prompt.value !== null,
  // Cerrar sin confirmar (Esc, fuera del diálogo) equivale a cancelar. El diálogo cierra antes de emitir `confirm`: se espera
  // un instante para no tomar esa confirmación por una cancelación.
  set: (v: boolean) => {
    if (!v) queueMicrotask(() => edits.discardPrompt && edits.answerDiscard(false));
  },
});
</script>

<template>
  <BaseDialogConfirm
    v-model:open="open"
    :title="t('edit.discardTitle')"
    :message="t('edit.discardMessage', { n: prompt?.count ?? 0 })"
    :confirm-label="t('edit.discardConfirm')"
    danger
    @confirm="edits.answerDiscard(true)"
  />
</template>
