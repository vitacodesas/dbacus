// Borradores del editor de estructura (spec 19) y su traducción a un ChangeSet de operaciones tipadas.
// Aquí no hay SQL: Go valida y genera el ALTER; esto solo decide QUÉ cambió y en qué orden.
import type { AlterModel, AlterOp, ChangeSet, Column, ForeignKey, Index, IndexColumn } from "../types";

export interface ColDraft {
  key: string;
  /** Nombre original; null si la columna es nueva. */
  orig: string | null;
  name: string;
  type: string;
  nullable: boolean;
  def: string | null;
  defExpr: boolean;
  autoInc: boolean;
  comment: string;
  deleted: boolean;
  /** Motivo por el que no se puede editar ("" = editable). */
  locked: "" | "generated";
  isPk: boolean;
}

export type IndexKind = "index" | "unique" | "fulltext";

export interface IdxDraft {
  key: string;
  orig: string | null;
  name: string;
  kind: IndexKind;
  columns: IndexColumn[];
  deleted: boolean;
  locked: "" | "primary" | "functional";
}

export interface FkDraft {
  key: string;
  orig: string | null;
  name: string;
  columns: string[];
  refSchema: string;
  refTable: string;
  refColumns: string[];
  onUpdate: string;
  onDelete: string;
  deleted: boolean;
}

export interface Drafts {
  cols: ColDraft[];
  idxs: IdxDraft[];
  fks: FkDraft[];
}

let seq = 0;
export const newKey = (p: string) => `${p}${++seq}`;

const kindOf = (ix: Index): IndexKind => (ix.type?.toUpperCase() === "FULLTEXT" ? "fulltext" : ix.unique ? "unique" : "index");

/** Columnas de un índice con su longitud de prefijo y orden, alineadas por posición con `columns`. */
const indexColumns = (ix: Index): IndexColumn[] =>
  (ix.columns ?? []).map((name, i) => ({ name, desc: ix.desc?.[i] ?? false, prefix: ix.prefixLens?.[i] ?? 0 }));

export function draftsFromModel(m: AlterModel): Drafts {
  return {
    cols: (m.columns ?? []).map((c) => ({
      key: newKey("c"), orig: c.name, name: c.name, type: c.columnType, nullable: c.nullable, def: c.default, defExpr: c.defaultExpr,
      autoInc: c.isAutoInc, comment: c.comment, deleted: false, locked: c.isGenerated ? "generated" : "", isPk: c.isPrimaryKey,
    })),
    idxs: (m.indexes ?? []).map((ix) => ({
      key: newKey("i"), orig: ix.name, name: ix.name, kind: kindOf(ix), columns: indexColumns(ix), deleted: false,
      locked: ix.name === "PRIMARY" ? "primary" : ix.functional ? "functional" : "",
    })),
    fks: (m.foreignKeys ?? []).map((fk: ForeignKey) => ({
      key: newKey("f"), orig: fk.name, name: fk.name, columns: [...(fk.columns ?? [])], refSchema: fk.refSchema, refTable: fk.refTable,
      refColumns: [...(fk.refColumns ?? [])], onUpdate: fk.onUpdate, onDelete: fk.onDelete, deleted: false,
    })),
  };
}

export const blankColumn = (): ColDraft => ({
  key: newKey("c"), orig: null, name: "", type: "varchar(255)", nullable: true, def: null, defExpr: false, autoInc: false, comment: "", deleted: false, locked: "", isPk: false,
});
export const blankIndex = (): IdxDraft => ({ key: newKey("i"), orig: null, name: "", kind: "index", columns: [], deleted: false, locked: "" });
export const blankFk = (): FkDraft => ({
  key: newKey("f"), orig: null, name: "", columns: [], refSchema: "", refTable: "", refColumns: [], onUpdate: "", onDelete: "", deleted: false,
});

const op = (o: Partial<AlterOp> & Pick<AlterOp, "kind" | "name">): AlterOp => ({ newName: "", column: null, first: false, after: "", index: null, fk: null, ...o });

const sameList = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i]);
const sameCols = (a: IndexColumn[], b: IndexColumn[]) => a.length === b.length && a.every((x, i) => x.name === b[i].name && x.desc === b[i].desc && x.prefix === b[i].prefix);

/** Un borrador de columna distinto de su original (nombre aparte): ¿cambió algo de lo que Go necesita regenerar? */
function specChanged(d: ColDraft, o: Column): boolean {
  return d.type.trim() !== o.columnType || d.nullable !== o.nullable || d.def !== o.default || (d.def !== null && d.defExpr !== o.defaultExpr) || d.autoInc !== o.isAutoInc || d.comment !== o.comment;
}

