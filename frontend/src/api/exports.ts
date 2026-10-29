// Único punto que importa ExportService (bindings generados). Ver docs/specs/07-historial-y-exportacion.md.
import { ExportService } from "../../bindings/github.com/vitacodesas/dbacus/internal/services";
import type { ExportRequest, ExportStart } from "../types";
import { wrap } from "./errors";

export const startExport = (req: ExportRequest): Promise<ExportStart> => wrap(ExportService.Export(req));
export const cancelExport = (jobId: string): Promise<void> => wrap(ExportService.CancelExport(jobId));
