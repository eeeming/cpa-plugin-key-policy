import { afterEach, describe, it, expect, beforeEach, vi } from "vitest";
import {
  setSession,
  clearSession,
  getSession,
  isAuthed,
  subscribe,
  verifyCredentials,
  bootstrapFromPanel,
  disablePanelBootstrap,
  _resetPanelBootstrap,
  isInsecureBase,
} from "./session";
import { obfuscateData, _resetKeyCache } from "./panelAuth";

beforeEach(() => {
  clearSession();
  _resetPanelBootstrap();
});

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
  _resetKeyCache();
  _resetPanelBootstrap();
  clearSession();
});

function fakeFetch(status: number): { fn: typeof fetch; calls: string[] } {
  const calls: string[] = [];
  const fn = (async (input: RequestInfo | URL) => {
    calls.push(String(input));
    return {
      ok: status >= 200 && status < 300,
      status,
      json: async () => ({}),
    } as unknown as Response;
  }) as unknown as typeof fetch;
  return { fn, calls };
}

function setEmbedded(embedded: boolean): void {
  Object.defineProperty(window, "self", { value: window, configurable: true });
  Object.defineProperty(window, "top", {
    value: embedded ? ({ location: { origin: window.location.origin } } as Window) : window,
    configurable: true,
  });
}

function storePanelAuth(): void {
  localStorage.setItem(
    "cli-proxy-auth",
    obfuscateData(
      JSON.stringify({
        state: { apiBase: "http://127.0.0.1:8317", managementKey: "secret-xyz" },
        version: 0,
      }),
    ),
  );
}

describe("session storage", () => {
  it("starts unauthenticated", () => {
    expect(isAuthed()).toBe(false);
    expect(getSession()).toBeNull();
  });

  it("stores base url and key in memory", () => {
    localStorage.clear();
    setSession("http://localhost:8317/", "secret-xyz");
    const s = getSession();
    expect(s).not.toBeNull();
    expect(s!.baseUrl).toBe("http://localhost:8317");
    expect(s!.secretKey).toBe("secret-xyz");
    expect(isAuthed()).toBe(true);
    expect(localStorage.length).toBe(0);
  });

  it("adds http:// scheme when missing", () => {
    setSession("127.0.0.1:8317", "k");
    expect(getSession()!.baseUrl).toBe("http://127.0.0.1:8317");
  });

  it("preserves https://", () => {
    setSession("https://cpa.example.com/", "k");
    expect(getSession()!.baseUrl).toBe("https://cpa.example.com");
  });

  it("trims trailing slashes", () => {
    setSession("http://h:8317///", "k");
    expect(getSession()!.baseUrl).toBe("http://h:8317");
  });

  it("clears on logout", () => {
    setSession("http://h", "k");
    clearSession();
    expect(isAuthed()).toBe(false);
    expect(getSession()).toBeNull();
  });

  it("notifies subscribers on set and clear", () => {
    let calls = 0;
    const unsub = subscribe(() => calls++);
    setSession("http://h", "k");
    clearSession();
    expect(calls).toBeGreaterThanOrEqual(2);
    unsub();
  });

  it("is not authed when key empty", () => {
    setSession("http://h", "");
    expect(isAuthed()).toBe(false);
  });
});

describe("verifyCredentials", () => {
  it("probes without creating a session", async () => {
    const { fn, calls } = fakeFetch(200);
    await verifyCredentials(fn, "http://h:8317/", " k ");
    expect(calls).toEqual(["http://h:8317/v0/management/plugins/cpa-key-quota/status"]);
    expect(getSession()).toBeNull();
    expect(isAuthed()).toBe(false);
  });

  it("throws on a rejected key and still leaves the session empty", async () => {
    const { fn } = fakeFetch(401);
    await expect(verifyCredentials(fn, "http://h:8317", "bad")).rejects.toThrow("401");
    expect(isAuthed()).toBe(false);
  });

  it("rejects a blank base url or key before calling the network", async () => {
    const { fn, calls } = fakeFetch(200);
    await expect(verifyCredentials(fn, "", "k")).rejects.toThrow();
    await expect(verifyCredentials(fn, "http://h", "  ")).rejects.toThrow();
    expect(calls).toEqual([]);
  });
});

describe("bootstrapFromPanel", () => {
  afterEach(() => {
    Object.defineProperty(window, "self", { value: window, configurable: true });
    Object.defineProperty(window, "top", { value: window, configurable: true });
  });

  it("restores the panel session when a remembered key exists", async () => {
    setEmbedded(true);
    storePanelAuth();
    vi.stubGlobal("fetch", fakeFetch(200).fn);
    expect(await bootstrapFromPanel()).toBe(true);
    expect(isAuthed()).toBe(true);
    expect(getSession()!.secretKey).toBe("secret-xyz");
  });

  it("stays logged out after an explicit logout", async () => {
    setEmbedded(true);
    storePanelAuth();
    vi.stubGlobal("fetch", fakeFetch(200).fn);
    disablePanelBootstrap();
    expect(await bootstrapFromPanel()).toBe(false);
    expect(isAuthed()).toBe(false);
  });

  it("clears a session whose key the host rejects", async () => {
    setEmbedded(true);
    storePanelAuth();
    vi.stubGlobal("fetch", fakeFetch(401).fn);
    expect(await bootstrapFromPanel()).toBe(false);
    expect(isAuthed()).toBe(false);
  });
});

describe("isInsecureBase", () => {
  it("flags cleartext non-loopback hosts only", () => {
    expect(isInsecureBase("http://cpa.example.com")).toBe(true);
    expect(isInsecureBase("cpa.example.com")).toBe(true);
    expect(isInsecureBase("http://127.0.0.1:8317")).toBe(false);
    expect(isInsecureBase("http://localhost:8317")).toBe(false);
    expect(isInsecureBase("https://cpa.example.com")).toBe(false);
  });
});
