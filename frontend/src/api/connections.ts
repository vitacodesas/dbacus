// Único punto que importa ConnectionService (bindings generados). Ver docs/specs/02-contrato-go-vue.md.
import type { CancellablePromise } from "@wailsio/runtime";
import { ConnectionService } from "../../bindings/github.com/vitacodesas/dbacus/internal/services";
import type { ConnectionProfile, ConnectionSecrets, DriverInfo, ParsedDSN, SessionInfo, TestResult } from "../types";
import { wrap } from "./errors";

export const noSecrets = (): ConnectionSecrets => ({ password: null, sshPassword: null, sshKeyPassphrase: null, clear: null });

export interface Cancellable<T> {
  result: Promise<T>;
  cancel: () => void;
}

function cancellable<T>(p: CancellablePromise<T>): Cancellable<T> {
  return { result: wrap(p), cancel: () => void p.cancel() };
}

export const listDrivers = async (): Promise<DriverInfo[]> => (await wrap(ConnectionService.ListDrivers())) ?? [];
export const listProfiles = async (): Promise<ConnectionProfile[]> => (await wrap(ConnectionService.List())) ?? [];
export const getProfile = (id: string): Promise<ConnectionProfile> => wrap(ConnectionService.Get(id));
export const saveProfile = (p: ConnectionProfile, s: ConnectionSecrets): Promise<ConnectionProfile> => wrap(ConnectionService.Save(p, s));
export const cloneProfile = (id: string): Promise<ConnectionProfile> => wrap(ConnectionService.Clone(id));
export const deleteProfile = (id: string, deleteHistory: boolean): Promise<void> => wrap(ConnectionService.Delete(id, deleteHistory));
export const parseDsn = (dsn: string): Promise<ParsedDSN> => wrap(ConnectionService.ParseDSN(dsn));
export const pickFile = (title: string): Promise<string> => wrap(ConnectionService.PickFile(title));
export const trustHostKey = (host: string, port: number, fingerprint: string): Promise<void> => wrap(ConnectionService.TrustHostKey(host, port, fingerprint));
export const disconnect = (sessionId: string): Promise<void> => wrap(ConnectionService.Disconnect(sessionId));
export const listSessions = async (): Promise<SessionInfo[]> => (await wrap(ConnectionService.ListSessions())) ?? [];

/** La prueba y la conexión pueden colgarse en la red: devuelven también `cancel`. */
export const startTest = (p: ConnectionProfile, s: ConnectionSecrets): Cancellable<TestResult> => cancellable(ConnectionService.Test(p, s));
export const startConnect = (id: string, overrides: ConnectionSecrets | null): Cancellable<SessionInfo> => cancellable(ConnectionService.Connect(id, overrides));
