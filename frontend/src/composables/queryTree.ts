import type { QueryNode } from "../types";

export type QuerySort = "name" | "modified";
export interface QueryRow {
  node: QueryNode;
  depth: number;
}

export const parentOf = (path: string) => (path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "");
export const baseOf = (path: string) => path.slice(path.lastIndexOf("/") + 1);
export const joinPath = (folder: string, name: string) => (folder ? `${folder}/${name}` : name);

const byName = (a: QueryNode, b: QueryNode) => baseOf(a.path).localeCompare(baseOf(b.path), undefined, { sensitivity: "base", numeric: true });

/** Filas visibles del árbol: carpetas primero, luego archivos; las carpetas plegadas ocultan a sus hijos. Solo ordena y filtra. */
export function flattenQueries(nodes: QueryNode[], collapsed: ReadonlySet<string>, sort: QuerySort): QueryRow[] {
  const children = new Map<string, QueryNode[]>();
  for (const n of nodes) {
    const p = parentOf(n.path);
    (children.get(p) ?? children.set(p, []).get(p)!).push(n);
  }
  const order = (a: QueryNode, b: QueryNode) => {
    if (a.kind !== b.kind) return a.kind === "folder" ? -1 : 1;
    if (sort === "modified" && a.kind === "file") return new Date(b.modifiedAt).getTime() - new Date(a.modifiedAt).getTime() || byName(a, b);
    return byName(a, b);
  };
  const out: QueryRow[] = [];
  const walk = (parent: string, depth: number) => {
    for (const n of (children.get(parent) ?? []).slice().sort(order)) {
      out.push({ node: n, depth });
      if (n.kind === "folder" && !collapsed.has(n.path)) walk(n.path, depth + 1);
    }
  };
  walk("", 0);
  return out;
}

/** Rutas de todas las carpetas, para los selectores «Guardar en» y «Mover a». `""` es la raíz. */
export const folderPaths = (nodes: QueryNode[]) => nodes.filter((n) => n.kind === "folder").map((n) => n.path).sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }));

/** Nombre libre dentro de una carpeta: «x», «x (copia)», «x (copia 2)»… sin distinguir mayúsculas. */
export function copyName(nodes: QueryNode[], folder: string, name: string): string {
  const used = new Set(nodes.filter((n) => parentOf(n.path) === folder).map((n) => n.name.toLowerCase()));
  for (let i = 1; ; i++) {
    const cand = i === 1 ? `${name} (copia)` : `${name} (copia ${i})`;
    if (!used.has(cand.toLowerCase())) return cand;
  }
}
