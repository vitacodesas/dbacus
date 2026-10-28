import { i18n } from "../i18n";
import * as api from "../api/queries";
import { asAppError } from "../api/errors";
import { useConnectFlow } from "./useConnectFlow";
import { useQueryTabsStore } from "../stores/queryTabs";
import { useResultsStore } from "../stores/results";
import { useSessionsStore } from "../stores/sessions";
import { useUiStore } from "../stores/ui";

const t = (k: string, p?: Record<string, unknown>) => i18n.global.t(k, p ?? {});

/** Texto que se envía y dónde empieza dentro del documento (para colocar los marcadores de error). */
export interface RunSource {
  text: string;
  baseOffset: number;
}

/** Lanza ejecuciones de una pestaña: resuelve la sesión (conectando si hace falta) y delega en el store de resultados. */
export function useQueryRunner() {
  const tabs = useQueryTabsStore();
  const results = useResultsStore();
  const sessions = useSessionsStore();
  const ui = useUiStore();
  const connect = useConnectFlow();

  async function sessionFor(profileId: string) {
    return sessions.sessionFor(profileId) ?? (await connect.connect(profileId));
  }

  async function run(tabId: string, source: RunSource) {
    const tab = tabs.byId(tabId);
    if (!tab) return;
    if (!source.text.trim()) {
      ui.toast("info", t("query.nothingToRun"));
      return;
    }
    if (results.running(tabId)) return;
    const session = await sessionFor(tab.profileId);
    if (!session) return; // el usuario canceló la conexión o ya se mostró el error
    await results.execute({
      tabId, sessionId: session.sessionId, sql: source.text, baseOffset: source.baseOffset,
      database: tab.database, autoLimit: ui.autoLimit,
    });
  }

  const runAll = (tabId: string) => run(tabId, { text: tabs.byId(tabId)?.sql ?? "", baseOffset: 0 });

  /** Con selección ejecuta lo seleccionado; sin ella, la sentencia bajo el cursor (la calcula Go). */
  async function runSelection(tabId: string, sel: { text: string; start: number; cursor: number }) {
    const tab = tabs.byId(tabId);
    if (!tab) return;
    if (sel.text.trim()) return run(tabId, { text: sel.text, baseOffset: sel.start });
    try {
      const st = await api.statementAt(tab.sql, sel.cursor);
      return run(tabId, { text: st.text, baseOffset: st.start });
    } catch (e) {
      ui.toast("info", asAppError(e).message);
    }
  }

  async function format(tabId: string) {
    const tab = tabs.byId(tabId);
    if (!tab || !tab.sql.trim()) return;
    try {
      tabs.setSql(tabId, await api.formatSql(tab.sql));
    } catch (e) {
      ui.toast("error", asAppError(e).message);
    }
  }

  const cancel = (tabId: string) => results.cancel(tabId);

  return { run, runAll, runSelection, format, cancel };
}
