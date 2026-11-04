import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const wails = vi.hoisted(() => ({ SetText: vi.fn() }));
vi.mock("@wailsio/runtime", () => ({ Clipboard: wails }));

import { writeClipboard } from "./clipboard";

const nav = { writeText: vi.fn() };
beforeEach(() => {
  nav.writeText.mockReset();
  wails.SetText.mockReset();
  Object.defineProperty(navigator, "clipboard", { value: nav, configurable: true });
});
afterEach(() => vi.restoreAllMocks());

describe("writeClipboard", () => {
  it("usa navigator.clipboard si funciona", async () => {
    nav.writeText.mockResolvedValue(undefined);
    expect(await writeClipboard("KILL 1;")).toBe(true);
    expect(wails.SetText).not.toHaveBeenCalled();
  });

  it("si el navegador lo rechaza, usa el portapapeles de Wails", async () => {
    nav.writeText.mockRejectedValue(new Error("NotAllowedError"));
    wails.SetText.mockResolvedValue(undefined);
    expect(await writeClipboard("KILL 1;")).toBe(true);
    expect(wails.SetText).toHaveBeenCalledWith("KILL 1;");
  });

  it("si ambos fallan, prueba execCommand", async () => {
    nav.writeText.mockRejectedValue(new Error("x"));
    wails.SetText.mockRejectedValue(new Error("x"));
    const exec = vi.fn().mockReturnValue(true);
    Object.defineProperty(document, "execCommand", { value: exec, configurable: true });
    expect(await writeClipboard("KILL 1;")).toBe(true);
    expect(exec).toHaveBeenCalledWith("copy");
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("devuelve false solo si las tres vías fallan", async () => {
    nav.writeText.mockRejectedValue(new Error("x"));
    wails.SetText.mockRejectedValue(new Error("x"));
    Object.defineProperty(document, "execCommand", { value: vi.fn().mockReturnValue(false), configurable: true });
    expect(await writeClipboard("KILL 1;")).toBe(false);
  });
});
