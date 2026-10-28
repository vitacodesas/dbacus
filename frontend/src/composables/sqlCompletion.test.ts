import { describe, expect, it } from "vitest";
import type { CompletionCatalog } from "../types";
import { referencedTables, suggest } from "./sqlCompletion";

const catalog: CompletionCatalog = {
  tables: [
    { name: "users", columns: [{ name: "id", dataType: "int" }, { name: "email", dataType: "varchar" }] },
    { name: "orders", columns: [{ name: "id", dataType: "int" }, { name: "user_id", dataType: "int" }, { name: "total", dataType: "decimal" }] },
  ],
};
const at = (text: string) => suggest({ catalog, text, offset: text.length });
const labels = (s: { label: string }[]) => s.map((x) => x.label);

describe("referencedTables", () => {
  it("detecta tablas y alias (con y sin AS)", () => {
    const m = referencedTables("SELECT * FROM users u JOIN orders AS o ON o.user_id = u.id WHERE x");
    expect(Object.fromEntries(m)).toEqual({ users: "users", u: "users", orders: "orders", o: "orders" });
  });
  it("no confunde palabras reservadas con alias", () => {
    const m = referencedTables("SELECT * FROM users WHERE id = 1");
    expect([...m.keys()]).toEqual(["users"]);
    expect([...referencedTables("DELETE FROM users LEFT JOIN orders ON 1").keys()].sort()).toEqual(["orders", "users"]);
  });
  it("soporta base.tabla y backticks", () => {
    expect([...referencedTables("SELECT * FROM `shop`.`orders` o").keys()].sort()).toEqual(["o", "orders"]);
  });
});

describe("suggest", () => {
  it("tras alias. ofrece solo las columnas de esa tabla", () => {
    const text = "SELECT u. FROM users u";
    const r = suggest({ catalog, text, offset: "SELECT u.".length });
    expect(labels(r)).toEqual(["id", "email"]);
    expect(r[0].detail).toBe("users · int");
  });

  it("tras nombre de tabla. también funciona y es insensible a mayúsculas", () => {
    const text = "SELECT ORDERS. FROM orders";
    expect(labels(suggest({ catalog, text, offset: "SELECT ORDERS.".length }))).toEqual(["id", "user_id", "total"]);
  });

  it("alias desconocido no ofrece nada", () => {
    expect(suggest({ catalog, text: "SELECT zz.", offset: 10 })).toEqual([]);
  });

  it("tras FROM/JOIN prioriza tablas y omite funciones y columnas", () => {
    const s = at("SELECT * FROM ");
    expect(s.filter((x) => x.kind === "table").every((x) => x.sortText.startsWith("0_"))).toBe(true);
    expect(s.some((x) => x.kind === "function")).toBe(false);
    expect(s.some((x) => x.kind === "column")).toBe(false);
  });

  it("en SELECT/WHERE ofrece las columnas de las tablas ya mencionadas primero", () => {
    const s = suggest({ catalog, text: "SELECT  FROM users u JOIN orders o ON 1", offset: 7 });
    const cols = s.filter((x) => x.kind === "column");
    expect(labels(cols)).toEqual(["id", "email", "id", "user_id", "total"]); // columnas de users y de orders
    expect(cols.every((c) => c.sortText.startsWith("0_"))).toBe(true);
    expect(s.some((x) => x.label === "COUNT" && x.kind === "function")).toBe(true);
    expect(s.some((x) => x.label === "WHERE" && x.kind === "keyword")).toBe(true);
  });

  it("sin catálogo (aún cargando) sigue ofreciendo palabras clave y funciones", () => {
    const s = suggest({ text: "SEL", offset: 3 });
    expect(s.some((x) => x.label === "SELECT")).toBe(true);
    expect(s.some((x) => x.kind === "table")).toBe(false);
  });
});
