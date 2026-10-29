import { monaco } from "../../monaco";
import { suggest, type SuggestionKind } from "../../composables/sqlCompletion";
import { useSchemaStore } from "../../stores/schema";

/** Contexto de cada modelo del editor (clave: uri del modelo), para que el autocompletado sepa de qué conexión/base hablar. */
export const editorContexts = new Map<string, { sessionId: string | null; database: string }>();

const kinds: Record<SuggestionKind, monaco.languages.CompletionItemKind> = {
  keyword: monaco.languages.CompletionItemKind.Keyword,
  function: monaco.languages.CompletionItemKind.Function,
  table: monaco.languages.CompletionItemKind.Struct,
  column: monaco.languages.CompletionItemKind.Field,
};

let registered = false;

/** Registra (una sola vez) el proveedor de autocompletado y los temas de la app. */
export function setupMonaco() {
  if (registered) return;
  registered = true;

  monaco.languages.registerCompletionItemProvider("mysql", {
    triggerCharacters: ["."],
    async provideCompletionItems(model, position) {
      const ctx = editorContexts.get(model.uri.toString());
      const catalog = ctx?.sessionId && ctx.database ? await useSchemaStore().loadCatalog(ctx.sessionId, ctx.database) : undefined;
      const word = model.getWordUntilPosition(position);
      const range = { startLineNumber: position.lineNumber, endLineNumber: position.lineNumber, startColumn: word.startColumn, endColumn: word.endColumn };
      const items = suggest({ catalog, text: model.getValue(), offset: model.getOffsetAt(position) });
      return {
        suggestions: items.map((s) => ({ label: s.label, kind: kinds[s.kind], detail: s.detail, insertText: s.insertText, sortText: s.sortText, range })),
      };
    },
  });

  // Colores equivalentes a los tokens de la app (styles/main.css): Monaco no lee variables CSS.
  monaco.editor.defineTheme("dbacus-light", {
    base: "vs", inherit: true, rules: [],
    colors: { "editor.background": "#ffffff", "editorGutter.background": "#ffffff", "editorLineNumber.foreground": "#94a3b8", "editor.lineHighlightBackground": "#f1f5f9" },
  });
  monaco.editor.defineTheme("dbacus-dark", {
    base: "vs-dark", inherit: true, rules: [],
    colors: { "editor.background": "#111c33", "editorGutter.background": "#111c33", "editorLineNumber.foreground": "#64748b", "editor.lineHighlightBackground": "#1a2742" },
  });
}

export const themeName = (resolved: "light" | "dark") => (resolved === "dark" ? "dbacus-dark" : "dbacus-light");
