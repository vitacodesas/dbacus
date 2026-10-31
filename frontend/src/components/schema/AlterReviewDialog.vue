<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useAlterStore } from "../../stores/alter";
import BaseButton from "../ui/BaseButton.vue";
import BaseModal from "../ui/BaseModal.vue";

const { t } = useI18n();
const alter = useAlterStore();

const review = computed(() => alter.review);
const open = computed({ get: () => review.value !== null, set: (v: boolean) => !v && alter.closeReview() });
const statements = computed(() => alter.preview?.statements ?? []);
const warnings = computed(() => alter.preview?.warnings ?? []);
const partial = computed(() => review.value?.partial ?? null);
const wClass = (s: string) => (s === "danger" ? "border-danger text-danger" : s === "warning" ? "border-warning" : "border-border text-muted");
</script>

<template>
  <!-- Siempre se revisa el SQL antes de ejecutarlo y no hay ajuste para saltarlo: el DDL no se puede revertir (spec 19, D5). -->
  <BaseModal v-model:open="open" :title="t('alter.reviewTitle')" :description="t('alter.reviewIntro')" size="lg">
    <div v-if="review" class="flex flex-col gap-3" data-testid="alter-review">
      <ol class="flex flex-col gap-2">
        <li v-for="(s, i) in statements" :key="i" class="rounded-md border bg-bg p-2" :class="alter.preview?.destructive ? 'border-danger' : 'border-border'">
          <pre class="font-data text-xs break-words whitespace-pre-wrap" data-testid="alter-review-sql">{{ s }};</pre>
        </li>
      </ol>
      <p class="rounded-md border border-warning bg-warning/10 p-2 text-sm" role="note" data-testid="alter-no-rollback">
        {{ statements.length > 1 ? t("alter.noRollbackMany", { n: statements.length }) : t("alter.noRollback") }}
      </p>
      <ul v-if="warnings.length" class="flex flex-col gap-1">
        <li v-for="w in warnings" :key="w.code + w.message" class="rounded-md border p-2 text-sm" :class="wClass(w.severity)" role="note" data-testid="alter-review-warning">{{ w.message }}</li>
      </ul>
      <div v-if="partial" class="rounded-md border border-danger p-2 text-sm" role="alert" data-testid="alter-partial">
        <p class="font-medium">{{ t("alter.partialTitle", { done: partial.applied?.length ?? 0, total: statements.length }) }}</p>
        <p class="mt-1">{{ t("alter.partialFailed", { n: (partial.failed?.index ?? 0) + 1 }) }} {{ partial.failed?.message }}</p>
        <p class="mt-1 text-muted">{{ t("alter.partialReloaded") }}</p>
      </div>
      <p v-if="review.error" class="rounded-md border border-danger p-2 text-sm" role="alert" data-testid="alter-error">{{ review.error }}</p>
    </div>
    <template #footer>
      <BaseButton v-if="partial" variant="primary" @click="alter.closeReview()">{{ t("common.close") }}</BaseButton>
      <template v-else>
        <BaseButton :disabled="review?.applying" @click="alter.closeReview()">{{ t("common.cancel") }}</BaseButton>
        <BaseButton :variant="alter.preview?.destructive ? 'danger' : 'primary'" :loading="review?.applying" data-testid="alter-confirm" @click="alter.apply()">{{ t("alter.execute") }}</BaseButton>
      </template>
    </template>
  </BaseModal>
</template>
