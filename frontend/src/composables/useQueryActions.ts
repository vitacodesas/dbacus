import { i18n } from "../i18n";
import { selectStatement } from "../api/schema";
import { asAppError } from "../api/errors";
import { useQueryRunner } from "./useQueryRunner";
import { useConnectionsStore } from "../stores/connections";
import { useQueryTabsStore } from "../stores/queryTabs";
import { useSessionsStore } from "../stores/sessions";
import { useUiStore } from "../stores/ui";

const t = (k: string) => i18n.global.t(k);

/** Acciones de alto nivel sobre las pestañas de consulta; las comparten botones, atajos y el árbol de esquema. */
export function useQueryActions() {
  const tabs = useQueryTabsStore();
  const sessions = useSessionsStore();
  const connections = useConnectionsStore();
  const ui = useUiStore();
  const runner = useQueryRunner();

  /** Conexión sobre la que se abre una consulta nueva: la de la pestaña actual o la sesión activa. */
  function currentProfileId(): string | null {
    return tabs.active?.profileId ?? sessions.active?.profileId ?? connections.profiles[0]?.id ?? null;
  }

  function newQuery(): string | null {
    const profileId = sessions.active?.profileId ?? currentProfileId();
    if (!profileId) {
      ui.toast("info", t("query.needSession"));
      return null;
    }
    return tabs.openTab({ profileId });
  }

  /** Doble clic en una tabla del árbol: abre una pestaña con su SELECT (armado por Go) y lo ejecuta. */
  async function openTable(sessionId: string, db: string, table: string) {
    const session = sessions.byId[sessionId];
    if (!session) return;
    try {
      const sql = await selectStatement(sessionId, db, table);
      const id = tabs.openTab({ profileId: session.profileId, sql, title: table, database: db });
      await runner.runAll(id);
    } catch (e) {
      ui.toast("error", asAppError(e).message);
    }
  }

  const closeActive = () => (tabs.activeTabId ? tabs.closeTab(tabs.activeTabId) : undefined);

  return { newQuery, openTable, closeActive, runner };
}
