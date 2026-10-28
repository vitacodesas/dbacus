import { ref } from "vue";
import { defineStore } from "pinia";

export type SecretField = "password" | "sshPassword" | "sshKeyPassphrase";

export interface PasswordRequest {
  profileName: string;
  fields: SecretField[];
  resolve: (values: Partial<Record<SecretField, string>> | null) => void;
}

export interface HostKeyRequest {
  host: string;
  port: number;
  fingerprint: string;
  /** true = la huella cambió: solo informa, no ofrece confiar. */
  mismatch: boolean;
  resolve: (trust: boolean) => void;
}

/** Estado de los diálogos que interrumpen una conexión. La lógica de reintento vive en useConnectFlow. */
export const useConnectFlowStore = defineStore("connectFlow", () => {
  const passwordRequest = ref<PasswordRequest | null>(null);
  const hostKeyRequest = ref<HostKeyRequest | null>(null);

  function askSecrets(profileName: string, fields: SecretField[]) {
    return new Promise<Partial<Record<SecretField, string>> | null>((resolve) => {
      passwordRequest.value = {
        profileName, fields,
        resolve: (v) => {
          passwordRequest.value = null;
          resolve(v);
        },
      };
    });
  }

  function askHostKey(req: Omit<HostKeyRequest, "resolve">) {
    return new Promise<boolean>((resolve) => {
      hostKeyRequest.value = {
        ...req,
        resolve: (v) => {
          hostKeyRequest.value = null;
          resolve(v);
        },
      };
    });
  }

  return { passwordRequest, hostKeyRequest, askSecrets, askHostKey };
});
