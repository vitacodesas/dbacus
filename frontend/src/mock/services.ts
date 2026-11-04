// Backend simulado en memoria para los E2E (VITE_MOCK_API, spec 11). Imita los contratos de docs/specs/02: mismos
// nombres, DTOs y eventos que los bindings generados, sin red ni base de datos. No se incluye en el build normal.
import { Events } from "./runtime";

/* eslint-disable @typescript-eslint/no-explicit-any */
type Any = any;
interface Seed {
  profiles?: Any[];
  groups?: Any[];
  /** Sesiones ya abiertas al arrancar (p. ej. para probar el Monitor sin pasar por el diálogo de conexión). */
  sessions?: Any[];
  /**
   * Simula la paginación por cursor (spec 18) para `SELECT * FROM <tabla con clave>`. Es opt-in porque con ella activa los
   * resultados dejan de cargarse enteros y los E2E anteriores a T12.6 dan por hecho el LIMIT automático.
   */
  paging?: boolean;
  /** Consultas guardadas ya existentes en la carpeta de consultas (spec 20). */
  queries?: { path: string; sql: string; profileId?: string; profileName?: string; database?: string }[];
  /** Milisegundos de latencia simulada en cada llamada (por defecto 20). */
  latencyMs?: number;
  /** Procedimientos y funciones extra generados en `sakila` (spec 21): sirve para probar bases con cientos de rutinas. */
  routines?: number;
  /**
   * Monitor DBA (spec 22): `caps` fija qué puede leer cada conexión (un escenario para todas o uno por id de perfil:
   * ok | own_only | no_privilege | perf_schema_off | consumer_off), `processes` cuántos procesos simula (por defecto 8; con
   * cientos se prueba la lista virtualizada) y `sampleMs` cada cuánto emite una muestra (por defecto, el intervalo).
   */
  monitor?: { caps?: string | Record<string, string>; processes?: number; sampleMs?: number };
}
const seed: Seed = (window as Any).__MOCK_SEED__ ?? {};
const latency = seed.latencyMs ?? 20;
const maxRowsConst = 20;

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
const groups: Any[] = (seed.groups ?? []).map((g) => ({ id: "", name: "", color: "", createdAt: "", updatedAt: "", ...g }));
const sessions: Any[] = (seed.sessions ?? []).map((x) => ({ sessionId: "", profileId: "", serverVersion: "8.0.36", currentDatabase: "", ...x }));
const history: Any[] = [];
let settings: Any = { theme: "light", locale: "es", maxRows: 100_000, maxResultMemoryMiB: 512, historyRetention: 5000, showSystemDatabases: false, confirmEdits: true, sidebarWidth: 300, splitRatio: 0.5, collapsedGroups: [], monitorRawDays: 7, monitorHourlyDays: 90, monitorDailyDays: 365, monitorMaxMiB: 500, monitorIntervalSec: 30, monitorLiveAuto: true, monitorLiveIntervalSec: 2 };
let tabs: Any[] = [];

