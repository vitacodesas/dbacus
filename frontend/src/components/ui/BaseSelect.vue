<script setup lang="ts">
import { useId } from "vue";

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

defineProps<{ options: SelectOption[]; label?: string; error?: string; disabled?: boolean }>();
const model = defineModel<string>({ default: "" });
const id = useId();
</script>

<template>
  <div class="flex flex-col gap-1">
    <label v-if="label" :for="id" class="text-xs font-medium text-muted">{{ label }}</label>
    <select
      :id="id"
      v-model="model"
      :disabled="disabled"
      :aria-invalid="!!error || undefined"
      class="h-8 rounded-md border bg-panel px-2 text-sm disabled:opacity-50"
      :class="error ? 'border-danger' : 'border-border'"
    >
      <option v-for="o in options" :key="o.value" :value="o.value" :disabled="o.disabled">{{ o.label }}</option>
    </select>
    <p v-if="error" class="text-xs text-danger">{{ error }}</p>
  </div>
</template>
