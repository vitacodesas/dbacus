import { describe, expect, it } from "vitest";
import { blankColumn, blankFk, blankIndex, buildChangeSet, columnsFromNames, draftsFromModel, splitList } from "./alterOps";
import type { AlterModel, Column } from "../types";

const col = (name: string, extra: Partial<Column> = {}): Column => ({
  name, dataType: "int", columnType: "int", nullable: false, default: null, isPrimaryKey: false, isAutoInc: false, isGenerated: false, comment: "", ordinal: 0,
  defaultExpr: false, charset: "", collation: "", onUpdate: "", generationExpr: "", ...extra,
});
const model = (): AlterModel => ({
  database: "app", table: "t", fingerprint: "fp", createSql: "", tableCollation: "", rowEstimate: null, incomingFks: [],
  columns: [col("id", { isPrimaryKey: true, isAutoInc: true }), col("a", { columnType: "varchar(10)", nullable: true }), col("b"), col("c"), col("gen", { isGenerated: true })],
  indexes: [
    { name: "PRIMARY", columns: ["id"], unique: true, type: "BTREE", prefix: false, prefixLens: [0], desc: [false], functional: false },
    { name: "idx_a", columns: ["a", "b"], unique: false, type: "BTREE", prefix: false, prefixLens: [0, 0], desc: [false, true], functional: false },
    { name: "ft", columns: ["a"], unique: false, type: "FULLTEXT", prefix: false, prefixLens: [0], desc: [false], functional: false },
  ],
  foreignKeys: [{ name: "fk_b", columns: ["b"], refSchema: "app", refTable: "other", refColumns: ["id"], onUpdate: "RESTRICT", onDelete: "CASCADE" }],
});
const build = (mut: (d: ReturnType<typeof draftsFromModel>) => void = () => {}) => {
  const m = model();
  const d = draftsFromModel(m);
  mut(d);
  return buildChangeSet(m, d, "app", "t");
};
const kinds = (cs: ReturnType<typeof build>) => (cs.ops ?? []).map((o) => `${o.kind}:${o.name}`);