function blankProfile(): Any {
  return {
    id: "", name: "", driver: "mysql", host: "127.0.0.1", port: 3306, user: "root", database: "", color: "", groupId: "", params: null,
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
  // Spec 18: estado de la paginación simulada por sentencia (la tabla completa y la página actual).
  pages: Record<number, { table: Any; start: number; size: number; known: boolean; col?: string; offer?: boolean }>
}
const runs = new Map<string, Run>();

function planStatement(sql: string, index: number, start: number, maxRows: number, autoLimit: number) {
  const text = sql.trim();
  const lower = text.toLowerCase();
  const base = { index, kind: "rows", columns: null, rowCount: 0, truncated: false, affectedRows: 0, lastInsertId: 0, warnings: 0, elapsedMs: 5, sql: text, limitApplied: 0, start, end: start + sql.length };
  if (/^(insert|update|delete|create|drop|alter)\b/.test(lower)) return { stmt: { ...base, kind: "exec", affectedRows: 3 } };
  if (/sleep\s*\(/.test(lower)) return { stmt: base, sleep: true, table: { rows: 1, cell: () => ({ v: 0 }), cols: [col("SLEEP(60)", "int")] } };
  const name = Object.keys(TABLES).find((t) => new RegExp(`\\b${t}\\b`).test(lower));
  if (seed.paging && /^select\b.*\bjoin\b/.test(lower) && name) {
    // Un JOIN no se pagina por clave; si el resultado llega al límite se ofrece el modo aproximado por las columnas enteras.
    const t = TABLES[name];
    const size = Math.min(maxRows, autoLimit > 0 ? autoLimit : maxRows);
    const n = Math.min(t.rows, size);
    const candidates = autoLimit > 0 && t.rows > size ? t.cols.filter((c: Any) => /int/.test(c.dataType)).map((c: Any) => c.name) : [];
    const paging = { mode: "none", reason: "multi_table", pageSize: size, hasNext: false, hasPrev: false, page: 0, keyColumns: null, warning: "", estimated: 0, column: "", candidates, tieNext: false, tiePrev: false };
    return { tableName: name, stmt: { ...base, columns: asMeta(t.cols), rowCount: n, paging }, table: { ...t, rows: n }, ...(candidates.length ? { page: { table: t, start: 0, size, known: true, offer: true } } : {}) };
  }
  if (lower.startsWith("select") && name) {
    const t = TABLES[name];
    // Como Go: LIMIT automático solo a un SELECT simple sin LIMIT; no cuenta como truncado.
    const limit = autoLimit > 0 && !/\blimit\b/.test(lower) ? autoLimit : 0;
    if (seed.paging && autoLimit > 0 && /^select\s+\*\s+from\s+\w+\s*;?$/.test(lower) && t.cols[0]?.isPrimaryKey) {
      const size = Math.min(maxRows, autoLimit);
      const n = Math.min(t.rows, size);
      const paging = { mode: "keyset", reason: "", pageSize: size, hasNext: t.rows > size, hasPrev: false, page: 1, keyColumns: [t.cols[0].name], warning: "", estimated: 0, column: "", candidates: [], tieNext: false, tiePrev: false };
      return { tableName: name, stmt: { ...base, columns: asMeta(t.cols), rowCount: n, paging }, table: { ...t, rows: n }, page: { table: t, start: 0, size, known: true } };
    }
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
    if (plan.page) run.pages[idx] = plan.page;
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
      if (p.groupId && !groups.some((g) => g.id === p.groupId)) throw fail("VALIDATION", "El grupo no existe", "groupId");
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
  // Grupos (spec 17): mismas reglas que Go (nombre único sin distinguir mayúsculas, color #RRGGBB, borrar deja las
  // conexiones en «Sin grupo» sin tocar nada más).
  ListGroups: () => cp(() => [...groups].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" })).map((g) => ({ ...g }))),
  SaveGroup: (g: Any) =>
    cp(() => {
      const name = (g.name ?? "").trim();
      const color = (g.color ?? "").trim();
      if (!name) throw fail("VALIDATION", "El nombre del grupo es obligatorio", "name");
      if (name.length > 100) throw fail("VALIDATION", "El nombre del grupo es demasiado largo", "name");
      if (color && !/^#[0-9a-fA-F]{6}$/.test(color)) throw fail("VALIDATION", "El color debe tener formato #RRGGBB", "color");
      if (groups.some((o) => o.id !== g.id && o.name.toLowerCase() === name.toLowerCase())) throw fail("VALIDATION", "Ya existe un grupo con ese nombre", "name");
      const now = new Date().toISOString();
      const prev = groups.find((o) => o.id === g.id);
      const saved = { id: g.id || uid("g"), name, color, createdAt: prev?.createdAt ?? now, updatedAt: now };
      if (prev) Object.assign(prev, saved);
      else groups.push(saved);
      return { ...saved };
    }),
  DeleteGroup: (id: string) =>
    cp(() => {
      const i = groups.findIndex((g) => g.id === id);
      if (i >= 0) groups.splice(i, 1);
      profiles.forEach((p) => {
        if (p.groupId === id) p.groupId = "";
      });
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
  Disconnect: (id: string) => cp(() => { monOnDisconnect(id); sessions.splice(0, sessions.length, ...sessions.filter((s) => s.sessionId !== id)); }),
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

// ---- Editor de estructura (spec 19) -----------------------------------------------------------------------------------------
// Imita AlterService: lee/aplica sobre una copia en memoria de la tabla, rechaza en solo lectura y genera un SQL sencillo.
// Una columna llamada «boom» hace fallar la ejecución (para probar el fallo a mitad).
interface AlterState { columns: Any[]; indexes: Any[]; foreignKeys: Any[]; version: number }
const alterStates = new Map<string, AlterState>();
function alterState(db: string, table: string): AlterState {
  const t = TABLES[table];
  if (!t) throw fail("NOT_FOUND", "Tabla no encontrada");
  const key = `${db}.${table}`;
  if (!alterStates.has(key)) {
    const pk = t.cols.find((c: Any) => c.isPrimaryKey);
    alterStates.set(key, {
      version: 1,
      columns: t.cols.map((c: Any, i: number) => ({ ...c, columnType: c.dataType === "varchar" ? "varchar(45)" : c.dataType, ordinal: i + 1, defaultExpr: false, charset: "", collation: "", onUpdate: "", generationExpr: "" })),
      indexes: pk ? [{ name: "PRIMARY", columns: [pk.name], unique: true, type: "BTREE", prefix: false, prefixLens: [0], desc: [false], functional: false }] : [],
      foreignKeys: [],
    });
  }
  return alterStates.get(key) as AlterState;
}
const alterModel = (db: string, table: string) => {
  const st = alterState(db, table);
  return { database: db, table, columns: st.columns.map((c, i) => ({ ...c, ordinal: i + 1 })), indexes: st.indexes, foreignKeys: st.foreignKeys, incomingFks: [], createSql: "", tableCollation: "", fingerprint: `v${st.version}`, rowEstimate: TABLES[table].rows };
};
function alterGuard(sessionId: string) {
  const sess = sessions.find((x) => x.sessionId === sessionId);
  if (profileOf(sess?.profileId)?.readOnly) throw fail("QUERY_FAILED", "Esta conexión es de solo lectura: no se puede modificar la estructura de las tablas", "readonly");
}
const q = (n: string) => `\`${n}\``;
function alterStatements(cs: Any): string[] {
  const out: string[] = [];
  const def = (n: string, c: Any) => `${q(n)} ${c.type} ${c.nullable ? "NULL" : "NOT NULL"}${c.default != null ? ` DEFAULT '${c.default}'` : ""}`;
  for (const o of cs.ops ?? []) {
    if (!o.name?.trim()) throw fail("VALIDATION", "Falta un nombre", "ops[0].name");
    if (o.kind === "addColumn") out.push(`ADD COLUMN ${def(o.name, o.column)}${o.first ? " FIRST" : o.after ? ` AFTER ${q(o.after)}` : ""}`);
    else if (o.kind === "modifyColumn") out.push(`${o.newName ? `CHANGE COLUMN ${q(o.name)} ${def(o.newName, o.column ?? { type: "int", nullable: false })}` : `MODIFY COLUMN ${def(o.name, o.column ?? { type: "int", nullable: false })}`}${o.first ? " FIRST" : o.after ? ` AFTER ${q(o.after)}` : ""}`);
    else if (o.kind === "dropColumn") out.push(`DROP COLUMN ${q(o.name)}`);
    else if (o.kind === "addIndex") out.push(`ADD ${o.index.kind === "unique" ? "UNIQUE " : o.index.kind === "fulltext" ? "FULLTEXT " : ""}INDEX ${q(o.name)} (${o.index.columns.map((c: Any) => q(c.name)).join(", ")})`);
    else if (o.kind === "dropIndex") out.push(`DROP INDEX ${q(o.name)}`);
    else if (o.kind === "renameIndex") out.push(`RENAME INDEX ${q(o.name)} TO ${q(o.newName)}`);
    else if (o.kind === "addFK") out.push(`ADD CONSTRAINT ${q(o.name)} FOREIGN KEY (${o.fk.columns.map(q).join(", ")}) REFERENCES ${q(o.fk.refTable)} (${o.fk.refColumns.map(q).join(", ")})`);
    else if (o.kind === "dropFK") out.push(`DROP FOREIGN KEY ${q(o.name)}`);
  }
  return out.length ? [`ALTER TABLE ${q(cs.database)}.${q(cs.table)}\n  ${out.join(",\n  ")}`] : [];
}
const hashOf = (stmts: string[]) => stmts.join("\u0000").split("").reduce((h, ch) => (h * 31 + ch.charCodeAt(0)) >>> 0, 7).toString(16);
function alterPlan(sessionId: string, cs: Any) {
  alterGuard(sessionId);
  const st = alterState(cs.database, cs.table);
  if (cs.fingerprint && cs.fingerprint !== `v${st.version}`) throw fail("VALIDATION", "La tabla cambió desde que se abrió el editor: vuelve a abrirlo", "stale_model");
  const statements = alterStatements(cs);
  const warnings: Any[] = [];
  for (const o of cs.ops ?? []) {
    if (o.kind === "dropColumn") warnings.push({ code: "drop_column", severity: "danger", message: `Se borrará la columna «${o.name}» con todos sus datos.` });
    if (o.kind === "dropIndex") warnings.push({ code: "drop_index", severity: "danger", message: `Se borrará el índice «${o.name}»: las consultas que lo usan pueden volverse lentas.` });
    if (o.kind === "dropFK") warnings.push({ code: "drop_fk", severity: "danger", message: `Se borrará la clave foránea «${o.name}»: dejará de garantizarse la integridad referencial.` });
  }
  return { statements, sqlHash: hashOf(statements), warnings, destructive: warnings.length > 0, noRollback: statements.length > 0, estimatedRows: null };
}
export const AlterService = {
  GetModel: (sessionId: string, db: string, table: string) => cp(() => (alterGuard(sessionId), alterModel(db, table))),
  Preview: (sessionId: string, cs: Any) => cp(() => alterPlan(sessionId, cs)),
  Apply: (sessionId: string, cs: Any, sqlHash: string) =>
    cp(() => {
      const plan = alterPlan(sessionId, cs);
      if (!plan.statements.length) throw fail("VALIDATION", "No hay cambios que aplicar", "empty");
      if (plan.sqlHash !== sqlHash) throw fail("VALIDATION", "El SQL cambió desde la revisión: vuelve a revisarlo", "stale_preview");
      const st = alterState(cs.database, cs.table);
      if ((cs.ops ?? []).some((o: Any) => o.name === "boom" || o.newName === "boom")) return { applied: [], failed: { index: 0, message: "Duplicate column name 'boom'" }, model: alterModel(cs.database, cs.table) };
      for (const o of cs.ops ?? []) {
        if (o.kind === "addColumn") {
          const c = { name: o.name, dataType: o.column.type.split("(")[0], columnType: o.column.type, nullable: o.column.nullable, default: o.column.default, isPrimaryKey: false, isAutoInc: o.column.autoInc, isGenerated: false, comment: o.column.comment, ordinal: 0, defaultExpr: o.column.defaultExpr, charset: "", collation: "", onUpdate: "", generationExpr: "" };
          const at = o.first ? 0 : o.after ? st.columns.findIndex((x) => x.name === o.after) + 1 : st.columns.length;
          st.columns.splice(at, 0, c);
        } else if (o.kind === "modifyColumn") {
          const i = st.columns.findIndex((x) => x.name === o.name);
          const [c] = st.columns.splice(i, 1);
          if (o.column) Object.assign(c, { columnType: o.column.type, dataType: o.column.type.split("(")[0], nullable: o.column.nullable, default: o.column.default, defaultExpr: o.column.defaultExpr, isAutoInc: o.column.autoInc, comment: o.column.comment });
          if (o.newName) c.name = o.newName;
          const at = o.first ? 0 : o.after ? st.columns.findIndex((x) => x.name === o.after) + 1 : i;
          st.columns.splice(at, 0, c);
        } else if (o.kind === "dropColumn") st.columns = st.columns.filter((x) => x.name !== o.name);
        else if (o.kind === "addIndex") st.indexes.push({ name: o.name, columns: o.index.columns.map((c: Any) => c.name), unique: o.index.kind === "unique", type: o.index.kind === "fulltext" ? "FULLTEXT" : "BTREE", prefix: false, prefixLens: o.index.columns.map(() => 0), desc: o.index.columns.map(() => false), functional: false });
        else if (o.kind === "dropIndex") st.indexes = st.indexes.filter((x) => x.name !== o.name);
        else if (o.kind === "renameIndex") st.indexes.forEach((x) => x.name === o.name && (x.name = o.newName));
        else if (o.kind === "addFK") st.foreignKeys.push({ name: o.name, columns: o.fk.columns, refSchema: cs.database, refTable: o.fk.refTable, refColumns: o.fk.refColumns, onUpdate: o.fk.onUpdate, onDelete: o.fk.onDelete });
        else if (o.kind === "dropFK") st.foreignKeys = st.foreignKeys.filter((x) => x.name !== o.name);
      }
      st.version++;
      Events.Emit("schema:invalidated", { sessionId, database: cs.database });
      return { applied: plan.statements, failed: null, model: alterModel(cs.database, cs.table) };
    }),
};

// ---- Rutinas almacenadas (spec 21) ---------------------------------------------------------------------------------------------
// Imita RoutineService: lista sin cuerpos, lee el CREATE, genera el SQL de la revisión y aplica sobre una copia en memoria.
// Un CREATE que contiene «SYNTAX» falla en la pre-validación (la original no se toca); uno con «BOOM» falla al crear y se
// restaura; una rutina llamada «secreta» no deja ver su cuerpo.
interface MockRoutine { name: string; kind: "procedure" | "function"; comment: string; sql: string; params: Any[]; returns: string; version: number }
const mkRoutine = (kind: "procedure" | "function", name: string, body: string, comment = ""): MockRoutine => ({
  name, kind, comment, version: 1, returns: kind === "function" ? "int" : "",
  params: kind === "procedure" ? [{ name: "p_id", mode: "IN", type: "int", ordinal: 1 }] : [],
  sql: kind === "procedure" ? `CREATE DEFINER=\`root\`@\`localhost\` PROCEDURE \`${name}\`(IN p_id int)\nBEGIN\n  ${body}\nEND` : `CREATE DEFINER=\`root\`@\`localhost\` FUNCTION \`${name}\`() RETURNS int\nDETERMINISTIC\nRETURN ${body}`,
});
const routineStore: Record<string, MockRoutine[]> = {
  sakila: [
    mkRoutine("procedure", "film_in_stock", "SELECT 1;", "Películas disponibles"),
    mkRoutine("procedure", "rewards_report", "SELECT 2;"),
    mkRoutine("procedure", "secreta", "SELECT 3;"),
    mkRoutine("function", "inventory_in_stock", "1"),
    ...Array.from({ length: seed.routines ?? 0 }, (_, i) => (i % 2 ? mkRoutine("function", `fn_${String(i).padStart(3, "0")}`, `${i}`) : mkRoutine("procedure", `sp_${String(i).padStart(3, "0")}`, `SELECT ${i};`))),
  ],
};
const routinesIn = (db: string) => (routineStore[db] ??= []);
const routineSort = (a: MockRoutine, b: MockRoutine) => a.name.localeCompare(b.name) || a.kind.localeCompare(b.kind);
const headOf = (sql: string) => /^\s*CREATE\s+(?:DEFINER\s*=\s*\S+\s+)?(PROCEDURE|FUNCTION)\s+(?:IF NOT EXISTS\s+)?(?:`([^`]+)`|([A-Za-z0-9_$]+))/i.exec(sql);
function routineGuard(sessionId: string) {
  if (profileOf(sessions.find((x) => x.sessionId === sessionId)?.profileId)?.readOnly) throw fail("QUERY_FAILED", "Esta conexión es de solo lectura: no se pueden crear, modificar ni borrar rutinas", "readonly");
}
const routineDetail = (db: string, r: MockRoutine) => ({
  name: r.name, kind: r.kind, definer: "root@localhost", comment: r.comment, securityType: "DEFINER", lastAltered: "2026-01-01 00:00:00", database: db,
  params: r.params, returns: r.returns, createSql: r.name === "secreta" ? "" : r.sql, sqlMode: "", charsetClient: "utf8mb4", collationConnection: "utf8mb4_0900_ai_ci", databaseCollation: "",
  bodyHidden: r.name === "secreta", fingerprint: `fp${r.version}:${r.sql.length}`,
});
function routinePlan(sessionId: string, ch: Any) {
  routineGuard(sessionId);
  const v = (detail: string, msg: string) => fail("VALIDATION", msg, detail);
  const list = routinesIn(ch.database);
  const cur = list.find((r) => r.name === ch.name && r.kind === ch.kind);
  const qn = `\`${ch.database}\`.\`${ch.name}\``;
  const warnings: Any[] = [{ code: "no_rollback", severity: "danger", message: "MySQL confirma el DDL al ejecutarlo: no hay transacción." }];
  let statements: string[];
  if (ch.op === "drop") {
    if (!cur) throw fail("NOT_FOUND", "La rutina ya no existe");
    statements = [`DROP ${ch.kind.toUpperCase()} ${qn}`];
    warnings.push({ code: "grants_lost", severity: "warning", message: "Al borrar una rutina el servidor elimina también sus permisos (GRANT)." });
  } else {
    if (!ch.sql.trim()) throw v("empty", "El código de la rutina está vacío");
    if (/^\s*delimiter\b/im.test(ch.sql)) throw v("delimiter", "Quita las líneas DELIMITER");
    const h = headOf(ch.sql);
    if (!h) throw v("head", "El texto debe empezar por CREATE PROCEDURE o CREATE FUNCTION");
    const name = h[2] ?? h[3];
    if (h[1].toLowerCase() !== ch.kind) throw v("name_changed", "El tipo de la rutina no puede cambiar");
    if (ch.op === "replace") {
      if (!cur) throw fail("NOT_FOUND", "La rutina ya no existe");
      if (name !== ch.name) throw v("name_changed", "El nombre de la rutina no puede cambiar: crea otra y borra esta");
      if (ch.fingerprint && ch.fingerprint !== routineDetail(ch.database, cur).fingerprint) throw v("stale_model", "La rutina cambió desde que se abrió: vuelve a abrirla");
      let sql = ch.sql;
      if (ch.recreateDefiner) sql = sql.replace(/DEFINER\s*=\s*\S+\s+/i, "");
      statements = ["SET SESSION sql_mode = ''", sql.replace(name, "__dbacus_tmp_1"), `DROP ${ch.kind.toUpperCase()} \`${ch.database}\`.\`__dbacus_tmp_1\``, `DROP ${ch.kind.toUpperCase()} ${qn}`, sql];
      warnings.push({ code: "grants_lost", severity: "warning", message: "Al borrar una rutina el servidor elimina también sus permisos (GRANT)." });
      warnings.push({ code: "temp_routine", severity: "info", message: "Antes de tocar la original se crea y se borra una rutina temporal." });
      if (/DEFINER\s*=\s*`?otro/i.test(ch.sql) && !ch.recreateDefiner) warnings.push({ code: "definer_foreign", severity: "warning", message: "La rutina pertenece a otro@h y tú eres root@localhost: recrearla puede fallar." });
    } else {
      if (list.some((r) => r.name === name && r.kind === ch.kind)) throw fail("QUERY_FAILED", `PROCEDURE ${name} already exists`);
      statements = [ch.sql];
    }
  }
  return { statements, sqlHash: hashOf(statements), warnings, destructive: ch.op !== "create", noRollback: true };
}
export const RoutineService = {
  List: (_s: string, db: string) => cp(() => routinesIn(db).slice().sort(routineSort).map((r) => ({ name: r.name, kind: r.kind, definer: "root@localhost", comment: r.comment, securityType: "DEFINER", lastAltered: "" }))),
  Get: (_s: string, db: string, kind: string, name: string) =>
    cp(() => {
      const r = routinesIn(db).find((x) => x.name === name && x.kind === kind);
      if (!r) throw fail("NOT_FOUND", "La rutina ya no existe");
      return routineDetail(db, r);
    }),
  Template: (kind: string) => cp(() => (kind === "procedure" ? "CREATE PROCEDURE nueva_rutina()\nBEGIN\n  SELECT 1;\nEND" : "CREATE FUNCTION nueva_rutina() RETURNS INT\nDETERMINISTIC\nBEGIN\n  RETURN 0;\nEND")),
  CallStatement: (_s: string, db: string, kind: string, name: string) => cp(() => (kind === "function" ? `SELECT \`${db}\`.\`${name}\`()` : `CALL \`${db}\`.\`${name}\`(/* p_id int */ NULL)`)),
  Preview: (sessionId: string, ch: Any) => cp(() => { const p = routinePlan(sessionId, ch); return { statements: p.statements, sqlHash: p.sqlHash, warnings: p.warnings, destructive: p.destructive, noRollback: p.noRollback }; }),
  Apply: (sessionId: string, ch: Any, sqlHash: string) =>
    cp(() => {
      const plan = routinePlan(sessionId, ch);
      if (plan.sqlHash !== sqlHash) throw fail("VALIDATION", "El SQL cambió desde la revisión: vuelve a revisarlo", "stale_preview");
      const list = routinesIn(ch.database);
      const detailOf = (name: string) => { const r = list.find((x) => x.name === name && x.kind === ch.kind); return r ? routineDetail(ch.database, r) : null; };
      const name = ch.op === "create" ? (headOf(ch.sql)![2] ?? headOf(ch.sql)![3]) : ch.name;
      if (ch.op === "replace" && /SYNTAX/.test(ch.sql)) return { applied: [], failed: { index: 1, message: "You have an error in your SQL syntax" }, restored: false, backupSql: "", detail: detailOf(name) };
      if (ch.op === "replace" && /BOOM/.test(ch.sql)) return { applied: [plan.statements[3]], failed: { index: 4, message: "Boom: el CREATE final falló" }, restored: true, backupSql: "", detail: detailOf(name) };
      if (ch.op === "drop") {
        routineStore[ch.database] = list.filter((x) => !(x.name === ch.name && x.kind === ch.kind));
      } else if (ch.op === "create") {
        list.push({ ...mkRoutine(ch.kind, name, "SELECT 1;"), sql: ch.sql });
      } else {
        const r = list.find((x) => x.name === ch.name && x.kind === ch.kind)!;
        r.sql = plan.statements[4];
        r.version++;
      }
      Events.Emit("schema:invalidated", { sessionId, database: ch.database, scope: "routines" });
      return { applied: ch.op === "replace" ? [plan.statements[3], plan.statements[4]] : plan.statements, failed: null, restored: false, backupSql: "", detail: ch.op === "drop" ? null : detailOf(name) };
    }),
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
      const run: Run = { stmts: [], data: {}, canceled: false, sessionId: req.sessionId, pending: {}, newRows: {}, deleted: {}, removed: {}, appended: {}, pages: {} };
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
  // Spec 18: otra página de una sentencia paginada. Como Go, rechaza si hay cambios sin aplicar y sustituye el buffer.
  // Con `column` empieza la paginación aproximada; una columna que no es la primera simula empates masivos en el borde
  // (el avance se bloquea con tie_at_edge, como la regla del spec 18 §5).
  Page: (req: Any) =>
    cp(() => {
      const run = runs.get(req.queryId);
      const pg = run?.pages[req.stmtIndex];
      const d = run?.data[req.stmtIndex];
      if (!run || !pg || !d) throw fail("RESULT_EXPIRED", "El resultado ya no está en memoria");
      if ((run.pending[req.stmtIndex]?.size ?? 0) + (run.newRows[req.stmtIndex]?.length ?? 0) + (run.deleted[req.stmtIndex]?.size ?? 0) > 0) {
        throw fail("VALIDATION", "Hay cambios sin aplicar en esta página", "pending");
      }
      const intCols: string[] = pg.table.cols.filter((c: Any) => /int/.test(c.dataType)).map((c: Any) => c.name);
      let dir = req.direction;
      if (req.column) {
        if (!intCols.includes(req.column)) throw fail("VALIDATION", "La columna no sirve para paginar", "column");
        Object.assign(pg, { col: req.column, offer: false, start: 0, known: true });
        dir = "first";
      } else if (pg.offer) {
        throw fail("VALIDATION", "Elige la columna por la que paginar", "column");
      }
      const unique = !pg.col || pg.col === pg.table.cols[0].name;
      const total = unique ? pg.table.rows : 1000; // por una columna con empates, solo caben las filas con el valor del primer borde
      const last = Math.floor((total - 1) / pg.size) * pg.size;
      const tieNextNow = !unique && pg.start + pg.size < total;
      if (dir === "next" && tieNextNow) throw fail("VALIDATION", "Hay valores repetidos en el borde de la página", "tie_at_edge");
      if (dir === "first") Object.assign(pg, { start: 0, known: true });
      else if (dir === "last") Object.assign(pg, { start: last, known: false });
      else if (dir === "next" && pg.start + pg.size < total) pg.start += pg.size;
      else if (dir === "prev" && pg.start > 0) pg.start -= pg.size;
      const n = Math.min(pg.size, total - pg.start);
      const base = pg.start;
      run.data[req.stmtIndex] = { ...d, rows: n, cell: (i: number, c: number) => pg.table.cell(unique ? base + i : (base + i) * 1000, c) };
      run.removed[req.stmtIndex] = [];
      run.appended[req.stmtIndex] = [];
      const hasNext = base + pg.size < total;
      const tieNext = !unique && hasNext && base === 0;
      const tiePrev = !unique && base > 0 && !pg.known;
      const paging = {
        mode: pg.col ? "approx" : "keyset", reason: "", pageSize: pg.size, hasNext, hasPrev: base > 0, page: pg.known ? base / pg.size + 1 : 0,
        keyColumns: [pg.col || pg.table.cols[0].name], warning: tieNext || tiePrev ? "tie_at_edge" : "", estimated: 0,
        column: pg.col ?? "", candidates: pg.col ? intCols : [], tieNext, tiePrev,
      };
      return { ...run.stmts[req.stmtIndex], index: req.stmtIndex, kind: "rows", columns: asMeta(pg.table.cols), rowCount: n, truncated: false, affectedRows: 0, lastInsertId: 0, warnings: 0, elapsedMs: 3, sql: d.sql ?? "", limitApplied: 0, paging };
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

// ---- consultas guardadas (spec 20) ---------------------------------------------------------------------------------------
// Imita QueryFilesService en memoria: mismas reglas de nombre, de unicidad sin distinguir mayúsculas y de versión.
interface QFile { sql: string; ver: number; mtime: string; profileId: string; profileName: string; database: string }
const qFiles = new Map<string, QFile>();
const qFolders = new Set<string>();
let qVer = 0;
const qRoot: Any = { path: "/Users/demo/Documentos/Dbacus/Consultas", isDefault: true, exists: true, writable: true };
const qVersion = (f: QFile) => `${f.ver}:${f.sql.length}`;
const qNow = () => new Date().toISOString();
const qFile = (sql: string, m: Partial<QFile> = {}): QFile => ({ sql, ver: ++qVer, mtime: qNow(), profileId: "", profileName: "", database: "", ...m });
for (const q of seed.queries ?? []) {
  const path = /\.sql$/i.test(q.path) ? q.path : `${q.path}.sql`;
  qFiles.set(path, qFile(q.sql, { profileId: q.profileId ?? "", profileName: q.profileName ?? "", database: q.database ?? "" }));
  const parts = path.split("/").slice(0, -1);
  parts.forEach((_, i) => qFolders.add(parts.slice(0, i + 1).join("/")));
}
const qExt = (p: string) => (/\.sql$/i.test(p) ? p : `${p}.sql`);
const qBad = (m: string, d: string) => fail("VALIDATION", m, d);
const qCheckName = (seg: string) => {
  if (!seg.trim() || seg.startsWith(".") || /[\\:*?"<>|\x00-\x1f]/.test(seg) || /[. ]$/.test(seg)) throw qBad("Nombre no válido", "name");
  if (seg.length > 120) throw qBad("El nombre es demasiado largo", "name");
  if (/^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\.|$)/i.test(seg)) throw qBad("Ese nombre está reservado por el sistema", "name");
};
const qCheckPath = (rel: string) => {
  if (!rel || rel.startsWith("/") || rel.includes("\\") || rel.split("/").some((x) => x === "" || x === "." || x === "..")) throw qBad("Ruta no válida", "path");
  if (rel.split("/").length > 8) throw qBad("Demasiados niveles de carpetas", "depth");
  rel.split("/").forEach(qCheckName);
};
const qTaken = (path: string, except = "") => {
  const low = path.toLowerCase();
  return [...qFiles.keys(), ...qFolders].some((k) => k !== except && k.toLowerCase() === low);
};
const qName = (p: string) => p.slice(p.lastIndexOf("/") + 1).replace(/\.sql$/i, "");
const qHeaderless = (sql: string) => sql;

export const QueryFilesService = {
  Root: () => cp(() => ({ ...qRoot })),
  List: () =>
    cp(() => {
      const nodes = [
        ...[...qFolders].map((p) => ({ path: p, name: p.slice(p.lastIndexOf("/") + 1), kind: "folder", size: 0, modifiedAt: qNow(), version: "", profileId: "", profileName: "", database: "" })),
        ...[...qFiles].map(([p, f]) => ({ path: p, name: qName(p), kind: "file", size: f.sql.length, modifiedAt: f.mtime, version: qVersion(f), profileId: f.profileId, profileName: f.profileName, database: f.database })),
      ].sort((a, b) => a.path.toLowerCase().localeCompare(b.path.toLowerCase()));
      return { root: { ...qRoot }, nodes, truncated: false };
    }),
  Open: (path: string) =>
    cp(() => {
      const key = qExt(path);
      const f = qFiles.get(key);
      if (!f) throw fail("NOT_FOUND", "El archivo ya no existe");
      return { path: key, name: qName(key), sql: qHeaderless(f.sql), version: qVersion(f), profileId: f.profileId, profileName: f.profileName, database: f.database, profileMissing: !!f.profileId && !profileOf(f.profileId) };
    }),
  Save: (req: Any) =>
    cp(() => {
      let path = qExt(req.path);
      qCheckPath(path);
      // Sobrescribir «X» cuando existe «x» escribe en el archivo existente (como Go).
      if (req.overwrite) path = [...qFiles.keys()].find((k) => k.toLowerCase() === path.toLowerCase()) ?? path;
      const cur = qFiles.get(path);
      if (!req.overwrite) {
        if (cur && !req.expectedVersion) throw qBad("Ya existe una consulta con ese nombre", "exists");
        if (cur && req.expectedVersion !== qVersion(cur)) throw qBad("El archivo cambió en el disco desde que lo abriste", "changed_on_disk");
        if (!cur && req.expectedVersion) throw fail("NOT_FOUND", "El archivo ya no existe");
      }
      if (!cur && qTaken(path)) throw qBad("Ya existe una consulta con ese nombre", "exists");
      let name = "";
      if (req.rememberConnection && req.profileId) {
        const p = profileOf(req.profileId);
        if (!p) throw qBad("La conexión no existe", "profileId");
        name = p.name;
      }
      const f = qFile(req.sql, req.rememberConnection ? { profileId: req.profileId, profileName: name, database: req.database } : {});
      qFiles.set(path, f);
      const parts = path.split("/").slice(0, -1);
      parts.forEach((_, i) => qFolders.add(parts.slice(0, i + 1).join("/")));
      return { path, name: qName(path), version: qVersion(f) };
    }),
  CreateFolder: (path: string) =>
    cp(() => {
      qCheckPath(path);
      if (qTaken(path)) throw qBad("Ya existe algo con ese nombre", "exists");
      path.split("/").forEach((_, i, a) => qFolders.add(a.slice(0, i + 1).join("/")));
    }),
  Move: (from: string, to: string) =>
    cp(() => {
      const isDir = qFolders.has(from);
      const src = isDir ? from : qExt(from);
      if (!isDir && !qFiles.has(src)) throw fail("NOT_FOUND", "El archivo o la carpeta ya no existe");
      const dst = isDir ? to : qExt(to);
      qCheckPath(dst);
      if (dst === src) return [];
      if (isDir && (dst + "/").startsWith(src + "/")) throw qBad("No se puede mover una carpeta dentro de sí misma", "path");
      if (qTaken(dst, src)) throw qBad("Ya existe algo con ese nombre", "exists");
      const changes: Any[] = [];
      if (isDir) {
        for (const k of [...qFolders]) if (k === src || k.startsWith(`${src}/`)) { qFolders.delete(k); qFolders.add(dst + k.slice(src.length)); }
        for (const [k, f] of [...qFiles]) if (k.startsWith(`${src}/`)) { qFiles.delete(k); qFiles.set(dst + k.slice(src.length), f); changes.push({ from: k, to: dst + k.slice(src.length) }); }
      } else {
        qFiles.set(dst, qFiles.get(src)!);
        qFiles.delete(src);
        changes.push({ from: src, to: dst });
      }
      const parts = dst.split("/").slice(0, -1);
      parts.forEach((_, i) => qFolders.add(parts.slice(0, i + 1).join("/")));
      return changes;
    }),
  Delete: (path: string) =>
    cp(() => {
      if (qFolders.has(path)) {
        for (const k of [...qFolders]) if (k === path || k.startsWith(`${path}/`)) qFolders.delete(k);
        for (const k of [...qFiles.keys()]) if (k.startsWith(`${path}/`)) qFiles.delete(k);
      } else if (!qFiles.delete(qExt(path))) throw fail("NOT_FOUND", "El archivo o la carpeta ya no existe");
    }),
  Search: (req: Any) =>
    cp(() => {
      const fold = (x: string) => x.toLowerCase().normalize("NFD").replace(/[\u0300-\u036f]/g, "");
      const needle = fold(String(req.text).trim());
      const hits: Any[] = [];
      if (!needle) return hits;
      for (const [p, f] of qFiles) {
        const lines = f.sql.split("\n");
        const i = req.content ? lines.findIndex((l) => fold(l).includes(needle)) : -1;
        if (i >= 0) hits.push({ path: p, name: qName(p), line: i + 1, snippet: lines[i].trim() });
        else if (fold(qName(p)).includes(needle)) hits.push({ path: p, name: qName(p), line: 0, snippet: "" });
        if (hits.length >= (req.limit || 50)) break;
      }
      return hits;
    }),
  PickRoot: () => cp(() => { qRoot.path = "/Users/demo/Otra carpeta"; qRoot.isDefault = false; return { ...qRoot }; }),
  ResetRoot: () => cp(() => { qRoot.path = "/Users/demo/Documentos/Dbacus/Consultas"; qRoot.isDefault = true; return { ...qRoot }; }),
  Reveal: (path: string) => cp(() => void ((window as Any).__mock.revealed as string[]).push(path)),
  PurgeTrash: () => cp(() => undefined),
};

export const SettingsService = {
  Get: () => cp(() => ({ ...settings })),
  Set: (s: Any) =>
    cp(() => {
      if (s.maxRows < maxRowsConst) throw fail("VALIDATION", "Ajuste inválido", "maxRows");
      settings = { ...s };
      return { ...settings };
    }),
};


// ---- Monitor DBA (spec 22) ----------------------------------------------------------------------------------------------
// Imita MonitorService: mismos errores y validaciones que Go, procesos que cambian con el tiempo, 5 000 muestras con
// huecos y anotaciones para Series, y un Top con datos de ejemplo. Nada de esto toca red ni base de datos.
const MON_METRICS = [
  "qps", "com_select", "com_insert", "com_update", "com_delete", "slow_queries", "aborted_connects", "aborted_clients",
  "row_lock_waits", "row_lock_time_ms", "bp_reads", "bp_read_requests", "tmp_tables", "tmp_disk_tables", "select_full_join",
  "bytes_received", "bytes_sent", "threads_connected", "threads_running", "proc_total", "proc_running", "proc_sleeping",
  "proc_waiting_lock", "proc_longest_sec", "proc_long_count",
  "bp_hit_ratio", // derivada: la calcula Go (store.MetricBufferPoolHitRatio)
];
const MON_ORDER = ["total_time", "execs", "avg_time", "rows_ratio", "no_index", "tmp_disk", "errors"];
const MON_SYSTEM = new Set(["mysql", "sys", "performance_schema", "information_schema"]);
const monScenarios: Record<string, string> = {};
/** Perfiles cuyo histórico se «borró» (Purge sin fecha): Runs/Series/Top dejan de devolver el de ejemplo. */
const monPurged = new Set<string>();
const monScenario = (profileId: string): string => monScenarios[profileId] ?? (typeof seed.monitor?.caps === "object" ? (seed.monitor.caps[profileId] ?? "ok") : (seed.monitor?.caps ?? "ok"));
const monOK = () => ({ state: "ok", reason: "ok", hint: "" });
const monSession = (id: string) => {
  const sess = sessions.find((x) => x.sessionId === id);
  if (!sess) throw fail("NOT_FOUND", "La sesión no existe");
  return sess;
};
function monCaps(sessionId: string) {
  const sess = monSession(sessionId);
  const sc = monScenario(sess.profileId);
  const caps: Any = { serverVersion: sess.serverVersion, isMariaDB: false, serverId: "mock-server-uuid", processlist: monOK(), digest: monOK(), status: monOK() };
  if (sc === "own_only") caps.processlist = { state: "degraded", reason: "own_only", hint: "GRANT PROCESS ON *.* TO 'app'@'%';" };
  if (sc === "no_privilege") caps.digest = { state: "unavailable", reason: "no_privilege", hint: "GRANT SELECT ON performance_schema.* TO 'app'@'%';" };
  if (sc === "perf_schema_off") caps.digest = { state: "unavailable", reason: "perf_schema_off", hint: "" };
  if (sc === "consumer_off") caps.digest = { state: "unavailable", reason: "consumer_off", hint: "" };
  return caps;
}

let monTick = 0;
const MON_USERS = ["app_rw", "app_ro", "report", "etl", "root"];
const MON_DBS = ["sakila", "pedidos", "catalogo", "staging"];
const MON_SQL = [
  "SELECT o.*, c.nombre FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.status = 'open'",
  "SELECT DATE(created_at), SUM(total) FROM orders GROUP BY 1",
  "UPDATE stock SET qty = qty - 1 WHERE sku = 'A-1029'",
  "SELECT * FROM products WHERE category_id = 7 ORDER BY price",
  "INSERT INTO audit_log (user_id, action) VALUES (42, 'login')",
];
const monKilled = new Set<number>(); // KILL: la conexión desaparece
const monCanceled = new Set<number>(); // KILL QUERY: queda en Sleep
function monProcesses(sessionId: string) {
  const sess = monSession(sessionId);
  const own = monScenario(sess.profileId) === "own_only";
  const count = seed.monitor?.processes ?? 8;
  let rows: Any[] = [];
  for (let i = 0; i < count; i++) {
    const sleeping = i % 3 === 1;
    const timeSec = sleeping ? (i * 37 + monTick * 2) % 600 : (i * 11 + monTick) % 90;
    const info = sleeping ? "" : MON_SQL[i % MON_SQL.length] + (i === 0 ? " /* " + "x".repeat(2100) + " */" : "");
    rows.push({
      id: 9000 + i, user: MON_USERS[i % MON_USERS.length], host: `10.0.0.${(i % 200) + 1}:5${String(i).padStart(4, "0")}`, db: MON_DBS[i % MON_DBS.length],
      command: sleeping ? "Sleep" : "Query", timeSec, state: sleeping ? "" : i % 5 === 0 ? "Waiting for table metadata lock" : "executing",
      info: info.length > 2048 ? info.slice(0, 2048) : info, infoTruncated: info.length > 2048,
    });
  }
  rows = rows.filter((r) => !monKilled.has(r.id)).map((r) => (monCanceled.has(r.id) ? { ...r, command: "Sleep", state: "", info: "", infoTruncated: false } : r));
  if (own) rows = rows.filter((r) => r.user === "app_rw");
  rows.sort((a, b) => b.timeSec - a.timeSec || a.id - b.id);
  const by = (key: string) => rows.reduce((m: Any, r) => ((m[r[key]] = (m[r[key]] ?? 0) + 1), m), {});
  return {
    at: new Date().toISOString(), rows, maxConnections: 151,
    summary: { total: rows.length, running: rows.filter((r) => r.command !== "Sleep").length, sleeping: rows.filter((r) => r.command === "Sleep").length, waitingLock: rows.filter((r) => /Waiting for/.test(r.state)).length, longestSec: Math.max(0, ...rows.filter((r) => r.command !== "Sleep").map((r) => r.timeSec)), byCommand: by("command"), byUser: by("user") },
  };
}

const MON_LEASE_MS = 30_000;
const monWatchers = new Map<string, { timer: ReturnType<typeof setInterval>; until: number; ms: number }>();
const monStopWatch = (sessionId: string) => {
  const w = monWatchers.get(sessionId);
  if (w) clearInterval(w.timer);
  monWatchers.delete(sessionId);
};
const monRuns: Any[] = [];
const monTimers = new Map<string, ReturnType<typeof setInterval>>();
let monRunSeq = 0;
let monAnnSeq = 0;
const monAnn: Any[] = [{ id: String(++monAnnSeq), profileId: "*", at: new Date(Date.now() - 20 * 3600_000).toISOString(), text: "deploy v2.14" }];
const monActive = (profileId: string) => monRuns.find((r) => r.profileId === profileId && r.reason === "running");
function monEnd(run: Any, reason: string, source = "") {
  const t = monTimers.get(run.id);
  if (t) clearInterval(t);
  monTimers.delete(run.id);
  run.reason = reason;
  run.endedAt = new Date().toISOString();
  Events.Emit("monitor:state", { runId: run.id, profileId: run.profileId, state: reason, source });
}
function monOnDisconnect(sessionId: string) {
  const sess = sessions.find((x) => x.sessionId === sessionId);
  monStopWatch(sessionId);
  const run = sess && monActive(sess.profileId);
  if (run) monEnd(run, "disconnected");
}

/** Muestras crudas simuladas (una cada 30 s, 5 000 en total, con dos huecos de «app cerrada»). Se generan una vez. */
const MON_STEP_MS = 30_000;
const MON_SAMPLES = 5000;
let monRaw: { t: number; i: number }[] | null = null;
const monRawSamples = () => {
  if (monRaw) return monRaw;
  const end = Math.floor(Date.now() / MON_STEP_MS) * MON_STEP_MS;
  monRaw = [];
  for (let i = 0; i < MON_SAMPLES; i++) {
    if ((i >= 1800 && i < 1900) || (i >= 3500 && i < 3560)) continue;
    monRaw.push({ t: end - (MON_SAMPLES - 1 - i) * MON_STEP_MS, i });
  }
  return monRaw;
};
const monValue = (metric: string, i: number) => {
  if (metric === "bp_hit_ratio") return Math.round((99.2 + 0.7 * Math.sin(i / 120) - (i % 400 < 6 ? 2.5 : 0)) * 100) / 100;
  const k = MON_METRICS.indexOf(metric) + 1;
  const base = 200 + k * 40;
  return Math.max(0, Math.round((base * (1 + 0.5 * Math.sin(i / (90 + k * 7)) + 0.15 * Math.sin(i / 7 + k)) ) * 100) / 100);
};
const monRange = (from: string, to: string) => {
  const a = Date.parse(from), b = Date.parse(to);
  if (!from || !to || Number.isNaN(a) || Number.isNaN(b) || a >= b) throw fail("VALIDATION", "El rango no es válido", "range");
  return [a, b] as const;
};
function monSeries(req: Any) {
  if (!req.profileId) throw fail("VALIDATION", "Falta el perfil", "profileId");
  const [a, b] = monRange(req.from, req.to);
  const metrics: string[] = req.metrics ?? [];
  if (!metrics.length || metrics.some((m) => !MON_METRICS.includes(m))) throw fail("VALIDATION", "Métrica desconocida", "metric");
  const max = req.maxPoints > 0 && req.maxPoints <= 300 ? req.maxPoints : 300;
  const inRange = monPurged.has(req.profileId) ? [] : monRawSamples().filter((s) => s.t >= a && s.t < b);
  const gaps: Any[] = [];
  const edge = (from: number, to: number) => from < to && gaps.push({ from: new Date(from).toISOString(), to: new Date(to).toISOString() });
  if (!inRange.length) edge(a, b);
  else {
    edge(a, inRange[0].t);
    for (let i = 1; i < inRange.length; i++) if (inRange[i].t - inRange[i - 1].t > 2 * MON_STEP_MS) edge(inRange[i - 1].t, inRange[i].t);
    edge(inRange[inRange.length - 1].t, b);
  }
  const anns = monAnn.filter((x) => (x.profileId === req.profileId || x.profileId === "*") && Date.parse(x.at) >= a && Date.parse(x.at) < b).map((x) => ({ ...x, profileId: req.profileId }));
  const level = b - a <= 6 * 3600_000 ? "raw" : "hour";
  return metrics.map((metric) => {
    const per = Math.max(1, Math.ceil(inRange.length / max));
    const points: Any[] = [];
    for (let i = 0; i < inRange.length; i += per) {
      const chunk = inRange.slice(i, i + per);
      const vals = chunk.map((s) => monValue(metric, s.i));
      points.push({ t: new Date(chunk[Math.floor(chunk.length / 2)].t).toISOString(), v: Math.round((vals.reduce((x, y) => x + y, 0) / vals.length) * 100) / 100, min: Math.min(...vals), max: Math.max(...vals) });
    }
    return { metric, level, points, gaps, annotations: anns };
  });
}

const MON_DIGESTS: Any[] = [
  { digest: "d1", schema: "pedidos", text: "SELECT o . * , c . nombre FROM orders o JOIN customers c ON c . id = o . customer_id WHERE o . status = ?", execs: 182_000, totalMs: 11_520_000, rowsExamined: 75_000_000, rowsSent: 182_000, noIndex: 182_000, tmpDisk: 0, errors: 0, trend: 1.38 },
  { digest: "d2", schema: "pedidos", text: "SELECT DATE ( created_at ) , SUM ( total ) FROM orders GROUP BY ?", execs: 1_440, totalMs: 3_900_000, rowsExamined: 9_600_000, rowsSent: 100_000, noIndex: 1_440, tmpDisk: 1_440, errors: 0, trend: 1.02 },
  { digest: "d3", schema: "pedidos", text: "UPDATE stock SET qty = qty - ? WHERE sku = ?", execs: 2_400_000, totalMs: 2_880_000, rowsExamined: 2_400_000, rowsSent: 0, noIndex: 0, tmpDisk: 0, errors: 12, trend: 0.96 },
  { digest: "d4", schema: "catalogo", text: "SELECT * FROM products WHERE category_id = ? ORDER BY price", execs: 640_000, totalMs: 1_860_000, rowsExamined: 5_100_000, rowsSent: 640_000, noIndex: 0, tmpDisk: 0, errors: 0, trend: 0, isNew: true },
  { digest: "d5", schema: "sakila", text: "SELECT COUNT ( * ) FROM rental WHERE customer_id = ?", execs: 90_000, totalMs: 450_000, rowsExamined: 90_000, rowsSent: 90_000, noIndex: 0, tmpDisk: 0, errors: 0, trend: 1.0 },
  { digest: "d6", schema: "staging", text: "INSERT INTO audit_log ( user_id , action ) VALUES (...)", execs: 800_000, totalMs: 400_000, rowsExamined: 0, rowsSent: 0, noIndex: 0, tmpDisk: 0, errors: 3, trend: 1.1 },
  { digest: "d7", schema: "information_schema", text: "SELECT SCHEMA_NAME FROM information_schema . SCHEMATA", execs: 5_000, totalMs: 60_000, rowsExamined: 50_000, rowsSent: 50_000, noIndex: 5_000, tmpDisk: 0, errors: 0, trend: 1.0 },
];
function monTop(req: Any) {
  if (!req.profileId) throw fail("VALIDATION", "Falta el perfil", "profileId");
  const [a, b] = monRange(req.from, req.to);
  const order = req.orderBy || "total_time";
  if (!MON_ORDER.includes(order)) throw fail("VALIDATION", "Criterio de orden desconocido", "orderBy");
  const limit = req.limit || 50;
  if (limit < 1 || limit > 1000) throw fail("VALIDATION", "El límite debe estar entre 1 y 1000", "topK");
  if (monPurged.has(req.profileId)) return [];
  const scale = (b - a) / 86_400_000;
  const stat = (d: Any, k: number): Any => {
    const execs = Math.round(d.execs * k), totalMs = Math.round(d.totalMs * k);
    return { digest: d.digest, schema: d.schema, text: d.text, execs, totalMs, avgMs: execs ? Math.round((totalMs / execs) * 100) / 100 : 0, maxObservedMs: Math.round(totalMs / Math.max(1, execs) * 40), rowsExamined: Math.round(d.rowsExamined * k), rowsSent: Math.round(d.rowsSent * k), noIndex: Math.round(d.noIndex * k), tmpDisk: Math.round(d.tmpDisk * k), errors: Math.round(d.errors * k), prev: null, changePct: 0, isNew: false, regression: false, approx: false };
  };
  const key: Record<string, (s: Any) => number> = {
    total_time: (s) => s.totalMs, execs: (s) => s.execs, avg_time: (s) => s.avgMs, rows_ratio: (s) => (s.rowsExamined ? s.rowsExamined / Math.max(1, s.rowsSent) : 0),
    no_index: (s) => s.noIndex, tmp_disk: (s) => s.tmpDisk, errors: (s) => s.errors,
  };
  let out = MON_DIGESTS.filter((d) => (!req.schema || d.schema === req.schema) && (req.includeSystem || !MON_SYSTEM.has(d.schema))).map((d) => {
    const cur = stat(d, scale);
    if (req.compare) {
      cur.isNew = !!d.isNew;
      if (!d.isNew) {
        cur.prev = stat({ ...d, totalMs: d.totalMs / d.trend }, scale);
        cur.changePct = Math.round((cur.avgMs / (cur.prev.avgMs || 1) - 1) * 1000) / 10;
        cur.regression = cur.changePct >= 25 && cur.execs >= 100 && cur.prev.execs >= 100;
      }
    }
    return cur;
  });
  out = out.filter((s) => order === "total_time" || order === "execs" || order === "avg_time" || key[order](s) > 0);
  out.sort((x, y) => key[order](y) - key[order](x));
  return out.slice(0, limit);
}

export const MonitorService = {
  Capabilities: (sessionId: string) => cp(() => monCaps(sessionId)),
  RefreshCapabilities: (sessionId: string) => cp(() => monCaps(sessionId)),
  Start: (sessionId: string, cfg: Any) =>
    cp(() => {
      const sess = monSession(sessionId);
      const c = { ...cfg };
      if (!c.intervalSec) c.intervalSec = settings.monitorIntervalSec;
      if (!c.topK) c.topK = 30;
      if (!c.processes && !c.status && !c.digest) Object.assign(c, { processes: true, status: true, digest: true });
      if (c.intervalSec < 5 || c.intervalSec > 900) throw fail("VALIDATION", "El intervalo debe estar entre 5 s y 15 min", "interval");
      if (c.topK < 1 || c.topK > 200) throw fail("VALIDATION", "El top debe estar entre 1 y 200", "topK");
      if (monActive(sess.profileId)) throw fail("QUERY_FAILED", "Ya hay un monitoreo en marcha para esta conexión", "busy");
      const caps = monCaps(sessionId);
      const usable = (c.processes && caps.processlist.state !== "unavailable") || (c.status && caps.status.state !== "unavailable") || (c.digest && caps.digest.state !== "unavailable");
      if (!usable) throw fail("UNSUPPORTED", "Ninguna fuente de monitoreo está disponible en esta conexión");
      const run: Any = { id: String(++monRunSeq), profileId: sess.profileId, startedAt: new Date().toISOString(), endedAt: null, reason: "running", intervalSec: c.intervalSec, serverVersion: sess.serverVersion, serverId: "mock-server-uuid", samples: 0, gaps: 0 };
      monRuns.push(run);
      Events.Emit("monitor:state", { runId: run.id, profileId: run.profileId, state: "running" });
      monTimers.set(run.id, setInterval(() => {
        run.samples++;
        Events.Emit("monitor:sample", { runId: run.id, at: new Date().toISOString(), qps: 800 + (monTick % 7) * 40, threadsRunning: 3 + (monTick % 5), threadsConnected: 40 + (monTick % 9), longestSec: 12 + (monTick % 40) });
      }, seed.monitor?.sampleMs ?? c.intervalSec * 1000));
      return { ...run };
    }),
  Stop: (runId: string) =>
    cp(() => {
      const run = monRuns.find((r) => r.id === runId);
      if (!run) throw fail("NOT_FOUND", "La ejecución no existe");
      if (run.reason === "running") monEnd(run, "stopped");
    }),
  Runs: (profileId: string) =>
    cp(() => {
      const real = monRuns.filter((r) => r.profileId === profileId).map((r) => ({ ...r })).reverse();
      if (monPurged.has(profileId)) return real;
      // Historial de ejemplo: una ejecución por bloque de muestras entre los huecos de «app cerrada»; la última viene de otro servidor.
      const raw = monRawSamples();
      const block = (i0: number, i1: number, reason: string, serverId: string, id: string) => {
        const a = raw.find((x) => x.i >= i0)!, b = [...raw].reverse().find((x) => x.i < i1)!;
        return { id, profileId, startedAt: new Date(a.t).toISOString(), endedAt: new Date(b.t).toISOString(), reason, intervalSec: 30, serverVersion: "8.0.36", serverId, samples: raw.filter((x) => x.i >= i0 && x.i < i1).length, gaps: id === "h2" ? 1 : 0 };
      };
      return [...real, block(3560, MON_SAMPLES, "stopped", "mock-server-uuid-2", "h3"), block(1900, 3500, "app_closed", "mock-server-uuid", "h2"), block(0, 1800, "crashed", "mock-server-uuid", "h1")];
    }),
  WatchProcesses: (sessionId: string, intervalMs: number) =>
    cp(() => {
      monSession(sessionId);
      const ms = intervalMs || 2000;
      if (ms < 0 || ms > 60_000) throw fail("VALIDATION", "El intervalo de la vista en vivo debe estar entre 1 y 60 segundos", "interval");
      const cur = monWatchers.get(sessionId);
      if (cur) { cur.until = Date.now() + MON_LEASE_MS; return; }
      if (monCaps(sessionId).processlist.state === "unavailable") throw fail("UNSUPPORTED", "La lista de procesos no está disponible en esta conexión");
      const emit = () => {
        const w = monWatchers.get(sessionId);
        if (!w) return;
        if (Date.now() > w.until || !sessions.some((x) => x.sessionId === sessionId)) return monStopWatch(sessionId);
        monTick++;
        Events.Emit("monitor:processes", { sessionId, snapshot: monProcesses(sessionId) });
      };
      monWatchers.set(sessionId, { timer: setInterval(emit, Math.max(1000, ms)), until: Date.now() + MON_LEASE_MS, ms });
      emit();
    }),
  UnwatchProcesses: (sessionId: string) => cp(() => monStopWatch(sessionId)),
  Top: (req: Any) => cp(() => monTop(req)),
  Digest: (profileId: string, digest: string, from: string, to: string) =>
    cp(() => {
      const d = MON_DIGESTS.find((x) => x.digest === digest);
      if (!d) throw fail("NOT_FOUND", "El digest no existe");
      const [a, b] = monRange(from, to);
      const stat = monTop({ profileId, from, to, orderBy: "total_time", limit: 1000, includeSystem: true }).find((s: Any) => s.digest === digest);
      const n = 24, step = (b - a) / n;
      const points = Array.from({ length: n }, (_, i) => { const execs = Math.round((d.execs / n) * (1 + 0.3 * Math.sin(i / 3))); return { t: new Date(a + step * (i + 0.5)).toISOString(), execs, totalMs: Math.round((d.totalMs / d.execs) * execs), avgMs: Math.round((d.totalMs / d.execs) * 100) / 100 }; });
      return { digest, text: d.text, schemas: [d.schema], stat, points, gaps: [] };
    }),
  Series: (req: Any) => cp(() => monSeries(req)),
  Compare: (profileId: string, a: Any, b: Any) =>
    cp(() => {
      monRange(a.from, a.to); monRange(b.from, b.to);
      const all = monTop({ profileId, from: b.from, to: b.to, orderBy: "total_time", limit: 1000, compare: true });
      const gone = { digest: "gone1", schema: "pedidos", text: "SELECT * FROM legacy_orders WHERE status = ?", execs: 120, totalMs: 9000, avgMs: 75, maxObservedMs: 300, rowsExamined: 12_000, rowsSent: 120, noIndex: 120, tmpDisk: 0, errors: 0, prev: null, changePct: 0, isNew: false, regression: false, approx: false };
      return { regressions: all.filter((s: Any) => s.regression), new: all.filter((s: Any) => s.isNew), gone: [gone] };
    }),
  Annotate: (profileId: string, at: string, text: string) =>
    cp(() => {
      if (!profileId) throw fail("VALIDATION", "Falta el perfil", "profileId");
      if (!text || [...text].length > 200 || /[\u0000-\u001f\u007f]/.test(text)) throw fail("VALIDATION", "La nota debe tener entre 1 y 200 caracteres, sin caracteres de control", "text");
      const a = { id: String(++monAnnSeq), profileId, at: at || new Date().toISOString(), text };
      monAnn.push(a);
      return { ...a };
    }),
  DeleteAnnotation: (id: string) =>
    cp(() => {
      if (!/^[1-9]\d*$/.test(id)) throw fail("VALIDATION", "Identificador de nota no válido", "id");
      const i = monAnn.findIndex((x) => x.id === id);
      if (i < 0) throw fail("NOT_FOUND", "La nota no existe");
      monAnn.splice(i, 1);
    }),
  Purge: (profileId: string, before: string | null) =>
    cp(() => {
      if (!profileId) throw fail("VALIDATION", "Falta el perfil", "profileId");
      if (!before && monActive(profileId)) throw fail("QUERY_FAILED", "Detén el monitoreo antes de borrar sus datos", "busy");
      const cut = before ? Date.parse(before) : Infinity;
      for (let i = monRuns.length - 1; i >= 0; i--) if (monRuns[i].profileId === profileId && monRuns[i].reason !== "running" && Date.parse(monRuns[i].startedAt) < cut) monRuns.splice(i, 1);
      if (!before) {
        for (let i = monAnn.length - 1; i >= 0; i--) if (monAnn[i].profileId === profileId || monAnn[i].profileId === "*") monAnn.splice(i, 1);
        monPurged.add(profileId);
      }
    }),
  KillProcess: (sessionId: string, processId: number, user: string, host: string, queryOnly: boolean) =>
    cp(() => {
      const sess = monSession(sessionId);
      if (!(processId > 0)) throw fail("VALIDATION", "Identificador de proceso no válido", "processId");
      if (!user) throw fail("VALIDATION", "Falta el usuario del proceso", "user");
      if (!host) throw fail("VALIDATION", "Falta el host del proceso", "host");
      if (profileOf(sess.profileId)?.readOnly) throw fail("QUERY_FAILED", "Esta conexión es de solo lectura: no se pueden terminar procesos", "readonly");
      const row = monProcesses(sessionId).rows.find((r: Any) => r.id === processId && r.user === user && r.host === host);
      if (!row) throw fail("NOT_FOUND", "El proceso ya terminó o ahora es otro", "process_gone");
      (queryOnly ? monCanceled : monKilled).add(processId);
    }),
  CopyKill: (sessionId: string, processId: number, queryOnly: boolean) =>
    cp(() => {
      monSession(sessionId);
      if (!(processId > 0)) throw fail("VALIDATION", "Identificador de proceso no válido", "processId");
      return queryOnly ? `KILL QUERY ${processId};` : `KILL ${processId};`;
    }),
};

export const PingService = { Hello: (name: string) => cp(() => `Hola, ${name}`), PickFile: () => cp(() => "") };

/** Para que las pruebas lean el estado interno si lo necesitan. */
(window as Any).__mock = {
  profiles, sessions, history, exports: [] as Any[], revealed: [] as string[],
  /** Simula una edición hecha fuera de la app (otro editor, git…) o un borrado externo. */
  queryFiles: {
    files: qFiles,
    touch(path: string, sql: string) { const f = qFiles.get(path); if (f) { f.sql = sql; f.ver = ++qVer; f.mtime = qNow(); } else qFiles.set(path, qFile(sql)); },
    remove(path: string) { qFiles.delete(path); },
  }, forceConflict: "" as string, get settings() { return settings; },
  /** Monitor (spec 22): cambiar el escenario de permisos de un perfil y leer el estado interno. */
  monitor: { scenarios: monScenarios, runs: monRuns, annotations: monAnn, watchers: monWatchers } };
