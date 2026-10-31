<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Eye, EyeOff, FolderTree, RefreshCw } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { refreshSchema } from "../../api/schema";
import { asAppError } from "../../api/errors";
import { sessionNodeId } from "../../composables/schemaTree";
import { useQueryActions } from "../../composables/useQueryActions";
import { useSchemaStore } from "../../stores/schema";
import { useSessionsStore } from "../../stores/sessions";
import { useUiStore } from "../../stores/ui";
import BaseButton from "../ui/BaseButton.vue";
import BaseEmptyState from "../ui/BaseEmptyState.vue";
import BaseModal from "../ui/BaseModal.vue";
import { useAlterStore } from "../../stores/alter";
import AlterModal from "./AlterModal.vue";
import AlterReviewDialog from "./AlterReviewDialog.vue";
import SchemaTree from "./SchemaTree.vue";

const { t } = useI18n();
const schema = useSchemaStore();
const sessions = useSessionsStore();
const ui = useUiStore();
const { openTable } = useQueryActions();
const alter = useAlterStore();

// Cada conexión que se abre aparece desplegada y con sus bases cargadas; las que se cierran se olvidan.
watch(
  () => Object.keys(sessions.byId),
  (now, before = []) => {
    for (const id of now.filter((x) => !before.includes(x))) {
      schema.setExpanded(id, sessionNodeId(id), true);
      void schema.loadDatabases(id);
    }
    before.filter((id) => !now.includes(id)).forEach((id) => schema.forgetSession(id));
  },
  { immediate: true },
);

async function refresh(sessionId: string, db = "") {
  try {
    await refreshSchema(sessionId, db); // Go invalida y emite schema:invalidated → el store recarga
  } catch (e) {
    ui.toast("error", asAppError(e).message);
  }
}
const refreshAll = () => Promise.all(sessions.list.map((s) => refresh(s.sessionId)));

async function disconnect(sessionId: string) {
  try {
    await sessions.disconnect(sessionId);
  } catch (e) {
    ui.toast("error", asAppError(e).message);
  }
}

const createTarget = ref<{ sessionId: string; db: string; table: string } | null>(null);
const createOpen = computed({ get: () => !!createTarget.value, set: (v) => !v && (createTarget.value = null) });
const createSql = computed(() => {
  const c = createTarget.value;
  return c ? (schema.detailOf(c.sessionId, c.db, c.table)?.createSql ?? "") : "";
});
async function viewCreate(sessionId: string, db: string, table: string) {
  createTarget.value = { sessionId, db, table };
  await schema.loadDetail(sessionId, db, table);
}
async function copyCreate() {
  await navigator.clipboard.writeText(createSql.value);
  ui.toast("success", t("schema.copied"));
}
</script>

<template>
  <section class="flex h-full flex-col" :aria-label="t('schema.title')">
    <div class="flex items-center justify-between gap-2 px-3 py-2">
      <h2 class="min-w-0 truncate text-xs font-semibold tracking-wide text-muted uppercase">{{ t("schema.title") }}</h2>
      <div v-if="sessions.list.length" class="flex shrink-0">
        <BaseButton size="sm" variant="ghost" :aria-label="schema.showSystem ? t('schema.hideSystem') : t('schema.showSystem')" :title="schema.showSystem ? t('schema.hideSystem') : t('schema.showSystem')" @click="schema.showSystem = !schema.showSystem">
          <component :is="schema.showSystem ? EyeOff : Eye" class="size-4" />
        </BaseButton>
        <BaseButton size="sm" variant="ghost" :aria-label="t('schema.refresh')" :title="t('schema.refresh')" @click="refreshAll()">
          <RefreshCw class="size-4" />
        </BaseButton>
      </div>
    </div>

    <BaseEmptyState v-if="!sessions.list.length" :title="t('schema.noSession')" :description="t('schema.noSessionDesc')">
      <template #icon><FolderTree class="size-8" /></template>
    </BaseEmptyState>

    <template v-else>
      <div class="px-3 pb-2">
        <input
          v-model="schema.filter"
          type="search"
          :placeholder="t('schema.filter')"
          :aria-label="t('schema.filter')"
          class="h-8 w-full rounded-md border border-border bg-bg px-2.5 text-sm placeholder:text-muted/70"
        >
      </div>
      <SchemaTree class="min-h-0 flex-1" @open-table="openTable" @view-create="viewCreate" @edit-structure="(sessionId, db, table) => alter.open({ sessionId, db, table })" @refresh="refresh" @disconnect="disconnect" />
    </template>

    <AlterModal />
    <AlterReviewDialog />

    <BaseModal v-model:open="createOpen" :title="t('schema.createTitle', { name: createTarget?.table ?? '' })" size="lg">
      <pre class="font-data max-h-[60vh] overflow-auto rounded-md border border-border bg-bg p-3 text-xs whitespace-pre-wrap">{{ createSql || t("schema.loading") }}</pre>
      <template #footer>
        <BaseButton :disabled="!createSql" @click="copyCreate">{{ t("schema.copy") }}</BaseButton>
        <BaseButton variant="primary" @click="createOpen = false">{{ t("common.close") }}</BaseButton>
      </template>
    </BaseModal>
  </section>
</template>
