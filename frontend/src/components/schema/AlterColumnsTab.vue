<script setup lang="ts">
import { computed } from "vue";
import { ArrowDown, ArrowUp, Lock, Plus, Trash2, Undo2 } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { useAlterStore } from "../../stores/alter";
import BaseButton from "../ui/BaseButton.vue";

const { t } = useI18n();
const alter = useAlterStore();

const field = "h-7 w-full rounded border border-border bg-bg px-2 text-sm disabled:opacity-60";

// El estado de cada fila sale del ChangeSet (una sola fuente de verdad): lo que Go va a recibir es lo que se marca.
type State = "new" | "modified" | "deleted" | "";
const states = computed(() => {
  const map = new Map<string, State>();
  for (const o of alter.changeSet?.ops ?? []) {
    if (o.kind === "dropColumn") map.set(o.name, "deleted");
    else if (o.kind === "modifyColumn") map.set(o.name, "modified");
    else if (o.kind === "addColumn") map.set(`+${o.name}`, "new");
  }
  return map;
});
const stateOf = (d: (typeof alter.drafts.cols)[number]): State => (d.orig ? (states.value.get(d.orig) ?? "") : d.deleted ? "deleted" : (states.value.get(`+${d.name}`) ?? "new"));
const chip: Record<string, string> = { new: "bg-success/15 text-success", modified: "bg-warning/15 text-warning", deleted: "bg-danger/15 text-danger" };

const setDef = (d: (typeof alter.drafts.cols)[number], v: string) => (d.def = v === "" ? null : v);
</script>

<template>
  <div class="flex flex-col gap-2" data-testid="alter-columns">
    <div class="overflow-x-auto">
      <table class="w-full min-w-[44rem] border-separate border-spacing-y-1 text-left text-sm">
        <thead class="text-[11px] text-muted uppercase">
          <tr>
            <th class="w-12" /><th class="font-medium">{{ t("alter.col.name") }}</th><th class="font-medium">{{ t("alter.col.type") }}</th>
            <th class="font-medium" :title="t('alter.col.notNullHint')">{{ t("alter.col.notNull") }}</th><th class="font-medium">{{ t("alter.col.autoInc") }}</th>
            <th class="font-medium">{{ t("alter.col.default") }}</th><th class="font-medium">{{ t("alter.col.comment") }}</th><th class="w-24" />
          </tr>
        </thead>
        <tbody>
          <tr v-for="(d, i) in alter.drafts.cols" :key="d.key" :class="d.deleted && 'opacity-50'" :data-testid="`alter-col-${d.orig ?? 'new'}`" :data-state="stateOf(d)">
            <td class="pr-1 align-middle whitespace-nowrap">
              <button type="button" class="rounded p-0.5 text-muted hover:bg-hover disabled:opacity-30" :disabled="i === 0" :aria-label="t('alter.moveUp')" @click="alter.moveColumn(i, -1)"><ArrowUp class="size-3.5" /></button>
              <button type="button" class="rounded p-0.5 text-muted hover:bg-hover disabled:opacity-30" :disabled="i === alter.drafts.cols.length - 1" :aria-label="t('alter.moveDown')" @click="alter.moveColumn(i, 1)"><ArrowDown class="size-3.5" /></button>
            </td>
            <td class="pr-1"><input v-model="d.name" :class="[field, 'font-data', d.deleted && 'line-through']" :disabled="d.deleted || !!d.locked" :aria-label="t('alter.col.name')" data-testid="alter-col-name"></td>
            <td class="pr-1"><input v-model="d.type" :class="[field, 'font-data']" :disabled="d.deleted || !!d.locked" :aria-label="t('alter.col.type')" data-testid="alter-col-type"></td>
            <td class="pr-1 text-center"><input :checked="!d.nullable" type="checkbox" class="size-4 accent-[var(--accent)]" :disabled="d.deleted || !!d.locked" :aria-label="t('alter.col.notNull')" @change="d.nullable = !($event.target as HTMLInputElement).checked"></td>
            <td class="pr-1 text-center"><input v-model="d.autoInc" type="checkbox" class="size-4 accent-[var(--accent)]" :disabled="d.deleted || !!d.locked" :aria-label="t('alter.col.autoInc')"></td>
            <td class="pr-1">
              <div class="flex items-center gap-1">
                <input :value="d.def ?? ''" :class="[field, 'font-data']" :placeholder="t('alter.col.noDefault')" :disabled="d.deleted || !!d.locked" :aria-label="t('alter.col.default')" @input="setDef(d, ($event.target as HTMLInputElement).value)">
                <label class="flex shrink-0 items-center gap-1 text-[11px] text-muted" :title="t('alter.col.exprHint')"><input v-model="d.defExpr" type="checkbox" class="size-3.5 accent-[var(--accent)]" :disabled="d.deleted || !!d.locked || d.def === null">fx</label>
              </div>
            </td>
            <td class="pr-1"><input v-model="d.comment" :class="field" :disabled="d.deleted || !!d.locked" :aria-label="t('alter.col.comment')"></td>
            <td class="text-right align-middle whitespace-nowrap">
              <span v-if="stateOf(d)" class="mr-1 rounded px-1.5 py-0.5 text-[10px] font-medium" :class="chip[stateOf(d)]" data-testid="alter-chip">{{ t(`alter.state.${stateOf(d)}`) }}</span>
              <span v-if="d.locked" class="inline-flex text-muted" :title="t('alter.locked.generated')"><Lock class="size-3.5" /></span>
              <button v-else-if="d.deleted || (d.orig && stateOf(d))" type="button" class="rounded p-1 text-muted hover:bg-hover" :aria-label="t('alter.undo')" :title="t('alter.undo')" data-testid="alter-undo" @click="alter.revert('cols', d.key)"><Undo2 class="size-3.5" /></button>
              <button v-if="!d.deleted && !d.locked" type="button" class="rounded p-1 text-muted hover:bg-hover hover:text-danger disabled:opacity-30" :disabled="d.isPk" :aria-label="t('alter.remove')" :title="d.isPk ? t('alter.locked.pk') : t('alter.remove')" data-testid="alter-remove" @click="alter.removeColumn(d.key)"><Trash2 class="size-3.5" /></button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div><BaseButton size="sm" data-testid="alter-add-column" @click="alter.addColumn()"><Plus class="size-3.5" />{{ t("alter.addColumn") }}</BaseButton></div>
  </div>
</template>
