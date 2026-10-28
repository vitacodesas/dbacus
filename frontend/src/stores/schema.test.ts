import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

const api = vi.hoisted(() => ({ listDatabases: vi.fn(), listTables: vi.fn(), describeTable: vi.fn(), getCompletionCatalog: vi.fn() }));
vi.mock("../api/schema", () => api);

import { useSchemaStore } from "./schema";
import { dbId, tableId } from "../composables/schemaTree";

const S = "s1";

describe("schema store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    Object.values(api).forEach((f) => f.mockReset());
    api.listDatabases.mockResolvedValue([{ name: "app", isSystem: false }]);
    api.listTables.mockResolvedValue([{ name: "users", type: "table" }]);
    api.describeTable.mockResolvedValue({ name: "users", columns: [] });
    api.getCompletionCatalog.mockResolvedValue({ tables: [] });
  });

  it("carga bases una sola vez (lectura repetida no vuelve a pedir)", async () => {
    const s = useSchemaStore();
    await s.loadDatabases(S);
    await s.loadDatabases(S);
    expect(api.listDatabases).toHaveBeenCalledTimes(1);
    expect(s.databasesOf(S)).toHaveLength(1);
  });

  it("toggle expande y carga lo que falta; al plegar no recarga", async () => {
    const s = useSchemaStore();
    await s.toggle(S, dbId("app"), () => s.loadTables(S, "app"));
    expect(s.isExpanded(S, dbId("app"))).toBe(true);
    expect(s.tablesOf(S, "app")).toHaveLength(1);
    await s.toggle(S, dbId("app"));
    expect(s.isExpanded(S, dbId("app"))).toBe(false);
    await s.toggle(S, dbId("app"), () => s.loadTables(S, "app"));
    expect(api.listTables).toHaveBeenCalledTimes(1);
  });

  it("peticiones simultáneas de lo mismo se colapsan en una", async () => {
    const s = useSchemaStore();
    await Promise.all([s.loadTables(S, "app"), s.loadTables(S, "app")]);
    expect(api.listTables).toHaveBeenCalledTimes(1);
  });

  it("un fallo queda en errors, no rompe, y se puede reintentar", async () => {
    const s = useSchemaStore();
    api.listTables.mockRejectedValueOnce({ cause: { code: "QUERY_FAILED", message: "sin permisos" } });
    await s.loadTables(S, "app");
    const key = s.keyTables(S, "app");
    expect(s.errorOf(key)?.message).toBe("sin permisos");
    expect(s.isLoading(key)).toBe(false);
    expect(s.tablesOf(S, "app")).toBeUndefined();

    await s.loadTables(S, "app");
    expect(s.errorOf(key)).toBeUndefined();
    expect(s.tablesOf(S, "app")).toHaveLength(1);
  });

  it("schema:invalidated de una base recarga solo lo visible de esa base", async () => {
    const s = useSchemaStore();
    await s.loadDatabases(S);
    await s.loadTables(S, "app");
    await s.loadDetail(S, "app", "users");
    await s.loadTables(S, "otra");
    s.setExpanded(S, dbId("app"), true);
    s.setExpanded(S, tableId("app", "users"), true);
    api.listTables.mockClear();
    api.describeTable.mockClear();
    api.listDatabases.mockClear();

    await s.handleInvalidated({ sessionId: S, database: "app" });
    expect(api.listTables).toHaveBeenCalledTimes(1);
    expect(api.listTables).toHaveBeenCalledWith(S, "app");
    expect(api.describeTable).toHaveBeenCalledWith(S, "app", "users");
    expect(api.listDatabases).not.toHaveBeenCalled();
    expect(s.tablesOf(S, "otra")).toBeDefined();
  });

  it("schema:invalidated de la sesión entera recarga las bases y lo expandido", async () => {
    const s = useSchemaStore();
    await s.loadDatabases(S);
    await s.loadTables(S, "app");
    s.setExpanded(S, dbId("app"), true);
    api.listDatabases.mockClear();
    api.listTables.mockClear();

    await s.handleInvalidated({ sessionId: S, database: "" });
    expect(api.listDatabases).toHaveBeenCalledTimes(1);
    expect(api.listTables).toHaveBeenCalledTimes(1);
  });

  it("invalida el catálogo y no toca otras sesiones", async () => {
    const s = useSchemaStore();
    await s.loadCatalog(S, "app");
    await s.loadCatalog("s2", "app");
    await s.loadCatalog(S, "app");
    expect(api.getCompletionCatalog).toHaveBeenCalledTimes(2);

    await s.handleInvalidated({ sessionId: S, database: "app" });
    await s.loadCatalog(S, "app");
    await s.loadCatalog("s2", "app");
    expect(api.getCompletionCatalog).toHaveBeenCalledTimes(3);
  });

  it("forgetSession borra todo rastro de esa sesión", async () => {
    const s = useSchemaStore();
    await s.loadDatabases(S);
    await s.loadTables(S, "app");
    s.setExpanded(S, dbId("app"), true);
    s.forgetSession(S);
    expect(s.databasesOf(S)).toBeUndefined();
    expect(s.tablesOf(S, "app")).toBeUndefined();
    expect(s.isExpanded(S, dbId("app"))).toBe(false);
  });
});
