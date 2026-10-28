<script setup lang="ts">
import { computed, useAttrs, useId } from "vue";

// class/style se aplican al contenedor; el resto de atributos (autocomplete, list, autofocus…) van al <input>.
defineOptions({ inheritAttrs: false });

defineProps<{
  label?: string;
  error?: string;
  type?: string;
  placeholder?: string;
  disabled?: boolean;
  mono?: boolean;
}>();
const model = defineModel<string | number>({ default: "" });
const id = useId();
const attrs = useAttrs();
const inputAttrs = computed(() => {
  const rest = { ...attrs };
  delete rest.class;
  delete rest.style;
  return rest;
});
</script>

<template>
  <div class="flex flex-col gap-1" :class="attrs.class" :style="attrs.style as string">
    <label v-if="label" :for="id" class="text-xs font-medium text-muted">{{ label }}</label>
    <input
      v-bind="inputAttrs"
      :id="id"
      v-model="model"
      :type="type ?? 'text'"
      :placeholder="placeholder"
      :disabled="disabled"
      :aria-invalid="!!error || undefined"
      :aria-describedby="error ? `${id}-err` : undefined"
      class="h-8 rounded-md border bg-panel px-2.5 text-sm placeholder:text-muted/70 disabled:opacity-50"
      :class="[error ? 'border-danger' : 'border-border', mono && 'font-data']"
    >
    <p v-if="error" :id="`${id}-err`" class="text-xs text-danger">{{ error }}</p>
  </div>
</template>
