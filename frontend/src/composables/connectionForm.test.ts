import { describe, expect, it } from "vitest";
import type { ConnectionProfile } from "../types";
import { applyParsed, emptyForm, firstTabWithError, formFromProfile, tabOfField, toGroup, toProfile, toSecrets, validateForm } from "./connectionForm";

const t = (k: string) => k;
const valid = () => ({ ...emptyForm(), name: "Local", user: "root" });

describe("validateForm", () => {
  it("un formulario mínimo es válido", () => {
    expect(validateForm(valid(), t)).toEqual({});
  });

  it("detecta los campos obligatorios y rangos", () => {
    const f = { ...emptyForm(), host: " ", port: "70000" };
    expect(Object.keys(validateForm(f, t)).sort()).toEqual(["host", "name", "port", "user"]);
    expect(validateForm({ ...valid(), port: "abc" }, t).port).toBeTruthy();
    expect(validateForm({ ...valid(), port: "0" }, t).port).toBeTruthy();
    expect(validateForm({ ...valid(), port: "65535" }, t).port).toBeUndefined();
  });

  it("host con espacios, color y nombre largo", () => {
    expect(validateForm({ ...valid(), host: "a b" }, t).host).toBe("form.errors.hostSpaces");
    expect(validateForm({ ...valid(), color: "rojo" }, t).color).toBeTruthy();
    expect(validateForm({ ...valid(), color: "#a1B2c3" }, t).color).toBeUndefined();
    expect(validateForm({ ...valid(), name: "x".repeat(101) }, t).name).toBeTruthy();
    expect(validateForm({ ...valid(), name: "😀".repeat(100) }, t).name).toBeUndefined();
  });

  it("TLS: verify-ca exige CA y cert/clave van juntos", () => {
    const f = valid();
    f.tls.mode = "verify-ca";
    expect(validateForm(f, t)["tls.caPath"]).toBeTruthy();
    f.tls = { mode: "required", caPath: "", certPath: "/c.pem", keyPath: "" };
    expect(validateForm(f, t)["tls.certPath"]).toBeTruthy();
  });

  it("SSH solo se valida si está activo", () => {
    const f = valid();
    f.ssh = { ...f.ssh, host: "", user: "" };
    expect(validateForm(f, t)).toEqual({});
    f.ssh.enabled = true;
    expect(Object.keys(validateForm(f, t)).sort()).toEqual(["ssh.host", "ssh.user"]);
    f.ssh = { ...f.ssh, host: "b", user: "u", authMethod: "key" };
    expect(validateForm(f, t)["ssh.keyPath"]).toBeTruthy();
  });

  it("parámetros: clave inválida se marca por fila; filas vacías se ignoran", () => {
    const f = valid();
    f.params = [{ key: "a b", value: "1" }, { key: "", value: "x" }, { key: "time_zone", value: "UTC" }];
    expect(Object.keys(validateForm(f, t))).toEqual(["params.0"]);
  });
});

describe("conversión", () => {
  it("toProfile normaliza y descarta parámetros sin clave", () => {
    const f = valid();
    f.name = "  Local  ";
    f.port = "3307";
    f.params = [{ key: " sql_mode ", value: "ANSI" }, { key: "", value: "x" }];
    const p = toProfile(f);
    expect(p).toMatchObject({ name: "Local", port: 3307, params: { sql_mode: "ANSI" } });
  });

  it("los timestamps son un time.Time válido para Go (nunca cadena vacía)", () => {
    const p = toProfile(valid());
    for (const v of [p.createdAt, p.updatedAt]) expect(Number.isNaN(Date.parse(v))).toBe(false);
  });

  it("formFromProfile ↔ toProfile conserva los datos", () => {
    const p = {
      id: "1", name: "Prod", driver: "mysql", host: "db", port: 3310, user: "u", database: "app", color: "#112233", savePassword: false,
      tls: { mode: "verify-full", caPath: "/ca", certPath: "", keyPath: "" },
      ssh: { enabled: true, host: "b", port: 2222, user: "s", authMethod: "key", keyPath: "/k" },
      params: { time_zone: "UTC" }, hasPassword: true, hasSshPassword: false, hasSshPassphrase: true,
    } as unknown as ConnectionProfile;
    const f = formFromProfile(p);
    expect(f.has).toEqual({ password: true, sshPassword: false, sshPassphrase: true });
    expect(toProfile(f)).toMatchObject({ id: "1", name: "Prod", port: 3310, savePassword: false, tls: p.tls, ssh: p.ssh, params: p.params });
    expect(f.noHistory).toBe(false);
    expect(toProfile({ ...f, noHistory: true })).toMatchObject({ noHistory: true });
    expect(formFromProfile({ ...p, noHistory: true } as never).noHistory).toBe(true);
    expect(f.readOnly).toBe(false);
    expect(toProfile({ ...f, readOnly: true })).toMatchObject({ readOnly: true });
    expect(formFromProfile({ ...p, readOnly: true } as never).readOnly).toBe(true);
  });
});

