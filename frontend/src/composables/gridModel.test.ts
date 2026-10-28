import { describe, expect, it } from "vitest";
import type { Cell, ColumnMeta } from "../types";
import {
  clampWidth, columnOffsets, copyText, defaultWidth, displayCell, GUTTER_WIDTH, HEADER_HEIGHT, inSelection, MAX_COL, MIN_COL, ROW_HEIGHT,
  selectionBounds, toTSV, visibleColumns, visibleRows,
} from "./gridModel";

const col = (name: string, dataType: string): ColumnMeta => ({ name, dataType, nullable: true, precision: null, scale: null });

describe("anchos", () => {
  it("dependen del tipo y siempre caben en [MIN, MAX]", () => {
    expect(defaultWidth(col("id", "INT"))).toBe(90);
    expect(defaultWidth(col("id", "UNSIGNED BIGINT"))).toBe(90);
    expect(defaultWidth(col("created", "DATETIME"))).toBe(190);
    expect(defaultWidth(col("body", "LONGTEXT"))).toBe(240);
    expect(defaultWidth(col("x", "INT"))).toBeGreaterThanOrEqual(MIN_COL);
    expect(defaultWidth(col("n".repeat(200), "INT"))).toBe(MAX_COL);
  });
  it("el encabezado largo ensancha la columna", () => {
    expect(defaultWidth(col("customer_lifetime_value", "INT"))).toBeGreaterThan(90);
  });
  it("clampWidth redondea y acota", () => {
    expect(clampWidth(10)).toBe(MIN_COL);
    expect(clampWidth(9999)).toBe(MAX_COL);
    expect(clampWidth(100.6)).toBe(101);
  });
});

describe("ventana visible", () => {
  const widths = [100, 100, 100, 100, 100];
  const offsets = columnOffsets(widths);

  it("offsets acumulados", () => expect(offsets).toEqual([0, 100, 200, 300, 400, 500]));

  it("columnas: sin scroll muestra las primeras que caben (+margen)", () => {
    expect(visibleColumns(offsets, 0, 250, 0)).toEqual([0, 3]);
  });
  it("columnas: con scroll se desplaza (descontando el margen izquierdo)", () => {
    const [first, last] = visibleColumns(offsets, GUTTER_WIDTH + 250, 200, 0);
    expect([first, last]).toEqual([2, 5]);
  });
  it("columnas: el margen no sale de los límites y sin columnas es vacío", () => {
    expect(visibleColumns(offsets, 0, 100, 5)).toEqual([0, 5]);
    expect(visibleColumns([0], 0, 100)).toEqual([0, 0]);
  });
  it("filas: ventana con margen y acotada", () => {
    expect(visibleRows(1000, 0, 240, 0)).toEqual([0, 10]);
    expect(visibleRows(1000, ROW_HEIGHT * 100, 240, 2)).toEqual([98, 112]);
    expect(visibleRows(5, 0, 1000)).toEqual([0, 5]);
    expect(visibleRows(0, 0, 1000)).toEqual([0, 0]);
  });
  it("con 5 millones de filas solo se piden decenas", () => {
    const [a, b] = visibleRows(5_000_000, ROW_HEIGHT * 2_500_000, 600);
    expect(b - a).toBeLessThan(60);
    expect(HEADER_HEIGHT).toBeGreaterThan(0);
  });
});

describe("displayCell", () => {
  const c = (over: Partial<Cell>): Cell => ({ v: null, ...over }) as Cell;
  it("NULL se distingue de la cadena vacía", () => {
    expect(displayCell(c({ v: null }))).toMatchObject({ text: "NULL", isNull: true });
    expect(displayCell(undefined).isNull).toBe(true);
    expect(displayCell(c({ v: "" }))).toMatchObject({ text: "", isNull: false });
  });
  it("números, bigint y decimal van a la derecha", () => {
    expect(displayCell(c({ v: 42 }))).toMatchObject({ numeric: true, type: "number" });
    expect(displayCell(c({ v: "18446744073709551615", t: "bigint" }))).toMatchObject({ numeric: true, text: "18446744073709551615" });
    expect(displayCell(c({ v: "12.340", t: "decimal" }))).toMatchObject({ numeric: true, text: "12.340" });
  });
  it("tipos especiales", () => {
    expect(displayCell(c({ v: "0x00ff", t: "bytes" })).type).toBe("bytes");
    expect(displayCell(c({ v: '{"a":1}', t: "json" })).type).toBe("json");
    expect(displayCell(c({ v: "2024-01-02 03:04:05", t: "datetime" })).type).toBe("datetime");
    expect(displayCell(c({ v: true })).text).toBe("true");
  });
  it("aplana saltos de línea y marca el recorte", () => {
    expect(displayCell(c({ v: "a\nb\tc" })).text).toBe("a b c");
    expect(displayCell(c({ v: "abc", trunc: true })).text).toBe("abc…");
  });
});

describe("copia", () => {
  const cols = [col("id", "INT"), col("note", "TEXT"), col("n", "INT")];
  const rows: Cell[][] = [
    [{ v: 1 }, { v: "hola" }, { v: null }],
    [{ v: 2 }, { v: 'con\ttab y "comillas"\nsalto' }, { v: 7 }],
  ];
  it("TSV con encabezados y escape", () => {
    expect(toTSV(rows, cols, { c0: 0, c1: 2 }, true)).toBe(`id\tnote\tn\n1\thola\tNULL\n2\t"con\ttab y ""comillas""\nsalto"\t7`);
  });
  it("sin encabezados y solo un rango de columnas", () => {
    expect(toTSV(rows, cols, { c0: 2, c1: 2 }, false)).toBe("NULL\n7");
  });
  it("copyText: NULL, booleanos y valores", () => {
    expect(copyText({ v: null } as Cell)).toBe("NULL");
    expect(copyText({ v: false } as Cell)).toBe("false");
    expect(copyText({ v: 3.5 } as Cell)).toBe("3.5");
  });
});

describe("selección", () => {
  it("normaliza esquinas en cualquier dirección", () => {
    expect(selectionBounds({ ar: 5, ac: 3, fr: 2, fc: 1 })).toEqual({ r0: 2, r1: 5, c0: 1, c1: 3 });
  });
  it("inSelection", () => {
    const s = { ar: 1, ac: 1, fr: 3, fc: 2 };
    expect(inSelection(s, 2, 2)).toBe(true);
    expect(inSelection(s, 4, 2)).toBe(false);
    expect(inSelection(null, 0, 0)).toBe(false);
  });
});
