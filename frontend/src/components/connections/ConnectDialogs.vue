<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useConnectFlowStore, type SecretField } from "../../stores/connectFlow";
import BaseButton from "../ui/BaseButton.vue";
import BaseInput from "../ui/BaseInput.vue";
import BaseModal from "../ui/BaseModal.vue";

const { t } = useI18n();
const flow = useConnectFlowStore();

// Contraseñas pedidas al conectar: viven solo en esta ventana y se descartan al cerrar.
const values = ref<Partial<Record<SecretField, string>>>({});
const passwordOpen = computed({
  get: () => !!flow.passwordRequest,
  set: (open) => {
    if (!open) flow.passwordRequest?.resolve(null);
  },
});
watch(() => flow.passwordRequest, () => (values.value = {}));

function submitPassword() {
  flow.passwordRequest?.resolve({ ...values.value });
}

const hostKeyOpen = computed({
  get: () => !!flow.hostKeyRequest,
  set: (open) => {
    if (!open) flow.hostKeyRequest?.resolve(false);
  },
});
</script>

<template>
  <BaseModal v-model:open="passwordOpen" :title="t('prompt.title', { name: flow.passwordRequest?.profileName ?? '' })" :description="t('prompt.hint')" size="sm">
    <form class="flex flex-col gap-3" @submit.prevent="submitPassword">
      <BaseInput
        v-for="field in flow.passwordRequest?.fields ?? []"
        :key="field"
        v-model="values[field]"
        type="password"
        :label="t(`prompt.fields.${field}`)"
        autofocus
      />
      <button type="submit" class="hidden" />
    </form>
    <template #footer>
      <BaseButton @click="passwordOpen = false">{{ t("common.cancel") }}</BaseButton>
      <BaseButton variant="primary" @click="submitPassword">{{ t("prompt.connect") }}</BaseButton>
    </template>
  </BaseModal>

  <BaseModal
    v-model:open="hostKeyOpen"
    :title="flow.hostKeyRequest?.mismatch ? t('hostkey.mismatchTitle') : t('hostkey.title')"
    size="md"
  >
    <template v-if="flow.hostKeyRequest">
      <p class="text-sm" :class="flow.hostKeyRequest.mismatch && 'text-danger'">
        {{ t(flow.hostKeyRequest.mismatch ? "hostkey.mismatchMessage" : "hostkey.message", { host: flow.hostKeyRequest.host, port: flow.hostKeyRequest.port }) }}
      </p>
      <p class="mt-3 text-xs text-muted">{{ t("hostkey.fingerprint") }}</p>
      <code class="font-data mt-1 block rounded-md border border-border bg-bg p-2 text-xs break-all select-all">{{ flow.hostKeyRequest.fingerprint }}</code>
    </template>
    <template #footer>
      <BaseButton @click="hostKeyOpen = false">{{ flow.hostKeyRequest?.mismatch ? t("common.close") : t("common.cancel") }}</BaseButton>
      <BaseButton v-if="flow.hostKeyRequest && !flow.hostKeyRequest.mismatch" variant="primary" @click="flow.hostKeyRequest.resolve(true)">
        {{ t("hostkey.trust") }}
      </BaseButton>
    </template>
  </BaseModal>
</template>
