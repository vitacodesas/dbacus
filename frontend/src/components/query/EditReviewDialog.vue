<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useQueryRunner } from "../../composables/useQueryRunner";
import { useEditsStore } from "../../stores/edits";
import BaseButton from "../ui/BaseButton.vue";
import BaseModal from "../ui/BaseModal.vue";

const { t } = useI18n();
const edits = useEditsStore();
const runner = useQueryRunner();

const review = computed(() => edits.review);
const open = computed({
  get: () => review.value !== null,
  set: (v: boolean) => !v && edits.closeReview(),
});
// Tablas que un DELETE del lote modificaría por claves foráneas (CASCADE/SET NULL): aviso de texto fijo.
const cascades = computed(() => {
  const r = review.value;
  if (!r || !r.statements.some((s) => s.kind === "delete")) return [];
  return edits.infoOf(r.queryId, r.stmt)?.deleteAffects ?? [];
});
const kindClass = (kind: string) => (kind === "delete" ? "border-danger" : kind === "insert" ? "border-success" : "border-border");
const conflictText = computed(() => {
  const c = review.value?.conflict;
  if (!c) return "";
  return t(c.reason === "missing" ? "edit.conflictMissing" : "edit.conflictChanged", { row: c.row + 1 });
});

// Tras un conflicto lo habitual es empezar de nuevo con datos frescos: se descartan los cambios y se vuelve a ejecutar.
async function discardAndReload() {
  const r = review.value;
  if (!r) return;
  edits.closeReview();
  await edits.discard(r.tabId, r.queryId, r.stmt);
  await runner.runAll(r.tabId);
}
</script>

<template>
  <BaseModal v-model:open="open" :title="t('edit.reviewTitle')" :description="t('edit.reviewIntro')" size="lg">
    <div v-if="review" class="flex flex-col gap-3" data-testid="edit-review">
      <ol class="flex flex-col gap-2">
        <li v-for="s in review.statements" :key="`${s.kind}:${s.row}`" class="rounded-md border bg-bg p-2" :class="kindClass(s.kind)" :data-kind="s.kind">
          <div class="mb-1 text-[11px] text-muted">{{ t(`edit.kind.${s.kind}`) }} · {{ t("query.rowN", { n: s.row + 1 }) }}</div>
          <pre class="font-data text-xs break-words whitespace-pre-wrap" data-testid="edit-sql">{{ s.sql }}</pre>
        </li>
      </ol>
      <p v-if="cascades.length" class="rounded-md border border-warning bg-warning/10 p-2 text-sm" role="note" data-testid="edit-cascade">
        {{ t("edit.cascadeWarning", { tables: cascades.join(", ") }) }}
      </p>
      <p v-if="conflictText" class="rounded-md border border-danger p-2 text-sm" role="alert" data-testid="edit-conflict">{{ conflictText }}</p>
      <p v-if="review.error" class="rounded-md border border-danger p-2 text-sm" role="alert" data-testid="edit-error">{{ review.error }}</p>
    </div>
    <template #footer>
      <template v-if="review?.conflict">
        <BaseButton @click="edits.closeReview()">{{ t("edit.close") }}</BaseButton>
        <BaseButton variant="primary" data-testid="edit-reload" @click="discardAndReload">{{ t("edit.discardAndReload") }}</BaseButton>
      </template>
      <template v-else>
        <BaseButton :disabled="review?.applying" @click="edits.closeReview()">{{ t("common.cancel") }}</BaseButton>
        <BaseButton variant="primary" :loading="review?.applying" data-testid="edit-confirm" @click="edits.apply()">{{ t("edit.apply") }}</BaseButton>
      </template>
    </template>
  </BaseModal>
</template>
