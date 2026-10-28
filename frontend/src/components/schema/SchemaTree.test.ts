import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { i18n } from "../../i18n";

const api = vi.hoisted(() => ({
  listDatabases: vi.fn(), listTables: vi.fn(), describeTable: vi.fn(), getCompletionCatalog: vi.fn(), selectStatement: vi.fn(), refreshSchema: vi.fn(),
}));
vi.mock("../../api/schema", () => api);

import { useConnectionsStore } from "../../stores/connections";
import { useSchemaStore } from "../../stores/schema";
import { useSessionsStore } from "../../stores/sessions";
import SchemaTree from "./SchemaTree.vue";

const info = (id: string, profileId: string) => ({ sessionId: id, profileId, serverVersion: "8.0", currentDatabase: "app" });

/** Abre N sesiones (s1, s2…) con su perfil y las deja desplegadas como hace el panel. */
function setup(n = 1) {
  const sessions = useSessionsStore();
  const connections = useConnectionsStore();
  connections.profiles = Array.from({ length: n }, (_, i) => ({ id: `p${i + 1}`, name: `Conn ${i + 1}` }) as never);
  sessions.byId = Object.fromEntries(Array.from({ length: n }, (_, i) => [`s${i + 1}`, info(`s${i + 1}`, `p${i + 1}`)]));
  sessions.activeSessionId = "s1";
  const schema = useSchemaStore();
  for (let i = 1; i <= n; i++) schema.setExpanded(`s${i}`, `s:s${i}`, true);
  return { sessions, schema };
}

function mountTree() {
  return mount(SchemaTree, { global: { plugins: [i18n] }, attachTo: document.body });
}
// Solo la etiqueta de cada fila (sin pistas como la versión del servidor).
const labels = (w: ReturnType<typeof mountTree>) => w.findAll("[data-row-id]").map((r) => r.find("span.truncate").text());

describe("SchemaTree", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    Object.values(api).forEach((f) => f.mockReset());
    api.listDatabases.mockResolvedValue([{ name: "app", isSystem: false }, { name: "mysql", isSystem: true }]);
    api.listTables.mockResolvedValue([{ name: "users", type: "table", comment: "" }]);
    api.describeTable.mockResolvedValue({ name: "users", columns: [{ name: "id", columnType: "int", isPrimaryKey: true, nullable: false }], indexes: [], foreignKeys: [] });
  });

  it("muestra las bases (sin las del sistema) y carga las tablas al expandir", async () => {
    const { schema } = setup();
    await schema.loadDatabases("s1");
    const w = mountTree();
    expect(labels(w)).toEqual(["Conn 1", "app"]);

    await w.get('[data-row-id="s1/db:app"]').trigger("click");
    await flushPromises();
    expect(api.listTables).toHaveBeenCalledWith("s1", "app");
    expect(labels(w)).toEqual(["Conn 1", "app", expect.stringContaining("Tablas (1)")]);
  });

  it("navegación por teclado: → expande, ↓ baja, ← pliega", async () => {
    const { schema } = setup();
    await schema.loadDatabases("s1");
    const w = mountTree();

    await w.get('[data-row-id="s1/db:app"]').trigger("keydown", { key: "ArrowRight" });
    await flushPromises();
    expect(schema.isExpanded("s1", "db:app")).toBe(true);
    expect(w.findAll("[data-row-id]")).toHaveLength(3);

    await w.get('[data-row-id="s1/db:app"]').trigger("keydown", { key: "ArrowLeft" });
    await flushPromises();
    expect(schema.isExpanded("s1", "db:app")).toBe(false);
    expect(w.findAll("[data-row-id]")).toHaveLength(2);
  });

  it("solo una fila es tabulable y los niveles tienen aria-level/expanded", async () => {
    const { schema } = setup();
    await schema.loadDatabases("s1");
    schema.setExpanded("s1", "db:app", true);
    await schema.loadTables("s1", "app");
    const w = mountTree();
    const items = w.findAll('[role="treeitem"]');
    expect(items.filter((i) => i.attributes("tabindex") === "0")).toHaveLength(1);
    expect(items[0].attributes("aria-level")).toBe("1");
    expect(items[0].attributes("aria-expanded")).toBe("true");
    expect(items[1].attributes("aria-level")).toBe("2");
    expect(items[2].attributes("aria-level")).toBe("3");
  });

  it("virtualiza: con miles de tablas solo monta unas decenas de filas", async () => {
    api.listTables.mockResolvedValue(Array.from({ length: 5000 }, (_, i) => ({ name: `t${i}`, type: "table", comment: "" })));
    const { schema } = setup();
    await schema.loadDatabases("s1");
    await schema.loadTables("s1", "app");
    schema.setExpanded("s1", "db:app", true);
    schema.setExpanded("s1", "db:app/tables", true);
    const w = mountTree();
    await flushPromises();
    const mounted = w.findAll("[data-row-id]").length;
    expect(mounted).toBeGreaterThan(5);
    expect(mounted).toBeLessThan(80);
  });

  it("un error de carga muestra la fila con 'Reintentar' y reintenta", async () => {
    api.listTables.mockRejectedValueOnce({ cause: { code: "QUERY_FAILED", message: "sin permisos" } });
    const { schema } = setup();
    await schema.loadDatabases("s1");
    const w = mountTree();
    await w.get('[data-row-id="s1/db:app"]').trigger("click");
    await flushPromises();
    expect(w.text()).toContain("sin permisos");

    await w.get('[data-kind="error"] button').trigger("click");
    await flushPromises();
    expect(api.listTables).toHaveBeenCalledTimes(2);
    expect(w.text()).not.toContain("sin permisos");
  });

  it("con varias conexiones muestra una raíz por sesión, cada una con su árbol", async () => {
    const { schema } = setup(2);
    await schema.loadDatabases("s1");
    await schema.loadDatabases("s2");
    const w = mountTree();
    expect(labels(w)).toEqual(["Conn 1", "app", "Conn 2", "app"]);
    expect(w.findAll('[data-kind="session"]')).toHaveLength(2);
  });

  it("tocar algo de una conexión la convierte en la activa", async () => {
    const { sessions, schema } = setup(2);
    await schema.loadDatabases("s1");
    await schema.loadDatabases("s2");
    const w = mountTree();
    expect(sessions.activeSessionId).toBe("s1");

    await w.get('[data-row-id="s2/db:app"]').trigger("click");
    await flushPromises();
    expect(sessions.activeSessionId).toBe("s2");
    expect(api.listTables).toHaveBeenCalledWith("s2", "app");
  });

  it("plegar una conexión oculta su subárbol sin tocar las demás", async () => {
    const { schema } = setup(2);
    await schema.loadDatabases("s1");
    await schema.loadDatabases("s2");
    const w = mountTree();
    await w.get('[data-row-id="s:s1"]').trigger("click");
    await flushPromises();
    expect(labels(w)).toEqual(["Conn 1", "Conn 2", "app"]);
  });
});

