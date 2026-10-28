<script setup lang="ts">
import { computed } from "vue";
import BaseSpinner from "./BaseSpinner.vue";

const props = withDefaults(
  defineProps<{
    variant?: "primary" | "secondary" | "danger" | "ghost";
    size?: "sm" | "md";
    loading?: boolean;
    disabled?: boolean;
    type?: "button" | "submit";
  }>(),
  { variant: "secondary", size: "md", type: "button" },
);

const variants = {
  primary: "bg-accent text-accent-fg hover:opacity-90",
  secondary: "border border-border bg-panel hover:bg-hover",
  danger: "bg-danger text-white hover:opacity-90",
  ghost: "hover:bg-hover",
} as const;

const classes = computed(() => [
  "inline-flex items-center justify-center gap-1.5 rounded-md font-medium whitespace-nowrap transition-colors select-none",
  "disabled:cursor-not-allowed disabled:opacity-50",
  props.size === "sm" ? "h-7 px-2.5 text-xs" : "h-8 px-3 text-sm",
  variants[props.variant],
]);
</script>

<template>
  <button :type="type" :disabled="disabled || loading" :aria-busy="loading || undefined" :class="classes">
    <BaseSpinner v-if="loading" class="size-3.5" />
    <slot />
  </button>
</template>
