// Único punto que importa RoutineService (bindings generados). Ver docs/specs/21-rutinas-almacenadas.md.
import { RoutineService } from "../../bindings/github.com/vitacodesas/dbacus/internal/services";
import type { Routine, RoutineChange, RoutineDetail, RoutinePreview, RoutineResult } from "../types";
import { wrap } from "./errors";

/** Lista sin cuerpos (una base puede tener cientos). */
export const listRoutines = async (sessionId: string, db: string): Promise<Routine[]> => (await wrap(RoutineService.List(sessionId, db))) ?? [];
/** El CREATE completo, leído sin caché. Funciona en conexiones de solo lectura. */
export const getRoutine = (sessionId: string, db: string, kind: string, name: string): Promise<RoutineDetail> => wrap(RoutineService.Get(sessionId, db, kind, name));
/** Esqueleto de una rutina nueva: el front nunca arma SQL. */
export const routineTemplate = (kind: string): Promise<string> => wrap(RoutineService.Template(kind));
export const routineCallStatement = (sessionId: string, db: string, kind: string, name: string): Promise<string> => wrap(RoutineService.CallStatement(sessionId, db, kind, name));
/** Go genera las sentencias y no ejecuta nada. */
export const previewRoutine = (sessionId: string, ch: RoutineChange): Promise<RoutinePreview> => wrap(RoutineService.Preview(sessionId, ch));
/** Ejecuta el SQL de la última vista previa; `sqlHash` garantiza que lo ejecutado es lo que se vio. */
export const applyRoutine = (sessionId: string, ch: RoutineChange, sqlHash: string): Promise<RoutineResult> => wrap(RoutineService.Apply(sessionId, ch, sqlHash));
