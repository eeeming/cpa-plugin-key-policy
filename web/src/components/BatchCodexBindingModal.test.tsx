import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { batchCodexBinding, listCodexAuths, listKeys } from "../api/keys";
import { _resetLocale, translate } from "../i18n";
import type { BatchCodexBindingResponse, KeyPublic } from "../types";
import type * as KeysApi from "../api/keys";
import Policy from "../pages/Policy";
import BatchCodexBindingModal from "./BatchCodexBindingModal";

vi.mock("../api/keys", async (importOriginal) => ({
  ...await importOriginal<typeof KeysApi>(),
  batchCodexBinding: vi.fn(),
  listCodexAuths: vi.fn(),
  listKeys: vi.fn(),
}));

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let root: Root;
let container: HTMLDivElement;
const onApplied = vi.fn(async () => {});
const onClose = vi.fn();
const ids = ["stable-key-a", "stable-key-b"];
const before = { codex_auth_ids: ["old-auth"], codex_group: "old-team", codex_fallback: true };
const after = { codex_auth_ids: ["server-auth"], codex_group: "server-team", codex_fallback: false };
const preview: BatchCodexBindingResponse = {
  dry_run: true, updated: 0, changes: ids.map((id) => ({ id, before, after })),
};

beforeEach(() => {
  vi.clearAllMocks();
  _resetLocale("en");
  vi.mocked(listCodexAuths).mockResolvedValue({ available: true, source: "host", accounts: [{ id: "auth-a", label: "Shared name", source: "host" }, { id: "auth-b", label: "Shared name", source: "host" }] });
  vi.mocked(batchCodexBinding).mockResolvedValue(preview);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  _resetLocale("zh-CN");
});

async function mount() {
  await act(async () => root.render(<BatchCodexBindingModal ids={ids} onClose={onClose} onApplied={onApplied} />));
}

function button(key: string): HTMLButtonElement {
  const found = [...container.querySelectorAll("button")].find((item) => item.textContent === translate(key));
  if (!found) throw new Error(`Missing button ${key}`);
  return found;
}

async function click(key: string) {
  await act(async () => button(key).click());
}

