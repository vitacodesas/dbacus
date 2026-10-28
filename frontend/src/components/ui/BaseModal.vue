<script setup lang="ts">
import { DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from "reka-ui";

withDefaults(defineProps<{ title: string; description?: string; size?: "sm" | "md" | "lg" }>(), { size: "md" });
const open = defineModel<boolean>("open", { default: false });

const widths = { sm: "max-w-sm", md: "max-w-lg", lg: "max-w-3xl" } as const;
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-40 bg-black/50" />
      <DialogContent
        class="fixed top-1/2 left-1/2 z-50 flex max-h-[85vh] w-[calc(100%-2rem)] -translate-x-1/2 -translate-y-1/2 flex-col rounded-lg border border-border bg-panel shadow-xl"
        :class="widths[size]"
      >
        <header class="border-b border-border px-4 py-3">
          <DialogTitle class="text-sm font-semibold">{{ title }}</DialogTitle>
          <DialogDescription v-if="description" class="mt-0.5 text-xs text-muted">{{ description }}</DialogDescription>
          <DialogDescription v-else class="sr-only">{{ title }}</DialogDescription>
        </header>
        <div class="min-h-0 flex-1 overflow-auto p-4"><slot /></div>
        <footer v-if="$slots.footer" class="flex justify-end gap-2 border-t border-border px-4 py-3"><slot name="footer" /></footer>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
