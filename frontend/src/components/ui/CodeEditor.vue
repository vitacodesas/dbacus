<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { monaco } from "../../monaco";
import { useUiStore } from "../../stores/ui";
import { setupMonaco, themeName } from "../query/editorSetup";

// Editor Monaco independiente de las pestañas de consulta (spec 21): un modelo, opcionalmente de solo lectura.
// `SqlEditor` sigue siendo el de las pestañas (ejecutar, marcadores, un modelo por pestaña).
const props = withDefaults(defineProps<{ modelValue: string; readOnly?: boolean; label?: string }>(), { readOnly: false, label: "Editor SQL" });
const emit = defineEmits<{ "update:modelValue": [value: string] }>();

const ui = useUiStore();
const host = ref<HTMLElement | null>(null);
let editor: monaco.editor.IStandaloneCodeEditor | null = null;
let model: monaco.editor.ITextModel | null = null;
let applying = false;
let seq = 0;

onMounted(() => {
  setupMonaco();
  // Una uri única por instancia: dos editores abiertos a la vez no comparten modelo ni historial.
  model = monaco.editor.createModel(props.modelValue, "mysql", monaco.Uri.parse(`inmemory://code/${++seq}-${Date.now()}`));
  model.onDidChangeContent(() => {
    if (!applying) emit("update:modelValue", model!.getValue());
  });
  editor = monaco.editor.create(host.value!, {
    model,
    language: "mysql",
    theme: themeName(ui.resolvedTheme),
    readOnly: props.readOnly,
    automaticLayout: true,
    minimap: { enabled: false },
    fontFamily: '"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
    fontLigatures: false,
    fontSize: 13,
    tabSize: 2,
    scrollBeyondLastLine: false,
    fixedOverflowWidgets: true,
    padding: { top: 8, bottom: 8 },
    ariaLabel: props.label,
    domReadOnly: props.readOnly,
  });
});

onBeforeUnmount(() => {
  editor?.dispose();
  model?.dispose();
});

watch(() => props.readOnly, (ro) => editor?.updateOptions({ readOnly: ro, domReadOnly: ro }));
watch(() => ui.resolvedTheme, (t) => monaco.editor.setTheme(themeName(t)));
// Cambios que vienen de fuera (cargar otro texto, descartar): se aplican como edición para poder deshacerlos.
watch(() => props.modelValue, (value) => {
  if (!model || !editor || model.getValue() === value) return;
  applying = true;
  editor.executeEdits("app", [{ range: model.getFullModelRange(), text: value, forceMoveMarkers: true }]);
  editor.pushUndoStop();
  applying = false;
});

defineExpose({ focus: () => editor?.focus() });
</script>

<template>
  <div ref="host" class="h-full w-full" data-testid="code-editor" />
</template>
