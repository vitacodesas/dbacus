<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { Lock } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { rowsAsInsert } from "../../api/queries";
import { asAppError } from "../../api/errors";
import {
  clampWidth, columnOffsets, copyText, defaultWidth, displayCell, GUTTER_WIDTH, HEADER_HEIGHT, inSelection, ROW_HEIGHT, selectionBounds,
  toTSV, visibleColumns, visibleRows, type Selection,
} from "../../composables/gridModel";
import { useEditsStore } from "../../stores/edits";
import { MAX_COPY_ROWS, useResultsStore } from "../../stores/results";
import { useUiStore } from "../../stores/ui";
import type { Cell, ColumnMeta } from "../../types";
import BaseContextMenu, { type MenuItem } from "../ui/BaseContextMenu.vue";
import CellEditorModal, { type CellEditTarget } from "./CellEditorModal.vue";
import CellViewerModal from "./CellViewerModal.vue";

const props = defineProps<{ tabId: string; queryId: string; stmtIndex: number; columns: ColumnMeta[]; rowCount: number }>();
const emit = defineEmits<{ visibleRange: [first: number, last: number]; cancel: [] }>();

const { t } = useI18n();
const results = useResultsStore();
const edits = useEditsStore();
const ui = useUiStore();

const root = ref<HTMLElement | null>(null);
const scrollTop = ref(0);
const scrollLeft = ref(0);
const view = reactive({ w: 800, h: 400 });
let observer: ResizeObserver | null = null;

// Selección actual (declarada antes de los watchers inmediatos que la reinician).
const sel = ref<Selection | null>(null);

// ---- edición de celdas (spec 13): Go valida y guarda los cambios pendientes; aquí solo se captura el texto ------------------
const editInfo = computed(() => edits.infoOf(props.queryId, props.stmtIndex));
const canEdit = (c: number) => edits.isEditable(props.queryId, props.stmtIndex, c);
const editing = ref<{ r: number; c: number; text: string; error: string | null; saving: boolean } | null>(null);
const editorEl = ref<HTMLInputElement | null>(null);
const expanded = ref<CellEditTarget | null>(null);
const CHAR_WIDTH = 7.5; // ancho aproximado de un carácter monoespaciado a 14 px
const needsExpandedEditor = (text: string, dataType: string, width: number) =>
  dataType.toLowerCase() === "json" || text.includes("\n") || text.length * CHAR_WIDTH > Math.max(0, width - 24);

// Anchos por columna; se reinician con cada resultado nuevo.
const widths = ref<number[]>([]);
watch(() => [props.queryId, props.stmtIndex, props.columns], () => {
  widths.value = props.columns.map(defaultWidth);
  sel.value = null;
  editing.value = null;
  if (root.value) root.value.scrollTop = root.value.scrollLeft = 0;
}, { immediate: true });

const offsets = computed(() => columnOffsets(widths.value));
const totalWidth = computed(() => GUTTER_WIDTH + (offsets.value.at(-1) ?? 0));
const totalHeight = computed(() => HEADER_HEIGHT + props.rowCount * ROW_HEIGHT);
const colRange = computed(() => visibleColumns(offsets.value, scrollLeft.value, view.w));
const rowRange = computed(() => visibleRows(props.rowCount, scrollTop.value, Math.max(0, view.h - HEADER_HEIGHT)));
const visibleColumnIdx = computed(() => Array.from({ length: colRange.value[1] - colRange.value[0] }, (_, i) => colRange.value[0] + i));
const visibleRowIdx = computed(() => Array.from({ length: rowRange.value[1] - rowRange.value[0] }, (_, i) => rowRange.value[0] + i));

watch(rowRange, ([a, b]) => emit("visibleRange", a, b), { immediate: true });

onMounted(() => {
  const el = root.value;
  if (!el) return;
  view.w = el.clientWidth || view.w;
  view.h = el.clientHeight || view.h;
  if (typeof ResizeObserver !== "undefined") {
    observer = new ResizeObserver(() => {
      view.w = el.clientWidth || view.w;
      view.h = el.clientHeight || view.h;
    });
    observer.observe(el);
  }
});
onBeforeUnmount(() => observer?.disconnect());

