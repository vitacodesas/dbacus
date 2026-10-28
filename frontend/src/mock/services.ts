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
let settings: Any = { theme: "light", locale: "es", maxRows: 100_000, maxResultMemoryMiB: 512, historyRetention: 5000, showSystemDatabases: false, sidebarWidth: 300, splitRatio: 0.5 };
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
    cols: [col("film_id", "smallint", { isPrimaryKey: true }), col("title", "varchar"), col("description", "text", { nullable: true })],
    cell: (i, c) => [{ v: i + 1 }, { v: `PELÍCULA ${i + 1}` }, i % 5 === 0 ? { v: null } : { v: `Descripción ${i + 1}` }][c],
  },
  big_numbers: {
    rows: 1_000_000,
    cols: [col("id", "bigint", { isPrimaryKey: true }), col("n", "int")],
    cell: (i, c) => (c === 0 ? { v: i + 1 } : { v: (i * 7) % 1000 }),
  },
};
const asMeta = (cols: Any[]) => cols.map((c) => ({ name: c.name, dataType: c.dataType, nullable: c.nullable, precision: null, scale: null }));

// ---- consultas ------------------------------------------------------------------------------------------------------------
interface Run { stmts: Any[]; data: Record<number, { rows: number; cell: (i: number, c: number) => Any }>; canceled: boolean; cancelWake?: () => void }
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
    return { stmt: { ...base, columns: asMeta(t.cols), rowCount: total, truncated: limit === 0 && t.rows > maxRows, limitApplied: limit > 0 && t.rows > limit ? limit : limit > 0 ? limit : 0 }, table: { ...t, rows: total } };
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
    if (plan.table) run.data[idx] = plan.table;
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
export const QueryService = {
  Execute: (req: Any) =>
    cp(() => {
      const sess = sessions.find((s) => s.sessionId === req.sessionId);
      if (!sess) throw fail("SESSION_CLOSED", "La sesión se cerró");
      if (profileOf(sess.profileId)?.readOnly && splitStatements(req.sql).some((p) => !/^\s*(select|show|describe|desc|explain|use|with|values|table|help)\b/i.test(p.text))) {
        throw fail("QUERY_FAILED", "Esta conexión es de solo lectura: solo se permiten SELECT, SHOW, DESCRIBE y EXPLAIN", "readonly");
      }
      const queryId = uid("q");
      const run: Run = { stmts: [], data: {}, canceled: false };
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
      const d = runs.get(queryId)?.data[stmt];
      if (!d) throw fail("RESULT_EXPIRED", "El resultado ya no está en memoria");
      const ncols = (d as Any).cols?.length || 1;
      const total = Math.min(d.rows, settings.maxRows);
      const end = Math.min(offset + limit, total);
      const rows: Any[] = [];
      for (let i = offset; i < end; i++) rows.push(Array.from({ length: ncols }, (_, c) => toCell(d.cell(i, c))));
      return { offset, rows, total, complete: true };
    }),
  GetCell: (queryId: string, stmt: number, row: number, col: number) => cp(() => toCell(runs.get(queryId)?.data[stmt]?.cell(row, col) ?? { v: null })),
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
(window as Any).__mock = { profiles, sessions, history, exports: [] as Any[], get settings() { return settings; } };
