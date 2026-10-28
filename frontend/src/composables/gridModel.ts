// Lógica pura del grid de resultados (sin Vue ni DOM): anchos, ventana visible, texto de celdas y copia. Spec 06.
import type { Cell, ColumnMeta } from "../types";

export const ROW_HEIGHT = 24;
export const HEADER_HEIGHT = 28;
export const GUTTER_WIDTH = 56;
export const MIN_COL = 60;
export const MAX_COL = 600;

/** Ancho inicial de columna según su nombre y tipo. */
export function defaultWidth(col: ColumnMeta): number {
  const t = col.dataType.toUpperCase().replace("UNSIGNED ", "");
  let base = 140;
  if (/INT|YEAR|BIT|BOOL/.test(t)) base = 90;
  else if (/DECIMAL|NUMERIC|FLOAT|DOUBLE/.test(t)) base = 110;
  else if (t === "DATE" || t === "TIME") base = 110;
  else if (/DATETIME|TIMESTAMP/.test(t)) base = 190;
  else if (/JSON|TEXT|BLOB/.test(t)) base = 240;
  const byName = col.name.length * 8 + 32; // que el encabezado quepa
  return Math.max(MIN_COL, Math.min(MAX_COL, Math.max(base, byName)));
}

export const clampWidth = (w: number) => Math.max(MIN_COL, Math.min(MAX_COL, Math.round(w)));

/** Desplazamientos acumulados: offsets[i] = x donde empieza la columna i; offsets[n] = ancho total. */
export function columnOffsets(widths: number[]): number[] {
  const out = [0];
  for (const w of widths) out.push(out[out.length - 1] + w);
  return out;
}

/** Rango [first, last) de columnas visibles (con margen) para un viewport horizontal. */
export function visibleColumns(offsets: number[], scrollLeft: number, viewWidth: number, overscan = 2): [number, number] {
  const n = offsets.length - 1;
  if (n <= 0) return [0, 0];
  const x0 = Math.max(0, scrollLeft - GUTTER_WIDTH);
  const x1 = x0 + viewWidth;
  let first = 0;
  while (first < n - 1 && offsets[first + 1] <= x0) first++;
  let last = first;
  while (last < n && offsets[last] < x1) last++;
  return [Math.max(0, first - overscan), Math.min(n, last + overscan)];
}

/** Rango [first, last) de filas visibles (con margen). */
export function visibleRows(total: number, scrollTop: number, viewHeight: number, overscan = 6): [number, number] {
  if (total <= 0) return [0, 0];
  const first = Math.floor(scrollTop / ROW_HEIGHT) - overscan;
  const last = Math.ceil((scrollTop + viewHeight) / ROW_HEIGHT) + overscan;
  return [Math.max(0, first), Math.min(total, last)];
}

export interface CellDisplay {
  text: string;
  /** Alinear a la derecha (números). */
  numeric: boolean;
  isNull: boolean;
  /** Tipo visual para estilos: bytes/json/datetime. */
  type: "" | "bytes" | "json" | "datetime" | "number" | "bool" | "default";
}

/** Cómo se muestra una celda. El texto largo se recorta con CSS; aquí solo se aplanan los saltos de línea. */
export function displayCell(c: Cell | undefined): CellDisplay {
  // Celda de una fila nueva a la que aún no se le ha dado valor: el servidor usará su DEFAULT.
  if (c?.t === "default") return { text: "DEFAULT", numeric: false, isNull: true, type: "default" };
  if (!c || c.v === null || c.v === undefined) return { text: "NULL", numeric: false, isNull: true, type: "" };
  const v = c.v;
  switch (c.t) {
    case "bigint":
    case "decimal":
      return { text: String(v), numeric: true, isNull: false, type: "number" };
    case "bytes":
      return { text: String(v), numeric: false, isNull: false, type: "bytes" };
    case "json":
      return { text: oneLine(String(v)) + (c.trunc ? "…" : ""), numeric: false, isNull: false, type: "json" };
    case "datetime":
      return { text: String(v), numeric: false, isNull: false, type: "datetime" };
  }
  if (typeof v === "number") return { text: String(v), numeric: true, isNull: false, type: "number" };
  if (typeof v === "boolean") return { text: v ? "true" : "false", numeric: false, isNull: false, type: "bool" };
  return { text: oneLine(String(v)) + (c.trunc ? "…" : ""), numeric: false, isNull: false, type: "" };
}

const oneLine = (s: string) => s.replace(/[\r\n\t]+/g, " ");

/** Texto de una celda para copiar (completo, sin recortar para mostrar). NULL → "NULL". */
export function copyText(c: Cell | undefined): string {
  if (!c || c.v === null || c.v === undefined) return "NULL";
  if (typeof c.v === "boolean") return c.v ? "true" : "false";
  return String(c.v);
}

/** TSV compatible con hojas de cálculo: los campos con tabulador, salto de línea o comillas se entrecomillan. */
export function toTSV(rows: Cell[][], columns: ColumnMeta[], range: { c0: number; c1: number }, withHeaders: boolean): string {
  const esc = (s: string) => (/[\t\r\n"]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s);
  const lines: string[] = [];
  if (withHeaders) lines.push(columns.slice(range.c0, range.c1 + 1).map((c) => esc(c.name)).join("\t"));
  for (const row of rows) lines.push(row.slice(range.c0, range.c1 + 1).map((c) => esc(copyText(c))).join("\t"));
  return lines.join("\n");
}

export interface Selection {
  /** Esquina fija y esquina móvil (r = fila, c = columna). */
  ar: number;
  ac: number;
  fr: number;
  fc: number;
}

export function selectionBounds(s: Selection) {
  return { r0: Math.min(s.ar, s.fr), r1: Math.max(s.ar, s.fr), c0: Math.min(s.ac, s.fc), c1: Math.max(s.ac, s.fc) };
}

export const inSelection = (s: Selection | null, r: number, c: number) => {
  if (!s) return false;
  const b = selectionBounds(s);
  return r >= b.r0 && r <= b.r1 && c >= b.c0 && c <= b.c1;
};
