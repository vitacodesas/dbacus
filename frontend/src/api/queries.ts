// Único punto que importa QueryService (bindings generados). Ver docs/specs/02-contrato-go-vue.md.
import { QueryService } from "../../bindings/tabulariswails/internal/services";
import type { ExecuteRequest, FullCell, RowWindow, StatementRange, TabState } from "../types";
import { wrap } from "./errors";

export const execute = async (req: ExecuteRequest): Promise<string> => (await wrap(QueryService.Execute(req))).queryId;
export const cancel = (queryId: string): Promise<void> => wrap(QueryService.Cancel(queryId));
export const getRows = (queryId: string, stmtIndex: number, offset: number, limit: number): Promise<RowWindow> =>
  wrap(QueryService.GetRows(queryId, stmtIndex, offset, limit));
export const getCell = (queryId: string, stmtIndex: number, row: number, col: number): Promise<FullCell> =>
  wrap(QueryService.GetCell(queryId, stmtIndex, row, col));
export const rowsAsInsert = (queryId: string, stmtIndex: number, offset: number, limit: number, table: string): Promise<string> =>
  wrap(QueryService.RowsAsInsert(queryId, stmtIndex, offset, limit, table));
export const releaseResult = (queryId: string): Promise<void> => wrap(QueryService.ReleaseResult(queryId));
export const closeTab = (tabId: string): Promise<void> => wrap(QueryService.CloseTab(tabId));
export const statementAt = (sql: string, offset: number): Promise<StatementRange> => wrap(QueryService.StatementAt(sql, offset));
export const formatSql = (sql: string): Promise<string> => wrap(QueryService.FormatSQL(sql));
export const saveTabs = (tabs: TabState[]): Promise<void> => wrap(QueryService.SaveTabs(tabs));
export const loadTabs = async (): Promise<TabState[]> => (await wrap(QueryService.LoadTabs())) ?? [];