function onScroll(e: Event) {
  const el = e.target as HTMLElement;
  scrollTop.value = el.scrollTop;
  scrollLeft.value = el.scrollLeft;
}

const rowAt = (r: number): Cell[] | undefined => results.rowAt(props.tabId, props.stmtIndex, r);

// ---- selección ----------------------------------------------------------------------------------------------------
const lastRow = () => Math.max(0, props.rowCount - 1);
const lastCol = () => Math.max(0, props.columns.length - 1);

/** Celda bajo el puntero (coordenadas del contenido, ya con el scroll aplicado). null si cae en cabecera o fuera. */
function hit(e: MouseEvent): { r: number; c: number; header: boolean; gutter: boolean } | null {
  const el = root.value;
  if (!el) return null;
  const rect = el.getBoundingClientRect();
  const x = e.clientX - rect.left + el.scrollLeft;
  const y = e.clientY - rect.top + el.scrollTop;
  const header = y < HEADER_HEIGHT;
  const gutter = x < GUTTER_WIDTH;
  const r = Math.min(lastRow(), Math.max(0, Math.floor((y - HEADER_HEIGHT) / ROW_HEIGHT)));
  const cx = x - GUTTER_WIDTH;
  let c = 0;
  while (c < props.columns.length - 1 && offsets.value[c + 1] <= cx) c++;
  if (cx < 0) c = 0;
  return { r, c, header, gutter };
}

let dragging = false;
function onMouseDown(e: MouseEvent) {
  if (e.button !== 0 || (e.target as HTMLElement).dataset.resize) return;
  const h = hit(e);
  if (!h) return;
  root.value?.focus();
  if (h.header && h.gutter) sel.value = { ar: 0, ac: 0, fr: lastRow(), fc: lastCol() }; // esquina: todo
  else if (h.header) sel.value = e.shiftKey && sel.value ? { ...sel.value, fr: lastRow(), fc: h.c } : { ar: 0, ac: h.c, fr: lastRow(), fc: h.c };
  else if (h.gutter) sel.value = e.shiftKey && sel.value ? { ...sel.value, fr: h.r, fc: lastCol() } : { ar: h.r, ac: 0, fr: h.r, fc: lastCol() };
  else sel.value = e.shiftKey && sel.value ? { ...sel.value, fr: h.r, fc: h.c } : { ar: h.r, ac: h.c, fr: h.r, fc: h.c };
  dragging = !h.header && !h.gutter;
}
function onMouseMove(e: MouseEvent) {
  if (!dragging || !sel.value || e.buttons !== 1) return void (dragging = false);
  const h = hit(e);
  if (h) sel.value = { ...sel.value, fr: h.r, fc: h.c };
}
const endDrag = () => (dragging = false);
onMounted(() => window.addEventListener("mouseup", endDrag));
onBeforeUnmount(() => window.removeEventListener("mouseup", endDrag));

// ---- teclado ------------------------------------------------------------------------------------------------------
function move(dr: number, dc: number, extend: boolean) {
  const cur = sel.value ?? { ar: 0, ac: 0, fr: 0, fc: 0 };
  const fr = Math.max(0, Math.min(lastRow(), cur.fr + dr));
  const fc = Math.max(0, Math.min(lastCol(), cur.fc + dc));
  sel.value = extend ? { ...cur, fr, fc } : { ar: fr, ac: fc, fr, fc };
  ensureVisible(fr, fc);
}

function ensureVisible(r: number, c: number) {
  const el = root.value;
  if (!el) return;
  const top = HEADER_HEIGHT + r * ROW_HEIGHT;
  if (top - HEADER_HEIGHT < el.scrollTop) el.scrollTop = top - HEADER_HEIGHT;
  else if (top + ROW_HEIGHT > el.scrollTop + el.clientHeight) el.scrollTop = top + ROW_HEIGHT - el.clientHeight;
  const left = GUTTER_WIDTH + offsets.value[c];
  const right = left + widths.value[c];
  if (left - GUTTER_WIDTH < el.scrollLeft) el.scrollLeft = left - GUTTER_WIDTH;
  else if (right > el.scrollLeft + el.clientWidth) el.scrollLeft = right - el.clientWidth;
}

