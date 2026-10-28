import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { defineComponent, h } from "vue";
import { i18n } from "../../i18n";

const api = vi.hoisted(() => ({
  execute: vi.fn(), cancel: vi.fn(), getRows: vi.fn(), getCell: vi.fn(), rowsAsInsert: vi.fn(), releaseResult: vi.fn(), closeTab: vi.fn(),
  statementAt: vi.fn(), formatSql: vi.fn(), saveTabs: vi.fn(), loadTabs: vi.fn(),
  listProfiles: vi.fn(), listDrivers: vi.fn(), listSessions: vi.fn(), startConnect: vi.fn(),
}));
vi.mock("../../api/queries", () => api);
vi.mock("../../api/connections", () => ({ ...api, noSecrets: () => ({}) }));
// Monaco necesita un navegador real: se sustituye el editor por un stub con la misma interfaz.
// QueryWorkspace lo carga con defineAsyncComponent: __esModule le indica que debe usar `default`.
vi.mock("./SqlEditor.vue", () => ({
  __esModule: true,
  default: defineComponent({
    props: { tabId: String, modelValue: String, sessionId: String, database: String, marker: Object },
    emits: ["update:modelValue", "run", "runSelection", "format"],
    setup(props, { expose }) {
      expose({ selection: () => ({ text: "", start: 0, cursor: 0 }), dispose: vi.fn(), focus: vi.fn() });
      return () => h("div", { "data-testid": "editor-stub", "data-marker": props.marker ? JSON.stringify(props.marker) : "" }, props.modelValue);
    },
  }),
}));

import { useConnectionsStore } from "../../stores/connections";
import { useQueryTabsStore } from "../../stores/queryTabs";
import { useResultsStore } from "../../stores/results";
import { useSessionsStore } from "../../stores/sessions";
import QueryTabsBar from "./QueryTabsBar.vue";
import QueryToolbar from "./QueryToolbar.vue";
import QueryWorkspace from "./QueryWorkspace.vue";
import ResultPane from "./ResultPane.vue";

const plugins = [i18n];
const stmt = (index: number, over: Record<string, unknown> = {}) =>
  ({ index, kind: "rows", columns: [{ name: "n", dataType: "INT" }], rowCount: 1, truncated: false, affectedRows: 0, lastInsertId: 0, warnings: 0, elapsedMs: 3, sql: "SELECT 1", limitApplied: 0, start: 0, end: 8, ...over }) as never;

async function boot() {
  const tabs = useQueryTabsStore();
  api.loadTabs.mockResolvedValue([]);
  api.saveTabs.mockResolvedValue(undefined);
  api.closeTab.mockResolvedValue(undefined);
  await tabs.restore();
  const connections = useConnectionsStore();
  connections.profiles = [{ id: "p1", name: "Prod", color: "", ssh: {} } as never];
  const sessions = useSessionsStore();
  sessions.byId = { s1: { sessionId: "s1", profileId: "p1", serverVersion: "8.0", currentDatabase: "app" } };
  sessions.activeSessionId = "s1";
  return { tabs, results: useResultsStore() };
}

