import type { CodexAuthAccount, CodexAuthListResponse, KeyPublic, KeyWriteRequest } from "./types";

export interface CodexBindingValue {
  ids: string[];
  group: string;
  fallback: boolean;
}

export interface CodexAccountOption extends Omit<CodexAuthAccount, "source"> {
  source: CodexAuthAccount["source"] | "manual";
  selected: boolean;
  missing: boolean;
  unconfirmed: boolean;
}

export function normalizeAuthIds(ids: readonly string[]): string[] {
  return [...new Set(ids.map((id) => id.trim()).filter(Boolean))];
}

export function parseAuthIds(text: string): string[] {
  return normalizeAuthIds(text.split(/[\r\n,;，；、]+/u));
}

export function addAuthIds(ids: readonly string[], text: string): string[] {
  return normalizeAuthIds([...ids, ...parseAuthIds(text)]);
}

export function toggleAuthId(ids: readonly string[], id: string): string[] {
  return ids.includes(id) ? ids.filter((value) => value !== id) : normalizeAuthIds([...ids, id]);
}

export function initialCodexBinding(key?: Pick<KeyPublic, "codex_auth_ids" | "codex_group" | "codex_fallback">): CodexBindingValue {
  return {
    ids: normalizeAuthIds(key?.codex_auth_ids ?? []),
    group: key?.codex_group ?? "",
    fallback: key?.codex_fallback ?? false,
  };
}

// Always include empty/false values: PATCH must be able to clear a binding.
export function codexBindingPatch(value: CodexBindingValue, manualText = ""): Required<Pick<KeyWriteRequest, "codex_auth_ids" | "codex_group" | "codex_fallback">> {
  return {
    codex_auth_ids: addAuthIds(value.ids, manualText),
    codex_group: value.group.trim(),
    codex_fallback: value.fallback,
  };
}

// The directory enriches selection; it never owns or prunes selected IDs.
export function mergeCodexAccounts(directory: CodexAuthListResponse | null, selectedIds: readonly string[]): CodexAccountOption[] {
  const selected = new Set(normalizeAuthIds(selectedIds));
  const accounts = new Map<string, CodexAccountOption>();
  for (const account of directory?.accounts ?? []) {
    if (!account.id.trim()) continue;
    accounts.set(account.id, {
      ...account,
      selected: selected.has(account.id),
      missing: !!directory?.available && account.source === "recent",
      unconfirmed: !directory?.available || account.source === "recent",
    });
  }
  for (const id of selected) {
    if (!accounts.has(id)) {
      accounts.set(id, {
        id,
        source: "manual",
        selected: true,
        missing: true,
        unavailable: true,
        unconfirmed: true,
      });
    }
  }
  return [...accounts.values()];
}

export function filterCodexAccounts(accounts: readonly CodexAccountOption[], search: string): CodexAccountOption[] {
  const query = search.trim().toLocaleLowerCase();
  return accounts.filter((account) =>
    [account.id, account.label, account.name, account.email, account.status]
      .some((value) => value?.toLocaleLowerCase().includes(query)),
  );
}
