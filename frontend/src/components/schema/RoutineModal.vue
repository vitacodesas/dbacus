<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useConnectionsStore } from "../../stores/connections";
import { useRoutinesStore } from "../../stores/routines";
import { useSchemaStore } from "../../stores/schema";
import { useSessionsStore } from "../../stores/sessions";
import BaseButton from "../ui/BaseButton.vue";
import BaseModal from "../ui/BaseModal.vue";
import BaseSpinner from "../ui/BaseSpinner.vue";
import BaseTabs from "../ui/BaseTabs.vue";
import CodeEditor from "../ui/CodeEditor.vue";

const { t } = useI18n();
const routines = useRoutinesStore();
const connections = useConnectionsStore();
const sessions = useSessionsStore();
const schema = useSchemaStore();

const target = computed(() => routines.target);
const open = computed({
  get: () => target.value !== null,
  set: (v: boolean) => {
    if (v) return;
    // Cerrar con cambios sin guardar pide confirmación dentro del propio modal (sin diálogos nativos).
    if (routines.dirty) closing.value = true;
    else routines.reset();
  },
});
const closing = ref(false);
watch(() => routines.target, () => (closing.value = false));

const readOnly = computed(() => !!connections.byId.get(sessions.byId[target.value?.sessionId ?? ""]?.profileId ?? "")?.readOnly);
const system = computed(() => !!schema.databasesOf(target.value?.sessionId ?? "")?.find((d) => d.name === target.value?.db)?.isSystem);
const detail = computed(() => routines.detail);
const canEdit = computed(() => !readOnly.value && !system.value && !!detail.value && !detail.value.bodyHidden);
const kindLabel = computed(() => t(`routines.kind.${target.value?.kind ?? "procedure"}`));
const title = computed(() =>
  target.value?.name === null ? t("routines.titleNew", { kind: kindLabel.value.toLowerCase() }) : t("routines.titleView", { kind: kindLabel.value, name: `${target.value?.db}.${target.value?.name}` }),
);
const tab = ref("code");
watch(() => routines.target, () => (tab.value = "code"));
const tabs = computed(() => [{ value: "code", label: t("routines.code") }, { value: "params", label: t("routines.params") }]);

function discardAndClose() {
  closing.value = false;
  routines.reset();
}
</script>

<template>
  <BaseModal v-model:open="open" :title="title" size="xl">
    <div v-if="routines.loading" class="flex items-center gap-2 py-8 text-sm text-muted"><BaseSpinner class="size-4" />{{ t("routines.loading") }}</div>
    <p v-else-if="routines.loadError" class="rounded-md border border-danger p-2 text-sm" role="alert" data-testid="routine-load-error">{{ routines.loadError }}</p>
    <div v-else class="flex flex-col gap-3" data-testid="routine-modal">
      <dl v-if="detail" class="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted">
        <div v-if="detail.definer"><dt class="inline">{{ t("routines.definer") }}: </dt><dd class="font-data inline">{{ detail.definer }}</dd></div>
        <div v-if="detail.securityType"><dt class="inline">{{ t("routines.security") }}: </dt><dd class="font-data inline">{{ detail.securityType }}</dd></div>
        <div v-if="detail.returns"><dt class="inline">{{ t("routines.returns") }}: </dt><dd class="font-data inline">{{ detail.returns }}</dd></div>
        <div v-if="detail.comment"><dt class="inline">{{ t("routines.comment") }}: </dt><dd class="inline">{{ detail.comment }}</dd></div>
      </dl>
      <p v-if="detail?.bodyHidden" class="rounded-md border border-warning bg-warning/10 p-2 text-sm" role="note" data-testid="routine-body-hidden">{{ t("routines.bodyHidden") }}</p>
      <p v-else-if="detail && readOnly" class="text-xs text-muted" role="note" data-testid="routine-readonly-note">{{ t("routines.readOnlyNote") }}</p>
      <p v-else-if="detail && system" class="text-xs text-muted" role="note">{{ t("routines.systemNote") }}</p>

      <BaseTabs v-if="detail && !detail.bodyHidden" v-model="tab" :items="tabs">
        <template #code>
          <div class="h-[50vh] pt-3"><CodeEditor v-model="routines.text" :read-only="!routines.editing" :label="title" /></div>
        </template>
        <template #params>
          <div class="pt-3">
            <p v-if="!detail.params?.length" class="text-sm text-muted">{{ t("routines.noParams") }}</p>
            <table v-else class="w-full text-sm" data-testid="routine-params">
              <tbody>
                <tr v-for="p in detail.params" :key="p.ordinal" class="border-b border-border">
                  <td class="py-1 pr-3 text-xs text-muted">{{ p.mode }}</td>
                  <td class="font-data py-1 pr-3">{{ p.name }}</td>
                  <td class="font-data py-1 text-muted">{{ p.type }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </BaseTabs>
      <div v-else-if="!detail" class="h-[50vh]"><CodeEditor v-model="routines.text" :label="title" /></div>

      <p v-if="routines.actionError" class="rounded-md border border-danger p-2 text-sm" role="alert" data-testid="routine-action-error">{{ routines.actionError }}</p>
      <div v-if="closing" class="flex items-center gap-2 rounded-md border border-warning bg-warning/10 p-2 text-sm" role="alert" data-testid="routine-close-confirm">
        <span class="mr-auto">{{ t("routines.changed") }}</span>
        <BaseButton size="sm" @click="closing = false">{{ t("routines.edit") }}</BaseButton>
        <BaseButton size="sm" variant="danger" data-testid="routine-discard-close" @click="discardAndClose()">{{ t("routines.discard") }}</BaseButton>
      </div>
    </div>
    <template #footer>
      <BaseButton v-if="routines.mode === 'edit' && routines.dirty" class="mr-auto" data-testid="routine-discard" @click="routines.discard()">{{ t("routines.discard") }}</BaseButton>
      <BaseButton @click="open = false">{{ t("routines.close") }}</BaseButton>
      <BaseButton v-if="routines.mode === 'view' && detail && !detail.bodyHidden" variant="primary" :disabled="!canEdit" data-testid="routine-edit" @click="routines.startEdit()">{{ t("routines.edit") }}</BaseButton>
      <BaseButton v-if="routines.editing" variant="primary" :disabled="!routines.dirty || routines.checking" :loading="routines.checking" data-testid="routine-review" @click="routines.requestReview()">{{ t("routines.review") }}</BaseButton>
    </template>
  </BaseModal>
</template>