describe("toSecrets", () => {
  it("vacío = sin cambios", () => {
    expect(toSecrets(valid())).toEqual({ password: null, sshPassword: null, sshKeyPassphrase: null, clear: null });
  });
  it("texto escrito = fijar; clearPassword = borrar", () => {
    const f = valid();
    f.password = "pw";
    expect(toSecrets(f).password).toBe("pw");
    f.clearPassword = true;
    expect(toSecrets(f)).toMatchObject({ password: null, clear: ["password"] });
  });
  it("los secretos SSH dependen del método y de que SSH esté activo", () => {
    const f = valid();
    f.ssh = { ...f.ssh, password: "sp", passphrase: "pp" };
    expect(toSecrets(f)).toMatchObject({ sshPassword: null, sshKeyPassphrase: null });
    f.ssh.enabled = true;
    expect(toSecrets(f)).toMatchObject({ sshPassword: "sp", sshKeyPassphrase: null });
    f.ssh.authMethod = "key";
    expect(toSecrets(f)).toMatchObject({ sshPassword: null, sshKeyPassphrase: "pp" });
  });
});

describe("pestañas", () => {
  it("mapea campos a su pestaña y elige la primera con error", () => {
    expect(tabOfField("name")).toBe("general");
    expect(tabOfField("tls.caPath")).toBe("tls");
    expect(tabOfField("ssh.host")).toBe("ssh");
    expect(tabOfField("params.2")).toBe("advanced");
    expect(tabOfField("dsn")).toBe("import");
    expect(firstTabWithError({ "ssh.host": "x", "tls.caPath": "y" })).toBe("tls");
    expect(firstTabWithError({})).toBeNull();
  });
});

describe("applyParsed", () => {
  it("rellena desde un DSN sin pisar lo que no define y mezcla parámetros", () => {
    const f = valid();
    f.params = [{ key: "charset", value: "latin1" }];
    applyParsed(f, { host: "h", port: 3307, user: "u2", database: "d", tls: { mode: "verify-full" }, params: { charset: "utf8mb4", time_zone: "UTC" } } as unknown as ConnectionProfile, "pw");
    expect(f).toMatchObject({ host: "h", port: "3307", user: "u2", database: "d", password: "pw", name: "Local" });
    expect(f.tls.mode).toBe("verify-full");
    expect(f.params).toEqual([{ key: "charset", value: "utf8mb4" }, { key: "time_zone", value: "UTC" }]);
  });
});

describe("grupos en el formulario (spec 17)", () => {
  it("la conexión conserva su grupo al editarla y sin grupo es cadena vacía", () => {
    const base = { id: "1", name: "A", driver: "mysql", host: "h", port: 3306, user: "u", savePassword: true } as ConnectionProfile;
    expect(formFromProfile({ ...base, groupId: "g1" }).groupId).toBe("g1");
    expect(formFromProfile(base).groupId).toBe("");
    expect(toProfile({ ...valid(), groupId: "g1" }).groupId).toBe("g1");
  });

  it("con un grupo nuevo activo, la conexión se envía sin grupo (aún no tiene id)", () => {
    const f = { ...valid(), groupId: "g-viejo", newGroup: { active: true, name: "Prod", color: "#ff0000" } };
    expect(toProfile(f).groupId).toBe("");
    const g = toGroup(f);
    expect(g).toMatchObject({ id: "", name: "Prod", color: "#ff0000" });
  });

  it("valida nombre y color del grupo nuevo solo mientras está activo", () => {
    expect(validateForm({ ...valid(), newGroup: { active: false, name: "", color: "rojo" } }, t)).toEqual({});
    const e = validateForm({ ...valid(), newGroup: { active: true, name: "  ", color: "rojo" } }, t);
    expect(e["group.name"]).toBeTruthy();
    expect(e["group.color"]).toBeTruthy();
    expect(validateForm({ ...valid(), newGroup: { active: true, name: "x".repeat(101), color: "" } }, t)["group.name"]).toBeTruthy();
    expect(validateForm({ ...valid(), newGroup: { active: true, name: "Prod", color: "#a1B2c3" } }, t)).toEqual({});
  });

  it("los errores de grupo caen en la pestaña General", () => {
    expect(tabOfField("group.name")).toBe("general");
    expect(tabOfField("groupId")).toBe("general");
  });
});