async function onKey(e: KeyboardEvent) {
  const mod = e.metaKey || e.ctrlKey;
  const page = Math.max(1, Math.floor((view.h - HEADER_HEIGHT) / ROW_HEIGHT) - 1);
  switch (e.key) {
    case "ArrowDown": e.preventDefault(); return move(1, 0, e.shiftKey);
    case "ArrowUp": e.preventDefault(); return move(-1, 0, e.shiftKey);
    case "ArrowRight": e.preventDefault(); return move(0, 1, e.shiftKey);
    case "ArrowLeft": e.preventDefault(); return move(0, -1, e.shiftKey);
    case "PageDown": e.preventDefault(); return move(page, 0, e.shiftKey);
    case "PageUp": e.preventDefault(); return move(-page, 0, e.shiftKey);
    case "Home": e.preventDefault(); return move(0, -lastCol(), e.shiftKey);
    case "End": e.preventDefault(); return move(0, lastCol(), e.shiftKey);
    case "Escape":
      if (results.running(props.tabId)) emit("cancel");
      else sel.value = null;
      return;
    case "F2":
      e.preventDefault();
      return void startEdit();
    case "Enter":
    case " ":
      e.preventDefault();
      return viewCurrent();
  }
  if (mod && e.key.toLowerCase() === "a") {
    e.preventDefault();
    sel.value = { ar: 0, ac: 0, fr: lastRow(), fc: lastCol() };
  } else if (mod && e.key.toLowerCase() === "c") {
    e.preventDefault();
    await copy(e.shiftKey);
  }
}

function startEdit(): boolean {
  const s = sel.value;
  if (!s || !canEdit(s.fc)) return false;
  const cell = rowAt(s.fr)?.[s.fc];
  if (!cell) return false;
  if (cell.trunc) {
    ui.toast("info", t("edit.reasons.truncated"));
    return true;
  }
  const text = cell.v == null ? "" : String(cell.v);
  // JSON, textos con saltos de línea o que no caben en la celda se editan en un diálogo ampliado, no en una línea.
  if (needsExpandedEditor(text, props.columns[s.fc]?.dataType ?? "", widths.value[s.fc] ?? 0)) {
    expanded.value = {
      tabId: props.tabId, queryId: props.queryId, stmt: props.stmtIndex, row: s.fr, col: s.fc, name: props.columns[s.fc]?.name ?? "",
      dataType: props.columns[s.fc]?.dataType ?? "", nullable: !!editInfo.value?.columns?.[s.fc]?.nullable, text,
    };
    return true;
  }
  editing.value = { r: s.fr, c: s.fc, text, error: null, saving: false };
  void nextTick(() => editorEl.value?.select());
  return true;
}

async function commitEdit(then: "down" | "right" | "stay") {
  const e = editing.value;
  if (!e || e.saving) return;
  e.saving = true;
  const err = await edits.stage(props.tabId, props.queryId, props.stmtIndex, e.r, e.c, { null: false, text: e.text });
  if (err) { // se queda abierto con el motivo, para corregirlo
    e.error = err.message;
    e.saving = false;
    void nextTick(() => editorEl.value?.focus());
    return;
  }
  editing.value = null;
  root.value?.focus();
  if (then === "down") move(1, 0, false);
  else if (then === "right") nextEditableColumn(e.r, e.c);
}

function nextEditableColumn(r: number, from: number) {
  for (let c = from + 1; c <= lastCol(); c++) {
    if (canEdit(c)) {
      sel.value = { ar: r, ac: c, fr: r, fc: c };
      ensureVisible(r, c);
      return;
    }
  }
}

function cancelEdit() {
  editing.value = null;
  root.value?.focus();
}

function onEditorKey(e: KeyboardEvent) {
  if (e.key === "Enter") {
    e.preventDefault();
    void commitEdit("down");
  } else if (e.key === "Tab") {
    e.preventDefault();
    void commitEdit("right");
  } else if (e.key === "Escape") {
    e.preventDefault();
    cancelEdit();
  }
}