export function buildChangeSet(m: AlterModel, drafts: Drafts, database: string, table: string): ChangeSet {
  const ops: AlterOp[] = [];
  const origCol = new Map(m.columns?.map((c) => [c.name, c]));
  const origIdx = new Map(m.indexes?.map((i) => [i.name, i]));
  const origFk = new Map(m.foreignKeys?.map((f) => [f.name, f]));

  const idxChanged = (d: IdxDraft) => {
    const o = origIdx.get(d.orig ?? "");
    return !!o && (kindOf(o) !== d.kind || !sameCols(indexColumns(o), d.columns));
  };
  const fkChanged = (d: FkDraft) => {
    const o = origFk.get(d.orig ?? "");
    return !!o && (!sameList(o.columns ?? [], d.columns) || o.refSchema !== d.refSchema || o.refTable !== d.refTable || !sameList(o.refColumns ?? [], d.refColumns) || o.onUpdate !== d.onUpdate || o.onDelete !== d.onDelete);
  };

  // 1) Lo que se borra o se rehace va primero: claves foráneas, índices, columnas.
  for (const d of drafts.fks) if (d.orig && (d.deleted || fkChanged(d))) ops.push(op({ kind: "dropFK", name: d.orig }));
  for (const d of drafts.idxs) if (d.orig && d.locked === "" && (d.deleted || idxChanged(d))) ops.push(op({ kind: "dropIndex", name: d.orig }));
  for (const d of drafts.cols) if (d.orig && d.deleted) ops.push(op({ kind: "dropColumn", name: d.orig }));

  // 2) Columnas en el orden final. `cur` simula el orden real tras cada operación para emitir FIRST/AFTER solo cuando hace falta.
  const final = drafts.cols.filter((d) => !d.deleted);
  // El orden de partida es el del servidor (no el de los borradores, que ya pueden estar reordenados).
  const live = new Map(drafts.cols.filter((d) => d.orig && !d.deleted).map((d) => [d.orig as string, d.key]));
  const cur = (m.columns ?? []).map((c) => live.get(c.name)).filter((k): k is string => !!k);
  final.forEach((d, i) => {
    const wantPrev = i === 0 ? null : final[i - 1];
    const isNew = d.orig === null;
    const at = isNew ? cur.length : cur.indexOf(d.key);
    const curPrev = at === 0 ? null : cur[at - 1];
    const misplaced = curPrev !== (wantPrev?.key ?? null);
    if (misplaced) {
      if (!isNew) cur.splice(at, 1);
      cur.splice(wantPrev ? cur.indexOf(wantPrev.key) + 1 : 0, 0, d.key);
    } else if (isNew) {
      cur.push(d.key);
    }
    const pos = misplaced ? (wantPrev ? { after: wantPrev.name } : { first: true }) : {};
    const spec = { type: d.type.trim(), nullable: d.nullable, default: d.def, defaultExpr: d.defExpr, autoInc: d.autoInc, comment: d.comment };
    if (isNew) {
      ops.push(op({ kind: "addColumn", name: d.name, column: spec, ...pos }));
      return;
    }
    const o = origCol.get(d.orig as string);
    if (!o || d.locked) return;
    const renamed = d.name !== d.orig;
    const changed = specChanged(d, o);
    if (renamed || changed || misplaced) {
      ops.push(op({ kind: "modifyColumn", name: d.orig as string, newName: renamed ? d.name : "", column: changed ? spec : null, ...pos }));
    }
  });

  // 3) Lo que se crea: renombrar índices, índices nuevos o rehechos, claves foráneas nuevas o rehechas.
  for (const d of drafts.idxs) {
    if (d.locked || d.deleted) continue;
    if (d.orig && !idxChanged(d) && d.name !== d.orig) ops.push(op({ kind: "renameIndex", name: d.orig, newName: d.name }));
    else if (!d.orig || idxChanged(d)) ops.push(op({ kind: "addIndex", name: d.name, index: { kind: d.kind, columns: d.columns } }));
  }
  for (const d of drafts.fks) {
    if (d.deleted || (d.orig && !fkChanged(d))) continue;
    ops.push(op({ kind: "addFK", name: d.name, fk: { columns: d.columns, refSchema: d.refSchema, refTable: d.refTable, refColumns: d.refColumns, onUpdate: d.onUpdate, onDelete: d.onDelete } }));
  }
  return { database, table, fingerprint: m.fingerprint, ops };
}

/** «a, b ,c» → ["a","b","c"]: lo que se escribe en los campos de lista de columnas. */
export const splitList = (s: string): string[] => s.split(",").map((x) => x.trim()).filter(Boolean);

/** Índices nuevos o editados: conserva prefijo y orden de las columnas que ya tenían, si siguen en la lista. */
export function columnsFromNames(names: string[], previous: IndexColumn[]): IndexColumn[] {
  return names.map((name) => previous.find((p) => p.name === name) ?? { name, desc: false, prefix: 0 });
}
