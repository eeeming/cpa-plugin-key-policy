import { useEffect, useId, useState } from "react";
import { listCodexAuths } from "../api/keys";
import { errText } from "../api/error";
import {
  addAuthIds,
  filterCodexAccounts,
  mergeCodexAccounts,
  toggleAuthId,
  type CodexBindingValue,
} from "../codexBinding";
import { useT } from "../i18n";
import type { CodexAuthListResponse } from "../types";

export default function CodexBindingFields({
  value,
  manualText,
  onChange,
  onManualText,
}: {
  value: CodexBindingValue;
  manualText: string;
  onChange: (value: CodexBindingValue) => void;
  onManualText: (text: string) => void;
}) {
  const t = useT();
  const id = useId();
  const [directory, setDirectory] = useState<CodexAuthListResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");

  useEffect(() => {
    let active = true;
    void listCodexAuths().then((result) => {
      if (active) setDirectory(result);
    }).catch((ex) => {
      if (active) setError(errText(ex, ""));
    }).finally(() => {
      if (active) setLoading(false);
    });
    return () => { active = false; };
  }, []);

  const accounts = filterCodexAccounts(mergeCodexAccounts(directory, value.ids), search);
  const directoryUnavailable = !loading && !directory?.available;
  const directoryError = directory?.error || error;

  const addManual = () => {
    onChange({ ...value, ids: addAuthIds(value.ids, manualText) });
    onManualText("");
  };

  return (
    <fieldset className="codex-binding">
      <legend>{t("codex.title")}</legend>
      <p className="muted codex-hint" id={`${id}-hint`}>{t("codex.hint")}</p>
      <div role="status" className="codex-directory-status">
        {loading ? t("codex.loading") : directoryUnavailable ? t("codex.directoryUnavailable") : t("codex.directoryAvailable")}
        {!loading && directoryUnavailable && directoryError && (
          <div className="muted codex-directory-error">{directoryError}</div>
        )}
      </div>
      <div className="form-row">
        <label htmlFor={`${id}-search`}>{t("codex.search")}</label>
        <input
          id={`${id}-search`}
          type="search"
          className="input"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          aria-controls={`${id}-accounts`}
          aria-describedby={`${id}-selection`}
        />
      </div>
      <p className="muted codex-hint" id={`${id}-selection`}>
        {t("codex.selectedCount", { count: value.ids.length })} {t("codex.searchHint")}
      </p>
      <div id={`${id}-accounts`} className="codex-accounts" aria-busy={loading}>
        {accounts.length === 0 && <p className="muted">{t("codex.noResults")}</p>}
        {accounts.map((account) => (
          <label className="codex-account" key={account.id}>
            <input
              type="checkbox"
              checked={account.selected}
              onChange={() => onChange({ ...value, ids: toggleAuthId(value.ids, account.id) })}
            />
            <span className="codex-account-details">
              <span className="codex-account-name">{account.label || account.name || account.id}</span>
              <span className="mono">{t("codex.authId")}: {account.id}</span>
              {account.label && <span>{t("codex.label")}: {account.label}</span>}
              {account.name && <span>{t("codex.name")}: {account.name}</span>}
              {account.email && <span>{t("codex.email")}: {account.email}</span>}
              <span>{t("codex.status")}: {account.status || t("codex.unknownStatus")}</span>
              <span>{t("codex.source")}: {t(`codex.sources.${account.source}`)}</span>
              <span className="codex-account-flags">
                {account.missing && <span className="tag off">{t("codex.missing")}</span>}
                {!account.missing && account.unavailable && <span className="tag off">{t("codex.unavailable")}</span>}
                {account.disabled && <span className="tag off">{t("codex.disabled")}</span>}
                {!account.missing && account.unconfirmed && <span className="tag off">{t("codex.unconfirmed")}</span>}
              </span>
            </span>
          </label>
        ))}
      </div>
      <div className="form-row">
        <label htmlFor={`${id}-manual`}>{t("codex.manual")}</label>
        <textarea
          id={`${id}-manual`}
          className="input codex-manual"
          rows={3}
          value={manualText}
          onChange={(e) => onManualText(e.target.value)}
          aria-describedby={`${id}-manual-hint`}
          autoComplete="off"
          spellCheck={false}
        />
        <p className="muted codex-hint" id={`${id}-manual-hint`}>{t("codex.manualHint")}</p>
        <button className="btn sm codex-add" type="button" onClick={addManual} disabled={!manualText.trim()}>
          {t("codex.addManual")}
        </button>
      </div>
      <div className="form-row">
        <label htmlFor={`${id}-group`}>{t("codex.group")}</label>
        <input
          id={`${id}-group`}
          className="input"
          value={value.group}
          onChange={(e) => onChange({ ...value, group: e.target.value })}
          aria-describedby={`${id}-group-hint`}
        />
        <p className="muted codex-hint" id={`${id}-group-hint`}>{t("codex.groupHint")}</p>
      </div>
      <label className="codex-fallback" htmlFor={`${id}-fallback`}>
        <input
          id={`${id}-fallback`}
          type="checkbox"
          checked={value.fallback}
          onChange={(e) => onChange({ ...value, fallback: e.target.checked })}
          aria-describedby={`${id}-fallback-hint`}
        />
        {t("codex.fallback")}
      </label>
      <p className="muted codex-hint" id={`${id}-fallback-hint`}>{t("codex.fallbackHint")}</p>
    </fieldset>
  );
}