function onEditorBlur() {
  if (editing.value && !editing.value.saving && !editing.value.error) void commitEdit("stay");
}

function onDblClick() {
  if (startEdit()) return;
  // En un resultado editable, doble clic sobre una columna que no lo es explica el motivo (antes solo abría el visor).
  const s = sel.value;
  const reason = s && editInfo.value?.editable ? editInfo.value.columns?.[s.fc]?.reason : "";
  if (reason) ui.toast("info", t(`edit.reasons.${reason}`));
  viewCurrent();
}

async function setNull() {
  const s = sel.value;
  if (!s) return;
  const err = await edits.stage(props.tabId, props.queryId, props.stmtIndex, s.fr, s.fc, { null: true, text: "" });
  if (err) ui.toast("error", err.message);
}

// ---- copiar -------------------------------------------------------------------------------------------------------
async function selectedRows() {
  if (!sel.value) return null;
  const b = selectionBounds(sel.value);
  const to = Math.min(b.r1 + 1, b.r0 + MAX_COPY_ROWS);
  if (to < b.r1 + 1) ui.toast("info", t("query.tooManyRows", { n: MAX_COPY_ROWS.toLocaleString() }));
  const rows = await results.fetchRange(props.tabId, props.stmtIndex, b.r0, to);
  return { b, rows };
}

async function writeClipboard(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    ui.toast("success", t("query.copied"));
  } catch {
    ui.toast("error", t("query.cannotCopy"));
  }
}

async function copy(withHeaders: boolean) {
  try {
    const s = await selectedRows();
    if (s) await writeClipboard(toTSV(s.rows, props.columns, { c0: s.b.c0, c1: s.b.c1 }, withHeaders));
  } catch (e) {
    ui.toast("error", asAppError(e).message);
  }
}

async function copyInsert() {
  if (!sel.value) return;
  const b = selectionBounds(sel.value);
  try {
    // El SQL lo arma Go (escapado del driver); aquí solo se pide el rango.
    await writeClipboard(await rowsAsInsert(props.queryId, props.stmtIndex, b.r0, Math.min(b.r1 - b.r0 + 1, 1000), ""));
  } catch (e) {
    ui.toast("error", asAppError(e).message);
  }
}

// ---- valor completo -----------------------------------------------------------------------------------------------
const viewing = ref<{ queryId: string; stmt: number; row: number; col: number; name: string; cell: Cell } | null>(null);
function viewCurrent() {
  if (!sel.value) return;
  const r = sel.value.fr;
  const c = sel.value.fc;
  const cell = rowAt(r)?.[c];
  if (cell) viewing.value = { queryId: props.queryId, stmt: props.stmtIndex, row: r, col: c, name: props.columns[c]?.name ?? "", cell };
}

const menu = computed<MenuItem[]>(() => {
  const items: MenuItem[] = [
    { id: "copy", label: t("query.menu.copy") },
    { id: "copyHeaders", label: t("query.menu.copyHeaders") },
    { id: "copyInsert", label: t("query.menu.copyInsert") },
  ];
  if (sel.value) items.push({ id: "view", label: t("query.menu.viewCell"), separatorBefore: true });
  const cur = sel.value ? rowAt(sel.value.fr)?.[sel.value.fc] : undefined;
  if (sel.value && canEdit(sel.value.fc)) {
    items.push({ id: "editCell", label: t("edit.editCell"), separatorBefore: true });
    if (editInfo.value?.columns?.[sel.value.fc]?.nullable) items.push({ id: "setNull", label: t("edit.setNull") });
  }
  if (cur?.edited) items.push({ id: "revert", label: t("edit.revert") });
  return items;
});
function onMenu(id: string) {
  if (id === "copy") void copy(false);
  else if (id === "copyHeaders") void copy(true);
  else if (id === "copyInsert") void copyInsert();
  else if (id === "view") viewCurrent();
  else if (id === "editCell") startEdit();
  else if (id === "setNull") void setNull();
  else if (id === "revert" && sel.value) void edits.unstage(props.tabId, props.queryId, props.stmtIndex, sel.value.fr, sel.value.fc);
}
function onContextMenu(e: MouseEvent) {
  // El clic derecho sobre una celda fuera de la selección actual la selecciona (como en un explorador de archivos).
  const h = hit(e);
  if (h && !h.header && !h.gutter && !inSelection(sel.value, h.r, h.c)) sel.value = { ar: h.r, ac: h.c, fr: h.r, fc: h.c };
}

