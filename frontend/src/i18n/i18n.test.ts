import { describe, expect, it } from "vitest";
import es from "./es";
import en from "./en";

const keys = (o: object, p = ""): string[] =>
  Object.entries(o).flatMap(([k, v]) => (typeof v === "object" ? keys(v, `${p}${k}.`) : [`${p}${k}`]));

describe("i18n", () => {
  it("es y en tienen las mismas claves", () => {
    expect(keys(en).sort()).toEqual(keys(es).sort());
  });
});

describe("sintaxis de mensajes", () => {
  // vue-i18n reserva @ | { } $ en los mensajes; un literal mal escapado deja la pantalla vacía en runtime.
  const leaves = (o: object, p = ""): [string, string][] =>
    Object.entries(o).flatMap(([k, v]) => (typeof v === "object" ? leaves(v, `${p}${k}.`) : [[`${p}${k}`, v as string] as [string, string]]));

  for (const [name, messages] of [["es", es], ["en", en]] as const) {
    it(`todos los mensajes de ${name} compilan`, async () => {
      const { baseCompile } = await import("@intlify/message-compiler");
      for (const [key, msg] of leaves(messages)) {
        expect(() => baseCompile(msg, { onError: (e) => { throw new Error(`${key}: ${e.message}`); } }), key).not.toThrow();
      }
    });
  }
});
