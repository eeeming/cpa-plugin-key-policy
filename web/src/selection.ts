export type Rect = { x: number; y: number; w: number; h: number };

export function toggleId(selected: string[], id: string): string[] {
  return selected.includes(id) ? selected.filter((x) => x !== id) : [...selected, id];
}

export function selectAll(ids: string[]): string[] {
  return [...ids];
}

export function invertSelection(selected: string[], ids: string[]): string[] {
  const set = new Set(selected);
  return ids.filter((id) => !set.has(id));
}

export function normalizeRect(x0: number, y0: number, x1: number, y1: number): Rect {
  const x = Math.min(x0, x1);
  const y = Math.min(y0, y1);
  return { x, y, w: Math.abs(x1 - x0), h: Math.abs(y1 - y0) };
}

export function rectsIntersect(a: Rect, b: Rect): boolean {
  return a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;
}

export function idsHitByMarquee(marquee: Rect, cards: { id: string; rect: Rect }[]): string[] {
  if (marquee.w <= 0 && marquee.h <= 0) return [];
  return cards.filter((c) => rectsIntersect(marquee, c.rect)).map((c) => c.id);
}

export function mergeSelection(current: string[], added: string[]): string[] {
  const set = new Set(current);
  for (const id of added) set.add(id);
  return [...set];
}

// Drop selected ids that no longer exist, so a reload cannot leave the bulk
// toolbar acting on ghosts (which would fail every request with 404).
export function pruneSelection(selected: string[], ids: string[]): string[] {
  if (selected.length === 0) return selected;
  const live = new Set(ids);
  const next = selected.filter((id) => live.has(id));
  return next.length === selected.length ? selected : next;
}
