<script setup lang="ts">
defineProps<{ title: string; description?: string }>();
</script>

<template>
  <div class="flex flex-col items-center gap-3 p-8 text-center">
    <div v-if="$slots.icon" class="text-muted"><slot name="icon" /></div>
    <h2 class="text-base font-semibold">{{ title }}</h2>
    <p v-if="description" class="max-w-sm text-sm text-muted">{{ description }}</p>
    <div v-if="$slots.default" class="mt-1 flex gap-2"><slot /></div>
  </div>
</template>
