import { computed, ref } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/schema";
import { asAppError } from "../api/errors";
import { useUiStore } from "./ui";
import { omit, omitWhere } from "../utils/record";
import type { AppError, CompletionCatalog, Database, SchemaInvalidated, Table, TableDetail } from "../types";

const k = (...parts: string[]) => parts.join("\u0001"); // separador que no puede aparecer en identificadores razonables

/**
 * Proyección en cliente del esquema. La verdad (y la caché con TTL) vive en Go; este store solo guarda lo ya pedido
 * para pintar el árbol y se vacía cuando Go avisa con `schema:invalidated`.
 */
export const useSchemaStore = defineStore("schema", () => {
  const databases = ref<Record<string, Database[]>>({});
  const tables = ref<Record<string, Table[]>>({});
  const details = ref<Record<string, TableDetail>>({});
  const catalogs = ref<Record<string, CompletionCatalog>>({});
  /** Nodos expandidos: `${sessionId}\u0001${nodeId}`. */
  const expanded = ref<Record<string, true>>({});
  const loading = ref<Record<string, true>>({});
  const errors = ref<Record<string, AppError>>({});
  const filter = ref("");
  // Es un ajuste de usuario (SettingsService): vive en el store ui para persistirse junto al resto.
  const showSystem = computed({ get: () => useUiStore().showSystemDatabases, set: (v: boolean) => (useUiStore().showSystemDatabases = v) });

  const keyDbs = (s: string) => k(s, "dbs");
  const keyTables = (s: string, db: string) => k(s, "tables", db);
  const keyDetail = (s: string, db: string, t: string) => k(s, "detail", db, t);
  const keyCatalog = (s: string, db: string) => k(s, "catalog", db);

  const isExpanded = (s: string, nodeId: string) => !!expanded.value[k(s, nodeId)];
  const isLoading = (key: string) => !!loading.value[key];

  /** Ejecuta una carga marcando loading/error; no repite si ya está en curso. */
  async function run<T>(key: string, action: () => Promise<T>): Promise<T | undefined> {
    if (loading.value[key]) return undefined;
    loading.value = { ...loading.value, [key]: true };
    errors.value = omit(errors.value, key);
    try {
      return await action();
    } catch (e) {
      errors.value = { ...errors.value, [key]: asAppError(e) };
      return undefined;
    } finally {
      loading.value = omit(loading.value, key);
    }
  }

  async function loadDatabases(sessionId: string) {
    if (databases.value[sessionId]) return;
    const r = await run(keyDbs(sessionId), () => api.listDatabases(sessionId));
    if (r) databases.value = { ...databases.value, [sessionId]: r };
  }

  async function loadTables(sessionId: string, db: string) {
    const key = keyTables(sessionId, db);
    if (tables.value[key]) return;
    const r = await run(key, () => api.listTables(sessionId, db));
    if (r) tables.value = { ...tables.value, [key]: r };
  }

  async function loadDetail(sessionId: string, db: string, table: string) {
    const key = keyDetail(sessionId, db, table);
    if (details.value[key]) return;
    const r = await run(key, () => api.describeTable(sessionId, db, table));
    if (r) details.value = { ...details.value, [key]: r };
  }

  /** Catálogo para el autocompletado del editor (T4.4); se cachea hasta que cambie el esquema. */
  async function loadCatalog(sessionId: string, db: string): Promise<CompletionCatalog | undefined> {
    const key = keyCatalog(sessionId, db);
    if (catalogs.value[key]) return catalogs.value[key];
    const r = await run(key, () => api.getCompletionCatalog(sessionId, db));
    if (r) catalogs.value = { ...catalogs.value, [key]: r };
    return r;
  }

  const databasesOf = (s: string) => databases.value[s];
  const tablesOf = (s: string, db: string) => tables.value[keyTables(s, db)];
  const detailOf = (s: string, db: string, t: string) => details.value[keyDetail(s, db, t)];
  const errorOf = (key: string) => errors.value[key];

  /** Expande o pliega un nodo; al expandir pide lo que le falte. `load` lo decide el árbol según el tipo de nodo. */
  async function toggle(sessionId: string, nodeId: string, load?: () => Promise<void>) {
    const key = k(sessionId, nodeId);
    if (expanded.value[key]) {
      expanded.value = omit(expanded.value, key);
      return;
    }
    expanded.value = { ...expanded.value, [key]: true };
    await load?.();
  }

  function setExpanded(sessionId: string, nodeId: string, open: boolean) {
    const key = k(sessionId, nodeId);
    expanded.value = open ? { ...expanded.value, [key]: true } : omit(expanded.value, key);
  }

  /** Borra lo cacheado de una sesión (y de una base si se indica) y recarga lo que está a la vista. */
  async function handleInvalidated(ev: SchemaInvalidated) {
    const s = ev.sessionId;
    const dropByDb = <T>(rec: Record<string, T>, kind: string): Record<string, T> =>
      omitWhere(rec, (key) => {
        const [sid, kd, db] = key.split("\u0001");
        return sid === s && kd === kind && (!ev.database || db === ev.database);
      });

    if (!ev.database) databases.value = omit(databases.value, s);
    tables.value = dropByDb(tables.value, "tables");
    details.value = dropByDb(details.value, "detail");
    catalogs.value = dropByDb(catalogs.value, "catalog");

    // Recarga lo visible: bases expandidas → tablas; tablas expandidas → detalle.
    await loadDatabases(s);
    const reloads: Promise<void>[] = [];
    for (const key of Object.keys(expanded.value)) {
      const [sid, nodeId] = key.split("\u0001");
      if (sid !== s) continue;
      const m = /^db:(.*?)(?:\/t:(.*))?$/.exec(nodeId);
      if (!m || (ev.database && m[1] !== ev.database)) continue;
      if (m[2] === undefined) reloads.push(loadTables(s, m[1]));
      else reloads.push(loadDetail(s, m[1], m[2]));
    }
    await Promise.all(reloads);
  }

  /** Olvida todo lo de una sesión que ya no existe. */
  function forgetSession(sessionId: string) {
    const mine = (key: string) => key.split("\u0001")[0] === sessionId;
    databases.value = omit(databases.value, sessionId);
    tables.value = omitWhere(tables.value, mine);
    details.value = omitWhere(details.value, mine);
    catalogs.value = omitWhere(catalogs.value, mine);
    expanded.value = omitWhere(expanded.value, mine);
    errors.value = omitWhere(errors.value, mine);
  }

  return {
    databases, tables, details, catalogs, expanded, loading, errors, filter, showSystem,
    keyDbs, keyTables, keyDetail, keyCatalog, isExpanded, isLoading,
    loadDatabases, loadTables, loadDetail, loadCatalog, databasesOf, tablesOf, detailOf, errorOf,
    toggle, setExpanded, handleInvalidated, forgetSession,
  };
});
