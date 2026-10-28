import { describe, expect, it } from "vitest";
import type { Database, Table, TableDetail } from "../types";
import { buildForest, buildRows, dbId, tableId, type SessionNode, type TreeCtx } from "./schemaTree";

const t = (key: string, p?: Record<string, unknown>) => (p?.n !== undefined ? `${key}:${p.n}` : key);
const dbs: Database[] = [{ name: "app", isSystem: false }, { name: "mysql", isSystem: true }, { name: "shop", isSystem: false }];
const tables: Record<string, Table[]> = {
  app: [
    { name: "users", type: "table", schema: "app", rowEstimate: 10, comment: "Usuarios" },
    { name: "orders", type: "table", schema: "app", rowEstimate: null, comment: "" },
    { name: "v_active", type: "view", schema: "app", rowEstimate: null, comment: "" },
  ],
  shop: [],
};
const detail: TableDetail = {
  name: "users", schema: "app", type: "table", rowEstimate: null, comment: "", createSql: "",
  columns: [
    { name: "id", dataType: "int", columnType: "int unsigned", nullable: false, default: null, isPrimaryKey: true, isAutoInc: true, comment: "", ordinal: 1 },
    { name: "email", dataType: "varchar", columnType: "varchar(190)", nullable: true, default: null, isPrimaryKey: false, isAutoInc: false, comment: "correo", ordinal: 2 },
  ],
  indexes: [{ name: "uq_email", columns: ["email"], unique: true, type: "BTREE" }],
  foreignKeys: [{ name: "fk_org", columns: ["org_id"], refSchema: "app", refTable: "orgs", refColumns: ["id"], onUpdate: "RESTRICT", onDelete: "CASCADE" }],
};

function ctx(over: Partial<TreeCtx> & { open?: string[] } = {}): TreeCtx {
  const open = new Set(over.open ?? []);
  return {
    databases: dbs,
    tablesOf: (db) => tables[db],
    detailOf: (db, tb) => (db === "app" && tb === "users" ? detail : undefined),
    isExpanded: (id) => open.has(id),
    isLoading: { dbs: false, tables: () => false, detail: () => false },
    errorOf: { tables: () => undefined, detail: () => undefined },
    filter: "", showSystem: false, activeDb: "", t,
    ...over,
  };
}
const ids = (rows: { id: string }[]) => rows.map((r) => r.id);

describe("buildRows", () => {
  it("colapsado: solo bases, sin las de sistema", () => {
    const rows = buildRows(ctx());
    expect(rows.map((r) => r.label)).toEqual(["app", "shop"]);
    expect(rows.every((r) => r.depth === 0 && r.expandable && !r.expanded)).toBe(true);
  });

  it("showSystem las incluye (marcadas) y la base activa nunca se oculta", () => {
    expect(buildRows(ctx({ showSystem: true })).find((r) => r.label === "mysql")?.flags.system).toBe(true);
    const rows = buildRows(ctx({ activeDb: "mysql" }));
    expect(rows.find((r) => r.label === "mysql")?.flags.active).toBe(true);
  });

  it("base expandida: grupos Tablas/Vistas con contadores", () => {
    const rows = buildRows(ctx({ open: [dbId("app")] }));
    expect(rows.map((r) => [r.kind, r.label, r.depth])).toEqual([
      ["database", "app", 0], ["group", "schema.tables:2", 1], ["group", "schema.views:1", 1], ["database", "shop", 0],
    ]);
  });

  it("tablas visibles solo con el grupo expandido; el padre apunta bien", () => {
    const rows = buildRows(ctx({ open: [dbId("app"), `${dbId("app")}/tables`] }));
    const users = rows.find((r) => r.label === "users")!;
    expect(users).toMatchObject({ kind: "table", depth: 2, parentId: `${dbId("app")}/tables`, db: "app", table: "users", title: "Usuarios" });
    expect(rows.find((r) => r.label === "v_active")).toBeUndefined();
  });

  it("tabla con detalle: secciones y hojas con banderas", () => {
    const tid = tableId("app", "users");
    const rows = buildRows(ctx({ open: [dbId("app"), `${dbId("app")}/tables`, tid, `${tid}/cols`, `${tid}/idx`, `${tid}/fks`] }));
    const sections = rows.filter((r) => r.kind === "section").map((r) => r.label);
    expect(sections).toEqual(["schema.columns:2", "schema.indexes:1", "schema.foreignKeys:1"]);
    const id = rows.find((r) => r.label === "id")!;
    expect(id).toMatchObject({ kind: "column", hint: "int unsigned", flags: { pk: true, notNull: true } });
    expect(rows.find((r) => r.label === "email")?.title).toBe("correo");
    expect(rows.find((r) => r.label === "uq_email")).toMatchObject({ kind: "index", hint: "(email)", flags: { unique: true } });
    expect(rows.find((r) => r.label === "fk_org")?.hint).toBe("→ orgs(id)");
  });

  it("base sin tablas muestra fila vacía", () => {
    expect(buildRows(ctx({ open: [dbId("shop")] })).map((r) => r.kind)).toEqual(["database", "database", "empty"]);
  });

  it("estados de carga y error de una base", () => {
    const loading = buildRows(ctx({ open: [dbId("shop")], tablesOf: () => undefined, isLoading: { dbs: false, tables: () => true, detail: () => false } }));
    expect(loading.find((r) => r.kind === "loading")).toBeTruthy();
    const err = buildRows(ctx({
      open: [dbId("shop")], tablesOf: () => undefined,
      errorOf: { tables: () => ({ code: "QUERY_FAILED", message: "sin permisos", retryable: false }), detail: () => undefined },
    }));
    const row = err.find((r) => r.kind === "error")!;
    expect(row.label).toBe("sin permisos");
    expect(row.retry).toEqual({ level: "tables", db: "shop" });
  });

  it("sin bases cargadas: carga o error global", () => {
    expect(buildRows(ctx({ databases: undefined, isLoading: { dbs: true, tables: () => false, detail: () => false } })).map((r) => r.kind)).toEqual(["loading"]);
    const e = buildRows(ctx({ databases: undefined, errorOf: { dbs: { code: "X", message: "caído", retryable: true }, tables: () => undefined, detail: () => undefined } }));
    expect(e[0]).toMatchObject({ kind: "error", retry: { level: "dbs" } });
    expect(buildRows(ctx({ databases: undefined }))).toEqual([]);
  });
});