// ---- redimensionar columnas ---------------------------------------------------------------------------------------
let resizing: { col: number; startX: number; startW: number } | null = null;
function startResize(e: PointerEvent, col: number) {
  e.preventDefault();
  (e.target as HTMLElement).setPointerCapture(e.pointerId);
  resizing = { col, startX: e.clientX, startW: widths.value[col] };
}
function doResize(e: PointerEvent) {
  if (!resizing) return;
  const next = [...widths.value];
  next[resizing.col] = clampWidth(resizing.startW + e.clientX - resizing.startX);
  widths.value = next;
}
const endResize = () => (resizing = null);

const headerTitle = (c: number) => {
  const base = `${props.columns[c].name} · ${props.columns[c].dataType}`;
  const col = editInfo.value?.columns?.[c];
  return editInfo.value?.editable && col && !col.editable && col.reason ? `${base}\n${t(`edit.reasons.${col.reason}`)}` : base;
};

const cellClass = (r: number, c: number, d: ReturnType<typeof displayCell>, edited: boolean) => [
  edited && "bg-warning/20 shadow-[inset_3px_0_0_var(--color-warning)]",
  inSelection(sel.value, r, c) && "bg-accent/20",
  sel.value && sel.value.fr === r && sel.value.fc === c && "outline outline-1 -outline-offset-1 outline-accent",
  d.numeric && "justify-end text-right",
  d.isNull && "text-muted italic",
  (d.type === "bytes" || d.type === "json") && "text-muted",
];

/** Una sola pasada por fila visible: texto y clases de cada celda visible (los datos pendientes quedan como esqueleto). */
const rowsVm = computed(() =>
  visibleRowIdx.value.map((r) => {
    const row = rowAt(r);
    const s = sel.value;
    const selectedRow = !!s && s.ac === 0 && s.fc === lastCol() && inSelection(s, r, 0);
    if (!row) return { r, selectedRow, cells: null };
    const cells = visibleColumnIdx.value.map((c) => {
      const d = displayCell(row[c]);
      return {
        c, text: d.text, cls: cellClass(r, c, d, !!row[c].edited), mono: d.numeric || d.type === "bytes" || d.type === "json",
        title: row[c].edited ? t("edit.edited") : d.text.length > 40 ? copyText(row[c]).slice(0, 500) : undefined,
      };
    });
    return { r, selectedRow, cells };
  }),
);
</script>

