// In-memory session only. Never persisted to localStorage.
// Rationale: the secret-key is CPA's total management credential; we don't want
// it lying around in storage. Refreshing/closing the tab resets to the login page.

import { readPanelAuth } from "./panelAuth";
import type { StatusResponse } from "../types";

interface Session {
  baseUrl: string;
  secretKey: string;
}

let current: Session | null = null;

// Set when the operator explicitly logs out: the panel's remembered key must
// not silently log them back in on the next render.
let panelBootstrapDisabled = false;

const listeners = new Set<() => void>();

function normalizeBase(url: string): string {
  let u = url.trim();
  if (u === "") return "";
  u = u.replace(/\/+$/, "");
  if (!/^https?:\/\//i.test(u)) u = "http://" + u;
  return u;
}

// True when the base URL would send the management key over cleartext to a
// host other than loopback. The login form warns about this.
export function isInsecureBase(url: string): boolean {
  const base = normalizeBase(url);
  if (!/^http:\/\//i.test(base)) return false;
  try {
    const host = new URL(base).hostname.toLowerCase();
    return host !== "localhost" && host !== "127.0.0.1" && host !== "::1" && host !== "[::1]";
  } catch {
    return true;
  }
}

export function disablePanelBootstrap(): void {
  panelBootstrapDisabled = true;
}

// For tests: re-arm the panel bootstrap.
export function _resetPanelBootstrap(): void {
  panelBootstrapDisabled = false;
}

export function setSession(baseUrl: string, secretKey: string): Session {
  const session: Session = {
    baseUrl: normalizeBase(baseUrl),
    secretKey: secretKey.trim(),
  };
  current = session;
  emit();
  return session;
}

export function clearSession(): void {
  current = null;
  emit();
}

export function getSession(): Session | null {
  return current;
}

export function isAuthed(): boolean {
  return current !== null && current.secretKey !== "" && current.baseUrl !== "";
}

export function subscribe(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function emit(): void {
  for (const fn of listeners) fn();
}

// Attempt to restore a session from the official panel's saved management key
// (only available when this UI is loaded as a same-origin iframe inside the
// panel AND the user checked "remember password" there). On success the
// session is set and true is returned; on any failure the session is cleared
// and false is returned so the caller falls back to the login page.
export async function bootstrapFromPanel(): Promise<boolean> {
  if (panelBootstrapDisabled) return false;
  const auth = readPanelAuth();
  if (!auth) return false;
  setSession(auth.apiBase, auth.managementKey);
  try {
    await verifySession(fetch);
    return true;
  } catch {
    clearSession();
    return false;
  }
}

// Probe a candidate credential WITHOUT touching the live session, so the login
// form can show an error instead of briefly mounting the authenticated UI.
export async function verifyCredentials(
  fetchImpl: typeof fetch,
  baseUrl: string,
  secretKey: string,
): Promise<void> {
  const base = normalizeBase(baseUrl);
  const key = secretKey.trim();
  if (!base || !key) {
    throw new Error("base url and management key are required");
  }
  const res = await fetchImpl(base + "/v0/management/plugins/cpa-key-quota/status", {
    headers: { Authorization: "Bearer " + key },
  });
  if (!res.ok) {
    throw new Error("management key rejected (" + res.status + ")");
  }
  await res.json() as StatusResponse;
}

// Probe helper: confirm the current session's key works by hitting the plugin
// status route. Returns the session on success; throws on non-2xx.
export async function verifySession(
  fetchImpl: typeof fetch,
): Promise<Session> {
  const s = current;
  if (!s) throw new Error("no session");
  await verifyCredentials(fetchImpl, s.baseUrl, s.secretKey);
  return s;
}
