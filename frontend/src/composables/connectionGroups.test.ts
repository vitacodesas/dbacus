import { describe, expect, it } from "vitest";
import type { ConnectionGroup, ConnectionProfile } from "../types";
import { buildSections, NO_GROUP } from "./connectionGroups";

const p = (id: string, name: string, extra: Partial<ConnectionProfile> = {}) => ({ id, name, host: "h", user: "u", database: "", groupId: "", ...extra }) as ConnectionProfile;
const g = (id: string, name: string) => ({ id, name, color: "" }) as ConnectionGroup;
const ids = (s: { sections: { id: string; profiles: ConnectionProfile[] }[] }) => s.sections.map((x) => [x.id, x.profiles.map((y) => y.id)]);

describe("buildSections", () => {
  it("sin grupos devuelve la lista plana, filtrada", () => {
    const r = buildSections([p("1", "Alfa"), p("2", "Beta")], [], "bet");
    expect(r.flat).toBe(true);
    expect(ids(r)).toEqual([[NO_GROUP, ["2"]]]);
  });

  it("con grupos: una sección por grupo en su orden y «Sin grupo» al final", () => {
    const r = buildSections([p("1", "A", { groupId: "g2" }), p("2", "B"), p("3", "C", { groupId: "g1" })], [g("g1", "Dev"), g("g2", "Prod")], "");
    expect(r.flat).toBe(false);
    expect(ids(r)).toEqual([["g1", ["3"]], ["g2", ["1"]], [NO_GROUP, ["2"]]]);
  });

  it("sin búsqueda se ven los grupos vacíos; «Sin grupo» solo si tiene conexiones", () => {
    const r = buildSections([p("1", "A", { groupId: "g1" })], [g("g1", "Dev"), g("g2", "Vacío")], "");
    expect(ids(r)).toEqual([["g1", ["1"]], ["g2", []]]);
  });

  it("un groupId huérfano cae en «Sin grupo»", () => {
    const r = buildSections([p("1", "A", { groupId: "borrado" })], [g("g1", "Dev")], "");
    expect(ids(r)).toEqual([["g1", []], [NO_GROUP, ["1"]]]);
  });

  it("con búsqueda se ocultan las secciones sin resultados", () => {
    const r = buildSections([p("1", "Alfa", { groupId: "g1" }), p("2", "Beta", { groupId: "g2" })], [g("g1", "Dev"), g("g2", "Prod")], "alf");
    expect(ids(r)).toEqual([["g1", ["1"]]]);
  });

  it("si el texto coincide con el nombre del grupo se muestran todas sus conexiones", () => {
    const r = buildSections([p("1", "Alfa", { groupId: "g1" }), p("2", "Beta", { groupId: "g1" }), p("3", "Gamma")], [g("g1", "Producción")], "producc");
    expect(ids(r)).toEqual([["g1", ["1", "2"]]]);
  });

  it("la búsqueda no distingue mayúsculas y mira host, base y usuario", () => {
    const r = buildSections([p("1", "A", { host: "db.EXAMPLE.com" })], [], "example");
    expect(ids(r)).toEqual([[NO_GROUP, ["1"]]]);
  });
});
