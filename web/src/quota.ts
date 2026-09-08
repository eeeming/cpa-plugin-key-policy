export type QuotaTone = "ok" | "warn" | "over" | "none";

export function formatUsd(n: number): string {
  if (!Number.isFinite(n)) return "$0.0000";
  return "$" + n.toFixed(4);
}

export function quotaAmountLabel(used: number, limit: number): string {
  if (!(limit > 0)) {
    return formatUsd(used);
  }
  return `${formatUsd(used)} / ${formatUsd(limit)}`;
}

export function quotaProgress(used: number, limit: number): { pct: number | null; fill: number; tone: QuotaTone } {
  if (!(limit > 0)) {
    return { pct: null, fill: 0, tone: "none" };
  }
  const ratio = used / limit;
  const pct = Math.max(0, Math.round(ratio * 100));
  const fill = Math.max(0, Math.min(100, ratio * 100));
  if (ratio >= 1) return { pct, fill, tone: "over" };
  if (ratio >= 0.96) return { pct, fill, tone: "warn" };
  return { pct, fill, tone: "ok" };
}
