// Único punto que importa HistoryService (bindings generados). Ver docs/specs/07-historial-y-exportacion.md.
import { HistoryService } from "../../bindings/tabulariswails/internal/services";
import type { HistoryEntry, HistoryQuery } from "../types";
import { wrap } from "./errors";

/** Go devuelve null para una lista vacía: aquí siempre es un arreglo. */
export interface HistoryList {
  entries: HistoryEntry[];
  nextCursor: string;
}

export const listHistory = async (q: HistoryQuery): Promise<HistoryList> => {
  const page = await wrap(HistoryService.List(q));
  return { entries: page.entries ?? [], nextCursor: page.nextCursor };
};
export const deleteHistory = (id: string): Promise<void> => wrap(HistoryService.Delete(id));
export const clearHistory = (profileId: string): Promise<void> => wrap(HistoryService.Clear(profileId));
