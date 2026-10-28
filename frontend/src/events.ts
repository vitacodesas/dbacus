import { onEditApplied, onExportDone, onExportProgress, onHistoryAdded, onQueryDone, onQueryError, onQueryProgress, onQueryStarted, onQueryStatement, onSchemaInvalidated, onSessionClosed } from "./api/events";
import { useEditsStore } from "./stores/edits";
import { useExportsStore } from "./stores/exports";
import { useHistoryStore } from "./stores/history";
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
  ];
  return () => offs.forEach((off) => off());
}
