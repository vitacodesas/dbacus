import { beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { nextTick } from "vue";

// jsdom no tiene canvas: uPlot se sustituye por un doble que guarda lo que recibe.
const instances = vi.hoisted(() => [] as { opts: Record<string, any>; data: unknown[][]; destroyed: boolean }[]); // eslint-disable-line @typescript-eslint/no-explicit-any
vi.mock("uplot", () => {
  class FakeUPlot {
    static pxRatio = 1;
    destroyed = false;
    constructor(public opts: Record<string, unknown>, public data: unknown[][]) {
      instances.push(this as never);
    }
    destroy() { this.destroyed = true; }
    setSize() {}
    over = document.createElement("div");
    valToPos(v: number) { return (v - 1_000) / 10; }
    posToVal(px: number) { return 1_000 + px; }
  }
  return { default: FakeUPlot };
});

import { i18n } from "../../i18n";
import { useUiStore } from "../../stores/ui";
import TimeSeriesChart from "./TimeSeriesChart.vue";
import type { Series } from "../../types";

const at = (min: number) => new Date(Date.UTC(2026, 10, 4, 12, min)).toISOString();
const series = (over: Partial<Series> = {}): Series => ({
  metric: "qps", level: "raw", points: [{ t: at(0), v: 10 }, { t: at(1), v: 20 }, { t: at(30), v: 30 }],
  gaps: [{ from: at(1), to: at(30) }], annotations: [{ id: "1", profileId: "p", at: at(31), text: "deploy" }], ...over,
});
const mountChart = (s: Series) => mount(TimeSeriesChart, { props: { series: s, label: "QPS" }, global: { plugins: [i18n] } });

beforeEach(() => {
  setActivePinia(createPinia());
  instances.length = 0;
});

describe("TimeSeriesChart", () => {
  it("pinta con uPlot los datos de Go y corta la línea en el hueco", () => {
    mountChart(series());
    expect(instances).toHaveLength(1);
    const [xs, ys] = instances[0]!.data as [number[], (number | null)[]];
    expect(ys).toEqual([10, 20, null, 30]);
    expect(xs).toHaveLength(4);
    expect(instances[0]!.opts.series[1].spanGaps).toBe(false);
  });

  it("las bandas «sin datos» y las anotaciones se dibujan desde los huecos y anotaciones recibidos", () => {
    mountChart(series());
    const { hooks } = instances[0]!.opts;
    const calls: string[] = [];
    const ctx = new Proxy({}, { get: (_t, k) => (k === "fillText" ? (txt: string) => calls.push(`text:${txt}`) : () => calls.push(String(k))), set: () => true });
    const u = { ctx, bbox: { left: 0, top: 0, width: 100_000, height: 100 }, valToPos: (v: number) => v - Date.parse(at(0)) / 1000 };
    hooks.drawClear[0](u);
    expect(calls).toContain("fillRect");
    expect(calls).toContain("text:sin datos");
    calls.length = 0;
    hooks.draw[0](u);
    expect(calls).toContain("stroke");
    expect(calls).toContain("text:deploy");
  });

  it("al cambiar la serie reconstruye y destruye la anterior; al desmontar destruye", async () => {
    const w = mountChart(series());
    await w.setProps({ series: series({ points: [{ t: at(0), v: 1 }, { t: at(1), v: 2 }], gaps: [], annotations: [] }) });
    expect(instances).toHaveLength(2);
    expect(instances[0]!.destroyed).toBe(true);
    expect(instances[1]!.destroyed).toBe(false);
    w.unmount();
    expect(instances[1]!.destroyed).toBe(true);
  });

  it("sin puntos no crea el gráfico y avisa", () => {
    const w = mountChart(series({ points: null, gaps: null, annotations: null }));
    expect(instances).toHaveLength(0);
    expect(w.get("[data-testid=chart-empty]").text()).toContain("Sin datos");
  });

  it("al cambiar de tema se repinta (canvas no resuelve variables CSS)", async () => {
    mountChart(series());
    const ui = useUiStore();
    ui.theme = ui.resolvedTheme === "dark" ? "light" : "dark";
    await nextTick();
    await nextTick();
    expect(instances.length).toBeGreaterThanOrEqual(2);
  });

  it("la tabla de valores alternativa solo se monta al abrirla y lista cada punto", async () => {
    const w = mountChart(series());
    expect(w.find("[data-testid=chart-data]").exists()).toBe(false);
    await w.get("[data-testid=chart-toggle-data]").trigger("click");
    expect(w.findAll("[data-testid=chart-data] tbody tr")).toHaveLength(3);
    expect(w.get("[role=img]").attributes("aria-label")).toContain("3 puntos");
  });

  it("300 puntos (el máximo que entrega Go) se pintan de una vez", () => {
    const pts = Array.from({ length: 300 }, (_, i) => ({ t: at(i), v: i }));
    mountChart(series({ points: pts, gaps: [], annotations: [] }));
    expect(instances[0]!.data[0]).toHaveLength(300);
  });

  it("anotable: el clic emite la hora del punto pulsado (la nota la crea Go); sin «annotatable» no se engancha", async () => {
    const w = mount(TimeSeriesChart, { props: { series: series(), label: "QPS", annotatable: true }, global: { plugins: [i18n] } });
    const over = (instances[0] as unknown as { over: HTMLElement }).over;
    expect(over.style.cursor).toBe("copy");
    over.dispatchEvent(new MouseEvent("click", { clientX: 500 }));
    expect(w.emitted("annotate")).toEqual([[new Date(1_500 * 1000).toISOString()]]);

    instances.length = 0;
    const plain = mountChart(series());
    (instances[0] as unknown as { over: HTMLElement }).over.dispatchEvent(new MouseEvent("click", { clientX: 500 }));
    expect(plain.emitted("annotate")).toBeUndefined();
  });
});
