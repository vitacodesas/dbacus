// Construcción pura de las filas del árbol de esquema (sin Vue): fácil de probar y de virtualizar (spec 05).
import type { AppError, Database, Table, TableDetail } from "../types";

export type RowKind = "session" | "database" | "group" | "table" | "view" | "section" | "column" | "index" | "fk" | "loading" | "error" | "empty";

export interface TreeRow {
  id: string;
  kind: RowKind;
  depth: number;
  label: string;
  hint?: string;
  title?: string;
  parentId: string | null;
  expandable: boolean;
  expanded: boolean;
  db?: string;
  table?: string;
  flags: { pk?: boolean; notNull?: boolean; unique?: boolean; system?: boolean; active?: boolean };
  /** Para filas de error: qué carga reintentar. */
  retry?: { level: "dbs" } | { level: "tables"; db: string } | { level: "detail"; db: string; table: string };
}

export interface TreeCtx {
  databases: Database[] | undefined;
  tablesOf: (db: string) => Table[] | undefined;
  detailOf: (db: string, table: string) => TableDetail | undefined;
  isExpanded: (nodeId: string) => boolean;
  isLoading: { dbs: boolean; tables: (db: string) => boolean; detail: (db: string, table: string) => boolean };
  errorOf: { dbs?: AppError; tables: (db: string) => AppError | undefined; detail: (db: string, table: string) => AppError | undefined };
  filter: string;
  showSystem: boolean;
  activeDb: string;
  /** Traducción de etiquetas de grupos/secciones. */
  t: (key: string, params?: Record<string, unknown>) => string;
}

export const dbId = (db: string) => `db:${db}`;
export const tableId = (db: string, table: string) => `db:${db}/t:${table}`;

interface Node {
  id: string;
  kind: Exclude<RowKind, "loading" | "error" | "empty">;
  label: string;
  hint?: string;
  title?: string;
  db?: string;
  table?: string;
  flags: TreeRow["flags"];
  /** undefined = hoja; null = hijos aún sin cargar; array = hijos cargados. */
  children?: Node[] | null;
  loading?: boolean;
  error?: AppError;
  retry?: TreeRow["retry"];
}

function buildNodes(ctx: TreeCtx): Node[] {
  return (ctx.databases ?? [])
    .filter((d) => ctx.showSystem || !d.isSystem || d.name === ctx.activeDb)
    .map((d): Node => {
      const tables = ctx.tablesOf(d.name);
      return {
        id: dbId(d.name), kind: "database", label: d.name, db: d.name,
        flags: { system: d.isSystem, active: d.name === ctx.activeDb },
        children: tables ? groupsOf(ctx, d.name, tables) : null,
        loading: ctx.isLoading.tables(d.name), error: ctx.errorOf.tables(d.name), retry: { level: "tables", db: d.name },
      };
    });
}

function groupsOf(ctx: TreeCtx, db: string, tables: Table[]): Node[] {
  const out: Node[] = [];
  for (const [kind, key] of [["table", "tables"], ["view", "views"]] as const) {
    const items = tables.filter((t) => t.type === kind);
    if (!items.length) continue;
    out.push({
      id: `${dbId(db)}/${key}`, kind: "group", label: ctx.t(`schema.${key}`, { n: items.length }), db, flags: {},
      children: items.map((t) => tableNode(ctx, db, t)),
    });
  }
  return out;
}

function tableNode(ctx: TreeCtx, db: string, t: Table): Node {
  const id = tableId(db, t.name);
  const detail = ctx.detailOf(db, t.name);
  return {
    id, kind: t.type === "view" ? "view" : "table", label: t.name, db, table: t.name,
    title: t.comment || undefined, flags: {},
    children: detail ? sectionsOf(ctx, id, db, t.name, detail) : null,
    loading: ctx.isLoading.detail(db, t.name), error: ctx.errorOf.detail(db, t.name), retry: { level: "detail", db, table: t.name },
  };
}

function sectionsOf(ctx: TreeCtx, id: string, db: string, table: string, d: TableDetail): Node[] {
  const cols = d.columns ?? [];
  const idx = d.indexes ?? [];
  const fks = d.foreignKeys ?? [];
  const sections: Node[] = [
    {
      id: `${id}/cols`, kind: "section", label: ctx.t("schema.columns", { n: cols.length }), db, table, flags: {},
      children: cols.map((c) => ({
        id: `${id}/c:${c.name}`, kind: "column", label: c.name, hint: c.columnType, title: c.comment || undefined, db, table,
        flags: { pk: c.isPrimaryKey, notNull: !c.nullable },
      })),
    },
  ];
  if (idx.length) {
    sections.push({
      id: `${id}/idx`, kind: "section", label: ctx.t("schema.indexes", { n: idx.length }), db, table, flags: {},
      children: idx.map((i) => ({ id: `${id}/i:${i.name}`, kind: "index", label: i.name, hint: `(${(i.columns ?? []).join(", ")})`, db, table, flags: { unique: i.unique } })),
    });
  }
  if (fks.length) {
    sections.push({
      id: `${id}/fks`, kind: "section", label: ctx.t("schema.foreignKeys", { n: fks.length }), db, table, flags: {},
      children: fks.map((f) => ({
        id: `${id}/f:${f.name}`, kind: "fk", label: f.name, hint: `→ ${f.refTable}(${(f.refColumns ?? []).join(", ")})`, db, table, flags: {},
      })),
    });
  }
  return sections;
}

