import { apiClient, pluginPath } from "./client";
import type { KeyPublic, KeyWriteRequest, BindKeyResponse, KeyUsageResponse } from "../types";

export async function listKeys(): Promise<KeyPublic[]> {
  const c = apiClient();
  const { data } = await c.get<{ keys: KeyPublic[] }>(pluginPath("/keys"));
  return data.keys ?? [];
}

export async function bindKey(req: KeyWriteRequest): Promise<BindKeyResponse> {
  const c = apiClient();
  const { data } = await c.post<BindKeyResponse>(pluginPath("/keys"), req);
  return data;
}

export async function patchKey(req: KeyWriteRequest): Promise<KeyPublic> {
  const c = apiClient();
  const { data } = await c.patch<{ key: KeyPublic }>(pluginPath("/keys"), req);
  return data.key;
}

export async function deleteKey(id: string): Promise<void> {
  const c = apiClient();
  await c.delete(pluginPath("/keys"), { params: { id } });
}

export interface SyncPlusResult {
  added: number;
  skipped: number;
  total: number;
  added_ids?: string[];
}

export async function syncPlusKeys(): Promise<SyncPlusResult> {
  const c = apiClient();
  const { data } = await c.post<SyncPlusResult>(pluginPath("/keys/sync"));
  return data;
}

export async function fetchKeyUsage(id: string): Promise<KeyUsageResponse> {
  const c = apiClient();
  const { data } = await c.get<KeyUsageResponse>(pluginPath("/keys/usage"), {
    params: { id },
  });
  return data;
}

export function quotaStatus(k: KeyPublic): "ok" | "daily" | "weekly" | "disabled" {
  if (!k.enabled) return "disabled";
  const u = k.usage;
  if (u.weekly_limit_usd > 0 && u.weekly_usd >= u.weekly_limit_usd) return "weekly";
  if (u.daily_limit_usd > 0 && u.daily_usd >= u.daily_limit_usd) return "daily";
  return "ok";
}
