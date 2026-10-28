// Único punto que importa SchemaService (bindings generados). Ver docs/specs/02-contrato-go-vue.md.
import { SchemaService } from "../../bindings/tabulariswails/internal/services";
import type { CompletionCatalog, Database, Table, TableDetail } from "../types";
import { wrap } from "./errors";

export const listDatabases = async (sessionId: string): Promise<Database[]> => (await wrap(SchemaService.ListDatabases(sessionId))) ?? [];
export const listTables = async (sessionId: string, db: string): Promise<Table[]> => (await wrap(SchemaService.ListTables(sessionId, db))) ?? [];
export const describeTable = (sessionId: string, db: string, table: string): Promise<TableDetail> => wrap(SchemaService.DescribeTable(sessionId, db, table));
export const getCompletionCatalog = (sessionId: string, db: string): Promise<CompletionCatalog> => wrap(SchemaService.GetCompletionCatalog(sessionId, db));
export const refreshSchema = (sessionId: string, db = ""): Promise<void> => wrap(SchemaService.Refresh(sessionId, db));
/** SELECT * con los identificadores escapados por el driver: el front nunca arma SQL. */
export const selectStatement = (sessionId: string, db: string, table: string, limit = 100): Promise<string> =>
  wrap(SchemaService.SelectStatement(sessionId, db, table, limit));
