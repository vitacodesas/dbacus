// Backend simulado en memoria para los E2E (VITE_MOCK_API, spec 11). Imita los contratos de docs/specs/02: mismos
// nombres, DTOs y eventos que los bindings generados, sin red ni base de datos. No se incluye en el build normal.
import { Events } from "./runtime";

/* eslint-disable @typescript-eslint/no-explicit-any */
type Any = any;
interface Seed {
  profiles?: Any[];
  /** Milisegundos de latencia simulada en cada llamada (por defecto 20). */
  latencyMs?: number;
}
const seed: Seed = (window as Any).__MOCK_SEED__ ?? {};
const latency = seed.latencyMs ?? 20;

let n = 0;
const uid = (p: string) => `${p}${++n}`;
const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

/** Promesa con `cancel()`, como CancellablePromise de Wails. */
function cp<T>(fn: () => Promise<T> | T): Any {
  let canceled = false;
  const p: Any = (async () => {
    await sleep(latency);
    if (canceled) throw Object.assign(new Error("cancelado"), { name: "CancelError" });
    return fn();
  })();
  p.cancel = () => void (canceled = true);
  return p;
}
const fail = (code: string, message: string, detail = "") =>
  Object.assign(new Error(message), { cause: JSON.stringify({ code, message, detail, retryable: false }) });

// ---- estado ------------------------------------------------------------------------------------------------------------
const profiles: Any[] = (seed.profiles ?? []).map((p) => ({ ...blankProfile(), ...p }));
const sessions: Any[] = [];
const history: Any[] = [];
let settings: Any = { theme: "light", locale: "es", maxRows: 100_000, maxResultMemoryMiB: 512, historyRetention: 5000, showSystemDatabases: false, confirmEdits: true, sidebarWidth: 300, splitRatio: 0.5 };
let tabs: Any[] = [];

function blankProfile(): Any {
  return {
    id: "", name: "", driver: "mysql", host: "127.0.0.1", port: 3306, user: "root", database: "", color: "", params: null,
    tls: { mode: "preferred", caPath: "", certPath: "", keyPath: "" },
    ssh: { enabled: false, host: "", port: 22, user: "", authMethod: "password", keyPath: "" },
    savePassword: true, noHistory: false, readOnly: false, hasPassword: false, hasSshPassword: false, hasSshPassphrase: false, createdAt: "", updatedAt: "",
  };
}
const profileOf = (id: string) => profiles.find((p) => p.id === id);

// ---- datos de ejemplo ----------------------------------------------------------------------------------------------------
const col = (name: string, dataType: string, extra: Any = {}) => ({ name, dataType, columnType: dataType, nullable: false, default: null, isPrimaryKey: false, isAutoInc: false, isGenerated: false, comment: "", ordinal: 0, ...extra });
const TABLES: Record<string, { rows: number; cols: Any[]; cell: (i: number, c: number) => Any }> = {
  actor: {
    rows: 200,
    cols: [col("actor_id", "smallint", { isPrimaryKey: true, isAutoInc: true }), col("first_name", "varchar"), col("last_name", "varchar"), col("last_update", "timestamp")],
    cell: (i, c) => [{ v: i + 1 }, { v: `NOMBRE${i + 1}` }, { v: `APELLIDO${i + 1}` }, { v: "2026-01-01 00:00:00", t: "datetime" }][c],
  },
  film: {
    rows: 1000,
    cols: [col("film_id", "smallint", { isPrimaryKey: true }), col("title", "varchar"), col("description", "text", { nullable: true }), col("extra", "json", { nullable: true })],
    cell: (i, c) => [{ v: i + 1 }, { v: `PELÍCULA ${i + 1}` }, i % 5 === 0 ? { v: null } : { v: `Descripción ${i + 1}` }, { v: `{"id": ${i + 1}, "host": ["lk-stg-internal"]}`, t: "json" }][c],
  },
  big_numbers: {
    rows: 1_000_000,
    cols: [col("id", "bigint", { isPrimaryKey: true }), col("n", "int")],
    cell: (i, c) => (c === 0 ? { v: i + 1 } : { v: (i * 7) % 1000 }),
  },
};
const asMeta = (cols: Any[]) => cols.map((c) => ({ name: c.name, dataType: c.dataType, nullable: c.nullable, precision: null, scale: null }));

