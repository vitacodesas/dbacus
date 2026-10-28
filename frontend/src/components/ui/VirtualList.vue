<script setup lang="ts" generic="T">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";

// Lista virtualizada de filas de altura fija: solo se montan las visibles (+ overscan). Spec 05/06.
const props = withDefaults(defineProps<{ items: T[]; rowHeight: number; itemKey: (item: T) => string | number; overscan?: number }>(), { overscan: 8 });
defineSlots<{ default(p: { item: T; index: number }): unknown }>();

const root = ref<HTMLElement | null>(null);
const scrollTop = ref(0);
const height = ref(400); // valor inicial razonable hasta medir (y en entornos sin layout, como jsdom)

const start = computed(() => Math.max(0, Math.floor(scrollTop.value / props.rowHeight) - props.overscan));
const end = computed(() => Math.min(props.items.length, Math.ceil((scrollTop.value + height.value) / props.rowHeight) + props.overscan));
const visible = computed(() => props.items.slice(start.value, end.value).map((item, i) => ({ item, index: start.value + i })));

let observer: ResizeObserver | null = null;
onMounted(() => {
  const el = root.value;
  if (!el) return;
  if (el.clientHeight) height.value = el.clientHeight;
  if (typeof ResizeObserver !== "undefined") {
    observer = new ResizeObserver(() => (height.value = el.clientHeight || height.value));
    observer.observe(el);
  }
});
onBeforeUnmount(() => observer?.disconnect());

function scrollToIndex(index: number) {
  const el = root.value;
  if (!el) return;
  const top = index * props.rowHeight;
  if (top < el.scrollTop) el.scrollTop = top;
  else if (top + props.rowHeight > el.scrollTop + el.clientHeight) el.scrollTop = top + props.rowHeight - el.clientHeight;
}
defineExpose({ scrollToIndex, root });
</script>

<template>
  <div ref="root" class="relative overflow-auto" @scroll.passive="scrollTop = ($event.target as HTMLElement).scrollTop">
    <div :style="{ height: `${items.length * rowHeight}px` }">
      <div :style="{ transform: `translateY(${start * rowHeight}px)` }">
        <div v-for="v in visible" :key="itemKey(v.item)" :style="{ height: `${rowHeight}px` }">
          <slot :item="v.item" :index="v.index" />
        </div>
      </div>
    </div>
  </div>
</template>
