const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 24 * 3600], ["month", 30 * 24 * 3600], ["day", 24 * 3600], ["hour", 3600], ["minute", 60],
];

/** "hace 3 minutos" / "3 minutes ago"; por debajo de un minuto, "ahora". `now` se inyecta para poder probarlo. */
export function relativeTime(iso: string | Date, locale: string, now = Date.now()): string {
  const secs = Math.round((new Date(iso).getTime() - now) / 1000);
  const fmt = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  const abs = Math.abs(secs);
  for (const [unit, size] of UNITS) if (abs >= size) return fmt.format(Math.round(secs / size), unit);
  return fmt.format(0, "second");
}

/** Duración corta: 850 ms, 2,4 s, 1 min 5 s. */
export function formatDuration(ms: number, locale: string): string {
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const nf = new Intl.NumberFormat(locale, { maximumFractionDigits: 1 });
  if (ms < 60_000) return `${nf.format(ms / 1000)} s`;
  const m = Math.floor(ms / 60_000);
  return `${m} min ${Math.round((ms % 60_000) / 1000)} s`;
}

export function formatBytes(n: number, locale: string): string {
  const nf = new Intl.NumberFormat(locale, { maximumFractionDigits: 1 });
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${nf.format(n / 1024)} KB`;
  if (n < 1024 ** 3) return `${nf.format(n / 1024 ** 2)} MB`;
  return `${nf.format(n / 1024 ** 3)} GB`;
}
