import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { ConnectionGroup, ConnectionProfile } from "../types";

const mk = (id: string, name: string, extra: Partial<ConnectionProfile> = {}): ConnectionProfile => ({ id, name, ...extra }) as ConnectionProfile;
const grp = (id: string, name: string, color = ""): ConnectionGroup => ({ id, name, color }) as ConnectionGroup;
const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listDrivers: vi.fn(),
  saveProfile: vi.fn(),
  cloneProfile: vi.fn(),
  deleteProfile: vi.fn(),
  listGroups: vi.fn(),
  saveGroup: vi.fn(),
  deleteGroup: vi.fn(),
}));
vi.mock("../api/connections", () => api);

import { useConnectionsStore } from "./connections";
import { useUiStore } from "./ui";

describe("connections store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    Object.values(api).forEach((f) => f.mockReset());
    api.listGroups.mockResolvedValue([]);
  });

  it("carga perfiles y drivers ordenados por nombre", async () => {
    api.listProfiles.mockResolvedValue([mk("2", "beta"), mk("1", "Alfa")]);
    api.listDrivers.mockResolvedValue([{ name: "mysql" }]);
    const s = useConnectionsStore();
    await s.load();
    expect(s.profiles.map((p) => p.name)).toEqual(["Alfa", "beta"]);
    expect(s.drivers).toHaveLength(1);
    expect(s.loaded).toBe(true);
  });

  it("un fallo de carga queda en lastError", async () => {
    api.listProfiles.mockRejectedValue(new Error("x"));
    api.listDrivers.mockResolvedValue([]);
    const s = useConnectionsStore();
    await s.load();
    expect(s.lastError?.code).toBe("INTERNAL");
    expect(s.loading).toBe(false);
  });

  it("save inserta o reemplaza; clone y remove actualizan la lista", async () => {
    const s = useConnectionsStore();
    api.saveProfile.mockResolvedValueOnce(mk("1", "B")).mockResolvedValueOnce(mk("1", "A"));
    await s.save(mk("", "B"), {} as never);
    await s.save(mk("1", "A"), {} as never);
    expect(s.profiles.map((p) => p.name)).toEqual(["A"]);

    api.cloneProfile.mockResolvedValue(mk("2", "A (copia)"));
    await s.clone("1");
    expect(s.profiles).toHaveLength(2);

    api.deleteProfile.mockResolvedValue(undefined);
    await s.remove("1", false);
    expect(api.deleteProfile).toHaveBeenCalledWith("1", false);
    expect(s.profiles.map((p) => p.id)).toEqual(["2"]);
    expect(s.byId.get("2")?.name).toBe("A (copia)");
  });

  it("si save falla la lista no cambia", async () => {
    const s = useConnectionsStore();
    api.saveProfile.mockRejectedValue(new Error("no"));
    await expect(s.save(mk("", "X"), {} as never)).rejects.toThrow();
    expect(s.profiles).toEqual([]);
  });
  describe("grupos (spec 17)", () => {
    it("carga los grupos ordenados por nombre sin distinguir mayúsculas", async () => {
      api.listProfiles.mockResolvedValue([]);
      api.listDrivers.mockResolvedValue([]);
      api.listGroups.mockResolvedValue([grp("2", "staging"), grp("1", "Producción")]);
      const s = useConnectionsStore();
      await s.load();
      expect(s.groups.map((g) => g.name)).toEqual(["Producción", "staging"]);
    });

    it("color efectivo: el de la conexión; si no, el del grupo; si no, ninguno", async () => {
      api.listProfiles.mockResolvedValue([
        mk("a", "A", { color: "#111111", groupId: "g" }),
        mk("b", "B", { groupId: "g" }),
        mk("c", "C"),
        mk("d", "D", { groupId: "g-sin-color" }),
      ]);
      api.listDrivers.mockResolvedValue([]);
      api.listGroups.mockResolvedValue([grp("g", "Prod", "#ff0000"), grp("g-sin-color", "Dev")]);
      const s = useConnectionsStore();
      await s.load();
      const col = (id: string) => s.colorOf(s.byId.get(id));
      expect(col("a")).toBe("#111111");
      expect(col("b")).toBe("#ff0000");
      expect(col("c")).toBe("");
      expect(col("d")).toBe("");
    });

    it("un groupId huérfano cuenta como «Sin grupo» y no da color", async () => {
      api.listProfiles.mockResolvedValue([mk("a", "A", { groupId: "borrado" })]);
      api.listDrivers.mockResolvedValue([]);
      const s = useConnectionsStore();
      await s.load();
      expect(s.groupOf(s.byId.get("a"))).toBeUndefined();
      expect(s.colorOf(s.byId.get("a"))).toBe("");
    });

    it("al cargar, olvida los grupos plegados que ya no existen", async () => {
      api.listProfiles.mockResolvedValue([]);
      api.listDrivers.mockResolvedValue([]);
      api.listGroups.mockResolvedValue([grp("vivo", "Prod")]);
      const ui = useUiStore();
      ui.collapsedGroups = ["vivo", "borrado"];
      await useConnectionsStore().load();
      expect(ui.collapsedGroups).toEqual(["vivo"]);
    });

    it("saveGroup inserta o actualiza y mantiene el orden", async () => {
      api.listProfiles.mockResolvedValue([]);
      api.listDrivers.mockResolvedValue([]);
      api.listGroups.mockResolvedValue([grp("1", "Zeta")]);
      const s = useConnectionsStore();
      await s.load();
      api.saveGroup.mockResolvedValueOnce(grp("2", "Alfa", "#00ff00"));
      await s.saveGroup(grp("", "Alfa", "#00ff00"));
      expect(s.groups.map((g) => g.name)).toEqual(["Alfa", "Zeta"]);
      api.saveGroup.mockResolvedValueOnce(grp("2", "Alfa", "#0000ff"));
      await s.saveGroup(grp("2", "Alfa", "#0000ff"));
      expect(s.groups).toHaveLength(2);
      expect(s.groupById.get("2")?.color).toBe("#0000ff");
    });

    it("removeGroup borra el grupo y recarga las conexiones, que Go deja en «Sin grupo»", async () => {
      api.listProfiles.mockResolvedValueOnce([mk("a", "A", { groupId: "g" })]);
      api.listDrivers.mockResolvedValue([]);
      api.listGroups.mockResolvedValue([grp("g", "Prod", "#ff0000")]);
      const s = useConnectionsStore();
      await s.load();
      expect(s.colorOf(s.byId.get("a"))).toBe("#ff0000");

      api.deleteGroup.mockResolvedValue(undefined);
      api.listProfiles.mockResolvedValueOnce([mk("a", "A", { groupId: "" })]);
      await s.removeGroup("g");
      expect(api.deleteGroup).toHaveBeenCalledWith("g");
      expect(s.groups).toEqual([]);
      expect(s.byId.get("a")?.groupId).toBe("");
      expect(s.colorOf(s.byId.get("a"))).toBe("");
    });
  });
});
