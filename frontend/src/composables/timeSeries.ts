import type { DigestDetail, Series, TimeGap } from "../types";

const sec = (iso: string) => Date.parse(iso) / 1000;

/**
 * Datos alineados para uPlot. Un hueco entre dos puntos recibe un `null` en su punto medio: así la línea se corta en
 * lugar de unir los dos lados (los huecos se marcan, no se interpolan: spec 22). Los puntos y los huecos ya vienen
 * ordenados de Go; aquí solo se adaptan para pintar.
 */
export function buildData(pts: Series["points"], gs: TimeGap[] | null): [number[], (number | null)[]] {
  const xs: number[] = [];
  const ys: (number | null)[] = [];
  const gaps = (gs ?? []).map((g) => [sec(g.from), sec(g.to)] as const);
  let gi = 0;
  (pts ?? []).forEach((p, i) => {
    const x = sec(p.t);
    if (i > 0) {
      const prev = xs[xs.length - 1]!;
      while (gi < gaps.length && gaps[gi]![1] <= prev) gi++;
      const g = gaps[gi];
      if (g && g[0] < x && g[1] > prev) {
        xs.push((Math.max(g[0], prev) + Math.min(g[1], x)) / 2);
        ys.push(null);
      }
    }
    xs.push(x);
    ys.push(p.v);
  });
  return [xs, ys];
}

/** Extremos del eje X: el rango pedido (los huecos de cabecera y cola lo delimitan) o, si no hay, los puntos. */
export function seriesDomain(s: Series): [number, number] | null {
  const p = s.points ?? [];
  const g = s.gaps ?? [];
  const lo = [p[0] && sec(p[0].t), g[0] && sec(g[0].from)].filter((v): v is number => typeof v === "number");
  const hi = [p.length ? sec(p[p.length - 1]!.t) : undefined, g.length ? sec(g[g.length - 1]!.to) : undefined].filter((v): v is number => typeof v === "number");
  return lo.length && hi.length ? [Math.min(...lo), Math.max(...hi)] : null;
}

export const formatCompact = (v: number, locale?: string) => new Intl.NumberFormat(locale, { notation: "compact", maximumFractionDigits: 1 }).format(v);

/** Adapta la serie propia de un digest (Go ya la entrega con sus huecos) a la forma que pinta TimeSeriesChart. */
export function digestSeries(d: DigestDetail, field: "execs" | "avgMs" | "totalMs"): Series {
  return { metric: field, level: "raw", points: (d.points ?? []).map((p) => ({ t: p.t, v: p[field] })), gaps: d.gaps ?? [], annotations: [] };
}