describe("filtro", () => {
  it("coincide con la base: se muestra aunque esté colapsada", () => {
    expect(buildRows(ctx({ filter: "sho" })).map((r) => r.label)).toEqual(["shop"]);
  });

  it("coincide con una tabla cargada: despliega el camino y oculta lo demás", () => {
    const rows = buildRows(ctx({ filter: "ord", open: [] }));
    expect(rows.map((r) => r.label)).toEqual(["app", "schema.tables:2", "orders"]);
    expect(rows.every((r) => r.expanded || r.kind === "table")).toBe(true);
  });

  it("coincide con una columna solo si su tabla está cargada", () => {
    const rows = buildRows(ctx({ filter: "email" }));
    // "uq_email" (índice) también contiene "email", por eso aparece su sección.
    expect(rows.map((r) => r.label)).toEqual(["app", "schema.tables:2", "users", "schema.columns:2", "email", "schema.indexes:1", "uq_email"]);
    expect(buildRows(ctx({ filter: "email", detailOf: () => undefined }))).toEqual([]);
  });

  it("es insensible a mayúsculas y vacío = sin filtro", () => {
    expect(ids(buildRows(ctx({ filter: "ORD" })))).toContain(tableId("app", "orders"));
    expect(buildRows(ctx({ filter: "   " }))).toHaveLength(2);
  });

  it("sin coincidencias no hay filas", () => {
    expect(buildRows(ctx({ filter: "zzz" }))).toEqual([]);
  });
});

describe("buildForest (varias conexiones)", () => {
  const node = (sessionId: string, label: string, over: Partial<SessionNode> = {}, ctxOver: Partial<TreeCtx> & { open?: string[] } = {}): SessionNode => ({
    sessionId, label, active: false, expanded: false, ctx: ctx(ctxOver), ...over,
  });

  it("cada sesión es una raíz; colapsadas solo se ven ellas", () => {
    const rows = buildForest([node("a", "Prod"), node("b", "Staging")], "");
    expect(rows.map((r) => [r.kind, r.label, r.depth])).toEqual([["session", "Prod", 0], ["session", "Staging", 0]]);
    expect(rows.every((r) => r.expandable && !r.expanded)).toBe(true);
  });

  it("la sesión expandida muestra sus bases un nivel más abajo y enlaza el padre", () => {
    const rows = buildForest([node("a", "Prod", { expanded: true }), node("b", "Staging")], "");
    expect(rows.map((r) => [r.label, r.depth])).toEqual([["Prod", 0], ["app", 1], ["shop", 1], ["Staging", 0]]);
    expect(rows[1].parentId).toBe("s:a");
    expect(rows[1].sessionId).toBe("a");
  });

  it("las claves son únicas aunque dos sesiones tengan la misma base", () => {
    const rows = buildForest([node("a", "Prod", { expanded: true }), node("b", "Staging", { expanded: true })], "");
    const keys = rows.map((r) => r.key);
    expect(new Set(keys).size).toBe(keys.length);
    expect(keys).toContain("a/db:app");
    expect(keys).toContain("b/db:app");
  });

  it("marca la sesión activa y conserva el color", () => {
    const [r] = buildForest([node("a", "Prod", { active: true, color: "#ff0000" })], "");
    expect(r.flags.active).toBe(true);
    expect(r.color).toBe("#ff0000");
  });

  it("el estado de carga de bases de una sesión queda bajo su raíz", () => {
    const loading = node("a", "Prod", { expanded: true }, { databases: undefined, isLoading: { dbs: true, tables: () => false, detail: () => false } });
    const rows = buildForest([loading], "");
    expect(rows.map((r) => [r.kind, r.depth])).toEqual([["session", 0], ["loading", 1]]);
    expect(rows[1].parentId).toBe("s:a");
  });

  it("filtro: muestra solo las sesiones con coincidencias y las despliega", () => {
    const rows = buildForest([node("a", "Prod"), node("b", "Staging", {}, { databases: [{ name: "inventario", isSystem: false }] })], "inven");
    expect(rows.map((r) => r.label)).toEqual(["Staging", "inventario"]);
    expect(rows[0].expanded).toBe(true);
  });

  it("filtro por nombre de la conexión la muestra aunque no tenga coincidencias dentro", () => {
    const rows = buildForest([node("a", "Prod"), node("b", "Staging")], "stag");
    expect(rows.map((r) => r.label)).toEqual(["Staging"]);
    expect(buildForest([node("a", "Prod")], "zzz")).toEqual([]);
  });
});
