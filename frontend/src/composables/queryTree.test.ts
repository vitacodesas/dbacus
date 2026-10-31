import { describe, expect, it } from "vitest";
import { baseOf, copyName, flattenQueries, folderPaths, joinPath, parentOf } from "./queryTree";
import type { QueryNode } from "../types";

const node = (path: string, kind: "file" | "folder", modifiedAt = "2026-01-01T00:00:00Z"): QueryNode =>
  ({ path, name: baseOf(path).replace(/\.sql$/i, ""), kind, size: 0, modifiedAt, version: "1:1", profileId: "", profileName: "", database: "" }) as QueryNode;

const nodes = [
  node("b.sql", "file", "2026-03-01T00:00:00Z"),
  node("Informes", "folder"),
  node("Informes/ventas.sql", "file", "2026-01-05T00:00:00Z"),
  node("Informes/2025", "folder"),
  node("Informes/2025/q1.sql", "file"),
  node("a.sql", "file", "2026-02-01T00:00:00Z"),
  node("Zeta", "folder"),
];

describe("queryTree", () => {
  it("ordena carpetas primero y por nombre sin distinguir mayúsculas", () => {
    const rows = flattenQueries(nodes, new Set(), "name");
    expect(rows.map((r) => `${r.depth}:${r.node.path}`)).toEqual([
      "0:Informes", "1:Informes/2025", "2:Informes/2025/q1.sql", "1:Informes/ventas.sql", "0:Zeta", "0:a.sql", "0:b.sql",
    ]);
  });

  it("las carpetas plegadas ocultan a sus hijos", () => {
    const rows = flattenQueries(nodes, new Set(["Informes"]), "name");
    expect(rows.map((r) => r.node.path)).toEqual(["Informes", "Zeta", "a.sql", "b.sql"]);
  });

  it("por modificación ordena los archivos del más reciente al más antiguo y deja las carpetas por nombre", () => {
    const rows = flattenQueries(nodes, new Set(["Informes"]), "modified");
    expect(rows.map((r) => r.node.path)).toEqual(["Informes", "Zeta", "b.sql", "a.sql"]);
  });

  it("lista las carpetas para los selectores y calcula nombres de copia libres", () => {
    expect(folderPaths(nodes)).toEqual(["Informes", "Informes/2025", "Zeta"]);
    expect(copyName(nodes, "Informes", "ventas")).toBe("ventas (copia)");
    expect(copyName([...nodes, node("Informes/ventas (copia).sql", "file")], "Informes", "ventas")).toBe("ventas (copia 2)");
    expect(copyName([node("X (COPIA).sql", "file")], "", "x")).toBe("x (copia 2)");
  });

  it("ayudas de ruta", () => {
    expect(parentOf("a/b/c.sql")).toBe("a/b");
    expect(parentOf("c.sql")).toBe("");
    expect(joinPath("", "x")).toBe("x");
    expect(joinPath("a", "x")).toBe("a/x");
  });
});
