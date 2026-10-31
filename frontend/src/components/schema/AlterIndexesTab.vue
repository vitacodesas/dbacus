<script setup lang="ts">
import { computed } from "vue";
import { Lock, Plus, Trash2, Undo2 } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { columnsFromNames, splitList, type IdxDraft, type IndexKind } from "../../composables/alterOps";
import { useAlterStore } from "../../stores/alter";
import BaseButton from "../ui/BaseButton.vue";

const { t } = useI18n();
const alter = useAlterStore();
const field = "h-7 w-full rounded border border-border bg-bg px-2 text-sm disabled:opacity-60";

const changed = computed(() => {
  const set = new Map<string, string>();
  for (const o of alter.changeSet?.ops ?? []) {
    if (o.kind === "dropIndex") set.set(o.name, "deleted");
    if (o.kind === "renameIndex") set.set(o.name, "modified");
  }
  for (const o of alter.changeSet?.ops ?? []) if (o.kind === "addIndex") set.set(set.has(o.name) ? o.name : `+${o.name}`, set.has(o.name) ? "modified" : "new");
  return set;
});
function stateOf(d: IdxDraft) {
  if (d.deleted) return "deleted";
  return d.orig ? (changed.value.get(d.orig) ?? "") : (changed.value.get(`+${d.name}`) ?? "new");
}
const chip: Record<string, string> = { new: "bg-success/15 text-success", modified: "bg-warning/15 text-warning", deleted: "bg-danger/15 text-danger" };
const kinds: IndexKind[] = ["index", "unique", "fulltext"];

const names = (d: IdxDraft) => d.columns.map((c) => c.name).join(", ");
const setNames = (d: IdxDraft, v: string) => (d.columns = columnsFromNames(splitList(v), d.columns));
const columnList = computed(() => (alter.model?.columns ?? []).map((c) => c.name));
</script>

<template>
  <div class="flex flex-col gap-2" data-testid="alter-indexes">
    <datalist id="alter-columns"><option v-for="c in columnList" :key="c" :value="c" /></datalist>
    <table class="w-full border-separate border-spacing-y-1 text-left text-sm">
      <thead class="text-[11px] text-muted uppercase">
        <tr><th class="font-medium">{{ t("alter.col.name") }}</th><th class="font-medium">{{ t("alter.idx.kind") }}</th><th class="font-medium">{{ t("alter.idx.columns") }}</th><th class="w-24" /></tr>
      </thead>
      <tbody>
        <tr v-for="d in alter.drafts.idxs" :key="d.key" :class="d.deleted && 'opacity-50'" :data-testid="`alter-idx-${d.orig ?? 'new'}`" :data-state="stateOf(d)">
          <td class="pr-1"><input v-model="d.name" :class="[field, 'font-data', d.deleted && 'line-through']" :disabled="d.deleted || !!d.locked" :aria-label="t('alter.col.name')" data-testid="alter-idx-name"></td>
          <td class="pr-1">
            <select v-model="d.kind" :class="field" :disabled="d.deleted || !!d.locked" :aria-label="t('alter.idx.kind')">
              <option v-for="k in kinds" :key="k" :value="k">{{ t(`alter.idx.kinds.${k}`) }}</option>
            </select>
          </td>
          <td class="pr-1">
            <input :value="names(d)" list="alter-columns" :class="[field, 'font-data']" :disabled="d.deleted || !!d.locked" :placeholder="t('alter.idx.columnsHint')" :aria-label="t('alter.idx.columns')" data-testid="alter-idx-columns" @change="setNames(d, ($event.target as HTMLInputElement).value)">
          </td>
          <td class="text-right align-middle whitespace-nowrap">
            <span v-if="stateOf(d)" class="mr-1 rounded px-1.5 py-0.5 text-[10px] font-medium" :class="chip[stateOf(d)]">{{ t(`alter.state.${stateOf(d)}`) }}</span>
            <span v-if="d.locked" class="inline-flex text-muted" :title="t(`alter.locked.${d.locked}`)"><Lock class="size-3.5" /></span>
            <template v-else>
              <button v-if="d.deleted || (d.orig && stateOf(d))" type="button" class="rounded p-1 text-muted hover:bg-hover" :aria-label="t('alter.undo')" @click="alter.revert('idxs', d.key)"><Undo2 class="size-3.5" /></button>
              <button v-if="!d.deleted" type="button" class="rounded p-1 text-muted hover:bg-hover hover:text-danger" :aria-label="t('alter.remove')" data-testid="alter-remove" @click="alter.removeIndex(d.key)"><Trash2 class="size-3.5" /></button>
            </template>
          </td>
        </tr>
      </tbody>
    </table>
    <div><BaseButton size="sm" data-testid="alter-add-index" @click="alter.addIndex()"><Plus class="size-3.5" />{{ t("alter.addIndex") }}</BaseButton></div>
  </div>
</template>
