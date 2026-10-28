import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { SessionInfo } from "../types";

const info = (id: string, profileId = "p1"): SessionInfo => ({ sessionId: id, profileId, serverVersion: "8", currentDatabase: "" });
const api = vi.hoisted(() => ({ startConnect: vi.fn(), disconnect: vi.fn(), listSessions: vi.fn(), listProfiles: vi.fn(), listDrivers: vi.fn() }));
vi.mock("../api/connections", () => api);

import { useSessionsStore } from "./sessions";
import { useConnectionsStore } from "./connections";
import { useUiStore } from "./ui";

describe("sessions store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    Object.values(api).forEach((f) => f.mockReset());
  });

  it("connect registra la sesión, la activa y limpia el estado 'conectando'", async () => {
    let release!: (v: SessionInfo) => void;
    api.startConnect.mockReturnValue({ result: new Promise((r) => (release = r)), cancel: vi.fn() });
    const s = useSessionsStore();
    const p = s.connect("p1");
    expect(s.isConnecting("p1")).toBe(true);
    release(info("s1"));
    await p;
    expect(s.isConnecting("p1")).toBe(false);
    expect(s.activeSessionId).toBe("s1");
    expect(s.connectedProfileIds.has("p1")).toBe(true);
    expect(s.sessionFor("p1")?.sessionId).toBe("s1");
  });

  it("connect fallido deja la lista intacta y limpia 'conectando'", async () => {
    api.startConnect.mockReturnValue({ result: Promise.reject(new Error("x")), cancel: vi.fn() });
    const s = useSessionsStore();
    await expect(s.connect("p1")).rejects.toThrow();
    expect(s.list).toEqual([]);
    expect(s.isConnecting("p1")).toBe(false);
  });

  it("cancelConnect invoca el cancel de la conexión en curso", async () => {
    const cancel = vi.fn();
    api.startConnect.mockReturnValue({ result: new Promise(() => {}), cancel });
    const s = useSessionsStore();
    void s.connect("p1");
    s.cancelConnect("p1");
    expect(cancel).toHaveBeenCalled();
  });

  it("disconnect quita la sesión y elige otra activa", async () => {
    api.listSessions.mockResolvedValue([info("s1"), info("s2", "p2")]);
    api.disconnect.mockResolvedValue(undefined);
    const s = useSessionsStore();
    await s.refresh();
    s.activeSessionId = "s1";
    await s.disconnect("s1");
    expect(api.disconnect).toHaveBeenCalledWith("s1");
    expect(s.activeSessionId).toBe("s2");
  });

  it("refresh descarta la sesión activa que ya no existe", async () => {
    const s = useSessionsStore();
    s.activeSessionId = "muerta";
    api.listSessions.mockResolvedValue([info("s1")]);
    await s.refresh();
    expect(s.activeSessionId).toBe("s1");
  });

  it("session:closed quita la sesión y avisa con un toast de error", async () => {
    api.listProfiles.mockResolvedValue([{ id: "p1", name: "Prod" }]);
    api.listDrivers.mockResolvedValue([]);
    api.listSessions.mockResolvedValue([info("s1")]);
    await useConnectionsStore().load();
    const s = useSessionsStore();
    await s.refresh();

    s.handleClosed({ sessionId: "s1", reason: "ssh_tunnel_down" });
    expect(s.list).toEqual([]);
    const toast = useUiStore().toasts[0];
    expect(toast.kind).toBe("error");
    expect(toast.message).toContain("Prod");

    s.handleClosed({ sessionId: "desconocida", reason: "x" });
    expect(useUiStore().toasts).toHaveLength(1);
  });
});
