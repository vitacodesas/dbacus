import { i18n } from "../i18n";
import * as api from "../api/connections";
import { asAppError, ErrorCode } from "../api/errors";
import { useConnectFlowStore, type SecretField } from "../stores/connectFlow";
import { useConnectionsStore } from "../stores/connections";
import { useSessionsStore } from "../stores/sessions";
import { useUiStore } from "../stores/ui";
import type { ConnectionSecrets, SessionInfo } from "../types";

const MAX_ATTEMPTS = 6;
const t = (k: string, p?: Record<string, unknown>) => i18n.global.t(k, p ?? {});

/**
 * Conecta resolviendo lo que Go pide por el camino: contraseñas no guardadas, huella SSH desconocida, passphrase.
 * Devuelve null si el usuario canceló o falló (el error ya se mostró).
 */
export function useConnectFlow() {
  const flow = useConnectFlowStore();
  const sessions = useSessionsStore();
  const connections = useConnectionsStore();
  const ui = useUiStore();

  async function connect(profileId: string, initial: ConnectionSecrets | null = null): Promise<SessionInfo | null> {
    const profile = connections.byId.get(profileId);
    if (!profile) return null;
    let overrides: ConnectionSecrets | null = initial;

    for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
      try {
        const info = await sessions.connect(profileId, overrides);
        ui.toast("success", t("connections.toast.connected", { name: profile.name }));
        return info;
      } catch (e) {
        const err = asAppError(e);

        if (err.code === ErrorCode.QueryCanceled) return null;

        const needsSecret: SecretField | null =
          err.code === ErrorCode.PasswordRequired ? ((err.detail as SecretField) ?? "password")
          : err.code === ErrorCode.SSHAuthFailed && err.detail === "ssh.passphrase" ? "sshKeyPassphrase"
          : null;
        if (needsSecret) {
          const values = await flow.askSecrets(profile.name, [needsSecret]);
          if (!values) return null;
          overrides = { ...(overrides ?? api.noSecrets()), [needsSecret]: values[needsSecret] ?? "" };
          continue;
        }

        if (err.code === ErrorCode.SSHHostKeyUnknown) {
          const trust = await flow.askHostKey({ host: profile.ssh.host, port: profile.ssh.port, fingerprint: err.detail ?? "", mismatch: false });
          if (!trust) return null;
          try {
            await api.trustHostKey(profile.ssh.host, profile.ssh.port, err.detail ?? "");
          } catch (te) {
            ui.toast("error", asAppError(te).message);
            return null;
          }
          continue;
        }

        if (err.code === ErrorCode.SSHHostKeyMismatch) {
          await flow.askHostKey({ host: profile.ssh.host, port: profile.ssh.port, fingerprint: err.detail ?? "", mismatch: true });
          return null;
        }

        ui.toast("error", err.message);
        return null;
      }
    }
    return null;
  }

  return { connect };
}