async function change(suffix: string, value: string) {
  const input = container.querySelector<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(`[id$='-${suffix}']`);
  if (!input) throw new Error(`Missing control ${suffix}`);
  await act(async () => {
    const prototype = input instanceof HTMLSelectElement ? HTMLSelectElement.prototype : input instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event(input instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
  });
}

describe("batch Codex binding modal", () => {
  it("requires an operation and an explicit server preview before apply", async () => {
    await mount();
    expect(container.querySelector("[role='dialog']")?.getAttribute("aria-modal")).toBe("true");
    expect(container.textContent).not.toContain(translate("batchCodex.confirm"));
    await click("batchCodex.preview");
    expect(batchCodexBinding).not.toHaveBeenCalled();
    expect(container.querySelector("[role='alert']")).not.toBeNull();
    await change("fallback", "off");
    await click("batchCodex.preview");
    expect(batchCodexBinding).toHaveBeenCalledTimes(1);
    expect(batchCodexBinding).toHaveBeenCalledWith({ ids, accounts_mode: "keep", codex_fallback: false, dry_run: true });
    expect(onApplied).not.toHaveBeenCalled();
    const rows = container.querySelectorAll("tbody tr");
    expect(rows).toHaveLength(4);
    expect(rows[0].textContent).toContain("old-auth");
    expect(rows[0].textContent).toContain("old-team");
    expect(rows[1].textContent).toContain("server-auth");
    expect(rows[1].textContent).toContain("server-team");
    expect(rows[1].textContent).toContain(translate("batchCodex.off"));
    vi.mocked(batchCodexBinding).mockResolvedValueOnce({ ...preview, dry_run: false, updated: 2 });
    await click("batchCodex.confirm");
    expect(batchCodexBinding).toHaveBeenLastCalledWith({ ids, accounts_mode: "keep", codex_fallback: false, dry_run: false, expected: ids.map((id) => ({ id, before })) });
    expect(onApplied).toHaveBeenCalledWith(2);
  });

  it.each(["set", "add", "remove"])("sends the %s operation with picker and pending manual stable AuthIDs", async (mode) => {
    vi.mocked(listCodexAuths).mockResolvedValueOnce({ available: false, source: "recent", accounts: [{ id: "auth-a", label: "Shared name", source: "recent" }], error: "directory unavailable" });
    await mount();
    await change("accounts-mode", mode);
    await act(async () => container.querySelector<HTMLInputElement>(".codex-account input")!.click());
    await change("manual", "auth-a, My Codex Account.json; auth-c\nauth-c");
    await change("group-mode", "set");
    await change("group", " team ");
    await change("fallback", "on");
    await click("batchCodex.preview");
    expect(batchCodexBinding).toHaveBeenCalledWith({ ids, accounts_mode: mode, codex_auth_ids: ["auth-a", "My Codex Account.json", "auth-c"], codex_group: "team", codex_fallback: true, dry_run: true });
  });

  it("allows empty set, empty group and false without quota or key material", async () => {
    vi.mocked(listCodexAuths).mockRejectedValueOnce(new Error("offline"));
    await mount();
    await change("accounts-mode", "set");
    expect(container.querySelector("textarea")).not.toBeNull();
    await change("group-mode", "set");
    await change("fallback", "off");
    await click("batchCodex.preview");
    expect(batchCodexBinding).toHaveBeenCalledWith({ ids, accounts_mode: "set", codex_auth_ids: [], codex_group: "", codex_fallback: false, dry_run: true });
  });

  it("keeps account and fallback fields omitted for a group-only operation", async () => {
    await mount();
    await change("group-mode", "set");
    await click("batchCodex.preview");
    expect(batchCodexBinding).toHaveBeenCalledWith({ ids, accounts_mode: "keep", codex_group: "", dry_run: true });
  });

  it("clear-all excludes every other binding operation", async () => {
    await mount();
    await change("accounts-mode", "add");
    await change("manual", "auth-a");
    await change("fallback", "on");
    await act(async () => container.querySelector<HTMLInputElement>("[id$='-clear']")!.click());
    await click("batchCodex.preview");
    expect(batchCodexBinding).toHaveBeenCalledTimes(1);
    expect(batchCodexBinding).toHaveBeenCalledWith({ ids, clear: true, dry_run: true });
  });

  it.each(["accounts-mode", "manual", "group-mode", "group", "fallback", "clear", "picker", "addManual"])("invalidates preview when %s changes", async (control) => {
    await mount();
    await change("accounts-mode", "set");
    await change("group-mode", "set");
    if (control === "addManual") await change("manual", "new-auth");
    await click("batchCodex.preview");
    expect(button("batchCodex.confirm")).toBeDefined();
    if (control === "clear" || control === "picker") {
      await act(async () => container.querySelector<HTMLInputElement>(control === "clear" ? "[id$='-clear']" : ".codex-account input")!.click());
    } else if (control === "addManual") {
      await click("codex.addManual");
    } else {
      const values: Record<string, string> = { "accounts-mode": "add", manual: "new-auth", "group-mode": "keep", group: "new-group", fallback: "on" };
      await change(control, values[control]);
    }
    expect(container.querySelector(".batch-codex-preview")).toBeNull();
    expect(container.textContent).not.toContain(translate("batchCodex.confirm"));
    expect(batchCodexBinding).toHaveBeenCalledTimes(1);
  });

  it.each([400, 404, 500])("displays a preview %s error and retains form state for retry", async (status) => {
    await mount();
    await change("accounts-mode", "add");
    await change("manual", "manual-auth");
    vi.mocked(batchCodexBinding).mockRejectedValueOnce({ response: { status, data: { error: { message: `failure-${status}` } } } });
    await click("batchCodex.preview");
    expect(container.querySelector("[role='alert']")?.textContent).toContain(`failure-${status}`);
    expect(container.querySelector("textarea")?.value).toBe("manual-auth");
    await click("batchCodex.preview");
    expect(button("batchCodex.confirm")).toBeDefined();
  });

  it("shows stale-preview 409 and requires a fresh preview with fresh expected bindings", async () => {
    await mount();
    await change("fallback", "off");
    await click("batchCodex.preview");
    vi.mocked(batchCodexBinding).mockRejectedValueOnce({ response: { status: 409, data: { error: { message: "Binding changed; preview again" } } } });
    await click("batchCodex.confirm");
    expect(container.querySelector("[role='alert']")?.textContent).toContain("Binding changed");
    expect(container.querySelector(".batch-codex-preview")).toBeNull();
    const freshBefore = { ...before, codex_group: "changed-elsewhere" };
    const fresh = { ...preview, changes: ids.map((id) => ({ id, before: freshBefore, after })) };
    vi.mocked(batchCodexBinding).mockResolvedValueOnce(fresh);
    await click("batchCodex.preview");
    await click("batchCodex.confirm");
    expect(batchCodexBinding).toHaveBeenLastCalledWith(expect.objectContaining({ dry_run: false, expected: ids.map((id) => ({ id, before: freshBefore })) }));
  });

  it("traps keyboard focus, closes on Escape and restores the opener", async () => {
    const opener = document.createElement("button");
    document.body.append(opener);
    opener.focus();
    await mount();
    const first = container.querySelector<HTMLInputElement>("[id$='-clear']")!;
    expect(document.activeElement).toBe(first);
    button("batchCodex.preview").focus();
    await act(async () => { button("batchCodex.preview").dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true })); });
    expect(document.activeElement).toBe(first);
    await act(async () => { first.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    expect(onClose).toHaveBeenCalledOnce();
    await act(async () => root.render(null));
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });
});

describe("policy batch toolbar", () => {
  it("uses selected stable IDs and preserves selection after a failed batch", async () => {
    const keys: KeyPublic[] = ids.map((id) => ({ id, name: "Duplicate label", enabled: true, key_preview: "sk-...masked", rpm: 8, daily_limit_usd: 4, weekly_limit_usd: 20, usage: { daily_usd: 0, weekly_usd: 0, daily_limit_usd: 4, weekly_limit_usd: 20 } }));
    vi.mocked(listKeys).mockResolvedValue(keys);
    await act(async () => root.render(<Policy />));
    expect(button("batchCodex.action").disabled).toBe(true);
    await click("keys.selectAll");
    await click("batchCodex.action");
    await change("fallback", "off");
    await click("batchCodex.preview");
    vi.mocked(batchCodexBinding).mockRejectedValueOnce(new Error("save failed"));
    await click("batchCodex.confirm");
    expect(batchCodexBinding).toHaveBeenCalledWith({ ids, accounts_mode: "keep", codex_fallback: false, dry_run: true });
    await click("bind.cancel");
    expect(container.querySelectorAll("[data-key-id] .kc-check input:checked")).toHaveLength(2);
    expect(button("batchCodex.action").disabled).toBe(false);
  });
});
