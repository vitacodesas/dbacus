/** Copia de `rec` sin la clave `key` (para actualizar estado reactivo de forma inmutable). */
export function omit<T>(rec: Record<string, T>, key: string): Record<string, T> {
  if (!(key in rec)) return rec;
  const copy = { ...rec };
  delete copy[key];
  return copy;
}

export function omitWhere<T>(rec: Record<string, T>, pred: (key: string) => boolean): Record<string, T> {
  return Object.fromEntries(Object.entries(rec).filter(([k]) => !pred(k)));
}