<template>
  <BaseContextMenu :items="menu" @select="onMenu">
    <div
      ref="root"
      class="relative h-full w-full overflow-auto bg-panel text-sm outline-none select-none"
      role="grid"
      tabindex="0"
      :aria-rowcount="rowCount + 1"
      :aria-colcount="columns.length"
      :aria-label="t('query.messages')"
      data-testid="result-grid"
      @scroll.passive="onScroll"
      @mousedown="onMouseDown"
      @mousemove="onMouseMove"
      @contextmenu="onContextMenu"
      @dblclick="onDblClick"
      @keydown="onKey"
    >
      <div :style="{ width: `${totalWidth}px`, height: `${totalHeight}px` }" class="relative">
        <!-- Cabecera fija: arriba al hacer scroll vertical y se desplaza con el horizontal -->
        <div class="sticky top-0 z-20 flex border-b border-border bg-hover font-medium" :style="{ height: `${HEADER_HEIGHT}px`, width: `${totalWidth}px` }" role="row">
          <div class="sticky left-0 z-30 shrink-0 border-r border-border bg-hover" :style="{ width: `${GUTTER_WIDTH}px` }" />
          <div :style="{ width: `${offsets[colRange[0]]}px` }" class="shrink-0" />
          <div
            v-for="c in visibleColumnIdx"
            :key="c"
            role="columnheader"
            class="relative flex shrink-0 items-center gap-1.5 truncate border-r border-border px-2"
            :style="{ width: `${widths[c]}px` }"
            :title="headerTitle(c)"
          >
            <span class="truncate">{{ columns[c].name }}</span>
            <Lock v-if="editInfo?.editable && !canEdit(c)" class="size-3 shrink-0 text-muted" data-testid="col-locked" />
            <span v-if="widths[c] >= 150" class="font-data truncate text-[10px] font-normal text-muted">{{ columns[c].dataType.toLowerCase() }}</span>
            <span
              data-resize="1"
              class="absolute top-0 right-0 h-full w-1.5 cursor-col-resize hover:bg-accent/40"
              @pointerdown="startResize($event, c)"
              @pointermove="doResize"
              @pointerup="endResize"
            />
          </div>
        </div>

        <!-- Editor de la celda (Go valida el valor al guardarlo) -->
        <template v-if="editing">
          <input
            ref="editorEl"
            v-model="editing.text"
            class="font-data absolute z-40 border border-accent bg-bg px-2 text-sm outline-none"
            :class="editing.error && 'border-danger'"
            :style="{ top: `${HEADER_HEIGHT + editing.r * ROW_HEIGHT}px`, left: `${GUTTER_WIDTH + offsets[editing.c]}px`, width: `${widths[editing.c]}px`, height: `${ROW_HEIGHT}px` }"
            data-testid="cell-editor"
            spellcheck="false"
            autocomplete="off"
            autocorrect="off"
            autocapitalize="off"
            :aria-label="t('edit.editCell')"
            @keydown.stop="onEditorKey"
            @mousedown.stop
            @dblclick.stop
            @blur="onEditorBlur"
          >
          <div
            v-if="editing.error"
            class="absolute z-50 max-w-sm rounded bg-danger px-2 py-1 text-xs text-white shadow"
            role="alert"
            data-testid="cell-editor-error"
            :style="{ top: `${HEADER_HEIGHT + (editing.r + 1) * ROW_HEIGHT + 2}px`, left: `${GUTTER_WIDTH + offsets[editing.c]}px` }"
          >
            {{ editing.error }}
          </div>
        </template>

        <!-- Filas visibles -->
        <div class="absolute left-0" :style="{ top: `${HEADER_HEIGHT + rowRange[0] * ROW_HEIGHT}px`, width: `${totalWidth}px` }">
          <div v-for="row in rowsVm" :key="row.r" class="flex border-b border-border/50" :style="{ height: `${ROW_HEIGHT}px` }" role="row" :aria-rowindex="row.r + 2">
            <div
              class="font-data sticky left-0 z-10 shrink-0 border-r border-border bg-hover px-2 text-right text-xs leading-6 text-muted"
              :class="row.selectedRow && 'bg-accent/30'"
              :style="{ width: `${GUTTER_WIDTH}px` }"
            >
              {{ row.r + 1 }}
            </div>
            <div :style="{ width: `${offsets[colRange[0]]}px` }" class="shrink-0" />
            <template v-if="row.cells">
              <div
                v-for="cell in row.cells"
                :key="cell.c"
                role="gridcell"
                class="flex shrink-0 items-center truncate border-r border-border/50 px-2"
                :class="cell.cls"
                :style="{ width: `${widths[cell.c]}px` }"
                :title="cell.title"
              >
                <span class="truncate" :class="cell.mono && 'font-data'">{{ cell.text }}</span>
              </div>
            </template>
            <div v-else class="flex shrink-0 items-center px-2" :style="{ width: `${offsets[colRange[1]] - offsets[colRange[0]]}px` }">
              <span class="h-2.5 w-24 animate-pulse rounded bg-border" />
            </div>
          </div>
        </div>
      </div>
    </div>
  </BaseContextMenu>
  <CellViewerModal :target="viewing" @close="viewing = null" />
  <CellEditorModal :target="expanded" @close="expanded = null; root?.focus()" />
</template>
