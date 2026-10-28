import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { i18n } from "../../i18n";

const api = vi.hoisted(() => ({ execute: vi.fn(), cancel: vi.fn(), getRows: vi.fn(), getCell: vi.fn(), rowsAsInsert: vi.fn() }));
vi.mock("../../api/queries", () => api);

import { useResultsStore, WINDOW } from "../../stores/results";
import ResultGrid from "./ResultGrid.vue";

const columns = [
  { name: "id", dataType: "INT", nullable: false, precision: null, scale: null },
  { name: "name", dataType: "VARCHAR", nullable: true, precision: null, scale: null },
];
const TAB = "t1";

async function setup(rowCount: number) {
  const results = useResultsStore();
  api.execute.mockResolvedValue("q1");
  await results.execute({ tabId: TAB, sessionId: "s", sql: "x", baseOffset: 0, database: "", autoLimit: 0 });
  results.handleStarted({ queryId: "q1", tabId: TAB, sessionId: "s", startedAt: "" });
  results.handleProgress({ queryId: "q1", stmtIndex: 0, rowCount });
  api.getRows.mockImplementation(async (_q, _s, off: number, lim: number) => ({
    offset: off, total: rowCount, complete: true,
    rows: Array.from({ length: Math.min(lim, rowCount - off) }, (_, i) => [{ v: off + i + 1 }, i % 7 === 0 ? { v: null } : { v: `fila ${off + i + 1}` }]),
  }));
  const w = mount(ResultGrid, { props: { tabId: TAB, queryId: "q1", stmtIndex: 0, columns, rowCount }, global: { plugins: [i18n] }, attachTo: document.body });
  await flushPromises();
  return { w, results };
}

describe("ResultGrid", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    Object.values(api).forEach((f) => f.mockReset());
  });

  it("pinta encabezados y las primeras filas con datos de Go", async () => {
    const { w } = await setup(1000);
    expect(w.findAll('[role="columnheader"]').map((h) => h.find("span").text())).toEqual(["id", "name"]);
    const first = w.findAll('[role="row"][aria-rowindex]')[0];
    expect(first.findAll('[role="gridcell"]').map((c) => c.text())).toEqual(["1", "NULL"]);
    expect(w.find('[role="grid"]').attributes("aria-rowcount")).toBe("1001");
  });

  it("virtualiza: con 5 millones de filas solo monta unas decenas", async () => {
    const { w } = await setup(5_000_000);
    const mounted = w.findAll('[role="row"][aria-rowindex]').length;
    expect(mounted).toBeGreaterThan(5);
    expect(mounted).toBeLessThan(80);
    expect(w.get('[role="grid"] > div').attributes("style")).toContain("120000028px"); // 28 + 5e6 * 24
  });

  it("al desplazarse pide la ventana que le toca y no las demás", async () => {
    const { w } = await setup(100_000);
    api.getRows.mockClear();
    const grid = w.get('[role="grid"]');
    (grid.element as HTMLElement).scrollTop = 50_000 * 24;
    await grid.trigger("scroll");
    await flushPromises();
    const offsets = api.getRows.mock.calls.map((c) => c[2] as number);
    expect(offsets.length).toBeGreaterThan(0);
    expect(offsets.every((o) => o >= 49_000 && o <= 51_000)).toBe(true);
    expect(offsets.every((o) => o % WINDOW === 0)).toBe(true);
  });

  it("emite el rango visible para el pie de estado", async () => {
    const { w } = await setup(1000);
    const emitted = w.emitted("visibleRange");
    expect(emitted?.at(-1)?.[0]).toBe(0);
  });

  it("las flechas mueven la selección", async () => {
    const { w } = await setup(1000);
    const grid = w.get('[role="grid"]');
    await grid.trigger("keydown", { key: "ArrowDown" });
    await grid.trigger("keydown", { key: "ArrowRight" });
    await flushPromises();
    expect(w.findAll(".outline-accent")).toHaveLength(1);
    expect(w.get(".outline-accent").text()).toBe("fila 2"); // fila de índice 1, columna "name"
  });

  it("Esc cancela cuando la consulta sigue corriendo", async () => {
    const { w } = await setup(10);
    await w.get('[role="grid"]').trigger("keydown", { key: "Escape" });
    expect(w.emitted("cancel")).toHaveLength(1);
  });
});
