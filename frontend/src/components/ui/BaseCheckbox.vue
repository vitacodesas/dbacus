<script setup lang="ts">
defineProps<{ label?: string; disabled?: boolean }>();
const model = defineModel<boolean>({ default: false });
</script>

<template>
  <label class="inline-flex cursor-pointer items-center gap-2 text-sm" :class="{ 'opacity-50': disabled }">
    <input v-model="model" type="checkbox" :disabled="disabled" class="size-4 accent-[var(--accent)]">
    <slot>{{ label }}</slot>
  </label>
</template>
