// Estado, conversión y validación del formulario de conexión. La validación es solo UX: Go repite todo (spec 04).
import type { ConnectionProfile, ConnectionSecrets } from "../types";

export interface ParamRow {
  key: string;
  value: string;
}

export interface FormState {
  id: string;
  name: string;
  driver: string;
  color: string;
  host: string;
  port: string;
  user: string;
  database: string;
  savePassword: boolean;
  noHistory: boolean;
  readOnly: boolean;
  password: string;
  clearPassword: boolean;
  tls: { mode: string; caPath: string; certPath: string; keyPath: string };
  ssh: {
    enabled: boolean;
    host: string;
    port: string;
    user: string;
    authMethod: "password" | "key";
    keyPath: string;
    password: string;
    clearPassword: boolean;
    passphrase: string;
    clearPassphrase: boolean;
  };
  params: ParamRow[];
  /** Estado de secretos guardados (solo lectura, vienen del servidor). */
  has: { password: boolean; sshPassword: boolean; sshPassphrase: boolean };
}

export type FieldErrors = Record<string, string>;
export type FormTab = "general" | "tls" | "ssh" | "advanced" | "import";

export const DEFAULT_PORT = 3306;
export const ZERO_TIME = "0001-01-01T00:00:00Z";
const colorRe = /^#[0-9a-fA-F]{6}$/;
const paramKeyRe = /^[A-Za-z_][A-Za-z0-9_]*$/;

export function emptyForm(driver = "mysql", port = DEFAULT_PORT): FormState {
  return {
    id: "", name: "", driver, color: "", host: "127.0.0.1", port: String(port), user: "", database: "",
    savePassword: true, noHistory: false, readOnly: false, password: "", clearPassword: false,
    tls: { mode: "preferred", caPath: "", certPath: "", keyPath: "" },
    ssh: { enabled: false, host: "", port: "22", user: "", authMethod: "password", keyPath: "", password: "", clearPassword: false, passphrase: "", clearPassphrase: false },
    params: [],
    has: { password: false, sshPassword: false, sshPassphrase: false },
  };
}

export function formFromProfile(p: ConnectionProfile): FormState {
  const f = emptyForm(p.driver, p.port || DEFAULT_PORT);
  Object.assign(f, {
    id: p.id, name: p.name, color: p.color ?? "", host: p.host, user: p.user, database: p.database ?? "", savePassword: p.savePassword, noHistory: !!p.noHistory, readOnly: !!p.readOnly,
  });
  f.tls = { mode: p.tls?.mode || "preferred", caPath: p.tls?.caPath ?? "", certPath: p.tls?.certPath ?? "", keyPath: p.tls?.keyPath ?? "" };
  f.ssh = {
    ...f.ssh,
    enabled: !!p.ssh?.enabled, host: p.ssh?.host ?? "", port: String(p.ssh?.port || 22), user: p.ssh?.user ?? "",
    authMethod: p.ssh?.authMethod === "key" ? "key" : "password", keyPath: p.ssh?.keyPath ?? "",
  };
  f.params = Object.entries(p.params ?? {}).map(([key, value]) => ({ key, value: value ?? "" }));
  f.has = { password: p.hasPassword, sshPassword: p.hasSshPassword, sshPassphrase: p.hasSshPassphrase };
  return f;
}

export function toProfile(f: FormState): ConnectionProfile {
  const params: Record<string, string> = {};
  for (const { key, value } of f.params) if (key.trim()) params[key.trim()] = value;
  return {
    id: f.id, name: f.name.trim(), driver: f.driver, host: f.host.trim(), port: Number(f.port), user: f.user.trim(), database: f.database.trim(),
    color: f.color.trim(), tls: { ...f.tls }, params, savePassword: f.savePassword, noHistory: f.noHistory, readOnly: f.readOnly,
    ssh: {
      enabled: f.ssh.enabled, host: f.ssh.host.trim(), port: Number(f.ssh.port) || 22, user: f.ssh.user.trim(),
      authMethod: f.ssh.authMethod, keyPath: f.ssh.keyPath,
    },
    // Los flags los fija el servidor; se reenvían tal cual solo para que el DTO esté completo.
    hasPassword: f.has.password, hasSshPassword: f.has.sshPassword, hasSshPassphrase: f.has.sshPassphrase,
    // Go (time.Time) rechaza ""; los timestamps reales los fija el servidor al guardar.
    createdAt: ZERO_TIME, updatedAt: ZERO_TIME,
  } as unknown as ConnectionProfile;
}

