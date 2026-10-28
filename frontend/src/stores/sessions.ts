import { computed, ref, shallowRef } from "vue";
import { defineStore } from "pinia";
import * as api from "../api/connections";
import { i18n } from "../i18n";
import type { ConnectionSecrets, SessionClosed, SessionInfo } from "../types";
import { useConnectionsStore } from "./connections";
import { useUiStore } from "./ui";

export const useSessionsStore = defineStore("sessions", () => {
  const byId = ref<Record<string, SessionInfo>>({});
  const activeSessionId = ref<string | null>(null);
  /** Conexiones en curso por perfil: permiten cancelarlas. */
  const connecting = ref<Record<string, () => void>>({});
  /** Lo registra la app al arrancar (useConnectFlow depende de este store: registrarlo aquí evitaría un ciclo de módulos). */
  const reconnect = shallowRef<((profileId: string) => void) | null>(null);

  const list = computed(() => Object.values(byId.value));
  const active = computed(() => (activeSessionId.value ? (byId.value[activeSessionId.value] ?? null) : null));
  const connectedProfileIds = computed(() => new Set(list.value.map((s) => s.profileId)));

  const sessionFor = (profileId: string) => list.value.find((s) => s.profileId === profileId) ?? null;
  const isConnecting = (profileId: string) => profileId in connecting.value;

  /** Recupera las sesiones vivas en Go (p. ej. tras recargar la ventana). */
  async function refresh() {
    byId.value = Object.fromEntries((await api.listSessions()).map((s) => [s.sessionId, s]));
    if (activeSessionId.value && !byId.value[activeSessionId.value]) activeSessionId.value = null;
    if (!activeSessionId.value) activeSessionId.value = list.value[0]?.sessionId ?? null;
  }

  /** Abre la sesión; lanza AppException (incluidos PASSWORD_REQUIRED y SSH_HOSTKEY_UNKNOWN) para que la UI los resuelva. */
  async function connect(profileId: string, overrides: ConnectionSecrets | null = null) {
    const run = api.startConnect(profileId, overrides);
    connecting.value = { ...connecting.value, [profileId]: run.cancel };
    try {
      const info = await run.result;
      byId.value = { ...byId.value, [info.sessionId]: info };
      activeSessionId.value = info.sessionId;
      return info;
    } finally {
      const rest = { ...connecting.value };
      delete rest[profileId];
      connecting.value = rest;
    }
  }

  /** Elige la sesión sobre la que actúan el editor y la barra de estado. */
  function setActive(sessionId: string) {
    if (byId.value[sessionId]) activeSessionId.value = sessionId;
  }

  function cancelConnect(profileId: string) {
    connecting.value[profileId]?.();
  }

  function drop(sessionId: string) {
    const rest = { ...byId.value };
    delete rest[sessionId];
    byId.value = rest;
    if (activeSessionId.value === sessionId) activeSessionId.value = Object.keys(rest)[0] ?? null;
  }

  async function disconnect(sessionId: string) {
    drop(sessionId);
    await api.disconnect(sessionId);
  }

  /** Evento `session:closed`: Go cerró la sesión (p. ej. caída del túnel SSH). */
  function handleClosed(ev: SessionClosed) {
    const s = byId.value[ev.sessionId];
    if (!s) return;
    drop(ev.sessionId);
    const name = useConnectionsStore().byId.get(s.profileId)?.name ?? "";
    const reason = ev.reason === "ssh_tunnel_down" ? i18n.global.t("session.tunnelDown") : ev.reason;
    useUiStore().toast("error", i18n.global.t("session.closed", { name, reason }), {
      label: i18n.global.t("connections.reconnect"),
      run: () => reconnect.value?.(s.profileId),
    });
  }

  return { byId, activeSessionId, connecting, reconnect, list, active, connectedProfileIds, sessionFor, isConnecting, refresh, connect, setActive, cancelConnect, disconnect, handleClosed };
});
