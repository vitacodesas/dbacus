import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { ConnectionProfile } from "../types";

const mk = (id: string, name: string): ConnectionProfile => ({ id, name }) as ConnectionProfile;
const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listDrivers: vi.fn(),
  saveProfile: vi.fn(),
  cloneProfile: vi.fn(),
  deleteProfile: vi.fn(),
}));
vi.mock("../api/connections", () => api);

import { useConnectionsStore } from "./connections";

describe("connections store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    Object.values(api).forEach((f) => f.mockReset());
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
});
