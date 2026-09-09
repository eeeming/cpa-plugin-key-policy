import { useCallback, useEffect, useId, useRef, useState } from "react";
import { listKeys, bindKey, patchKey, deleteKey, syncPlusKeys, resetWindows } from "../api/keys";
import type { KeyPublic, KeyWriteRequest } from "../types";
import { errText } from "../api/error";
import { hasAnyLimit, limitPatch, parseLimits, type LimitInputs } from "../bulk";
import { useT } from "../i18n";
import KeyCard from "../components/KeyCard";
import {
  invertSelection,
  idsHitByMarquee,
  mergeSelection,
  normalizeRect,
  pruneSelection,
  selectAll,
  toggleId,
  type Rect,
} from "../selection";

function usdLabel(v: number, unlimited: string): string {
  if (!v) return unlimited;
  return "$" + v;
}

// Close a modal on Escape (the overlay click is mouse-only).
function useEscape(onClose: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
}

const MARQUEE_MIN = 6;

export default function Policy() {
  const t = useT();
  const [keys, setKeys] = useState<KeyPublic[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [showBind, setShowBind] = useState(false);
  const [edit, setEdit] = useState<KeyPublic | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [showBulk, setShowBulk] = useState(false);
  const [bulkBusy, setBulkBusy] = useState(false);
  const [marquee, setMarquee] = useState<Rect | null>(null);
  const gridRef = useRef<HTMLDivElement>(null);
  const dragRef = useRef<{ x: number; y: number; cardId?: string; dragging: boolean; moved: boolean } | null>(null);
  // Only the newest listKeys() response may update state, so a slow refresh
  // cannot overwrite a newer one.
  const loadSeq = useRef(0);

  const load = useCallback(async () => {
    const seq = ++loadSeq.current;
    setLoading(true);
    setError("");
    try {
      const next = await listKeys();
      if (seq !== loadSeq.current) return;
      setKeys(next);
      // Drop selected ids that no longer exist so the bulk toolbar never acts
      // on ghosts (which would fail every request with 404).
      setSelected((cur) => pruneSelection(cur, next.map((k) => k.id)));
    } catch (e) {
      if (seq === loadSeq.current) setError(errText(e, t("keys.loadFailed")));
    } finally {
      if (seq === loadSeq.current) setLoading(false);
    }
  }, [t]);

  useEffect(() => { void load(); }, [load]);

  const keyIds = keys.map((k) => k.id);

  const toggle = async (k: KeyPublic) => {
    try {
      await patchKey({ id: k.id, enabled: !k.enabled });
      await load();
    } catch (e) {
      setError(errText(e, t("keys.loadFailed")));
    }
  };

  const unbind = async (k: KeyPublic) => {
    if (!confirm(t("keys.unbindConfirm", { id: k.id }))) return;
    try {
      await deleteKey(k.id);
      setSelected((cur) => cur.filter((id) => id !== k.id));
      await load();
    } catch (e) {
      setError(errText(e, t("keys.loadFailed")));
    }
  };

  const sync = async () => {
    if (!confirm(t("keys.syncConfirm"))) return;
    setSyncing(true);
    setError("");
    setNotice("");
    try {
      const got = await syncPlusKeys();
      setNotice(t("keys.syncDone", { added: got.added, skipped: got.skipped, total: got.total }));
      await load();
    } catch (e) {
      setError(errText(e, t("keys.syncFailed")));
    } finally {
      setSyncing(false);
    }
  };

  const finishBulk = async (result: { error?: string; notice?: string; clearSelection: boolean }) => {
    await load();
    setError(result.error ?? "");
    setNotice(result.notice ?? "");
    if (result.clearSelection) setSelected([]);
  };

  const runBulk = async (work: (ids: string[]) => Promise<{ error?: string; notice?: string }>) => {
    if (bulkBusy) return;
    const ids = [...selected];
    if (ids.length === 0) return;
    setBulkBusy(true);
    setError("");
    setNotice("");
    try {
      const result = await work(ids);
      await finishBulk({ ...result, clearSelection: !result.error });
    } catch (e) {
      await finishBulk({ error: errText(e, t("keys.loadFailed")), clearSelection: false });
    } finally {
      setBulkBusy(false);
    }
  };

  const applyBulkLimits = async (values: LimitInputs) => {
    await runBulk(async (ids) => {
      const failures: string[] = [];
      for (const id of ids) {
        try {
          await patchKey(limitPatch(id, values));
        } catch (e) {
          failures.push(id + ": " + errText(e, t("keys.loadFailed")));
        }
      }
      setShowBulk(false);
      if (failures.length) return { error: t("keys.bulkFailed", { detail: failures.join("; ") }) };
      return { notice: t("keys.setLimitsDone", { count: ids.length }) };
    });
  };

  const resetSelected = async () => {
    if (!confirm(t("keys.resetConfirm", { count: selected.length }))) return;
    await runBulk(async (ids) => {
      const got = await resetWindows(ids);
      if (got.failed?.length) {
        return {
          notice: t("keys.resetDone", { count: got.reset, failed: got.failed.length }),
          error: t("keys.bulkFailed", { detail: got.failed.join(", ") }),
        };
      }
      return { notice: t("keys.resetDone", { count: got.reset, failed: 0 }) };
    });
  };

  const resetOne = async (k: KeyPublic) => {
    if (!confirm(t("keys.resetOneConfirm", { name: k.name }))) return;
    try {
      const got = await resetWindows([k.id]);
      await load();
      setNotice(t("keys.resetDone", { count: got.reset, failed: (got.failed ?? []).length }));
      if (got.failed?.length) setError(t("keys.bulkFailed", { detail: got.failed.join(", ") }));
      else setError("");
    } catch (e) {
      setError(errText(e, t("keys.loadFailed")));
    }
  };

  const setEnabledSelected = async (enabled: boolean) => {
    const confirmKey = enabled ? "keys.bulkEnableConfirm" : "keys.bulkDisableConfirm";
    const doneKey = enabled ? "keys.bulkEnableDone" : "keys.bulkDisableDone";
    if (!confirm(t(confirmKey, { count: selected.length }))) return;
    await runBulk(async (ids) => {
      const failures: string[] = [];
      for (const id of ids) {
        try {
          await patchKey({ id, enabled });
        } catch (e) {
          failures.push(id + ": " + errText(e, t("keys.loadFailed")));
        }
      }
      if (failures.length) return { error: t("keys.bulkFailed", { detail: failures.join("; ") }) };
      return { notice: t(doneKey, { count: ids.length }) };
    });
  };

  const unbindSelected = async () => {
    if (!confirm(t("keys.bulkUnbindConfirm", { count: selected.length }))) return;
    await runBulk(async (ids) => {
      const failures: string[] = [];
      for (const id of ids) {
        try {
          await deleteKey(id);
        } catch (e) {
          failures.push(id + ": " + errText(e, t("keys.loadFailed")));
        }
      }
      if (failures.length) return { error: t("keys.bulkFailed", { detail: failures.join("; ") }) };
      return { notice: t("keys.bulkUnbindDone", { count: ids.length }) };
    });
  };

  const pointInGrid = (e: React.PointerEvent) => {
    const grid = gridRef.current;
    if (!grid) return { x: 0, y: 0 };
    const r = grid.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  };

  const onGridPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    const el = e.target as HTMLElement;
    if (el.closest("button, a, input, textarea, select, label")) return;
    const pt = pointInGrid(e);
    dragRef.current = {
      x: pt.x,
      y: pt.y,
      cardId: el.closest("[data-key-id]")?.getAttribute("data-key-id") ?? undefined,
      dragging: false,
      moved: false,
    };
  };

  const onGridPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current;
    if (!drag) return;
    const pt = pointInGrid(e);
    if (Math.hypot(pt.x - drag.x, pt.y - drag.y) >= MARQUEE_MIN) {
      drag.moved = true;
      if (!drag.dragging && window.matchMedia("(min-width: 641px)").matches) {
        drag.dragging = true;
        e.currentTarget.setPointerCapture(e.pointerId);
      }
    }
    if (drag.dragging) {
      setMarquee(normalizeRect(drag.x, drag.y, pt.x, pt.y));
    }
  };

  const onGridPointerUp = (e: React.PointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current;
    dragRef.current = null;
    const box = marquee;
    setMarquee(null);
    if (!drag) return;
    if (drag.dragging) {
      const pt = pointInGrid(e);
      const rect = box ?? normalizeRect(drag.x, drag.y, pt.x, pt.y);
      const grid = gridRef.current;
      if (!grid) return;
      const gr = grid.getBoundingClientRect();
      const cards = [...grid.querySelectorAll("[data-key-id]")].map((node) => {
        const br = (node as HTMLElement).getBoundingClientRect();
        return {
          id: node.getAttribute("data-key-id") || "",
          rect: { x: br.left - gr.left, y: br.top - gr.top, w: br.width, h: br.height },
        };
      }).filter((c) => c.id);
      setSelected((cur) => mergeSelection(cur, idsHitByMarquee(rect, cards)));
      return;
    }
    if (drag.moved) return;
    if (drag.cardId) {
      setSelected((cur) => toggleId(cur, drag.cardId!));
    }
  };

  const hasSel = selected.length > 0;

  return (
    <div>
      <div className="quota-toolbar">
        <div className="fp-actions">
          <button className="btn primary sm" onClick={() => setShowBind(true)}>{t("keys.bind")}</button>
          <button className="btn sm" disabled={syncing} onClick={() => void sync()}>
            {syncing ? t("keys.syncing") : t("keys.sync")}
          </button>
        </div>
        <span className="tb-split" aria-hidden="true" />
        <div className="fp-actions">
          <button className="btn sm" type="button" disabled={keys.length === 0} onClick={() => setSelected(selectAll(keyIds))}>{t("keys.selectAll")}</button>
          <button className="btn sm" type="button" disabled={keys.length === 0} onClick={() => setSelected(invertSelection(selected, keyIds))}>{t("keys.invert")}</button>
          <span className={"tb-count" + (hasSel ? " on" : "")}>{t("keys.selectedCount", { count: selected.length })}</span>
        </div>
        <span className="tb-split" aria-hidden="true" />
        <div className="fp-actions">
          <button className="btn sm" type="button" disabled={!hasSel || bulkBusy} onClick={() => setShowBulk(true)}>{t("keys.setLimits")}</button>
          <button className="btn sm" type="button" disabled={!hasSel || bulkBusy} onClick={() => void resetSelected()}>{t("keys.reset")}</button>
          <button className="btn sm" type="button" disabled={!hasSel || bulkBusy} onClick={() => void setEnabledSelected(true)}>{t("keys.enable")}</button>
          <button className="btn sm" type="button" disabled={!hasSel || bulkBusy} onClick={() => void setEnabledSelected(false)}>{t("keys.disable")}</button>
          <button className="btn sm danger-outline" type="button" disabled={!hasSel || bulkBusy} onClick={() => void unbindSelected()}>{t("keys.unbind")}</button>
        </div>
        <button className="btn sm tb-refresh" onClick={() => void load()}>{t("keys.refresh")}</button>
      </div>
      {error && <div className="quota-flash err" role="alert">{error}</div>}
      {notice && <div className="quota-flash ok" role="status" aria-live="polite">{notice}</div>}
      {loading && <div className="muted">{t("keys.loading")}</div>}
      {!loading && keys.length === 0 && <div className="card muted">{t("keys.empty")}</div>}
      <div
        className="card-stack"
        ref={gridRef}
        onPointerDown={onGridPointerDown}
        onPointerMove={onGridPointerMove}
        onPointerUp={onGridPointerUp}
        onPointerCancel={() => { dragRef.current = null; setMarquee(null); }}
      >
        {keys.map((k) => (
          <KeyCard
            key={k.id}
            k={k}
            selected={selected.includes(k.id)}
            onToggleSelect={() => setSelected((cur) => toggleId(cur, k.id))}
            actions={(
              <>
                <button className="btn sm" onClick={() => setEdit(k)}>{t("keys.edit")}</button>
                <button className="btn sm" onClick={() => void toggle(k)}>{k.enabled ? t("keys.disable") : t("keys.enable")}</button>
                <button className="btn sm" onClick={() => void resetOne(k)}>{t("keys.reset")}</button>
                <button className="btn sm danger-outline" onClick={() => void unbind(k)}>{t("keys.unbind")}</button>
              </>
            )}
          />
        ))}
        {marquee && (
          <div
            className="marquee"
            style={{ left: marquee.x, top: marquee.y, width: marquee.w, height: marquee.h }}
          />
        )}
      </div>
      {showBind && (
        <BindModal
          title={t("bind.title")}
          onClose={() => setShowBind(false)}
          onSubmit={async (req) => {
            await bindKey(req);
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
          onSubmit={async (req) => {
            const body = { ...req };
            if (!body.key) delete body.key;
            await patchKey(body);
            setEdit(null);
            await load();
          }}
        />
      )}
      {showBulk && (
        <BulkLimitsModal
          title={t("keys.setLimitsTitle")}
          count={selected.length}
          onClose={() => setShowBulk(false)}
          onSubmit={applyBulkLimits}
        />
      )}
    </div>
  );
}

function LimitFields({
  idPrefix,
  daily,
  weekly,
  rpm,
  onDaily,
  onWeekly,
  onRpm,
  hint,
}: {
  idPrefix: string;
  daily: string;
  weekly: string;
  rpm: string;
  onDaily: (v: string) => void;
  onWeekly: (v: string) => void;
  onRpm: (v: string) => void;
  hint?: string;
}) {
  const t = useT();
  return (
    <>
      <div className="row2">
        <div className="form-row">
          <label htmlFor={idPrefix + "-daily"}>{t("bind.daily")}</label>
          <input id={idPrefix + "-daily"} className="input" inputMode="decimal" value={daily} onChange={(e) => onDaily(e.target.value)} />
        </div>
        <div className="form-row">
          <label htmlFor={idPrefix + "-weekly"}>{t("bind.weekly")}</label>
          <input id={idPrefix + "-weekly"} className="input" inputMode="decimal" value={weekly} onChange={(e) => onWeekly(e.target.value)} />
        </div>
      </div>
      <div className="form-row">
        <label htmlFor={idPrefix + "-rpm"}>{t("bind.rpm")}</label>
        <input id={idPrefix + "-rpm"} className="input" inputMode="numeric" value={rpm} onChange={(e) => onRpm(e.target.value)} />
      </div>
      {hint && <div className="muted" style={{ fontSize: 12 }}>{hint}</div>}
    </>
  );
}

function BulkLimitsModal({
  title,
  count,
  onClose,
  onSubmit,
}: {
  title: string;
  count: number;
  onClose: () => void;
  onSubmit: (values: LimitInputs) => Promise<void>;
}) {
  const t = useT();
  const titleId = useId();
  const [daily, setDaily] = useState("");
  const [weekly, setWeekly] = useState("");
  const [rpm, setRpm] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  useEscape(onClose);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const parsed = parseLimits({ daily, weekly, rpm });
    if (!parsed.ok) {
      setErr(t("keys.invalidLimits"));
      return;
    }
    const values = parsed.values ?? {};
    if (!hasAnyLimit(values)) {
      setErr(t("keys.nothingToUpdate"));
      return;
    }
    if (!confirm(t("keys.selectedCount", { count }) + " — " + t("keys.setLimits"))) return;
    setBusy(true);
    setErr("");
    try {
      await onSubmit(values);
    } catch (ex) {
      setErr(errText(ex, t("keys.loadFailed")));
      setBusy(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <form
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onClick={(e) => e.stopPropagation()}
        onSubmit={(e) => void submit(e)}
      >
        <h2 id={titleId}>{title}</h2>
        <div className="muted" style={{ marginBottom: 12 }}>{t("keys.selectedCount", { count })}</div>
        <LimitFields
          idPrefix="bulk"
          daily={daily}
          weekly={weekly}
          rpm={rpm}
          onDaily={setDaily}
          onWeekly={setWeekly}
          onRpm={setRpm}
          hint={t("keys.setLimitsHint")}
        />
        {err && <div className="error" role="alert">{err}</div>}
        <div className="fp-actions" style={{ marginTop: 12 }}>
          <button className="btn" type="button" onClick={onClose}>{t("bind.cancel")}</button>
          <button className="btn primary" type="submit" disabled={busy}>{t("bind.save")}</button>
        </div>
      </form>
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
  onSubmit: (req: KeyWriteRequest) => Promise<void>;
}) {
  const t = useT();
  const titleId = useId();
  const [name, setName] = useState(existing?.name ?? "");
  const [key, setKey] = useState("");
  const [daily, setDaily] = useState(existing ? String(existing.daily_limit_usd || "") : "");
  const [weekly, setWeekly] = useState(existing ? String(existing.weekly_limit_usd || "") : "");
  const [rpm, setRpm] = useState(existing ? String(existing.rpm || "") : "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  useEscape(onClose);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    if (!existing && !key.trim()) {
      setErr(t("bind.keyRequired"));
      return;
    }
    // The single form's labels read "empty = unlimited", so a blank field is 0
    // here (not "unchanged"); the fields are pre-filled with the current limits.
    const parsed = parseLimits({ daily, weekly, rpm }, "zero");
    if (!parsed.ok) {
      setErr(t("keys.invalidLimits"));
      return;
    }
    const id = existing?.id || ("k-" + Date.now().toString(36));
    const req: KeyWriteRequest = { ...limitPatch(id, parsed.values ?? {}), name: name.trim() || id };
    const plain = key.trim();
    if (plain) req.key = plain;
    setBusy(true);
    try {
      await onSubmit(req);
    } catch (ex) {
      setErr(errText(ex, t("keys.loadFailed")));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <form
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onClick={(e) => e.stopPropagation()}
        onSubmit={(e) => void submit(e)}
      >
        <h2 id={titleId}>{title}</h2>
        {existing && (
          <div className="muted" style={{ marginBottom: 12 }}>
            {t("keys.colDaily")}: {usdLabel(existing.daily_limit_usd, t("keys.unlimited"))}
          </div>
        )}
        <div className="form-row">
          <label htmlFor="bind-name">{t("bind.name")}</label>
          <input id="bind-name" className="input" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="form-row">
          <label htmlFor="bind-key">{existing ? t("bind.keyRotate") : t("bind.key")}</label>
          <input
            id="bind-key"
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
        <LimitFields
          idPrefix="bind"
          daily={daily}
          weekly={weekly}
          rpm={rpm}
          onDaily={setDaily}
          onWeekly={setWeekly}
          onRpm={setRpm}
        />
        {err && <div className="error" role="alert">{err}</div>}
        <div className="fp-actions" style={{ marginTop: 12 }}>
          <button className="btn" type="button" onClick={onClose}>{t("bind.cancel")}</button>
          <button className="btn primary" type="submit" disabled={busy}>{existing ? t("bind.save") : t("bind.submit")}</button>
        </div>
      </form>
    </div>
  );
}
