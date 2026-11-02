import { useCallback, useEffect, useId, useRef, useState } from "react";
import { listCodexAuths } from "../api/keys";
import { errText } from "../api/error";
import { useT } from "../i18n";
import type { CodexSchedulerDiagnostics } from "../types";

export default function SchedulerDiagnostics() {
  const t = useT();
  const titleId = useId();
  const [diagnostics, setDiagnostics] = useState<CodexSchedulerDiagnostics | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const requestSequence = useRef(0);

  const refresh = useCallback(async () => {
    const sequence = ++requestSequence.current;
    setLoading(true);
    setError("");
    try {
      const response = await listCodexAuths();
      if (sequence === requestSequence.current) setDiagnostics(response.diagnostics ?? null);
    } catch (ex) {
      if (sequence === requestSequence.current) {
        setDiagnostics(null);
        setError(errText(ex, ""));
      }
    } finally {
      if (sequence === requestSequence.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
    return () => { requestSequence.current++; };
  }, [refresh]);

  const count = diagnostics?.scheduler_pick_count ?? null;
  const lastDate = diagnostics?.last_scheduler_pick_at ? new Date(diagnostics.last_scheduler_pick_at) : null;
  const lastUtc = lastDate && !Number.isNaN(lastDate.getTime()) ? lastDate.toISOString() : null;

  return (
    <section className="scheduler-diagnostics" aria-labelledby={titleId}>
      <div className="scheduler-diagnostics-head">
        <h2 id={titleId}>{t("schedulerDiagnostics.title")}</h2>
        <button className="btn sm" type="button" onClick={() => void refresh()} disabled={loading}>
          {loading ? t("schedulerDiagnostics.loading") : t("schedulerDiagnostics.refresh")}
        </button>
      </div>
      <div aria-live="polite" aria-busy={loading}>
        <dl>
          <div>
            <dt>{t("schedulerDiagnostics.pickCount")}</dt>
            <dd>{count === null ? t("schedulerDiagnostics.unknownCount") : count}</dd>
          </div>
          <div>
            <dt>{t("schedulerDiagnostics.lastPick")}</dt>
            <dd>{lastUtc ? <time dateTime={lastUtc}>{lastDate!.toLocaleString()}</time> : t("schedulerDiagnostics.unknownTime")}</dd>
          </div>
        </dl>
        {!loading && count === null && (
          <p className="scheduler-diagnostics-note">{t("schedulerDiagnostics.unavailable")}</p>
        )}
        {error && <p className="scheduler-diagnostics-note scheduler-diagnostics-error" role="alert">{error}</p>}
      </div>
    </section>
  );
}
