// Dónde marcar en el editor un error del servidor. Los offsets son UTF-16, igual que los índices de un string de JS
// y que `model.getPositionAt` de Monaco.

export interface EditorMarker {
  startOffset: number;
  endOffset: number;
  message: string;
}

export interface ErrorInfo {
  /** Dónde empieza en el editor el texto que se envió (0 = todo el contenido). */
  baseOffset: number;
  /** Rango de la sentencia fallida dentro del texto enviado. */
  start: number;
  end: number;
  message: string;
  /** Texto técnico del servidor; si trae "at line N" se marca solo esa línea. */
  detail?: string;
}

/**
 * Marca la sentencia que falló o, si MySQL/MariaDB informa "... at line N", solo esa línea (N es relativa a la sentencia).
 * Devuelve null si el rango no cae dentro del texto actual (el usuario ya editó y el marcador quedó obsoleto).
 */
export function errorMarker(sql: string, e: ErrorInfo): EditorMarker | null {
  const start = e.baseOffset + e.start;
  const end = Math.min(sql.length, e.baseOffset + e.end);
  if (start < 0 || start > sql.length || end < start) return null;

  const stmt = sql.slice(start, end);
  const line = /\bat line (\d+)/i.exec(e.detail ?? "")?.[1];
  if (line) {
    const lines = stmt.split("\n");
    const n = Math.min(Number(line), lines.length) - 1;
    if (n >= 0) {
      let from = start;
      for (let i = 0; i < n; i++) from += lines[i].length + 1;
      const text = lines[n];
      const lead = text.length - text.trimStart().length;
      const startOffset = from + lead;
      return { startOffset, endOffset: Math.max(startOffset + 1, from + text.trimEnd().length), message: e.message };
    }
  }
  return { startOffset: start, endOffset: Math.max(start + 1, end), message: e.message };
}
