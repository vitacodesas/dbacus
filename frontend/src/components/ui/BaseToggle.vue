<script setup lang="ts">
import { SwitchRoot, SwitchThumb } from "reka-ui";

defineProps<{ label?: string; disabled?: boolean }>();
const model = defineModel<boolean>({ default: false });
</script>

<template>
  <label class="inline-flex items-center gap-2 text-sm" :class="{ 'opacity-50': disabled }">
    <SwitchRoot
      v-model="model"
      :disabled="disabled"
      class="relative h-5 w-9 rounded-full bg-border transition-colors data-[state=checked]:bg-accent"
    >
      <SwitchThumb class="block size-4 translate-x-0.5 rounded-full bg-white transition-transform data-[state=checked]:translate-x-[18px]" />
    </SwitchRoot>
    <slot>{{ label }}</slot>
  </label>
</template>
