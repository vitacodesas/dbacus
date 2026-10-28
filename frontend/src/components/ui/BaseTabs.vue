<script setup lang="ts">
import { TabsContent, TabsList, TabsRoot, TabsTrigger } from "reka-ui";

export interface TabItem {
  value: string;
  label: string;
}

defineProps<{ items: TabItem[] }>();
const model = defineModel<string>({ required: true });
</script>

<template>
  <TabsRoot v-model="model" class="flex min-h-0 flex-col">
    <TabsList class="flex gap-1 border-b border-border">
      <TabsTrigger
        v-for="item in items"
        :key="item.value"
        :value="item.value"
        class="-mb-px border-b-2 border-transparent px-3 py-1.5 text-sm text-muted hover:text-fg data-[state=active]:border-accent data-[state=active]:text-fg"
      >
        {{ item.label }}
      </TabsTrigger>
    </TabsList>
    <TabsContent v-for="item in items" :key="item.value" :value="item.value" class="min-h-0 flex-1">
      <slot :name="item.value" />
    </TabsContent>
  </TabsRoot>
</template>
