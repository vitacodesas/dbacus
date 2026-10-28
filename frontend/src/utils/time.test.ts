import { describe, expect, it } from "vitest";
import { formatBytes, formatDuration, relativeTime } from "./time";

const NOW = Date.UTC(2026, 9, 28, 12, 0, 0);

describe("time utils", () => {
  it("relativeTime elige la unidad más grande que cabe", () => {
    expect(relativeTime(new Date(NOW - 5_000), "es", NOW)).toBe("ahora");
    expect(relativeTime(new Date(NOW - 3 * 60_000), "es", NOW)).toBe("hace 3 minutos");
    expect(relativeTime(new Date(NOW - 2 * 3600_000), "en", NOW)).toBe("2 hours ago");
    expect(relativeTime(new Date(NOW - 24 * 3600_000), "es", NOW)).toBe("ayer");
  });
  it("formatDuration", () => {
    expect(formatDuration(850, "es")).toBe("850 ms");
    expect(formatDuration(2400, "en")).toBe("2.4 s");
    expect(formatDuration(65_000, "en")).toBe("1 min 5 s");
  });
  it("formatBytes", () => {
    expect(formatBytes(512, "en")).toBe("512 B");
    expect(formatBytes(1536, "en")).toBe("1.5 KB");
    expect(formatBytes(5 * 1024 ** 2, "en")).toBe("5 MB");
  });
});
