import { useState, type ReactNode } from "react";
import type { AliasUsageRow, KeyPublic } from "../types";
import { fetchKeyUsage, quotaStatus } from "../api/keys";
import { useT } from "../i18n";
import QuotaMeter from "./QuotaMeter";
import { formatUsd } from "../quota";

export default function KeyCard({
  k,
  actions,
  selected,
  onToggleSelect,
}: {
  k: KeyPublic;
  actions?: ReactNode;
  selected?: boolean;
  onToggleSelect?: () => void;
}) {
  const t = useT();
  const st = quotaStatus(k);
  const statusLabel =
    st === "disabled" ? t("keys.disabled")
      : st === "daily" ? t("usage.statusDaily")
        : st === "weekly" ? t("usage.statusWeekly")
          : t("usage.statusOk");
  const over = st === "daily" || st === "weekly";
  const [open, setOpen] = useState(false);
  const [models, setModels] = useState<AliasUsageRow[] | null>(null);
  const [modelErr, setModelErr] = useState("");

  const toggleModels = async () => {
    if (open) {
      setOpen(false);
      return;
    }
    setOpen(true);
    setModelErr("");
    try {
      const detail = await fetchKeyUsage(k.id);
      setModels(detail.models ?? []);
    } catch (e) {
      setModelErr((e as Error).message ?? t("keys.loadFailed"));
    }
  };

  return (
    <div
      data-key-id={k.id}
      className={"keycard" + (k.enabled ? "" : " disabled") + (over ? " over" : "") + (selected ? " selected" : "")}
    >
      <div className="kc-head">
        <button
          type="button"
          className={"kc-check" + (selected ? " on" : "")}
          aria-pressed={!!selected}
          onClick={(e) => { e.stopPropagation(); onToggleSelect?.(); }}
        />
        <span className="kc-dot" />
        <span className="kc-name">{k.name}</span>
        <span className={"tag" + (k.enabled ? (over ? " off" : " on") : " off")}>{statusLabel}</span>
      </div>
      <div className="kc-preview">{k.key_preview}</div>
      <div className="kc-spend">{t("usage.calls")}: {k.usage.daily_call_count ?? 0}</div>
      <QuotaMeter
        label="24H"
        used={k.usage.daily_usd}
        limit={k.daily_limit_usd}
        resetAt={k.usage.daily_reset_at}
      />
      <QuotaMeter
        label="7D"
        used={k.usage.weekly_usd}
        limit={k.weekly_limit_usd}
        resetAt={k.usage.weekly_reset_at}
      />
      {open && (
        <div className="kc-models">
          {modelErr && <div className="muted" style={{ color: "var(--danger)" }}>{modelErr}</div>}
          {!modelErr && models && models.length === 0 && (
            <div className="muted">{t("usage.noModels")}</div>
          )}
          {models && models.length > 0 && (
            <table>
              <thead>
                <tr>
                  <th>{t("usage.model")}</th>
                  <th className="num">{t("usage.today")}</th>
                  <th className="num">{t("usage.calls")}</th>
                </tr>
              </thead>
              <tbody>
                {models.map((row) => (
                  <tr key={row.alias}>
                    <td className="mono">{row.alias}</td>
                    <td className="num">{formatUsd(row.daily?.total_usd ?? 0)}</td>
                    <td className="num">{row.daily?.call_count ?? 0}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
      <div className="kc-actions">
        <button className="btn sm" type="button" onClick={() => void toggleModels()}>
          {open ? t("usage.hideModels") : t("usage.showModels")}
        </button>
        {actions}
      </div>
    </div>
  );
}
