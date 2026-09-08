import { formatResetShort } from "../formatTime";
import { quotaAmountLabel, quotaProgress } from "../quota";
import { useT } from "../i18n";

export default function QuotaMeter({
  label,
  used,
  limit,
  resetAt,
}: {
  label: string;
  used: number;
  limit: number;
  resetAt?: string | null;
}) {
  const t = useT();
  const { pct, fill, tone } = quotaProgress(used, limit);
  const when = formatResetShort(resetAt) ?? t("usage.resetPending");
  const pctLabel = pct == null ? t("keys.unlimited") : `${pct}%`;
  return (
    <div className={"quota-meter is-" + tone}>
      <span className="quota-pill">{label}</span>
      <div className="quota-main">
        <div className="quota-track" aria-hidden="true">
          <span style={{ width: fill + "%" }} />
        </div>
        <div className="quota-meta">
          <span className="quota-amt">{quotaAmountLabel(used, limit)}</span>
          <span className="quota-pct">{pctLabel}</span>
          <span className="quota-when">{when}</span>
        </div>
      </div>
    </div>
  );
}
