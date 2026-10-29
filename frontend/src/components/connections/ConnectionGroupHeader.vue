<script setup lang="ts">
import { computed } from "vue";
import { ChevronDown, ChevronRight, Pencil } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import type { ConnectionGroup } from "../../types";
import BaseContextMenu, { type MenuItem } from "../ui/BaseContextMenu.vue";

// Cabecera de una sección de la lista de conexiones (spec 17). `group` nulo = «Sin grupo» (sin acciones).
const props = defineProps<{ group: ConnectionGroup | null; count: number; collapsed: boolean }>();
const emit = defineEmits<{ toggle: []; edit: []; delete: [] }>();
const { t } = useI18n();

const name = computed(() => props.group?.name ?? t("groups.none"));
const menu = computed<MenuItem[]>(() => [
  { id: "edit", label: t("groups.edit") },
  { id: "delete", label: t("groups.delete"), danger: true, separatorBefore: true },
]);

function onMenu(id: string) {
  if (id === "edit") emit("edit");
  else if (id === "delete") emit("delete");
}
</script>

<template>
  <!-- El menú contextual solo existe para grupos reales; el botón del lápiz es la vía visible y usable con teclado. -->
  <component :is="group ? BaseContextMenu : 'div'" v-bind="group ? { items: menu, onSelect: onMenu } : {}">
    <div class="group/header flex items-center gap-1 rounded-md px-1 py-1 hover:bg-hover focus-within:bg-hover" :data-testid="`group-${name}`">
      <button
        type="button"
        class="flex min-w-0 flex-1 items-center gap-1.5 text-left text-xs font-semibold text-muted"
        :aria-expanded="!collapsed"
        :aria-label="t('groups.toggle', { name })"
        data-testid="group-toggle"
        @click="emit('toggle')"
      >
        <component :is="collapsed ? ChevronRight : ChevronDown" class="size-3.5 shrink-0" />
        <span class="size-2 shrink-0 rounded-full" :class="!group?.color && 'bg-border'" :style="group?.color ? { backgroundColor: group.color } : undefined" data-testid="group-dot" />
        <span class="truncate">{{ name }}</span>
        <span class="font-data ml-auto shrink-0 text-[11px] font-normal">{{ t("groups.count", { n: count }) }}</span>
      </button>
      <button
        v-if="group"
        type="button"
        class="grid size-6 shrink-0 place-items-center rounded text-muted opacity-0 group-focus-within/header:opacity-100 group-hover/header:opacity-100 hover:bg-border hover:text-fg"
        :aria-label="t('groups.edit')"
        :title="t('groups.edit')"
        data-testid="group-edit"
        @click="emit('edit')"
      >
        <Pencil class="size-3.5" />
      </button>
    </div>
  </component>
</template>
