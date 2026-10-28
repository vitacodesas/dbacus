// Sugerencias de autocompletado para el editor. Heurística ligera sobre el catálogo que entrega Go (tablas y columnas):
// no es un parser SQL. El filtrado fino por prefijo lo hace Monaco.
import type { CompletionCatalog } from "../types";

export type SuggestionKind = "keyword" | "function" | "table" | "column";

export interface Suggestion {
  label: string;
  kind: SuggestionKind;
  detail?: string;
  insertText: string;
  sortText: string;
}

export const KEYWORDS = `SELECT FROM WHERE GROUP BY HAVING ORDER LIMIT OFFSET DISTINCT AS AND OR NOT IN IS NULL LIKE BETWEEN EXISTS CASE WHEN THEN ELSE END
JOIN INNER LEFT RIGHT OUTER CROSS NATURAL ON USING UNION ALL INTERSECT EXCEPT INSERT INTO VALUES UPDATE SET DELETE REPLACE CREATE ALTER DROP TRUNCATE RENAME
TABLE VIEW INDEX DATABASE SCHEMA ADD COLUMN PRIMARY KEY FOREIGN REFERENCES DEFAULT UNIQUE IF WITH RECURSIVE OVER PARTITION ASC DESC USE SHOW DESCRIBE EXPLAIN
BEGIN COMMIT ROLLBACK START TRANSACTION CALL TRIGGER PROCEDURE FUNCTION RETURNS FOR SHARE LOCK DUPLICATE REGEXP RLIKE INTERVAL TRUE FALSE`.split(/\s+/);

export const FUNCTIONS = `COUNT SUM AVG MIN MAX GROUP_CONCAT COALESCE IFNULL NULLIF IF CONCAT CONCAT_WS SUBSTRING SUBSTR LENGTH CHAR_LENGTH UPPER LOWER TRIM LTRIM RTRIM
REPLACE LEFT RIGHT LPAD RPAD REVERSE INSTR LOCATE ROUND FLOOR CEIL CEILING ABS MOD POWER SQRT RAND NOW CURDATE CURTIME CURRENT_TIMESTAMP DATE TIME YEAR MONTH DAY
HOUR MINUTE SECOND DATE_ADD DATE_SUB DATEDIFF TIMESTAMPDIFF DATE_FORMAT STR_TO_DATE UNIX_TIMESTAMP FROM_UNIXTIME CAST CONVERT JSON_EXTRACT JSON_OBJECT JSON_ARRAY
JSON_UNQUOTE JSON_CONTAINS ROW_NUMBER RANK DENSE_RANK LAG LEAD FIRST_VALUE LAST_VALUE UUID DATABASE VERSION CONNECTION_ID LAST_INSERT_ID FOUND_ROWS`.split(/\s+/);

const TABLE_CONTEXT = /\b(FROM|JOIN|INTO|UPDATE|TABLE|DESCRIBE|DESC|TRUNCATE)\s+[\w`.$]*$/i;
const REFERENCE = /\b(?:FROM|JOIN|UPDATE|INTO)\s+(?:`?\w+`?\.)?`?(\w+)`?(?:\s+(?:AS\s+)?`?(\w+)`?)?/gi;
const NOT_ALIAS = new Set(["WHERE", "ON", "USING", "SET", "GROUP", "ORDER", "LIMIT", "HAVING", "LEFT", "RIGHT", "INNER", "OUTER", "CROSS", "NATURAL", "JOIN", "UNION", "VALUES", "SELECT", "FOR", "FULL", "STRAIGHT_JOIN", "AS"]);

/** Tablas (y alias) que menciona el texto: alias/nombre en minúsculas → nombre de tabla. */
export function referencedTables(text: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const m of text.matchAll(REFERENCE)) {
    const table = m[1];
    out.set(table.toLowerCase(), table);
    const alias = m[2];
    if (alias && !NOT_ALIAS.has(alias.toUpperCase())) out.set(alias.toLowerCase(), table);
  }
  return out;
}

export interface SuggestInput {
  catalog?: CompletionCatalog;
  text: string;
  /** Posición del cursor (UTF-16). */
  offset: number;
}

export function suggest({ catalog, text, offset }: SuggestInput): Suggestion[] {
  const before = text.slice(0, offset);
  const tables = catalog?.tables ?? [];
  const byName = new Map(tables.map((t) => [t.name.toLowerCase(), t]));

  // "alias." o "tabla." → solo las columnas de esa tabla.
  const qual = /(?:`?(\w+)`?)\.\w*$/.exec(before);
  if (qual) {
    const refs = referencedTables(text);
    const target = byName.get((refs.get(qual[1].toLowerCase()) ?? qual[1]).toLowerCase());
    return (target?.columns ?? []).map((c) => ({ label: c.name, kind: "column", detail: `${target!.name} · ${c.dataType}`, insertText: c.name, sortText: `0_${c.name}` }));
  }

  const wantsTables = TABLE_CONTEXT.test(before);
  const out: Suggestion[] = [];

  for (const t of tables) {
    out.push({ label: t.name, kind: "table", detail: "tabla", insertText: t.name, sortText: `${wantsTables ? 0 : 2}_${t.name}` });
  }
  if (!wantsTables) {
    // Columnas de las tablas que ya aparecen en la consulta, primero; el resto de columnas no se ofrecen (serían miles).
    const seen = new Set<string>();
    for (const real of new Set(referencedTables(text).values())) {
      const t = byName.get(real.toLowerCase());
      for (const c of t?.columns ?? []) {
        const key = `${t!.name}.${c.name}`;
        if (seen.has(key)) continue;
        seen.add(key);
        out.push({ label: c.name, kind: "column", detail: `${t!.name} · ${c.dataType}`, insertText: c.name, sortText: `0_${c.name}` });
      }
    }
    for (const f of FUNCTIONS) out.push({ label: f, kind: "function", detail: "función", insertText: f, sortText: `3_${f}` });
  }
  for (const k of KEYWORDS) out.push({ label: k, kind: "keyword", insertText: k, sortText: `${wantsTables ? 3 : 1}_${k}` });
  return out;
}
