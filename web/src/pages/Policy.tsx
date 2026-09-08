import { useCallback, useEffect, useState } from "react";
import { listKeys, bindKey, patchKey, deleteKey, quotaStatus } from "../api/keys";
import type { KeyPublic } from "../types";
import { useT } from "../i18n";

function usdLabel(v: number, unlimited: string): string {
  if (!v) return unlimited;
  return "$" + v;
}

export default function Policy() {
  const t = useT();
  const [keys, setKeys] = useState<KeyPublic[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [showBind, setShowBind] = useState(false);
  const [edit, setEdit] = useState<KeyPublic | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setKeys(await listKeys());
    } catch (e) {
      const err = e as { message?: string };
      setError(err.message ?? t("keys.loadFailed"));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => { void load(); }, [load]);

  const toggle = async (k: KeyPublic) => {
    try {
      await patchKey({ id: k.id, enabled: !k.enabled });
      await load();
    } catch (e) {
      alert((e as Error).message);
    }
  };

  const unbind = async (k: KeyPublic) => {
    if (!confirm(t("keys.unbindConfirm", { id: k.id }))) return;
    try {
      await deleteKey(k.id);
      await load();
    } catch (e) {
      alert((e as Error).message);
    }
  };

  return (
    <div>
      <div className="fp-head" style={{ margin: "0 0 16px" }}>
        <h1>{t("header.policy")}</h1>
        <div className="fp-actions">
          <button className="btn sm" onClick={() => void load()}>{t("keys.refresh")}</button>
          <button className="btn primary sm" onClick={() => setShowBind(true)}>{t("keys.bind")}</button>
        </div>
      </div>
      <p className="muted" style={{ marginBottom: 16 }}>{t("keys.notice")}</p>
      {error && <div className="card" style={{ color: "var(--danger)" }}>{error}</div>}
      {loading && <div className="muted">{t("keys.loading")}</div>}
      {!loading && keys.length === 0 && <div className="card muted">{t("keys.empty")}</div>}
      {keys.map((k) => {
        const st = quotaStatus(k);
        return (
          <div key={k.id} className={"keycard" + (k.enabled ? "" : " disabled")}>
            <div className="keycard-title">{k.name} <span className="muted">{k.key_preview}</span></div>
            <div className="muted">
              {t("keys.colDaily")}: {usdLabel(k.daily_limit_usd, t("keys.unlimited"))} · {t("keys.colWeekly")}: {usdLabel(k.weekly_limit_usd, t("keys.unlimited"))} · RPM: {k.rpm || t("keys.unlimited")}
            </div>
            <div className="muted">{t("keys.colStatus")}: {st === "disabled" ? t("keys.disabled") : st === "daily" ? t("usage.statusDaily") : st === "weekly" ? t("usage.statusWeekly") : t("usage.statusOk")}</div>
            <div className="fp-actions" style={{ marginTop: 8 }}>
              <button className="btn sm" onClick={() => setEdit(k)}>{t("keys.edit")}</button>
              <button className="btn sm" onClick={() => void toggle(k)}>{k.enabled ? t("keys.disable") : t("keys.enable")}</button>
              <button className="btn sm" onClick={() => void unbind(k)}>{t("keys.unbind")}</button>
            </div>
          </div>
        );
      })}
      {showBind && (
        <BindModal
          title={t("bind.title")}
          onClose={() => setShowBind(false)}
          onSubmit={async (v) => {
            await bindKey(v);
            setShowBind(false);
            await load();
          }}
        />
      )}
      {edit && (
        <BindModal
          title={t("keys.edit")}
          existing={edit}
          onClose={() => setEdit(null)}
          onSubmit={async (v) => {
            const body = { ...v };
            if (!body.key) delete body.key;
            await patchKey(body);
            setEdit(null);
            await load();
          }}
        />
      )}
    </div>
  );
}

function BindModal({
  title,
  existing,
  onClose,
  onSubmit,
}: {
  title: string;
  existing?: KeyPublic;
  onClose: () => void;
  onSubmit: (v: { id: string; name: string; key?: string; rpm?: number; daily_limit_usd?: number; weekly_limit_usd?: number }) => Promise<void>;
}) {
  const t = useT();
  const [name, setName] = useState(existing?.name ?? "");
  const [key, setKey] = useState("");
  const [daily, setDaily] = useState(existing ? String(existing.daily_limit_usd || "") : "");
  const [weekly, setWeekly] = useState(existing ? String(existing.weekly_limit_usd || "") : "");
  const [rpm, setRpm] = useState(existing ? String(existing.rpm || "") : "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    if (!existing && !key.trim()) {
      setErr(t("login.secretRequired"));
      return;
    }
    setBusy(true);
    try {
      const id = existing?.id || ("k-" + Date.now().toString(36));
      await onSubmit({
        id,
        name: name.trim() || id,
        key: existing ? undefined : key.trim(),
        daily_limit_usd: daily.trim() === "" ? 0 : Number(daily),
        weekly_limit_usd: weekly.trim() === "" ? 0 : Number(weekly),
        rpm: rpm.trim() === "" ? 0 : Number(rpm),
      });
    } catch (ex) {
      setErr((ex as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <form className="modal" onClick={(e) => e.stopPropagation()} onSubmit={(e) => void submit(e)}>
        <h2>{title}</h2>
        <div className="form-row">
          <label>{t("bind.name")}</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="form-row">
          <label>{existing ? t("bind.keyRotate") : t("bind.key")}</label>
          <input
            className="input"
            type="password"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder={existing ? existing.key_preview : ""}
          />
          <div className="muted" style={{ fontSize: 12, marginTop: 4 }}>
            {existing ? t("bind.keyRotateHint") : t("bind.keyHint")}
          </div>
        </div>
        <div className="row2">
          <div className="form-row">
            <label>{t("bind.daily")}</label>
            <input className="input" value={daily} onChange={(e) => setDaily(e.target.value)} />
          </div>
          <div className="form-row">
            <label>{t("bind.weekly")}</label>
            <input className="input" value={weekly} onChange={(e) => setWeekly(e.target.value)} />
          </div>
        </div>
        <div className="form-row">
          <label>{t("bind.rpm")}</label>
          <input className="input" value={rpm} onChange={(e) => setRpm(e.target.value)} />
        </div>
        {err && <div className="muted" style={{ color: "var(--danger)" }}>{err}</div>}
        <div className="fp-actions" style={{ marginTop: 12 }}>
          <button className="btn" type="button" onClick={onClose}>{t("bind.cancel")}</button>
          <button className="btn primary" type="submit" disabled={busy}>{existing ? t("bind.save") : t("bind.submit")}</button>
        </div>
      </form>
    </div>
  );
}
