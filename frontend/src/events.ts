import { onEditApplied, onExportDone, onExportProgress, onHistoryAdded, onMigrateDone, onMigrateProgress, onMigrateState, onMonitorProcesses, onMonitorSample, onMonitorState, onQueryDone, onQueryError, onQueryProgress, onQueryStarted, onQueryStatement, onSchemaInvalidated, onSessionClosed } from "./api/events";
import { useEditsStore } from "./stores/edits";
import { useExportsStore } from "./stores/exports";
import { useHistoryStore } from "./stores/history";
import { useMigrationStore } from "./stores/migration";
import { useMonitorStore } from "./stores/monitor";
import { useResultsStore } from "./stores/results";
import { useSchemaStore } from "./stores/schema";
import { useSessionsStore } from "./stores/sessions";

/** Único punto de suscripción a eventos de Go: despacha a los stores (spec 09). Devuelve la función de baja. */
export function startEventDispatch(): () => void {
  const sessions = useSessionsStore();
  const schema = useSchemaStore();
  const results = useResultsStore();
  const history = useHistoryStore();
  const exports = useExportsStore();
  const edits = useEditsStore();
  const monitor = useMonitorStore();
  const migration = useMigrationStore();
  const offs = [onSessionClosed((ev) => sessions.handleClosed(ev)), onSchemaInvalidated((ev) => schema.handleInvalidated(ev)),
    onQueryStarted((ev) => results.handleStarted(ev)),
    onQueryStatement((ev) => results.handleStatement(ev)),
    onQueryProgress((ev) => results.handleProgress(ev)),
    onQueryDone((ev) => results.handleDone(ev)),
    onQueryError((ev) => results.handleError(ev)),
    onHistoryAdded((ev) => history.handleAdded(ev)),
    onEditApplied((ev) => edits.handleApplied(ev)),
    onExportProgress((ev) => exports.handleProgress(ev)),
    onExportDone((ev) => exports.handleDone(ev)),
    onMonitorSample((ev) => monitor.handleSample(ev)),
    onMonitorState((ev) => monitor.handleState(ev)),
    onMonitorProcesses((ev) => monitor.handleProcesses(ev)),
    onMigrateProgress((ev) => migration.handleProgress(ev)),
    onMigrateState((ev) => migration.handleState(ev)),
    onMigrateDone((ev) => migration.handleDone(ev)),
  ];
  return () => offs.forEach((off) => off());
}