// ---- consultas ------------------------------------------------------------------------------------------------------------
interface Run {
  stmts: Any[]; data: Record<number, { rows: number; cell: (i: number, c: number) => Any; name?: string; sql?: string }>; canceled: boolean; cancelWake?: () => void
  sessionId?: string; pending: Record<number, Map<string, { row: number; col: number; value: Any }>>
  // Spec 14: filas nuevas sin aplicar (col → valor), filas marcadas para borrar, y lo ya aplicado (quitadas y añadidas).
  newRows: Record<number, Map<number, Any>[]>; deleted: Record<number, Set<number>>
  removed: Record<number, number[]>; appended: Record<number, Any[][]>
}
const runs = new Map<string, Run>();

function planStatement(sql: string, index: number, start: number, maxRows: number, autoLimit: number) {
  const text = sql.trim();
  const lower = text.toLowerCase();
  const base = { index, kind: "rows", columns: null, rowCount: 0, truncated: false, affectedRows: 0, lastInsertId: 0, warnings: 0, elapsedMs: 5, sql: text, limitApplied: 0, start, end: start + sql.length };
  if (/^(insert|update|delete|create|drop|alter)\b/.test(lower)) return { stmt: { ...base, kind: "exec", affectedRows: 3 } };
  if (/sleep\s*\(/.test(lower)) return { stmt: base, sleep: true, table: { rows: 1, cell: () => ({ v: 0 }), cols: [col("SLEEP(60)", "int")] } };
  const name = Object.keys(TABLES).find((t) => new RegExp(`\\b${t}\\b`).test(lower));
  if (lower.startsWith("select") && name) {
    const t = TABLES[name];
    // Como Go: LIMIT automático solo a un SELECT simple sin LIMIT; no cuenta como truncado.
    const limit = autoLimit > 0 && !/\blimit\b/.test(lower) ? autoLimit : 0;
    const cap = limit > 0 ? Math.min(maxRows, limit) : maxRows;
    const total = Math.min(t.rows, cap);
    return { tableName: name, stmt: { ...base, columns: asMeta(t.cols), rowCount: total, truncated: limit === 0 && t.rows > maxRows, limitApplied: limit > 0 && t.rows > limit ? limit : limit > 0 ? limit : 0 }, table: { ...t, rows: total } };
  }
  if (lower.startsWith("select")) {
    const label = text.replace(/^select\s+/i, "").replace(/;$/, "") || "1";
    return { stmt: { ...base, columns: asMeta([col(label, "int")]), rowCount: 1 }, table: { rows: 1, cell: () => ({ v: 1 }), cols: [] } };
  }
  return { error: fail("QUERY_SYNTAX", `Error de sintaxis cerca de «${text.slice(0, 20)}»`, "1064") };
}

function splitStatements(sql: string): { text: string; start: number }[] {
  const out: { text: string; start: number }[] = [];
  let pos = 0;
  for (const part of sql.split(";")) {
    if (part.trim()) out.push({ text: part, start: pos });
    pos += part.length + 1;
  }
  return out;
}

async function runQuery(queryId: string, req: Any, run: Run) {
  Events.Emit("query:started", { queryId, tabId: req.tabId, sessionId: req.sessionId, startedAt: new Date().toISOString() });
  const t0 = Date.now();
  const done: Any[] = [];
  const maxRows = req.maxRows > 0 ? req.maxRows : settings.maxRows;
  let idx = 0;
  for (const part of splitStatements(req.sql)) {
    const plan: Any = planStatement(part.text, idx, part.start, maxRows, req.autoLimit ?? 0);
    if (plan.error) {
      const e = JSON.parse(plan.error.cause);
      Events.Emit("query:error", { queryId, stmtIndex: idx, error: e, start: part.start, end: part.start + part.text.length });
      addHistory(req, part.text, false, 0, 0, e.message, Date.now() - t0);
      return;
    }
    if (plan.table) run.data[idx] = { ...plan.table, name: plan.tableName, sql: part.text.trim() };
    Events.Emit("query:statement", { queryId, result: plan.stmt });
    if (plan.sleep) {
      await new Promise<void>((resolve) => {
        run.cancelWake = resolve;
        setTimeout(resolve, 60_000);
      });
      if (run.canceled) {
        Events.Emit("query:error", { queryId, stmtIndex: idx, error: { code: "QUERY_CANCELED", message: "Consulta cancelada", detail: "", retryable: false }, start: part.start, end: part.start + part.text.length });
        addHistory(req, part.text, false, 0, 0, "Consulta cancelada", Date.now() - t0);
        return;
      }
    } else {
      await sleep(15);
    }
    if (plan.stmt.kind === "rows") Events.Emit("query:progress", { queryId, stmtIndex: idx, rowCount: plan.stmt.rowCount });
    done.push(plan.stmt);
    addHistory(req, part.text, true, plan.stmt.rowCount, plan.stmt.affectedRows, "", Date.now() - t0);
    idx++;
  }
  Events.Emit("query:done", { queryId, results: done, elapsedMs: Date.now() - t0, database: req.database || "sakila" });
}

function addHistory(req: Any, sql: string, ok: boolean, rows: number, affected: number, msg: string, ms: number) {
  const profileId = sessions.find((s) => s.sessionId === req.sessionId)?.profileId;
  if (!profileId || profileOf(profileId)?.noHistory) return;
  history.unshift({ id: uid("h"), profileId, sql: sql.trim(), database: req.database || "sakila", executedAt: new Date().toISOString(), elapsedMs: ms, ok, rowCount: rows, affectedRows: affected, errorMessage: msg });
  Events.Emit("history:added", { profileId });
}

// ---- servicios (misma forma que los bindings generados) --------------------------------------------------------------------
export const ConnectionService = {
  List: () => cp(() => profiles.map((p) => ({ ...p }))),
  Get: (id: string) => cp(() => profileOf(id) ?? Promise.reject(fail("NOT_FOUND", "Conexión no encontrada"))),
  Save: (p: Any, sec: Any) =>
    cp(() => {
      if (!p.name?.trim()) throw fail("VALIDATION", "Falta el nombre", "name");
      const now = new Date().toISOString();
      const prev = profileOf(p.id);
      const saved = { ...p, id: p.id || uid("p"), hasPassword: sec.password != null ? sec.password !== "" : (prev?.hasPassword ?? false), createdAt: prev?.createdAt ?? now, updatedAt: now };
      if (prev) Object.assign(prev, saved);
      else profiles.push(saved);
      return { ...saved };
    }),
  Clone: (id: string) =>
    cp(() => {
      const src = profileOf(id);
      if (!src) throw fail("NOT_FOUND", "Conexión no encontrada");
      const copy = { ...src, id: uid("p"), name: `${src.name} (copia)` };
      profiles.push(copy);
      return { ...copy };
    }),
  Delete: (id: string) =>
    cp(() => {
      const i = profiles.findIndex((p) => p.id === id);
      if (i >= 0) profiles.splice(i, 1);
    }),
  Test: (p: Any) =>
    cp(async () => {
      await sleep(120);
      const bad = p.host === "bad.invalid";
      const err = { code: "NETWORK_UNREACHABLE", message: `No se pudo resolver ${p.host}`, detail: "", retryable: true };
      return {
        ok: !bad, latencyMs: 12, serverVersion: bad ? "" : "8.0.36-mock", error: bad ? err : null,
        steps: [{ name: "tcp", ok: !bad, skipped: false, elapsedMs: 4, error: bad ? err : null }, { name: "auth", ok: !bad, skipped: bad, elapsedMs: 5, error: null }, { name: "query", ok: !bad, skipped: bad, elapsedMs: 3, error: null }],
      };
    }),
  ParseDSN: (dsn: string) =>
    cp(() => {
      const m = /^mysql:\/\/(?:([^:@]*)(?::([^@]*))?@)?([^:/]+)(?::(\d+))?(?:\/(.*))?$/.exec(dsn);
      if (!m) throw fail("VALIDATION", "DSN no válido", "dsn");
      return { profile: { ...blankProfile(), user: m[1] ?? "", host: m[3], port: Number(m[4] ?? 3306), database: m[5] ?? "" }, secrets: { password: m[2] ?? null, sshPassword: null, sshKeyPassphrase: null, clear: null } };
    }),
  Connect: (id: string, overrides: Any) =>
    cp(async () => {
      const p = profileOf(id);
      if (!p) throw fail("NOT_FOUND", "Conexión no encontrada");
      if (!p.savePassword && !overrides?.password) throw fail("PASSWORD_REQUIRED", "Falta la contraseña", "password");
      await sleep(80);
      const s = { sessionId: uid("s"), profileId: id, serverVersion: "8.0.36-mock", currentDatabase: p.database };
      sessions.push(s);
      return { ...s };
    }),
  ListSessions: () => cp(() => sessions.map((s) => ({ ...s }))),
  ListDrivers: () => cp(() => [{ name: "mysql", displayName: "MySQL / MariaDB", defaultPort: 3306, ssl: true }]),
  PickFile: () => cp(() => "/tmp/mock-key.pem"),
  Disconnect: (id: string) => cp(() => void sessions.splice(0, sessions.length, ...sessions.filter((s) => s.sessionId !== id))),
  TrustHostKey: () => cp(() => undefined),
};

export const SchemaService = {
  ListDatabases: () => cp(() => [{ name: "information_schema", isSystem: true }, { name: "mysql", isSystem: true }, { name: "sakila", isSystem: false }]),
  ListTables: (_s: string, db: string) =>
    cp(() => (db === "sakila" ? Object.entries(TABLES).map(([name, t]) => ({ name, schema: db, type: "table", rowEstimate: t.rows, comment: "" })) : [])),
  DescribeTable: (_s: string, db: string, table: string) =>
    cp(() => {
      const t = TABLES[table];
      if (!t) throw fail("NOT_FOUND", "Tabla no encontrada");
      return { name: table, schema: db, type: "table", rowEstimate: t.rows, comment: "", columns: t.cols.map((c, i) => ({ ...c, ordinal: i + 1 })), indexes: [], foreignKeys: [], createSql: `CREATE TABLE \`${table}\` (...)` };
    }),
  GetCompletionCatalog: () => cp(() => ({ tables: Object.entries(TABLES).map(([name, t]) => ({ name, columns: t.cols.map((c) => ({ name: c.name, dataType: c.dataType })) })) })),
  Refresh: (s: string, db: string) => cp(() => Events.Emit("schema:invalidated", { sessionId: s, database: db })),
  SelectStatement: (_s: string, db: string, table: string, limit: number) => cp(() => `SELECT * FROM \`${db}\`.\`${table}\` LIMIT ${limit}`),
};

const toCell = (c: Any) => ({ v: c.v, t: c.t ?? "", trunc: false });
/** Valores ya aplicados con EditService.Apply: "tabla:fila:col" → valor. El servidor simulado «recuerda» lo editado. */
const applied = new Map<string, Any>();
/** Filas reales visibles: las del servidor menos las borradas ya aplicadas, más las insertadas ya aplicadas. */
const realTotal = (run: Run, d: Any, stmt: number) => Math.min(d.rows, settings.maxRows) - (run.removed[stmt]?.length ?? 0) + (run.appended[stmt]?.length ?? 0);
const originalIndex = (run: Run, stmt: number, i: number) => {
  let idx = i;
  for (const r of [...(run.removed[stmt] ?? [])].sort((a, b) => a - b)) {
    if (r <= idx) idx++;
    else break;
  }
  return idx;
};
const baseVisible = (run: Run, d: Any, stmt: number) => Math.min(d.rows, settings.maxRows) - (run.removed[stmt]?.length ?? 0);
/** Valor (sin cambios pendientes) de la celda visible i,c. */
const visibleCell = (run: Run, d: Any, stmt: number, i: number, c: number) =>
  i < baseVisible(run, d, stmt) ? cellAt(d, originalIndex(run, stmt, i), c) : { v: run.appended[stmt][i - baseVisible(run, d, stmt)][c], t: "" };
const cellAt = (d: Any, i: number, c: number) => {
  const k = `${d.name}:${i}:${c}`;
  return applied.has(k) ? { v: applied.get(k), t: "" } : d.cell(i, c);
};
export const QueryService = {
  Execute: (req: Any) =>
    cp(() => {
      const sess = sessions.find((s) => s.sessionId === req.sessionId);
      if (!sess) throw fail("SESSION_CLOSED", "La sesión se cerró");
      if (profileOf(sess.profileId)?.readOnly && splitStatements(req.sql).some((p) => !/^\s*(select|show|describe|desc|explain|use|with|values|table|help)\b/i.test(p.text))) {
        throw fail("QUERY_FAILED", "Esta conexión es de solo lectura: solo se permiten SELECT, SHOW, DESCRIBE y EXPLAIN", "readonly");
      }
      const queryId = uid("q");
      const run: Run = { stmts: [], data: {}, canceled: false, sessionId: req.sessionId, pending: {}, newRows: {}, deleted: {}, removed: {}, appended: {} };
      runs.set(queryId, run);
      void runQuery(queryId, req, run);
      return { queryId };
    }),
  Cancel: (queryId: string) =>
    cp(() => {
      const r = runs.get(queryId);
      if (r) {
        r.canceled = true;
        r.cancelWake?.();
      }
    }),
  GetRows: (queryId: string, stmt: number, offset: number, limit: number) =>
    cp(() => {
      const run = runs.get(queryId);
      const d = run?.data[stmt];
      if (!run || !d) throw fail("RESULT_EXPIRED", "El resultado ya no está en memoria");
      const ncols = (d as Any).cols?.length || 1;
      const real = realTotal(run, d, stmt);
      const news = run.newRows[stmt] ?? [];
      const total = real + news.length;
      const end = Math.min(offset + limit, total);
      const rows: Any[] = [];
      const deleted: number[] = [];
      const pend = run.pending[stmt];
      for (let i = offset; i < end; i++) {
        if (i >= real) {
          const n = news[i - real];
          rows.push(Array.from({ length: ncols }, (_, c) => (n.has(c) ? { v: n.get(c).null ? null : n.get(c).text, t: "", trunc: false, edited: true } : { v: null, t: "default", trunc: false, edited: true })));
          continue;
        }
        if (run.deleted[stmt]?.has(i)) deleted.push(i);
        rows.push(Array.from({ length: ncols }, (_, c) => {
          const p = pend?.get(`${i}:${c}`);
          return p ? { v: p.value.null ? null : p.value.text, t: "", trunc: false, edited: true } : toCell(visibleCell(run, d, stmt, i, c));
        }));
      }
      return { offset, rows, total, complete: true, deleted, newFrom: news.length ? real : -1 };
    }),
  GetCell: (queryId: string, stmt: number, row: number, col: number) => cp(() => toCell(cellAt(runs.get(queryId)?.data[stmt] ?? { cell: () => ({ v: null }) }, row, col))),
  RowsAsInsert: (_q: string, _s: number, _o: number, _l: number, table: string) => cp(() => `INSERT INTO \`${table || "t"}\` VALUES (1);`),
  ReleaseResult: (queryId: string) => cp(() => void runs.delete(queryId)),
  CloseTab: () => cp(() => undefined),
  StatementAt: (sql: string, offset: number) =>
    cp(() => {
      const hit = splitStatements(sql).find((s) => offset >= s.start && offset <= s.start + s.text.length) ?? { text: sql, start: 0 };
      return { text: hit.text.trim(), start: hit.start, end: hit.start + hit.text.length };
    }),
  FormatSQL: (sql: string) => cp(() => sql.replace(/\bselect\b/gi, "SELECT").replace(/\bfrom\b/gi, "FROM")),
  SaveTabs: (t: Any[]) => cp(() => void (tabs = t ?? [])),
  LoadTabs: () => cp(() => tabs),
};


// ---- edición de celdas y filas (spec 13 y 14), versión simplificada: la clave es siempre la primera columna autoincremental -----
const NOT_NULL: Record<string, number[]> = { actor: [1, 2], film: [1], big_numbers: [1] };
const MAX_LEN: Record<string, Record<number, number>> = { actor: { 1: 10, 2: 10 } };
const CASCADES: Record<string, string[]> = { film: ["sakila.film_actor"] };
const isJsonCol = (d: Any, c: number) => String(d.cols?.[c]?.dataType).toLowerCase() === "json";
function checkJson(text: string) {
  try {
    JSON.parse(text);
  } catch (e) {
    throw fail("VALIDATION", `El valor no es JSON válido: ${(e as Error).message}`, "cell");
  }
}
function counts(run: Run, stmt: number) {
  const inserts = run.newRows[stmt]?.length ?? 0;
  const deletes = run.deleted[stmt]?.size ?? 0;
  return { pendingInserts: inserts, pendingDeletes: deletes, pending: (run.pending[stmt]?.size ?? 0) + inserts + deletes };
}
function editPlan(queryId: string, stmt: number) {
  const run = runs.get(queryId);
  const d = run?.data[stmt];
  if (!run || !d) throw fail("RESULT_EXPIRED", "El resultado ya no está en memoria");
  const sess = sessions.find((s) => s.sessionId === run.sessionId);
  const ncols = (d as Any).cols?.length ?? 0;
  const notNull = NOT_NULL[d.name ?? ""] ?? [];
  const col = (editable: boolean, reason = "", c = 0) => ({
    editable, reason, dataType: String((d as Any).cols?.[c]?.dataType ?? "varchar"), nullable: !notNull.includes(c),
    insertable: c > 0, required: notNull.includes(c),
  });
  const info: Any = {
    editable: false, reason: "", database: "sakila", table: d.name ?? "", keyColumns: [] as number[], columns: Array.from({ length: ncols }, (_, c) => col(false, "", c)),
    canInsert: false, insertReason: "", insertMissing: [], deleteAffects: CASCADES[d.name ?? ""] ?? [], ...counts(run, stmt),
  };
  if (!d.name || !ncols) return { run, d, info: { ...info, reason: "no_single_table" } };
  if (profileOf(sess?.profileId)?.readOnly) return { run, d, info: { ...info, reason: "readonly_profile" } };
  if (/\bjoin\b/i.test(d.sql ?? "")) return { run, d, info: { ...info, reason: "no_single_table" } };
  info.editable = true;
  info.canInsert = true;
  info.keyColumns = [0];
  info.columns = Array.from({ length: ncols }, (_, c) => (c === 0 ? col(false, "key", c) : col(true, "", c)));
  return { run, d, info };
}
const pendingKey = (row: number, c: number) => `${row}:${c}`;
const origText = (run: Run, d: Any, stmt: number, row: number, col: number) => String(visibleCell(run, d, stmt, row, col).v ?? "");
function validateValue(d: Any, col: number, v: Any) {
  const notNull = NOT_NULL[d.name ?? ""] ?? [];
  if (v.null && notNull.includes(col)) throw fail("VALIDATION", `La columna «${col}» no admite NULL`, "cell");
  if (!v.null && isJsonCol(d, col)) checkJson(v.text);
  const max = MAX_LEN[d.name ?? ""]?.[col];
  if (!v.null && max && [...v.text].length > max) throw fail("VALIDATION", `El texto tiene ${[...v.text].length} caracteres y la columna admite ${max}`, "cell");
}
export const EditService = {
  FormatJSON: (text: string) =>
    cp(() => {
      checkJson(text);
      return JSON.stringify(JSON.parse(text), null, 2);
    }),
  GetEditInfo: (queryId: string, stmt: number) => cp(() => editPlan(queryId, stmt).info),
  Stage: (queryId: string, stmt: number, row: number, col: number, v: Any) =>
    cp(() => {
      const { run, d, info } = editPlan(queryId, stmt);
      if (!info.editable) throw fail("VALIDATION", "No se puede editar este resultado", info.reason);
      const real = realTotal(run, d, stmt);
      if (row >= real) {
        // fila nueva
        if (!info.columns[col]?.insertable) throw fail("VALIDATION", "Esta columna no se puede escribir en una fila nueva", "not_insertable");
        validateValue(d, col, v);
        run.newRows[stmt][row - real].set(col, v);
        return { ...info, ...counts(run, stmt) };
      }
      if (!info.columns[col]?.editable) throw fail("VALIDATION", "Esta columna no se puede editar", info.columns[col]?.reason || "col");
      if (run.deleted[stmt]?.has(row)) throw fail("VALIDATION", "La fila está marcada para borrar", "row_deleted");
      validateValue(d, col, v);
      const map = (run.pending[stmt] ??= new Map());
      if (!v.null && v.text === origText(run, d, stmt, row, col)) map.delete(pendingKey(row, col));
      else map.set(pendingKey(row, col), { row, col, value: v });
      return { ...info, ...counts(run, stmt) };
    }),
  Unstage: (queryId: string, stmt: number, row: number, col: number) =>
    cp(() => {
      const { run, d, info } = editPlan(queryId, stmt);
      const real = realTotal(run, d, stmt);
      if (row >= real) run.newRows[stmt]?.[row - real]?.delete(col);
      else run.pending[stmt]?.delete(pendingKey(row, col));
      return { ...info, ...counts(run, stmt) };
    }),
  Discard: (queryId: string, stmt: number) =>
    cp(() => {
      const { run, info } = editPlan(queryId, stmt);
      run.pending[stmt]?.clear();
      run.newRows[stmt] = [];
      run.deleted[stmt]?.clear();
      return { ...info, ...counts(run, stmt) };
    }),
  AddRow: (queryId: string, stmt: number) =>
    cp(() => {
      const { run, d, info } = editPlan(queryId, stmt);
      if (!info.canInsert) throw fail("VALIDATION", "No se pueden añadir filas a este resultado", info.insertReason);
      (run.newRows[stmt] ??= []).push(new Map());
      return { rows: [realTotal(run, d, stmt) + run.newRows[stmt].length - 1], info: { ...info, ...counts(run, stmt) } };
    }),
  DuplicateRows: (queryId: string, stmt: number, rows: number[]) =>
    cp(() => {
      const { run, d, info } = editPlan(queryId, stmt);
      if (!info.canInsert) throw fail("VALIDATION", "No se pueden añadir filas a este resultado", info.insertReason);
      const real = realTotal(run, d, stmt);
      const list = (run.newRows[stmt] ??= []);
      const out: number[] = [];
      for (const src of rows) {
        const n = new Map<number, Any>();
        for (let c = 1; c < info.columns.length; c++) {
          if (src >= real) {
            if (list[src - real]?.has(c)) n.set(c, list[src - real].get(c));
            continue;
          }
          const p = run.pending[stmt]?.get(pendingKey(src, c));
          if (p) n.set(c, p.value);
          else {
            const v = visibleCell(run, d, stmt, src, c).v;
            n.set(c, v == null ? { null: true, text: "" } : { null: false, text: String(v) });
          }
        }
        list.push(n);
        out.push(real + list.length - 1);
      }
      return { rows: out, info: { ...info, ...counts(run, stmt) } };
    }),
  MarkDelete: (queryId: string, stmt: number, rows: number[]) =>
    cp(() => {
      const { run, d, info } = editPlan(queryId, stmt);
      if (!info.editable) throw fail("VALIDATION", "No se puede editar este resultado", info.reason);
      const real = realTotal(run, d, stmt);
      for (const r of [...rows].sort((a, b) => b - a)) {
        if (r >= real) run.newRows[stmt].splice(r - real, 1);
        else {
          (run.deleted[stmt] ??= new Set()).add(r);
          for (const k of [...(run.pending[stmt]?.keys() ?? [])]) if (k.startsWith(`${r}:`)) run.pending[stmt].delete(k);
        }
      }
      return { ...info, ...counts(run, stmt) };
    }),
  UnmarkDelete: (queryId: string, stmt: number, rows: number[]) =>
    cp(() => {
      const { run, info } = editPlan(queryId, stmt);
      rows.forEach((r) => run.deleted[stmt]?.delete(r));
      return { ...info, ...counts(run, stmt) };
    }),
  Preview: (queryId: string, stmt: number) =>
    cp(() => {
      const { run, d } = editPlan(queryId, stmt);
      const lit = (v: Any) => (v.null ? "NULL" : `'${v.text}'`);
      const byRow = new Map<number, Any[]>();
      for (const p of run.pending[stmt]?.values() ?? []) byRow.set(p.row, [...(byRow.get(p.row) ?? []), p]);
      const out: Any[] = [...byRow.entries()].sort((a, b) => a[0] - b[0]).map(([row, ps]) => ({
        kind: "update", row, cols: ps.map((p) => p.col),
        sql: `UPDATE \`sakila\`.\`${d.name}\` SET ${ps.map((p) => `\`c${p.col}\` = ${lit(p.value)}`).join(", ")} WHERE \`id\` = ${visibleCell(run, d, stmt, row, 0).v}`,
      }));
      for (const row of [...(run.deleted[stmt] ?? [])].sort((a, b) => a - b)) {
        out.push({ kind: "delete", row, cols: [], sql: `DELETE FROM \`sakila\`.\`${d.name}\` WHERE \`id\` = ${visibleCell(run, d, stmt, row, 0).v} LIMIT 1` });
      }
      const real = realTotal(run, d, stmt);
      (run.newRows[stmt] ?? []).forEach((n, i) => {
        const cols = [...n.keys()].sort((a, b) => a - b);
        out.push({ kind: "insert", row: real + i, cols, sql: `INSERT INTO \`sakila\`.\`${d.name}\` (${cols.map((c) => `\`c${c}\``).join(", ")}) VALUES (${cols.map((c) => lit(n.get(c))).join(", ")})` });
      });
      return out;
    }),
  Apply: (queryId: string, stmt: number) =>
    cp(() => {
      const { run, d, info } = editPlan(queryId, stmt);
      if (!info.editable) throw fail("QUERY_FAILED", "Esta conexión es de solo lectura: no se pueden aplicar cambios", "readonly");
      const edits = run.pending[stmt];
      const news = run.newRows[stmt] ?? [];
      const dels = [...(run.deleted[stmt] ?? [])].sort((a, b) => a - b);
      if (!edits?.size && !news.length && !dels.length) return { applied: 0, inserted: 0, deleted: 0, conflict: null };
      for (const [i, n] of news.entries()) {
        for (const [c, col] of info.columns.entries()) {
          if (col.required && !n.has(c)) throw fail("VALIDATION", `Fila nueva ${i + 1}: falta un valor para «${c}»`, "required");
        }
      }
      if ((window as Any).__mock.forceConflict) {
        const first = [...(edits?.values() ?? [])].sort((a, b) => a.row - b.row)[0]?.row ?? dels[0] ?? 0;
        return { applied: 0, inserted: 0, deleted: 0, conflict: { row: first, reason: (window as Any).__mock.forceConflict } };
      }
      const rows = new Set([...(edits?.values() ?? [])].map((p) => p.row));
      // 1) ediciones: se guardan por fila original; 2) borrados; 3) filas nuevas.
      for (const p of edits?.values() ?? []) {
        applied.set(`${d.name}:${originalIndex(run, stmt, p.row)}:${p.col}`, p.value.null ? null : p.value.text);
      }
      const origs = dels.map((r) => originalIndex(run, stmt, r));
      const visibleBase = baseVisible(run, d, stmt);
      const appendedDel = dels.filter((r) => r >= visibleBase).map((r) => r - visibleBase);
      run.removed[stmt] = [...(run.removed[stmt] ?? []), ...dels.filter((r) => r < visibleBase).map((_, i) => origs[i])];
      if (appendedDel.length) run.appended[stmt] = run.appended[stmt].filter((_, i) => !appendedDel.includes(i));
      for (const n of news) {
        const cells = Array.from({ length: info.columns.length }, (_, c) => (c === 0 ? 1000 + (run.appended[stmt]?.length ?? 0) : n.has(c) ? (n.get(c).null ? null : n.get(c).text) : null));
        (run.appended[stmt] ??= []).push(cells);
      }
      run.pending[stmt]?.clear();
      run.newRows[stmt] = [];
      run.deleted[stmt]?.clear();
      Events.Emit("edit:applied", { queryId, stmtIndex: stmt, rows: rows.size + dels.length + news.length });
      return { applied: rows.size, inserted: news.length, deleted: dels.length, conflict: null };
    }),
};

export const HistoryService = {
  List: (q: Any) =>
    cp(() => {
      const all = history.filter((h) => h.profileId === q.profileId && (!q.status || (q.status === "ok") === h.ok) && (!q.search || h.sql.toLowerCase().includes(q.search.toLowerCase())));
      return { entries: all.slice(0, q.limit || 50), nextCursor: "" };
    }),
  Delete: (id: string) => cp(() => void history.splice(0, history.length, ...history.filter((h) => h.id !== id))),
  Clear: (profileId: string) => cp(() => void history.splice(0, history.length, ...history.filter((h) => h.profileId !== profileId))),
};

export const ExportService = {
  Export: (req: Any) =>
    cp(() => {
      (window as Any).__mock.exports.push(req);
      const jobId = uid("j");
      const path = `/tmp/mock-export.${req.options.format}`;
      setTimeout(() => Events.Emit("export:done", { jobId, path, rows: 123, bytes: 4567, canceled: false }), 150);
      return { jobId, path, canceled: false };
    }),
  CancelExport: () => cp(() => undefined),
};

export const SettingsService = {
  Get: () => cp(() => ({ ...settings })),
  Set: (s: Any) =>
    cp(() => {
      if (s.maxRows < 1000) throw fail("VALIDATION", "Ajuste inválido", "maxRows");
      settings = { ...s };
      return { ...settings };
    }),
};

export const PingService = { Hello: (name: string) => cp(() => `Hola, ${name}`), PickFile: () => cp(() => "") };

/** Para que las pruebas lean el estado interno si lo necesitan. */
(window as Any).__mock = { profiles, sessions, history, exports: [] as Any[], forceConflict: "" as string, get settings() { return settings; } };
