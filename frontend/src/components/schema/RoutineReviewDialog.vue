<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useRoutinesStore } from "../../stores/routines";
import { useUiStore } from "../../stores/ui";
import { writeClipboard } from "../../composables/clipboard";
import BaseButton from "../ui/BaseButton.vue";
import BaseCheckbox from "../ui/BaseCheckbox.vue";
import BaseModal from "../ui/BaseModal.vue";

const { t } = useI18n();
const routines = useRoutinesStore();
const ui = useUiStore();

const review = computed(() => routines.review);
const open = computed({ get: () => review.value !== null, set: (v: boolean) => !v && routines.closeReview() });
const isDrop = computed(() => review.value?.change.op === "drop");
const statements = computed(() => review.value?.preview.statements ?? []);
const warnings = computed(() => review.value?.preview.warnings ?? []);
// Los avisos fijos de «sin vuelta atrás» ya salen en su propio bloque: no se repiten en la lista.
const extra = computed(() => warnings.value.filter((w) => w.code !== "no_rollback"));
const canRecreateDefiner = computed(() => warnings.value.some((w) => w.code === "definer_foreign") || review.value?.change.recreateDefiner);
const failed = computed(() => review.value?.failed ?? null);
const untouched = computed(() => !!failed.value && !failed.value.restored && !failed.value.backupSql && !(failed.value.applied?.length));
const wClass = (s: string) => (s === "danger" ? "border-danger text-danger" : s === "warning" ? "border-warning" : "border-border text-muted");

async function copy(text: string) {
  if (await writeClipboard(text)) ui.toast("success", t("routines.copied"));
  else ui.toast("error", t("routines.copied") + " ✗");
}
</script>

<template>
  <!-- Siempre se revisa el SQL antes de ejecutarlo y no hay ajuste para saltarlo: el DDL no se puede revertir (spec 21, D5). -->
  <BaseModal v-model:open="open" :title="isDrop ? t('routines.deleteTitle', { kind: t(`routines.kind.${review?.change.kind ?? 'procedure'}`).toLowerCase() }) : t('routines.reviewTitle')" :description="isDrop ? t('routines.deleteIntro', { name: review?.change.name ?? '' }) : t('routines.reviewIntro')" size="lg">
    <div v-if="review" class="flex flex-col gap-3" data-testid="routine-review-dialog">
      <div v-if="isDrop && review.body" class="flex flex-col gap-1">
        <pre class="font-data max-h-48 overflow-auto rounded-md border border-border bg-bg p-2 text-xs whitespace-pre-wrap" data-testid="routine-delete-body">{{ review.body }}</pre>
        <BaseButton size="sm" class="self-end" @click="copy(review.body)">{{ t("routines.copy") }}</BaseButton>
      </div>
      <ol class="flex flex-col gap-2">
        <li v-for="(s, i) in statements" :key="i" class="rounded-md border bg-bg p-2" :class="review.preview.destructive ? 'border-danger' : 'border-border'">
          <pre class="font-data max-h-40 overflow-auto text-xs break-words whitespace-pre-wrap" data-testid="routine-review-sql">{{ s }};</pre>
        </li>
      </ol>
      <p class="rounded-md border border-warning bg-warning/10 p-2 text-sm" role="note" data-testid="routine-no-rollback">{{ t("routines.noRollback") }}</p>
      <ul v-if="extra.length" class="flex flex-col gap-1">
        <li v-for="w in extra" :key="w.code" class="rounded-md border p-2 text-sm" :class="wClass(w.severity)" role="note" data-testid="routine-review-warning">{{ w.message }}</li>
      </ul>
      <BaseCheckbox v-if="canRecreateDefiner && !failed" :model-value="review.change.recreateDefiner" data-testid="routine-recreate-definer" @update:model-value="routines.setRecreateDefiner($event)">{{ t("routines.recreateDefiner") }}</BaseCheckbox>

      <div v-if="failed" class="flex flex-col gap-2 rounded-md border border-danger p-2 text-sm" role="alert" data-testid="routine-failed">
        <p class="font-medium">{{ t("routines.partialTitle", { done: failed.applied?.length ?? 0, total: statements.length }) }}</p>
        <p>{{ t("routines.partialFailed", { n: (failed.failed?.index ?? 0) + 1 }) }} {{ failed.failed?.message }}</p>
        <p v-if="failed.restored" class="text-success" data-testid="routine-restored">{{ t("routines.restored") }}</p>
        <p v-else-if="untouched" data-testid="routine-untouched">{{ t("routines.untouched") }}</p>
        <template v-if="failed.backupSql">
          <p class="font-medium">{{ t("routines.notRestored") }}</p>
          <pre class="font-data max-h-40 overflow-auto rounded-md border border-border bg-bg p-2 text-xs whitespace-pre-wrap" data-testid="routine-backup">{{ failed.backupSql }}</pre>
          <BaseButton size="sm" class="self-end" @click="copy(failed.backupSql)">{{ t("routines.copy") }}</BaseButton>
        </template>
      </div>
      <p v-if="review.error" class="rounded-md border border-danger p-2 text-sm" role="alert" data-testid="routine-error">{{ review.error }}</p>
    </div>
    <template #footer>
      <BaseButton v-if="failed" variant="primary" @click="routines.closeReview()">{{ t("routines.close") }}</BaseButton>
      <template v-else>
        <BaseButton :disabled="review?.applying" @click="routines.closeReview()">{{ t("common.cancel") }}</BaseButton>
        <BaseButton :variant="review?.preview.destructive ? 'danger' : 'primary'" :loading="review?.applying" data-testid="routine-confirm" @click="routines.apply()">{{ isDrop ? t("routines.delete") : t("routines.execute") }}</BaseButton>
      </template>
    </template>
  </BaseModal>
</template>
