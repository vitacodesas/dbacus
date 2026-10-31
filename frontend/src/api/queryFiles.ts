// Único punto que importa QueryFilesService (bindings generados). Ver docs/specs/20-consultas-guardadas.md.
// Las rutas son relativas a la carpeta de consultas; el front nunca envía rutas absolutas.
import { QueryFilesService } from "../../bindings/github.com/vitacodesas/dbacus/internal/services";
import type { PathChange, QueryFile, QueryHit, QueryRef, QueryRoot, QuerySearchRequest, QueryTree, SaveQueryRequest } from "../types";
import { wrap } from "./errors";

export const getRoot = (): Promise<QueryRoot> => wrap(QueryFilesService.Root());
export const listQueries = async (): Promise<QueryTree> => {
  const tree = await wrap(QueryFilesService.List());
  return { ...tree, nodes: tree.nodes ?? [] };
};
export const openQuery = (path: string): Promise<QueryFile> => wrap(QueryFilesService.Open(path));
export const saveQuery = (req: SaveQueryRequest): Promise<QueryRef> => wrap(QueryFilesService.Save(req));
export const createFolder = (path: string): Promise<void> => wrap(QueryFilesService.CreateFolder(path));
export const moveQuery = async (from: string, to: string): Promise<PathChange[]> => (await wrap(QueryFilesService.Move(from, to))) ?? [];
export const deleteQuery = (path: string): Promise<void> => wrap(QueryFilesService.Delete(path));
export const searchQueries = async (req: QuerySearchRequest): Promise<QueryHit[]> => (await wrap(QueryFilesService.Search(req))) ?? [];
/** Abre el diálogo nativo de carpeta; `null` si se cancela. No mueve archivos. */
export const pickRoot = (): Promise<QueryRoot | null> => wrap(QueryFilesService.PickRoot());
export const resetRoot = (): Promise<QueryRoot> => wrap(QueryFilesService.ResetRoot());
/** Muestra una carpeta o archivo en el explorador del SO (`""` = la raíz). */
export const reveal = (path: string): Promise<void> => wrap(QueryFilesService.Reveal(path));