/**
 * Aplana el árbol a filas visibles. Con filtro, solo se muestran nodos cuyo nombre coincide (o con descendientes cargados
 * que coinciden) y se despliegan los caminos hasta ellos; no dispara cargas: filtra lo ya cargado (spec 05).
 */
export function buildRows(ctx: TreeCtx): TreeRow[] {
  const q = ctx.filter.trim().toLowerCase();
  const nodes = buildNodes(ctx);
  const top: TreeRow[] = [];

  if (!ctx.databases) {
    if (ctx.isLoading.dbs) top.push(pseudo("loading", "dbs", null, 0, ctx.t("schema.loading")));
    else if (ctx.errorOf.dbs) top.push({ ...pseudo("error", "dbs", null, 0, ctx.errorOf.dbs.message), retry: { level: "dbs" } });
    return top;
  }

  const walk = (list: Node[], depth: number, parent: string | null, filtered: boolean): { rows: TreeRow[]; matched: boolean } => {
    const rows: TreeRow[] = [];
    let any = false;
    for (const n of list) {
      const own = filtered && n.label.toLowerCase().includes(q);
      const open = ctx.isExpanded(n.id);
      let kidRows: TreeRow[] = [];
      let kidMatched = false;
      if (Array.isArray(n.children)) {
        // Si el propio nodo coincide, sus hijos se muestran sin filtrar.
        const sub = walk(n.children, depth + 1, n.id, filtered && !own);
        kidRows = sub.rows;
        kidMatched = sub.matched;
        if (!filtered && !open) kidRows = [];
      }
      if (filtered && !own && !kidMatched) continue;
      any = true;

      const showKids = filtered ? kidMatched || (own && open) : open;
      rows.push({
        id: n.id, kind: n.kind, depth, label: n.label, hint: n.hint, title: n.title, parentId: parent,
        expandable: n.children !== undefined, expanded: showKids, db: n.db, table: n.table, flags: n.flags,
      });
      if (!showKids) continue;
      if (n.children === null) {
        if (n.loading) rows.push(pseudo("loading", n.id, n.id, depth + 1, ctx.t("schema.loading")));
        else if (n.error) rows.push({ ...pseudo("error", n.id, n.id, depth + 1, n.error.message), retry: n.retry });
      } else if (Array.isArray(n.children) && n.children.length === 0 && !filtered) {
        rows.push(pseudo("empty", n.id, n.id, depth + 1, ctx.t(n.kind === "database" ? "schema.noTables" : "schema.empty")));
      } else rows.push(...kidRows);
    }
    return { rows, matched: any };
  };

  return walk(nodes, 0, null, q.length > 0).rows;
}

function pseudo(kind: "loading" | "error" | "empty", of: string, parent: string | null, depth: number, label: string): TreeRow {
  return { id: `${of}/${kind}`, kind, depth, label, parentId: parent, expandable: false, expanded: false, flags: {} };
}

// ---- Varias conexiones: cada sesión abierta es una raíz del árbol --------------------------------------------------

export interface SessionNode {
  sessionId: string;
  label: string;
  hint?: string;
  color?: string;
  active: boolean;
  expanded: boolean;
  /** Contexto del subárbol de esta sesión (sus bases, tablas…). */
  ctx: TreeCtx;
}

/** Fila del árbol completo: añade a qué sesión pertenece y una clave única entre sesiones. */
export interface FlatRow extends TreeRow {
  sessionId: string;
  key: string;
  color?: string;
}

export const sessionNodeId = (sessionId: string) => `s:${sessionId}`;

/**
 * Aplana todas las sesiones: una fila raíz por sesión y, si está expandida, su subárbol un nivel más abajo.
 * Con filtro solo se muestran las sesiones cuyo nombre o contenido ya cargado coincide.
 */
export function buildForest(nodes: SessionNode[], filter: string): FlatRow[] {
  const q = filter.trim().toLowerCase();
  const out: FlatRow[] = [];

  for (const n of nodes) {
    const rootId = sessionNodeId(n.sessionId);
    const inner = buildRows({ ...n.ctx, filter });
    const nameMatch = q.length > 0 && n.label.toLowerCase().includes(q);
    if (q && !nameMatch && inner.length === 0) continue;

    // Con filtro, una sesión con coincidencias se despliega sola para llegar a ellas.
    const expanded = q ? inner.length > 0 || (nameMatch && n.expanded) : n.expanded;
    out.push({
      id: rootId, key: rootId, sessionId: n.sessionId, kind: "session", depth: 0, label: n.label, hint: n.hint, color: n.color,
      parentId: null, expandable: true, expanded, flags: { active: n.active },
    });
    if (!expanded) continue;
    for (const r of inner) {
      out.push({ ...r, key: `${n.sessionId}/${r.id}`, sessionId: n.sessionId, depth: r.depth + 1, parentId: r.parentId ? `${n.sessionId}/${r.parentId}` : rootId });
    }
  }
  return out;
}
