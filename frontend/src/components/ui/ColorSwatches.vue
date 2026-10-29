<script setup lang="ts">
// Selector de color #RRGGBB con una paleta fija y «ninguno». Lo comparten la conexión y el grupo (spec 17).
defineProps<{ label: string; error?: string; noneLabel?: string }>();
const model = defineModel<string>({ default: "" });

const COLORS = ["#ef4444", "#f97316", "#eab308", "#22c55e", "#06b6d4", "#3b82f6", "#8b5cf6", "#ec4899"];
</script>

<template>
  <div class="flex flex-col gap-1">
    <span class="text-xs font-medium text-muted">{{ label }}</span>
    <div class="flex flex-wrap items-center gap-2" role="radiogroup" :aria-label="label">
      <button
        type="button"
        role="radio"
        :aria-checked="!model"
        :aria-label="noneLabel"
        class="size-6 rounded-full border border-border text-xs text-muted"
        :class="!model && 'ring-2 ring-accent'"
        @click="model = ''"
      >
        ∅
      </button>
      <button
        v-for="c in COLORS"
        :key="c"
        type="button"
        role="radio"
        :aria-checked="model === c"
        :aria-label="c"
        class="size-6 rounded-full"
        :class="model === c && 'ring-2 ring-accent ring-offset-2 ring-offset-panel'"
        :style="{ backgroundColor: c }"
        @click="model = c"
      />
    </div>
    <p v-if="error" class="text-xs text-danger">{{ error }}</p>
  </div>
</template>
