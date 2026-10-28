<script setup lang="ts">
// Divisor arrastrable. No conoce tamaños: emite deltas en px y el contenedor decide (y los limita).
const props = defineProps<{ orientation: "vertical" | "horizontal" }>(); // vertical = barra vertical (redimensiona ancho)
const emit = defineEmits<{ drag: [deltaPx: number]; end: [] }>();

const KEY_STEP = 16;
let last = 0;

const axis = (e: PointerEvent) => (props.orientation === "vertical" ? e.clientX : e.clientY);

function down(e: PointerEvent) {
  (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  last = axis(e);
}
function move(e: PointerEvent) {
  if (!(e.currentTarget as HTMLElement).hasPointerCapture(e.pointerId)) return;
  const pos = axis(e);
  emit("drag", pos - last);
  last = pos;
}
function up(e: PointerEvent) {
  (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
  emit("end");
}
function key(e: KeyboardEvent) {
  const [dec, inc] = props.orientation === "vertical" ? ["ArrowLeft", "ArrowRight"] : ["ArrowUp", "ArrowDown"];
  if (e.key !== dec && e.key !== inc) return;
  e.preventDefault();
  emit("drag", e.key === inc ? KEY_STEP : -KEY_STEP);
  emit("end");
}
</script>

<template>
  <div
    role="separator"
    tabindex="0"
    :aria-orientation="orientation === 'vertical' ? 'vertical' : 'horizontal'"
    class="shrink-0 touch-none bg-border transition-colors hover:bg-accent focus-visible:bg-accent"
    :class="orientation === 'vertical' ? 'w-px cursor-col-resize px-[2px] -mx-[2px] bg-clip-content' : 'h-px cursor-row-resize py-[2px] -my-[2px] bg-clip-content'"
    @pointerdown="down"
    @pointermove="move"
    @pointerup="up"
    @keydown="key"
  />
</template>
