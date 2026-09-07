import { useCallback, useEffect, useState } from "react";
import { listKeys, quotaStatus } from "../api/keys";
import type { KeyPublic } from "../types";
import { useT } from "../i18n";

export default function Usage() {
  const t = useT();
  const [keys, setKeys] = useState<KeyPublic[]>([]);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setError("");
    try {
      setKeys(await listKeys());
    } catch (e) {
      setError((e as Error).message ?? t("keys.loadFailed"));
    }
  }, [t]);

  useEffect(() => { void load(); }, [load]);

  const statusLabel = (k: KeyPublic) => {
    const st = quotaStatus(k);
    if (st === "disabled") return t("usage.statusDisabled");
    if (st === "daily") return t("usage.statusDaily");
    if (st === "weekly") return t("usage.statusWeekly");
    return t("usage.statusOk");
  };

  return (
    <div>
      <div className="fp-head" style={{ margin: "0 0 16px" }}>
        <h1>{t("usage.title")}</h1>
        <button className="btn sm" onClick={() => void load()}>{t("keys.refresh")}</button>
      </div>
      {error && <div className="card" style={{ color: "var(--danger)" }}>{error}</div>}
      {keys.length === 0 && <div className="card muted">{t("usage.empty")}</div>}
      {keys.map((k) => (
        <div key={k.id} className="keycard">
          <div className="keycard-title">{k.name} <span className="muted">{k.key_preview}</span></div>
          <div>{t("usage.today")}: ${k.usage.daily_usd.toFixed(4)}</div>
          <div>{t("usage.week")}: ${k.usage.weekly_usd.toFixed(4)}</div>
          <div>{t("usage.calls")}: {k.usage.daily_call_count ?? 0}</div>
          <div>{t("keys.colStatus")}: {statusLabel(k)}</div>
        </div>
      ))}
      <p className="muted" style={{ marginTop: 24 }}>{t("usage.footer")}</p>
    </div>
  );
}
