<script setup lang="ts">
import { ContextMenuContent, ContextMenuItem, ContextMenuPortal, ContextMenuRoot, ContextMenuSeparator, ContextMenuTrigger } from "reka-ui";

export interface MenuItem {
  id: string;
  label: string;
  danger?: boolean;
  disabled?: boolean;
  separatorBefore?: boolean;
}

defineProps<{ items: MenuItem[] }>();
const emit = defineEmits<{ select: [id: string] }>();
</script>

<template>
  <ContextMenuRoot>
    <ContextMenuTrigger as-child><slot /></ContextMenuTrigger>
    <ContextMenuPortal>
      <ContextMenuContent class="z-50 min-w-40 rounded-md border border-border bg-panel p-1 text-sm shadow-lg">
        <template v-for="item in items" :key="item.id">
          <ContextMenuSeparator v-if="item.separatorBefore" class="my-1 h-px bg-border" />
          <ContextMenuItem
            :disabled="item.disabled"
            class="cursor-default rounded px-2 py-1 outline-none data-[disabled]:opacity-50 data-[highlighted]:bg-hover"
            :class="item.danger && 'text-danger'"
            @select="emit('select', item.id)"
          >
            {{ item.label }}
          </ContextMenuItem>
        </template>
      </ContextMenuContent>
    </ContextMenuPortal>
  </ContextMenuRoot>
</template>
