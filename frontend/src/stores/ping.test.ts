import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";

vi.mock("../api/ping", () => ({
  hello: vi.fn(async (n: string) => `Hola, ${n}!`),
  pickFile: vi.fn(async () => "/tmp/a.sql"),
}));
const unsub = vi.fn();
let emit: (t: { seq: number; at: string }) => void = () => {};
vi.mock("../api/events", () => ({
  onPingTick: (h: typeof emit) => {
    emit = h;
    return unsub;
  },
}));

import { usePingStore } from "./ping";

describe("ping store", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("saluda vía api", async () => {
    const s = usePingStore();
    await s.greet("Luis");
    expect(s.greeting).toBe("Hola, Luis!");
  });

  it("recibe ticks y se desuscribe", () => {
    const s = usePingStore();
    s.listen();
    emit({ seq: 3, at: "" });
    expect(s.lastTick).toBe(3);
    s.stop();
    expect(unsub).toHaveBeenCalled();
  });
});
