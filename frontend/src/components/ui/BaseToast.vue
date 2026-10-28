<script setup lang="ts">
import { useUiStore } from "../../stores/ui";

const ui = useUiStore();
const tone = { info: "border-border", success: "border-success", error: "border-danger" } as const;
</script>

<template>
  <div class="pointer-events-none fixed right-4 bottom-10 z-[60] flex w-80 flex-col gap-2" aria-live="polite">
    <div
      v-for="t in ui.toasts"
      :key="t.id"
      class="pointer-events-auto flex items-start gap-2 rounded-md border bg-panel px-3 py-2 text-sm shadow-lg"
      :class="tone[t.kind]"
      :role="t.kind === 'error' ? 'alert' : 'status'"
    >
      <span class="min-w-0 flex-1 break-words">{{ t.message }}</span>
      <button v-if="t.action" class="font-medium text-accent hover:underline" @click="t.action.run(); ui.dismissToast(t.id)">{{ t.action.label }}</button>
      <button class="text-muted hover:text-fg" aria-label="Cerrar" @click="ui.dismissToast(t.id)">×</button>
    </div>
  </div>
</template>
