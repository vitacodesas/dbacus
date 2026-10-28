import { describe, expect, it } from "vitest";
import { errorMarker } from "./queryMarkers";

const base = { baseOffset: 0, message: "boom" };

describe("errorMarker", () => {
  const sql = "SELECT 1;\nSELECT boom FROM x;\nSELECT 3";

  it("marca la sentencia que falló", () => {
    const start = sql.indexOf("SELECT boom");
    const m = errorMarker(sql, { ...base, start, end: start + "SELECT boom FROM x".length });
    expect(sql.slice(m!.startOffset, m!.endOffset)).toBe("SELECT boom FROM x");
  });

  it("con 'at line N' marca solo esa línea de la sentencia (relativa a ella)", () => {
    const script = "SELECT 1;\nSELECT a,\n  b,\n  FROM t;";
    const start = script.indexOf("SELECT a");
    const end = script.length - 1;
    const m = errorMarker(script, { ...base, start, end, detail: "ERROR 1064: ... near 'FROM t' at line 3" });
    expect(script.slice(m!.startOffset, m!.endOffset)).toBe("FROM t");
  });

  it("una línea fuera de rango se acota a la última", () => {
    const m = errorMarker("SELECT\n  x", { ...base, start: 0, end: 10, detail: "at line 99" });
    expect("SELECT\n  x".slice(m!.startOffset, m!.endOffset)).toBe("x");
  });

  it("suma baseOffset cuando se ejecutó solo una selección", () => {
    const text = "SELECT 1;\n-- nada\nSELECT oops";
    const selStart = text.indexOf("SELECT oops");
    const m = errorMarker(text, { baseOffset: selStart, start: 0, end: "SELECT oops".length, message: "x" });
    expect(text.slice(m!.startOffset, m!.endOffset)).toBe("SELECT oops");
  });

  it("devuelve null si el texto cambió y el rango ya no existe", () => {
    expect(errorMarker("SELECT", { ...base, start: 50, end: 60 })).toBeNull();
    expect(errorMarker("SELECT", { baseOffset: -5, start: 0, end: 3, message: "x" })).toBeNull();
  });

  it("garantiza al menos un carácter de ancho", () => {
    const m = errorMarker("SELECT 1", { ...base, start: 3, end: 3 });
    expect(m!.endOffset - m!.startOffset).toBe(1);
  });

  it("funciona con caracteres de 2 unidades UTF-16", () => {
    const text = "SELECT '😀', boom";
    const start = text.indexOf("boom");
    const m = errorMarker(text, { ...base, start, end: start + 4 });
    expect(text.slice(m!.startOffset, m!.endOffset)).toBe("boom");
  });
});
