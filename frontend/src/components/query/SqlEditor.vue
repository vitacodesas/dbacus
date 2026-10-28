<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { monaco } from "../../monaco";
import type { EditorMarker } from "../../composables/queryMarkers";
import { useUiStore } from "../../stores/ui";
import { editorContexts, setupMonaco, themeName } from "./editorSetup";

export interface SelectionInfo {
  /** Texto seleccionado ("" si no hay selección). */
  text: string;
  /** Dónde empieza la selección (o el cursor) en el documento, en UTF-16. */
  start: number;
  cursor: number;
}

const props = defineProps<{ tabId: string; modelValue: string; sessionId: string | null; database: string; marker: EditorMarker | null }>();
const emit = defineEmits<{ "update:modelValue": [value: string]; run: []; runSelection: []; format: [] }>();

const ui = useUiStore();
const host = ref<HTMLElement | null>(null);

// Un modelo por pestaña conserva su historial de deshacer; el editor es uno solo y cambia de modelo.
const models = new Map<string, monaco.editor.ITextModel>();
const viewStates = new Map<string, monaco.editor.ICodeEditorViewState | null>();
let editor: monaco.editor.IStandaloneCodeEditor | null = null;
let applying = false;

function modelFor(tabId: string, value: string) {
  let m = models.get(tabId);
  if (!m) {
    m = monaco.editor.createModel(value, "mysql", monaco.Uri.parse(`inmemory://tab/${tabId}`));
    m.onDidChangeContent(() => {
      if (!applying && tabId === props.tabId) emit("update:modelValue", m!.getValue());
    });
    models.set(tabId, m);
  }
  return m;
}

function syncContext() {
  const m = models.get(props.tabId);
  if (m) editorContexts.set(m.uri.toString(), { sessionId: props.sessionId, database: props.database });
}

function show(tabId: string, previous?: string) {
  if (!editor) return;
  if (previous) viewStates.set(previous, editor.saveViewState());
  editor.setModel(modelFor(tabId, props.modelValue));
  const vs = viewStates.get(tabId);
  if (vs) editor.restoreViewState(vs);
  syncContext();
  applyMarker();
}

function applyMarker() {
  const m = models.get(props.tabId);
  if (!m) return;
  const mk = props.marker;
  if (!mk) return monaco.editor.setModelMarkers(m, "tabularis", []);
  const a = m.getPositionAt(mk.startOffset);
  const b = m.getPositionAt(mk.endOffset);
  monaco.editor.setModelMarkers(m, "tabularis", [
    { severity: monaco.MarkerSeverity.Error, message: mk.message, startLineNumber: a.lineNumber, startColumn: a.column, endLineNumber: b.lineNumber, endColumn: b.column },
  ]);
}

onMounted(() => {
  setupMonaco();
  editor = monaco.editor.create(host.value!, {
    model: null,
    language: "mysql",
    theme: themeName(ui.resolvedTheme),
    automaticLayout: true,
    minimap: { enabled: false },
    fontFamily: '"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
    fontLigatures: false,
    fontSize: 13,
    tabSize: 2,
    scrollBeyondLastLine: false,
    renderLineHighlight: "line",
    fixedOverflowWidgets: true, // los desplegables no se recortan dentro del panel
    padding: { top: 8, bottom: 8 },
    ariaLabel: "Editor SQL",
    quickSuggestions: { other: true, comments: false, strings: false },
  });

  const mod = monaco.KeyMod.CtrlCmd;
  editor.addAction({ id: "tabularis.run", label: "Ejecutar todo", keybindings: [mod | monaco.KeyCode.Enter], run: () => emit("run") });
  editor.addAction({ id: "tabularis.runSelection", label: "Ejecutar selección o sentencia actual", keybindings: [mod | monaco.KeyMod.Shift | monaco.KeyCode.Enter], run: () => emit("runSelection") });
  editor.addAction({ id: "tabularis.format", label: "Formatear SQL", keybindings: [mod | monaco.KeyMod.Alt | monaco.KeyCode.KeyL], contextMenuGroupId: "1_modification", run: () => emit("format") });

  show(props.tabId);
});

onBeforeUnmount(() => {
  editor?.dispose();
  models.forEach((m) => {
    editorContexts.delete(m.uri.toString());
    m.dispose();
  });
  models.clear();
});

watch(() => props.tabId, (id, prev) => show(id, prev));
watch(() => [props.sessionId, props.database], syncContext);
watch(() => props.marker, applyMarker);
watch(() => ui.resolvedTheme, (t) => monaco.editor.setTheme(themeName(t)));

// Cambios que vienen de fuera (p. ej. el formateo): se aplican como edición para poder deshacerlos.
watch(() => props.modelValue, (value) => {
  const m = models.get(props.tabId);
  if (!m || !editor || m.getValue() === value) return;
  applying = true;
  editor.executeEdits("app", [{ range: m.getFullModelRange(), text: value, forceMoveMarkers: true }]);
  editor.pushUndoStop();
  applying = false;
});

/** Libera el modelo de una pestaña cerrada. */
function dispose(tabId: string) {
  const m = models.get(tabId);
  if (!m) return;
  editorContexts.delete(m.uri.toString());
  m.dispose();
  models.delete(tabId);
  viewStates.delete(tabId);
}

function selection(): SelectionInfo {
  const m = models.get(props.tabId);
  if (!editor || !m) return { text: "", start: 0, cursor: 0 };
  const sel = editor.getSelection();
  const cursor = m.getOffsetAt(editor.getPosition() ?? { lineNumber: 1, column: 1 });
  if (!sel || sel.isEmpty()) return { text: "", start: cursor, cursor };
  return { text: m.getValueInRange(sel), start: m.getOffsetAt(sel.getStartPosition()), cursor };
}

defineExpose({ focus: () => editor?.focus(), selection, dispose });
</script>

<template>
  <div ref="host" class="h-full w-full" data-testid="sql-editor" />
</template>
