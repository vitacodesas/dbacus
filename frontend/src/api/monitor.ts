// Único punto que importa MonitorService (bindings generados). Ver docs/specs/22-monitoreo-dba.md.
import { MonitorService } from "../../bindings/github.com/vitacodesas/dbacus/internal/services";
import type { Annotation, Comparison, DigestDetail, DigestStat, MonitorCaps, MonitorConfig, MonitorRun, Series, SeriesRequest, TimeRange, TopRequest } from "../types";
import { wrap } from "./errors";

/** Qué fuentes puede leer el usuario de la conexión; la falta de `performance_schema` o de privilegios es estado, no error. */
export const monitorCapabilities = (sessionId: string): Promise<MonitorCaps> => wrap(MonitorService.Capabilities(sessionId));
export const refreshMonitorCapabilities = (sessionId: string): Promise<MonitorCaps> => wrap(MonitorService.RefreshCapabilities(sessionId));
export const startMonitor = (sessionId: string, cfg: MonitorConfig): Promise<MonitorRun> => wrap(MonitorService.Start(sessionId, cfg));
export const stopMonitor = (runId: string): Promise<void> => wrap(MonitorService.Stop(runId));
export const monitorRuns = async (profileId: string): Promise<MonitorRun[]> => (await wrap(MonitorService.Runs(profileId))) ?? [];
/** Arrendamiento de 30 s: llamar de nuevo lo renueva; si se deja de renovar, Go lo detiene. */
export const watchProcesses = (sessionId: string, intervalMs: number): Promise<void> => wrap(MonitorService.WatchProcesses(sessionId, intervalMs));
export const unwatchProcesses = (sessionId: string): Promise<void> => wrap(MonitorService.UnwatchProcesses(sessionId));
export const monitorTop = async (req: TopRequest): Promise<DigestStat[]> => (await wrap(MonitorService.Top(req))) ?? [];
export const monitorDigest = (profileId: string, digest: string, from: string, to: string): Promise<DigestDetail> => wrap(MonitorService.Digest(profileId, digest, from, to));
export const monitorSeries = async (req: SeriesRequest): Promise<Series[]> => (await wrap(MonitorService.Series(req))) ?? [];
export const monitorCompare = (profileId: string, a: TimeRange, b: TimeRange): Promise<Comparison> => wrap(MonitorService.Compare(profileId, a, b));
export const annotateMonitor = (profileId: string, at: string, text: string): Promise<Annotation> => wrap(MonitorService.Annotate(profileId, at, text));
export const deleteMonitorAnnotation = (id: string): Promise<void> => wrap(MonitorService.DeleteAnnotation(id));
export const purgeMonitor = (profileId: string, before: string | null): Promise<void> => wrap(MonitorService.Purge(profileId, before));
/** Termina un proceso (spec 22 ⚑D8). Go rechaza solo lectura y comprueba que id+usuario+host sigan siendo los mismos. */
export const killProcess = (sessionId: string, p: { id: number; user: string; host: string }, queryOnly: boolean): Promise<void> => wrap(MonitorService.KillProcess(sessionId, p.id, p.user, p.host, queryOnly));
/** Go arma el texto (`KILL n;` o `KILL QUERY n;`); no ejecuta nada. */
export const monitorCopyKill = (sessionId: string, processId: number, queryOnly: boolean): Promise<string> => wrap(MonitorService.CopyKill(sessionId, processId, queryOnly));
