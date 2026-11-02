import { describe, expect, it } from "vitest";
import {
  addAuthIds,
  codexBindingPatch,
  filterCodexAccounts,
  initialCodexBinding,
  mergeCodexAccounts,
  normalizeAuthIds,
  parseAuthIds,
  toggleAuthId,
} from "./codexBinding";
import type { CodexAuthListResponse, KeyWriteRequest } from "./types";

const directory: CodexAuthListResponse = {
  available: true,
  source: "host",
  accounts: [
    { id: "auth-a", label: "Work", name: "account.json", email: "a@example.com", status: "ready", source: "host" },
    { id: "auth-b", label: "Work", status: "disabled", disabled: true, source: "host+recent" },
    { id: "auth-old", source: "recent", unavailable: true },
  ],
};

describe("Codex binding selection consumers", () => {
  it("keeps selected IDs absent from a successful directory and marks them invalid", () => {
    const value = initialCodexBinding({ codex_auth_ids: ["auth-a", "auth-missing"] });
    const options = mergeCodexAccounts(directory, value.ids);
    expect(options.find((account) => account.id === "auth-missing")).toMatchObject({
      selected: true, missing: true, unavailable: true, unconfirmed: true, source: "manual",
    });
    expect(codexBindingPatch(value).codex_auth_ids).toEqual(["auth-a", "auth-missing"]);
  });

  it("retains bindings when the directory request fails or is unavailable", () => {
    const value = initialCodexBinding({ codex_auth_ids: ["auth-a", "auth-missing"], codex_group: "team", codex_fallback: true });
    const recent: CodexAuthListResponse = {
      available: false,
      source: "recent",
      accounts: [{ id: "auth-a", email: "a@example.com", source: "recent" }],
      error: "Host directory unavailable",
    };
    for (const response of [null, recent, { available: false, source: "unavailable", accounts: [] } as CodexAuthListResponse]) {
      expect(mergeCodexAccounts(response, value.ids).filter((account) => account.selected).map((account) => account.id)).toEqual(value.ids);
      expect(codexBindingPatch(value)).toEqual({ codex_auth_ids: ["auth-a", "auth-missing"], codex_group: "team", codex_fallback: true });
    }
    expect(mergeCodexAccounts(recent, value.ids)[0]).toMatchObject({ selected: true, unconfirmed: true, missing: false });
  });

  it("marks recent-only accounts missing when the host directory succeeded", () => {
    const options = mergeCodexAccounts(directory, ["auth-old", "auth-b"]);
    expect(options.find((account) => account.id === "auth-old")).toMatchObject({ selected: true, missing: true, unavailable: true, unconfirmed: true });
    expect(options.find((account) => account.id === "auth-b")).toMatchObject({ selected: true, disabled: true, unconfirmed: false });
    expect(options.find((account) => account.id === "auth-a")).toMatchObject({ unconfirmed: false });
  });

  it("merges manual entries, trimming empty entries and deduplicating all documented separators", () => {
    expect(parseAuthIds(" auth-a\nauth-b,auth-a; auth-c\t\r\nauth-d，auth-e；auth-f、auth-b ")).toEqual([
      "auth-a", "auth-b", "auth-c", "auth-d", "auth-e", "auth-f",
    ]);
    expect(addAuthIds(["auth-a", "", " auth-b "], "auth-b, auth-c\nauth-a")).toEqual(["auth-a", "auth-b", "auth-c"]);
    expect(normalizeAuthIds(["auth-a", "Auth-A", "auth-a"])).toEqual(["auth-a", "Auth-A"]);
  });

  it("preserves internal spaces in host IDs when merging manual entries and saving", () => {
    const hostDirectory: CodexAuthListResponse = {
      available: true,
      source: "host",
      accounts: [{ id: "My Codex Account.json", source: "host" }],
    };
    const value = initialCodexBinding({ codex_auth_ids: ["My Codex Account.json"] });
    const patch = codexBindingPatch(value, " My Codex Account.json , Another Account.json ;\n My Codex Account.json ");
    expect(patch.codex_auth_ids).toEqual(["My Codex Account.json", "Another Account.json"]);
    expect(mergeCodexAccounts(hostDirectory, patch.codex_auth_ids)[0]).toMatchObject({
      id: "My Codex Account.json", selected: true, missing: false, unconfirmed: false,
    });
  });

  it("includes pending manual text on save, without requiring the Add button", () => {
    const value = initialCodexBinding({ codex_auth_ids: ["auth-a"] });
    expect(codexBindingPatch(value, "auth-a,auth-new\n")).toEqual({
      codex_auth_ids: ["auth-a", "auth-new"], codex_group: "", codex_fallback: false,
    });
    expect(value.ids).toEqual(["auth-a"]);
  });

  it("filters display only and saves selected IDs hidden by search", () => {
    const value = initialCodexBinding({ codex_auth_ids: ["auth-b", "auth-missing"] });
    const options = mergeCodexAccounts(directory, value.ids);
    expect(filterCodexAccounts(options, "A@EXAMPLE.COM").map((account) => account.id)).toEqual(["auth-a"]);
    expect(filterCodexAccounts(options, "no-match")).toEqual([]);
    expect(codexBindingPatch(value).codex_auth_ids).toEqual(["auth-b", "auth-missing"]);
    expect(filterCodexAccounts(options, "").filter((account) => account.selected).map((account) => account.id)).toEqual(["auth-b", "auth-missing"]);
    for (const query of ["Work", "account.json", "ready", "auth-a"]) {
      expect(filterCodexAccounts(options, query).some((account) => account.id === "auth-a")).toBe(true);
    }
  });

  it("uses stable IDs rather than equal display labels for selection", () => {
    expect(toggleAuthId(["auth-a"], "auth-b")).toEqual(["auth-a", "auth-b"]);
    expect(toggleAuthId(["auth-a", "auth-b"], "auth-a")).toEqual(["auth-b"]);
    expect(mergeCodexAccounts(directory, ["auth-a"]).filter((account) => account.selected).map((account) => account.id)).toEqual(["auth-a"]);
  });

  it("initializes existing configuration and defaults fallback off for old keys", () => {
    expect(initialCodexBinding()).toEqual({ ids: [], group: "", fallback: false });
    expect(initialCodexBinding({ codex_auth_ids: [" auth-a ", "auth-a", ""], codex_group: "team", codex_fallback: true })).toEqual({
      ids: ["auth-a"], group: "team", fallback: true,
    });
  });

  it("sends empty arrays, empty groups and false in PATCH to clear existing configuration", () => {
    const existing = initialCodexBinding({ codex_auth_ids: ["auth-a"], codex_group: "team", codex_fallback: true });
    const cleared = { ids: toggleAuthId(existing.ids, "auth-a"), group: "  ", fallback: false };
    const request: KeyWriteRequest = { id: "stable-key-id", ...codexBindingPatch(cleared) };
    expect(JSON.parse(JSON.stringify(request))).toEqual({
      id: "stable-key-id", codex_auth_ids: [], codex_group: "", codex_fallback: false,
    });
    expect(codexBindingPatch({ ...existing, group: " team " }).codex_group).toBe("team");
  });
});
