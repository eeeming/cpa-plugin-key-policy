import { describe, it, expect, beforeEach } from "vitest";
import {
  setSession,
  clearSession,
  getSession,
  isAuthed,
  subscribe,
} from "./session";

beforeEach(() => clearSession());

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