/** Secretos a enviar: nulo = sin cambios; texto (aunque sea vacío tras tocar) = fijar; `clear` = borrar el guardado. */
export function toSecrets(f: FormState): ConnectionSecrets {
  const clear: string[] = [];
  const pick = (value: string, wantsClear: boolean, name: string) => {
    if (wantsClear) {
      clear.push(name);
      return null;
    }
    return value !== "" ? value : null;
  };
  const secrets: ConnectionSecrets = {
    password: pick(f.password, f.clearPassword, "password"),
    sshPassword: f.ssh.enabled && f.ssh.authMethod === "password" ? pick(f.ssh.password, f.ssh.clearPassword, "sshPassword") : null,
    sshKeyPassphrase: f.ssh.enabled && f.ssh.authMethod === "key" ? pick(f.ssh.passphrase, f.ssh.clearPassphrase, "sshKeyPassphrase") : null,
    clear: null,
  };
  secrets.clear = clear.length ? clear : null;
  return secrets;
}

type T = (key: string) => string;

export function validateForm(f: FormState, t: T): FieldErrors {
  const e: FieldErrors = {};
  const required = t("form.errors.required");
  const portOk = (v: string) => /^\d+$/.test(v) && Number(v) >= 1 && Number(v) <= 65535;

  if (!f.name.trim()) e.name = required;
  else if ([...f.name.trim()].length > 100) e.name = t("form.errors.nameLong");
  if (!f.driver) e.driver = required;
  if (!f.host.trim()) e.host = required;
  else if (/\s/.test(f.host.trim())) e.host = t("form.errors.hostSpaces");
  if (!portOk(f.port)) e.port = t("form.errors.port");
  if (!f.user.trim()) e.user = required;
  if (f.color.trim() && !colorRe.test(f.color.trim())) e.color = t("form.errors.color");

  if (f.tls.mode === "verify-ca" && !f.tls.caPath) e["tls.caPath"] = t("form.errors.caRequired");
  if (!!f.tls.certPath !== !!f.tls.keyPath) e["tls.certPath"] = t("form.errors.certPair");

  if (f.ssh.enabled) {
    if (!f.ssh.host.trim()) e["ssh.host"] = required;
    else if (/\s/.test(f.ssh.host.trim())) e["ssh.host"] = t("form.errors.hostSpaces");
    if (!portOk(f.ssh.port)) e["ssh.port"] = t("form.errors.port");
    if (!f.ssh.user.trim()) e["ssh.user"] = required;
    if (f.ssh.authMethod === "key" && !f.ssh.keyPath) e["ssh.keyPath"] = required;
  }
  f.params.forEach((row, i) => {
    if (row.key.trim() && !paramKeyRe.test(row.key.trim())) e[`params.${i}`] = t("form.errors.paramKey");
  });
  return e;
}

/** Pestaña donde vive un campo (ruta de AppError.Detail o clave de FieldErrors). */
export function tabOfField(field: string): FormTab {
  if (field.startsWith("tls.")) return "tls";
  if (field.startsWith("ssh.")) return "ssh";
  if (field.startsWith("params")) return "advanced";
  if (field === "dsn") return "import";
  return "general";
}

const TAB_ORDER: FormTab[] = ["general", "tls", "ssh", "advanced", "import"];

export function firstTabWithError(errors: FieldErrors): FormTab | null {
  const tabs = new Set(Object.keys(errors).map(tabOfField));
  return TAB_ORDER.find((t) => tabs.has(t)) ?? null;
}

/** Aplica un perfil parcial (p. ej. de ParseDSN) sobre el formulario sin tocar lo que el DSN no define. */
export function applyParsed(f: FormState, p: ConnectionProfile, password: string | null) {
  if (p.host) f.host = p.host;
  if (p.port) f.port = String(p.port);
  if (p.user) f.user = p.user;
  f.database = p.database ?? "";
  if (p.tls?.mode) f.tls.mode = p.tls.mode;
  for (const k of ["caPath", "certPath", "keyPath"] as const) if (p.tls?.[k]) f.tls[k] = p.tls[k];
  for (const [key, raw] of Object.entries(p.params ?? {})) {
    const value = raw ?? "";
    const row = f.params.find((r) => r.key === key);
    if (row) row.value = value;
    else f.params.push({ key, value });
  }
  if (password !== null) {
    f.password = password;
    f.clearPassword = false;
  }
}
