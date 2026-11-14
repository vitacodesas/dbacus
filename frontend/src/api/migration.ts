// Único punto que importa MigrationService (bindings generados). Ver docs/specs/23-migracion-de-schema.md.
import { MigrationService } from "../../bindings/github.com/vitacodesas/dbacus/internal/services";
import type { MigrationJob, MigrationPlan, MigrationRequest, MigrationResult, MigrationVerification, VerifyOptions } from "../types";
import { wrap } from "./errors";

/** Inspecciona origen y destino sin crear nada. */
export const planMigration = (req: MigrationRequest): Promise<MigrationPlan> => wrap(MigrationService.Plan(req));
/** Go repite el plan y exige el mismo hash: si el origen cambió desde que se vio, `stale_plan`. */
export const startMigration = (req: MigrationRequest, planHash: string): Promise<MigrationJob> => wrap(MigrationService.Start(req, planHash));
export const cancelMigration = (jobId: string): Promise<void> => wrap(MigrationService.Cancel(jobId));
export const migrationJob = (jobId: string): Promise<MigrationResult> => wrap(MigrationService.Job(jobId));
/** Repite solo lo que falló. */
export const retryMigration = (jobId: string): Promise<MigrationJob> => wrap(MigrationService.Retry(jobId));
export const verifyMigration = (jobId: string, opts: VerifyOptions): Promise<MigrationVerification> => wrap(MigrationService.Verify(jobId, opts));
/** Borra solo lo que creó la migración; hay que escribir el nombre del schema destino. */
export const cleanupMigration = (jobId: string, confirmName: string): Promise<void> => wrap(MigrationService.Cleanup(jobId, confirmName));
