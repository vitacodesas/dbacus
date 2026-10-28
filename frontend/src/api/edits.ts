// Único punto que importa EditService (bindings generados). Ver docs/specs/13-edicion-de-celdas.md.
import { EditService } from "../../bindings/tabulariswails/internal/services";
import type { EditApplyResult, EditInfo, EditPreview, EditValue } from "../types";
import { wrap } from "./errors";

export const getEditInfo = (queryId: string, stmt: number): Promise<EditInfo> => wrap(EditService.GetEditInfo(queryId, stmt));
export const stageEdit = (queryId: string, stmt: number, row: number, col: number, v: EditValue): Promise<EditInfo> =>
  wrap(EditService.Stage(queryId, stmt, row, col, v));
export const unstageEdit = (queryId: string, stmt: number, row: number, col: number): Promise<EditInfo> =>
  wrap(EditService.Unstage(queryId, stmt, row, col));
export const discardEdits = (queryId: string, stmt: number): Promise<EditInfo> => wrap(EditService.Discard(queryId, stmt));
export const previewEdits = async (queryId: string, stmt: number): Promise<EditPreview[]> => (await wrap(EditService.Preview(queryId, stmt))) ?? [];
export const applyEdits = (queryId: string, stmt: number): Promise<EditApplyResult> => wrap(EditService.Apply(queryId, stmt));
/** Da formato (sangría) a un JSON en Go; error de validación con la posición si no es válido. */
export const formatJson = (text: string): Promise<string> => wrap(EditService.FormatJSON(text));
