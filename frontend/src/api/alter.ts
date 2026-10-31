// Único punto que importa AlterService (bindings generados). Ver docs/specs/19-editar-estructura-de-tabla.md.
import { AlterService } from "../../bindings/github.com/vitacodesas/dbacus/internal/services";
import type { AlterModel, AlterPreview, AlterResult, ChangeSet } from "../types";
import { wrap } from "./errors";

export const getAlterModel = (sessionId: string, db: string, table: string): Promise<AlterModel> => wrap(AlterService.GetModel(sessionId, db, table));
/** Go genera el SQL (el front nunca lo arma) y no ejecuta nada. */
export const previewAlter = (sessionId: string, cs: ChangeSet): Promise<AlterPreview> => wrap(AlterService.Preview(sessionId, cs));
/** Ejecuta el SQL de la última vista previa; `sqlHash` garantiza que lo ejecutado es lo que se vio. */
export const applyAlter = (sessionId: string, cs: ChangeSet, sqlHash: string): Promise<AlterResult> => wrap(AlterService.Apply(sessionId, cs, sqlHash));
