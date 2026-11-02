import { useEffect, useId, useRef, useState } from "react";
import { batchCodexBinding } from "../api/keys";
import { errText } from "../api/error";
import { addAuthIds, initialCodexBinding } from "../codexBinding";
import { useT } from "../i18n";
import type { BatchCodexBindingRequest, BatchCodexBindingResponse } from "../types";
import CodexBindingFields from "./CodexBindingFields";

export default function BatchCodexBindingModal({ ids, onClose, onApplied }: {
  ids: string[];
  onClose: () => void;
  onApplied: (updated: number) => Promise<void>;
}) {
  const t = useT();
  const id = useId();
  const formRef = useRef<HTMLFormElement>(null);
  const previewTitleRef = useRef<HTMLHeadingElement>(null);
  const [accountsMode, setAccountsMode] = useState<NonNullable<BatchCodexBindingRequest["accounts_mode"]>>("keep");
  const [accounts, setAccounts] = useState(initialCodexBinding);
  const [manualText, setManualText] = useState("");
  const [groupMode, setGroupMode] = useState("keep");
  const [group, setGroup] = useState("");
  const [fallback, setFallback] = useState("keep");
  const [clear, setClear] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<{ request: BatchCodexBindingRequest; response: BatchCodexBindingResponse } | null>(null);

  useEffect(() => {
    const previous = document.activeElement;
    formRef.current?.querySelector<HTMLElement>("input, select, button")?.focus();
    return () => { if (previous instanceof HTMLElement && previous.isConnected) previous.focus(); };
  }, []);

  useEffect(() => {
    if (busy) formRef.current?.focus();
    else if (preview) previewTitleRef.current?.focus();
  }, [busy, preview]);

  const invalidate = () => {
    setPreview(null);
    setError("");
  };

  const request = (): BatchCodexBindingRequest | null => {
    if (clear) return { ids: [...ids], clear: true };
    if (accountsMode === "keep" && groupMode === "keep" && fallback === "keep") return null;
    return {
      ids: [...ids],
      accounts_mode: accountsMode,
      ...(accountsMode !== "keep" ? { codex_auth_ids: addAuthIds(accounts.ids, manualText) } : {}),
      ...(groupMode === "set" ? { codex_group: group.trim() } : {}),
      ...(fallback !== "keep" ? { codex_fallback: fallback === "on" } : {}),
    };
  };

  const showPreview = async (event: React.FormEvent) => {
    event.preventDefault();
    if (busy) return;
    const next = request();
    setPreview(null);
    if (!next) { setError(t("batchCodex.noAction")); return; }
    setBusy(true);
    setError("");
    try {
      const response = await batchCodexBinding({ ...next, dry_run: true });
      setPreview({ request: next, response });
    } catch (ex) {
      setError(errText(ex, t("batchCodex.failed")));
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    if (!preview || busy) return;
    setBusy(true);
    setError("");
    try {
      const response = await batchCodexBinding({
        ...preview.request,
        dry_run: false,
        expected: preview.response.changes.map(({ id: keyId, before }) => ({ id: keyId, before })),
      });
      await onApplied(response.updated);
    } catch (ex) {
      setPreview(null);
      setError(errText(ex, t("batchCodex.failed")));
    } finally {
      setBusy(false);
    }
  };

  const onKeyDown = (event: React.KeyboardEvent) => {
    if (event.key === "Escape") { event.preventDefault(); if (!busy) onClose(); }
    if (event.key !== "Tab") return;
    const controls = [...(formRef.current?.querySelectorAll<HTMLElement>("input:not(:disabled), select:not(:disabled), textarea:not(:disabled), button:not(:disabled), [tabindex='0']") ?? [])]
      .filter((control) => !control.matches(":disabled"));
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (!first) { event.preventDefault(); formRef.current?.focus(); return; }
    if (event.shiftKey && (document.activeElement === first || document.activeElement === formRef.current)) {
      event.preventDefault(); last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault(); first.focus();
    }
  };

  return (
    <div className="modal-overlay" onClick={() => { if (!busy) onClose(); }}>
      <form ref={formRef} className="modal batch-codex-modal" role="dialog" aria-modal="true" aria-labelledby={`${id}-title`} aria-describedby={`${id}-hint`} aria-busy={busy} tabIndex={-1}
        onClick={(event) => event.stopPropagation()} onKeyDown={onKeyDown} onSubmit={(event) => void showPreview(event)}>
        <h2 id={`${id}-title`}>{t("batchCodex.title")}</h2>
        <p id={`${id}-hint`} className="muted">{t("batchCodex.hint", { count: ids.length })}</p>
        <fieldset className="batch-codex-controls" disabled={busy}>
          <label className="codex-fallback" htmlFor={`${id}-clear`}>
            <input id={`${id}-clear`} type="checkbox" checked={clear} onChange={(event) => { setClear(event.target.checked); invalidate(); }} />
            {t("batchCodex.clear")}
          </label>
          {clear ? <p className="muted codex-hint">{t("batchCodex.clearHint")}</p> : <>
            <div className="form-row">
              <label htmlFor={`${id}-accounts-mode`}>{t("batchCodex.accountsMode")}</label>
              <select id={`${id}-accounts-mode`} className="input" value={accountsMode} onChange={(event) => { setAccountsMode(event.target.value as typeof accountsMode); invalidate(); }}>
                <option value="keep">{t("batchCodex.keep")}</option>
                <option value="set">{t("batchCodex.set")}</option>
                <option value="add">{t("batchCodex.add")}</option>
                <option value="remove">{t("batchCodex.remove")}</option>
              </select>
            </div>
            {accountsMode !== "keep" && <>
              <p className="muted codex-hint">{t("batchCodex.accountsHint")}</p>
              <CodexBindingFields accountsOnly value={accounts} manualText={manualText}
                onChange={(value) => { setAccounts(value); invalidate(); }}
                onManualText={(text) => { setManualText(text); invalidate(); }} />
            </>}
            <div className="form-row">
              <label htmlFor={`${id}-group-mode`}>{t("batchCodex.groupMode")}</label>
              <select id={`${id}-group-mode`} className="input" value={groupMode} onChange={(event) => { setGroupMode(event.target.value); invalidate(); }}>
                <option value="keep">{t("batchCodex.keep")}</option>
                <option value="set">{t("batchCodex.groupSet")}</option>
              </select>
            </div>
            {groupMode === "set" && <div className="form-row">
              <label htmlFor={`${id}-group`}>{t("codex.group")}</label>
              <input id={`${id}-group`} className="input" value={group} onChange={(event) => { setGroup(event.target.value); invalidate(); }} />
              <p className="muted codex-hint">{t("codex.groupHint")}</p>
            </div>}
            <div className="form-row">
              <label htmlFor={`${id}-fallback`}>{t("codex.fallback")}</label>
              <select id={`${id}-fallback`} className="input" value={fallback} onChange={(event) => { setFallback(event.target.value); invalidate(); }}>
                <option value="keep">{t("batchCodex.keep")}</option>
                <option value="on">{t("batchCodex.on")}</option>
                <option value="off">{t("batchCodex.off")}</option>
              </select>
              <p className="muted codex-hint">{t("codex.fallbackHint")}</p>
            </div>
          </>}
        </fieldset>
        {error && <div className="error" role="alert">{error}</div>}
        {preview && <section className="batch-codex-preview" aria-labelledby={`${id}-preview`} aria-live="polite">
          <h3 id={`${id}-preview`} ref={previewTitleRef} tabIndex={-1}>{t("batchCodex.previewTitle")}</h3>
          <p className="muted codex-hint">{t("batchCodex.previewHint")}</p>
          <div className="batch-codex-table-scroll" tabIndex={0} role="region" aria-label={t("batchCodex.previewTitle")}>
            <table>
              <thead><tr><th scope="col">{t("batchCodex.keyId")}</th><th scope="col">{t("batchCodex.stage")}</th><th scope="col">{t("batchCodex.accounts")}</th><th scope="col">{t("codex.group")}</th><th scope="col">{t("batchCodex.fallback")}</th></tr></thead>
              <tbody>{preview.response.changes.flatMap((change) => (["before", "after"] as const).map((stage) => {
                const binding = change[stage];
                return <tr key={`${change.id}-${stage}`}>
                  <th scope="row" className="mono">{change.id}</th>
                  <td>{t(`batchCodex.${stage}`)}</td>
                  <td className="mono">{binding.codex_auth_ids.length ? binding.codex_auth_ids.join(", ") : t("batchCodex.none")}</td>
                  <td>{binding.codex_group || t("batchCodex.none")}</td>
                  <td>{t(binding.codex_fallback ? "batchCodex.on" : "batchCodex.off")}</td>
                </tr>;
              }))}</tbody>
            </table>
          </div>
        </section>}
        <div className="fp-actions batch-codex-actions">
          <button className="btn" type="button" disabled={busy} onClick={onClose}>{t("bind.cancel")}</button>
          <button className="btn" type="submit" disabled={busy}>{busy ? t("batchCodex.busy") : t("batchCodex.preview")}</button>
          {preview && <button className="btn primary" type="button" disabled={busy} onClick={() => void apply()}>{t("batchCodex.confirm")}</button>}
        </div>
      </form>
    </div>
  );
}
