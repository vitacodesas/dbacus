// Organización de la lista de conexiones en secciones por grupo (spec 17). Solo presentación: los grupos y la
// pertenencia viven en Go; aquí se ordenan y filtran lo que Go ya devolvió.
import type { ConnectionGroup, ConnectionProfile } from "../types";

/** Clave de «Sin grupo» en los ajustes (`collapsedGroups`): no puede chocar con un uuid. */
export const NO_GROUP = "__none__";

export interface GroupSection {
  /** id del grupo, o NO_GROUP. */
  id: string;
  group: ConnectionGroup | null;
  profiles: ConnectionProfile[];
}

export interface GroupedList {
  /** Sin grupos definidos: se pinta la lista plana de siempre, sin cabeceras. */
  flat: boolean;
  sections: GroupSection[];
}

const matches = (p: ConnectionProfile, q: string) => [p.name, p.host, p.database, p.user].some((v) => v?.toLowerCase().includes(q));

/**
 * - Sin grupos: lista plana filtrada.
 * - Con grupos: una sección por grupo (el orden de `groups` se respeta) y «Sin grupo» al final si tiene conexiones.
 *   Un `groupId` huérfano (grupo inexistente) cae en «Sin grupo».
 * - Con búsqueda: se filtran las conexiones y se ocultan las secciones sin resultados; si el texto coincide con el
 *   nombre del grupo, se muestran todas sus conexiones. Sin búsqueda se ven también los grupos vacíos.
 */
export function buildSections(profiles: ConnectionProfile[], groups: ConnectionGroup[], query: string): GroupedList {
  const q = query.trim().toLowerCase();
  const keep = (p: ConnectionProfile) => !q || matches(p, q);

  if (!groups.length) return { flat: true, sections: [{ id: NO_GROUP, group: null, profiles: profiles.filter(keep) }] };

  const known = new Set(groups.map((g) => g.id));
  const sections: GroupSection[] = [];
  for (const g of groups) {
    const own = profiles.filter((p) => p.groupId === g.id);
    const nameHit = !!q && g.name.toLowerCase().includes(q);
    const shown = nameHit ? own : own.filter(keep);
    if (!q || shown.length) sections.push({ id: g.id, group: g, profiles: shown });
  }
  const loose = profiles.filter((p) => (!p.groupId || !known.has(p.groupId)) && keep(p));
  if (loose.length) sections.push({ id: NO_GROUP, group: null, profiles: loose });
  return { flat: false, sections };
}