describe("buildChangeSet", () => {
  it("sin tocar nada no hay operaciones y lleva la huella del modelo", () => {
    const cs = build();
    expect(cs.ops).toEqual([]);
    expect(cs.fingerprint).toBe("fp");
    expect([cs.database, cs.table]).toEqual(["app", "t"]);
  });

  it("cambiar una propiedad envía la definición; renombrar sola no la envía (Go parte de la actual)", () => {
    let cs = build((d) => (d.cols[1].type = "varchar(50)"));
    expect(cs.ops?.[0]).toMatchObject({ kind: "modifyColumn", name: "a", newName: "", column: { type: "varchar(50)", nullable: true } });

    cs = build((d) => (d.cols[1].name = "alpha"));
    expect(cs.ops?.[0]).toMatchObject({ kind: "modifyColumn", name: "a", newName: "alpha", column: null });
  });

  it("detecta cambios de nulabilidad, default, auto_increment y comentario", () => {
    expect(kinds(build((d) => (d.cols[2].nullable = true)))).toEqual(["modifyColumn:b"]);
    expect(kinds(build((d) => (d.cols[2].def = "0")))).toEqual(["modifyColumn:b"]);
    expect(kinds(build((d) => (d.cols[2].comment = "x")))).toEqual(["modifyColumn:b"]);
    expect(kinds(build((d) => (d.cols[2].autoInc = true)))).toEqual(["modifyColumn:b"]);
  });

  it("una columna generada bloqueada nunca genera operaciones aunque se toque", () => {
    expect(build((d) => (d.cols[4].type = "bigint")).ops).toEqual([]);
  });

  it("añadir al final no lleva posición; añadir en medio, sí", () => {
    let cs = build((d) => d.cols.push({ ...blankColumn(), name: "z", type: "int" }));
    expect(cs.ops?.[0]).toMatchObject({ kind: "addColumn", name: "z", first: false, after: "" });

    cs = build((d) => d.cols.splice(2, 0, { ...blankColumn(), name: "z", type: "int" }));
    expect(cs.ops?.[0]).toMatchObject({ kind: "addColumn", name: "z", after: "a" });

    cs = build((d) => d.cols.unshift({ ...blankColumn(), name: "z", type: "int" }));
    expect(cs.ops?.[0]).toMatchObject({ kind: "addColumn", name: "z", first: true });
  });

  it("mover una columna emite solo lo necesario (simula el orden real)", () => {
    // [id,a,b,c,gen] → [id,a,c,b,gen]: basta mover c tras a; b y gen ya quedan bien.
    const cs = build((d) => d.cols.splice(2, 2, d.cols[3], d.cols[2]));
    expect(cs.ops).toHaveLength(1);
    expect(cs.ops?.[0]).toMatchObject({ kind: "modifyColumn", name: "c", after: "a", column: null });

    // Pasar la última al principio: una sola operación.
    const cs2 = build((d) => d.cols.unshift(d.cols.splice(3, 1)[0]));
    expect(cs2.ops).toHaveLength(1);
    expect(cs2.ops?.[0]).toMatchObject({ name: "c", first: true });
  });

  it("borrar una columna emite dropColumn antes que el resto", () => {
    const cs = build((d) => {
      d.cols[2].deleted = true;
      d.cols[1].type = "varchar(20)";
    });
    expect(kinds(cs)).toEqual(["dropColumn:b", "modifyColumn:a"]);
  });

  it("un índice con otras columnas o tipo se rehace; renombrarlo solo lo renombra; borrarlo lo borra", () => {
    let cs = build((d) => (d.idxs[1].kind = "unique"));
    expect(kinds(cs)).toEqual(["dropIndex:idx_a", "addIndex:idx_a"]);
    expect(cs.ops?.[1].index).toMatchObject({ kind: "unique", columns: [{ name: "a", desc: false, prefix: 0 }, { name: "b", desc: true, prefix: 0 }] });

    cs = build((d) => (d.idxs[1].name = "idx_new"));
    expect(kinds(cs)).toEqual(["renameIndex:idx_a"]);
    expect(cs.ops?.[0].newName).toBe("idx_new");

    cs = build((d) => (d.idxs[1].deleted = true));
    expect(kinds(cs)).toEqual(["dropIndex:idx_a"]);

    cs = build((d) => d.idxs.push({ ...blankIndex(), name: "n", kind: "fulltext", columns: [{ name: "a", desc: false, prefix: 0 }] }));
    expect(cs.ops?.[0]).toMatchObject({ kind: "addIndex", name: "n", index: { kind: "fulltext" } });
  });

  it("PRIMARY nunca genera operaciones", () => {
    expect(build((d) => { d.idxs[0].deleted = true; d.idxs[0].name = "x"; }).ops).toEqual([]);
  });

  it("una clave foránea modificada se borra y se vuelve a crear con el mismo nombre", () => {
    const cs = build((d) => (d.fks[0].onDelete = "SET NULL"));
    expect(kinds(cs)).toEqual(["dropFK:fk_b", "addFK:fk_b"]);
    expect(cs.ops?.[1].fk).toMatchObject({ refTable: "other", onDelete: "SET NULL", columns: ["b"], refColumns: ["id"] });
  });

  it("orden global: borrados, columnas, renombres de índice, índices y claves nuevas", () => {
    const cs = build((d) => {
      d.fks[0].deleted = true;
      d.idxs[1].deleted = true;
      d.cols[3].deleted = true;
      d.cols[1].type = "varchar(99)";
      d.idxs.push({ ...blankIndex(), name: "n", columns: [{ name: "a", desc: false, prefix: 0 }] });
      d.fks.push({ ...blankFk(), name: "fk_n", columns: ["a"], refTable: "x", refColumns: ["id"] });
    });
    expect(kinds(cs)).toEqual(["dropFK:fk_b", "dropIndex:idx_a", "dropColumn:c", "modifyColumn:a", "addIndex:n", "addFK:fk_n"]);
  });

  it("no envía SQL: solo operaciones tipadas", () => {
    expect(JSON.stringify(build((d) => (d.cols[1].type = "text")))).not.toMatch(/ALTER|MODIFY COLUMN/i);
  });
});

describe("listas de columnas", () => {
  it("splitList recorta y descarta vacíos", () => expect(splitList(" a, b ,, c ")).toEqual(["a", "b", "c"]));
  it("columnsFromNames conserva prefijo y orden de las columnas que siguen", () => {
    const prev = [{ name: "a", desc: true, prefix: 10 }, { name: "b", desc: false, prefix: 0 }];
    expect(columnsFromNames(["b", "a", "z"], prev)).toEqual([{ name: "b", desc: false, prefix: 0 }, { name: "a", desc: true, prefix: 10 }, { name: "z", desc: false, prefix: 0 }]);
  });
});
