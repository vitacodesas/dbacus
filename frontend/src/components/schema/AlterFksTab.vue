<script setup lang="ts">
import { computed } from "vue";
import { Plus, Trash2, Undo2 } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { splitList, type FkDraft } from "../../composables/alterOps";
import { useAlterStore } from "../../stores/alter";
import { useSchemaStore } from "../../stores/schema";
import BaseButton from "../ui/BaseButton.vue";

const { t } = useI18n();
const alter = useAlterStore();
const schema = useSchemaStore();
const field = "h-7 w-full rounded border border-border bg-bg px-2 text-sm disabled:opacity-60";
const actions = ["", "RESTRICT", "CASCADE", "SET NULL", "NO ACTION"];

const changed = computed(() => {
  const set = new Map<string, string>();
  for (const o of alter.changeSet?.ops ?? []) if (o.kind === "dropFK") set.set(o.name, "deleted");
  for (const o of alter.changeSet?.ops ?? []) if (o.kind === "addFK") set.set(set.has(o.name) ? o.name : `+${o.name}`, set.has(o.name) ? "modified" : "new");
  return set;
});
function stateOf(d: FkDraft) {
  if (d.deleted) return "deleted";
  return d.orig ? (changed.value.get(d.orig) ?? "") : (changed.value.get(`+${d.name}`) ?? "new");
}
const chip: Record<string, string> = { new: "bg-success/15 text-success", modified: "bg-warning/15 text-warning", deleted: "bg-danger/15 text-danger" };

const tables = computed(() => {
  const tg = alter.target;
  return tg ? schema.tablesOf(tg.sessionId, tg.db).map((x) => x.name) : [];
});
const columnList = computed(() => (alter.model?.columns ?? []).map((c) => c.name));
const join = (l: string[]) => l.join(", ");
</script>

<template>
  <div class="flex flex-col gap-2" data-testid="alter-fks">
    <datalist id="alter-fk-tables"><option v-for="x in tables" :key="x" :value="x" /></datalist>
    <datalist id="alter-fk-columns"><option v-for="c in columnList" :key="c" :value="c" /></datalist>
    <div class="overflow-x-auto">
      <table class="w-full min-w-[44rem] border-separate border-spacing-y-1 text-left text-sm">
        <thead class="text-[11px] text-muted uppercase">
          <tr>
            <th class="font-medium">{{ t("alter.col.name") }}</th><th class="font-medium">{{ t("alter.fk.columns") }}</th><th class="font-medium">{{ t("alter.fk.refTable") }}</th>
            <th class="font-medium">{{ t("alter.fk.refColumns") }}</th><th class="font-medium">ON UPDATE</th><th class="font-medium">ON DELETE</th><th class="w-20" />
          </tr>
        </thead>
        <tbody>
          <tr v-for="d in alter.drafts.fks" :key="d.key" :class="d.deleted && 'opacity-50'" :data-testid="`alter-fk-${d.orig ?? 'new'}`" :data-state="stateOf(d)">
            <td class="pr-1"><input v-model="d.name" :class="[field, 'font-data', d.deleted && 'line-through']" :disabled="d.deleted || !!d.orig" :aria-label="t('alter.col.name')" data-testid="alter-fk-name"></td>
            <td class="pr-1"><input :value="join(d.columns)" list="alter-fk-columns" :class="[field, 'font-data']" :disabled="d.deleted" :aria-label="t('alter.fk.columns')" data-testid="alter-fk-columns" @change="d.columns = splitList(($event.target as HTMLInputElement).value)"></td>
            <td class="pr-1"><input v-model="d.refTable" list="alter-fk-tables" :class="[field, 'font-data']" :disabled="d.deleted" :aria-label="t('alter.fk.refTable')" data-testid="alter-fk-reftable"></td>
            <td class="pr-1"><input :value="join(d.refColumns)" :class="[field, 'font-data']" :disabled="d.deleted" :aria-label="t('alter.fk.refColumns')" data-testid="alter-fk-refcolumns" @change="d.refColumns = splitList(($event.target as HTMLInputElement).value)"></td>
            <td class="pr-1"><select v-model="d.onUpdate" :class="field" :disabled="d.deleted" aria-label="ON UPDATE"><option v-for="a in actions" :key="a" :value="a">{{ a || t("alter.fk.default") }}</option></select></td>
            <td class="pr-1"><select v-model="d.onDelete" :class="field" :disabled="d.deleted" aria-label="ON DELETE"><option v-for="a in actions" :key="a" :value="a">{{ a || t("alter.fk.default") }}</option></select></td>
            <td class="text-right align-middle whitespace-nowrap">
              <span v-if="stateOf(d)" class="mr-1 rounded px-1.5 py-0.5 text-[10px] font-medium" :class="chip[stateOf(d)]">{{ t(`alter.state.${stateOf(d)}`) }}</span>
              <button v-if="d.deleted || (d.orig && stateOf(d))" type="button" class="rounded p-1 text-muted hover:bg-hover" :aria-label="t('alter.undo')" @click="alter.revert('fks', d.key)"><Undo2 class="size-3.5" /></button>
              <button v-if="!d.deleted" type="button" class="rounded p-1 text-muted hover:bg-hover hover:text-danger" :aria-label="t('alter.remove')" data-testid="alter-remove" @click="alter.removeFk(d.key)"><Trash2 class="size-3.5" /></button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div><BaseButton size="sm" data-testid="alter-add-fk" @click="alter.addFk()"><Plus class="size-3.5" />{{ t("alter.addFk") }}</BaseButton></div>
  </div>
</template>