describe("componentes de consultas", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    Object.values(api).forEach((f) => f.mockReset());
  });

  describe("QueryTabsBar", () => {
    it("lista, activa, cierra y pide una nueva", async () => {
      const { tabs } = await boot();
      const a = tabs.openTab({ profileId: "p1" });
      const b = tabs.openTab({ profileId: "p1" });
      const w = mount(QueryTabsBar, { global: { plugins } });
      expect(w.findAll('[role="tab"]').map((t) => t.text())).toEqual(["Consulta 1", "Consulta 2"]);
      expect(w.findAll('[role="tab"]')[1].attributes("aria-selected")).toBe("true");

      await w.findAll('[role="tab"]')[0].trigger("click");
      expect(tabs.activeTabId).toBe(a);

      await w.findAll('[role="tab"] button')[1].trigger("click");
      await flushPromises();
      expect(tabs.byId(b)).toBeUndefined();

      await w.get('button[aria-label^="Nueva consulta"]').trigger("click");
      expect(w.emitted("new")).toHaveLength(1);
    });

    it("renombra con doble clic, Enter confirma y Esc cancela", async () => {
      const { tabs } = await boot();
      const id = tabs.openTab({ profileId: "p1" });
      const w = mount(QueryTabsBar, { global: { plugins }, attachTo: document.body });
      await w.get('[role="tab"]').trigger("dblclick");
      const input = w.get("input");
      await input.setValue("Ventas");
      await input.trigger("keydown", { key: "Enter" });
      expect(tabs.byId(id)?.title).toBe("Ventas");

      await w.get('[role="tab"]').trigger("dblclick");
      await w.get("input").setValue("Otro");
      await w.get("input").trigger("keydown", { key: "Escape" });
      await flushPromises();
      expect(tabs.byId(id)?.title).toBe("Ventas");
    });

    it("muestra el indicador de ejecución en la pestaña que corre", async () => {
      const { tabs, results } = await boot();
      const id = tabs.openTab({ profileId: "p1" });
      api.execute.mockResolvedValue("q1");
      await results.execute({ tabId: id, sessionId: "s1", sql: "x", baseOffset: 0, database: "", autoLimit: 0 });
      const w = mount(QueryTabsBar, { global: { plugins } });
      expect(w.findAll('[role="tab"] [role="status"]').length).toBeGreaterThan(0);
    });
  });

  describe("QueryToolbar", () => {
    it("muestra Ejecutar o Detener según el estado y la conexión de la pestaña", async () => {
      const { tabs, results } = await boot();
      const id = tabs.openTab({ profileId: "p1" });
      const w = mount(QueryToolbar, { props: { tab: tabs.byId(id)! }, global: { plugins } });
      expect(w.find('[data-testid="run-all"]').exists()).toBe(true);
      expect(w.get('[data-testid="toolbar-connection"]').text()).toBe("Prod");
      await w.get('[data-testid="run-all"]').trigger("click");
      expect(w.emitted("run")).toHaveLength(1);

      api.execute.mockResolvedValue("q1");
      await results.execute({ tabId: id, sessionId: "s1", sql: "x", baseOffset: 0, database: "", autoLimit: 0 });
      await flushPromises();
      expect(w.findAll('[data-testid="run-all"]')).toHaveLength(0);
      await w.get('[data-testid="stop"]').trigger("click");
      expect(w.emitted("stop")).toHaveLength(1);
    });

    it("el selector de base cambia la base de la pestaña y el de límite el límite automático", async () => {
      const { tabs } = await boot();
      const id = tabs.openTab({ profileId: "p1" });
      const w = mount(QueryToolbar, { props: { tab: tabs.byId(id)! }, global: { plugins } });
      await w.get('[data-testid="limit-select"]').setValue("500");
      const { useUiStore } = await import("../../stores/ui");
      expect(useUiStore().autoLimit).toBe(500);

      tabs.setDatabase(id, "shop");
      await w.setProps({ tab: tabs.byId(id)! });
      expect((w.get('[data-testid="database-select"]').element as HTMLSelectElement).value).toBe("shop");
    });
  });

  describe("ResultPane", () => {
    async function pane() {
      const { tabs, results } = await boot();
      const id = tabs.openTab({ profileId: "p1" });
      api.execute.mockResolvedValue("q1");
      api.getRows.mockResolvedValue({ offset: 0, total: 1, complete: true, rows: [[{ v: 7 }]] });
      return { id, results, mount: () => mount(ResultPane, { props: { tabId: id }, global: { plugins } }) };
    }
    const run = async (p: Awaited<ReturnType<typeof pane>>) => {
      await p.results.execute({ tabId: p.id, sessionId: "s1", sql: "x", baseOffset: 0, database: "", autoLimit: 0 });
      p.results.handleStarted({ queryId: "q1", tabId: p.id, sessionId: "s1", startedAt: "" });
    };

    it("sin ejecución invita a ejecutar", async () => {
      const p = await pane();
      expect(p.mount().text()).toContain("Ejecuta una consulta");
    });

    it("mientras corre muestra el progreso y el botón Detener", async () => {
      const p = await pane();
      await run(p);
      p.results.handleStatement({ queryId: "q1", result: stmt(0, { rowCount: 0 }) });
      p.results.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount: 1500 });
      const w = p.mount();
      expect(w.text()).toContain("1,500 filas recibidas");
      await w.get("button.bg-danger").trigger("click");
      expect(w.emitted("cancel")).toHaveLength(1);
    });

    it("terminada con filas muestra pestaña Resultado y el pie con el rango", async () => {
      const p = await pane();
      await run(p);
      p.results.handleStatement({ queryId: "q1", result: stmt(0) });
      p.results.handleDone({ queryId: "q1", results: [stmt(0)], elapsedMs: 12, database: "app" });
      const w = p.mount();
      await flushPromises();
      expect(w.findAll('[role="tab"]').map((t) => t.text())).toEqual(["Resultado 1", "Mensajes"]);
      expect(w.findAll('[role="grid"]')).toHaveLength(1);
      expect(w.get('[data-testid="range"]').text()).toContain("1");
    });

    it("un resultado vacío dice '0 filas'; una sentencia sin filas resume lo afectado", async () => {
      const p = await pane();
      await run(p);
      p.results.handleStatement({ queryId: "q1", result: stmt(0, { rowCount: 0 }) });
      p.results.handleDone({ queryId: "q1", results: [stmt(0, { rowCount: 0 })], elapsedMs: 1, database: "" });
      expect(p.mount().text()).toContain("0 filas");

      const q = await pane();
      await run(q);
      const exec = stmt(0, { kind: "exec", columns: null, rowCount: 0, affectedRows: 3, lastInsertId: 9 });
      q.results.handleStatement({ queryId: "q1", result: exec });
      q.results.handleDone({ queryId: "q1", results: [exec], elapsedMs: 1, database: "" });
      const w = q.mount();
      await flushPromises();
      expect(w.text()).toContain("3 filas afectadas");
    });

    it("un error sin filas se explica en Mensajes con código y botón para copiar", async () => {
      const p = await pane();
      await run(p);
      p.results.handleError({ queryId: "q1", stmtIndex: 0, error: { code: "QUERY_SYNTAX", message: "mal escrito", detail: "near 'x' at line 1", retryable: false }, start: 0, end: 1 });
      const w = p.mount();
      await flushPromises();
      expect(w.get('[data-testid="messages"]').text()).toContain("mal escrito");
      expect(w.text()).toContain("QUERY_SYNTAX");
      expect(w.text()).toContain("Copiar error");
    });

    it("cancelada y expirada muestran su aviso; expirada ofrece volver a ejecutar", async () => {
      const p = await pane();
      await run(p);
      p.results.handleError({ queryId: "q1", stmtIndex: 0, error: { code: "QUERY_CANCELED", message: "x", retryable: false }, start: 0, end: 1 });
      const w = p.mount();
      expect(w.text()).toContain("Consulta cancelada");

      p.results.byTab[p.id].expired = true;
      await flushPromises();
      await w.get('[role="status"] button').trigger("click");
      expect(w.emitted("rerun")).toHaveLength(1);
    });
  });

  describe("QueryWorkspace", () => {
    it("sin pestañas invita a abrir una; con pestañas monta toolbar, editor y panel de resultados", async () => {
      const { tabs } = await boot();
      const w = mount(QueryWorkspace, { global: { plugins }, attachTo: document.body });
      expect(w.text()).toContain("Sin consultas abiertas");

      tabs.openTab({ profileId: "p1", sql: "SELECT 1" });
      await flushPromises();
      expect(w.find('[data-testid="editor-stub"]').text()).toBe("SELECT 1");
      expect(w.find('[role="toolbar"]').exists()).toBe(true);
      expect(w.find('[data-testid="result-pane"]').exists()).toBe(true);
    });

    it("el botón Ejecutar envía el texto de la pestaña a Go con la sesión conectada", async () => {
      const { tabs } = await boot();
      tabs.openTab({ profileId: "p1", sql: "SELECT 42", database: "shop" });
      api.execute.mockResolvedValue("q1");
      const w = mount(QueryWorkspace, { global: { plugins }, attachTo: document.body });
      await w.get('[data-testid="run-all"]').trigger("click");
      await flushPromises();
      expect(api.execute).toHaveBeenCalledWith({ sessionId: "s1", tabId: tabs.activeTabId, sql: "SELECT 42", database: "shop", maxRows: 0, autoLimit: 1000 });
    });

    it("pasa al editor el marcador del error y lo retira al editar", async () => {
      const { tabs, results } = await boot();
      const id = tabs.openTab({ profileId: "p1", sql: "SELEC 1" });
      api.execute.mockResolvedValue("q1");
      await results.execute({ tabId: id, sessionId: "s1", sql: "SELEC 1", baseOffset: 0, database: "", autoLimit: 0 });
      results.handleStarted({ queryId: "q1", tabId: id, sessionId: "s1", startedAt: "" });
      results.handleError({ queryId: "q1", stmtIndex: 0, error: { code: "QUERY_SYNTAX", message: "sintaxis", retryable: false }, start: 0, end: 7 });

      const w = mount(QueryWorkspace, { global: { plugins }, attachTo: document.body });
      await flushPromises();
      expect(JSON.parse(w.get('[data-testid="editor-stub"]').attributes("data-marker") ?? "null")).toMatchObject({ startOffset: 0, endOffset: 7, message: "sintaxis" });

      tabs.setSql(id, "SELECT 1");
      await flushPromises();
      expect(w.get('[data-testid="editor-stub"]').attributes("data-marker")).toBe("");
    });

    it("sin sesión, ejecutar intenta conectar el perfil de la pestaña", async () => {
      const { tabs } = await boot();
      useSessionsStore().byId = {};
      tabs.openTab({ profileId: "p1", sql: "SELECT 1" });
      api.startConnect.mockReturnValue({ result: Promise.reject({ cause: { code: "QUERY_CANCELED", message: "x" } }), cancel: vi.fn() });
      const w = mount(QueryWorkspace, { global: { plugins }, attachTo: document.body });
      await w.get('[data-testid="run-all"]').trigger("click");
      await flushPromises();
      expect(api.startConnect).toHaveBeenCalledWith("p1", null);
      expect(api.execute).not.toHaveBeenCalled();
    });
  });
});
