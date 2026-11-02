export interface UsageSummary {
  daily_usd: number;
  weekly_usd: number;
  daily_limit_usd: number;
  weekly_limit_usd: number;
  daily_window_start?: string;
  weekly_window_start?: string;
  daily_reset_at?: string | null;
  weekly_reset_at?: string | null;
  daily_call_count?: number;
  weekly_call_count?: number;
}

export interface KeyPublic {
  id: string;
  name: string;
  enabled: boolean;
  key_preview: string;
  rpm: number;
  daily_limit_usd: number;
  weekly_limit_usd: number;
  codex_auth_ids?: string[];
  codex_group?: string;
  codex_fallback?: boolean;
  usage: UsageSummary;
  created_at?: string;
  updated_at?: string;
}

export interface KeyWriteRequest {
  id: string;
  name?: string;
  enabled?: boolean;
  key?: string;
  rpm?: number;
  daily_limit_usd?: number;
  weekly_limit_usd?: number;
  codex_auth_ids?: string[];
  codex_group?: string;
  codex_fallback?: boolean;
}

export interface CodexBinding {
  codex_auth_ids: string[];
  codex_group: string;
  codex_fallback: boolean;
}

export interface BatchCodexBindingRequest {
  ids: string[];
  accounts_mode?: "keep" | "set" | "add" | "remove";
  codex_auth_ids?: string[];
  codex_group?: string;
  codex_fallback?: boolean;
  clear?: boolean;
  dry_run?: boolean;
  expected?: { id: string; before: CodexBinding }[];
}

export interface BatchCodexBindingResponse {
  dry_run: boolean;
  updated: number;
  changes: { id: string; before: CodexBinding; after: CodexBinding }[];
}

export interface BindKeyResponse {
  key: KeyPublic;
}

export interface UsageWindow {
  total_usd: number;
  call_count?: number;
  input_tokens?: number;
  output_tokens?: number;
}

export interface AliasUsageRow {
  alias: string;
  daily: UsageWindow;
  weekly: UsageWindow;
}

export interface KeyUsageResponse {
  key_id: string;
  key_name: string;
  enabled: boolean;
  daily_limit_usd: number;
  weekly_limit_usd: number;
  daily_usd: number;
  weekly_usd: number;
  daily_call_count?: number;
  weekly_call_count?: number;
  daily_window_start?: string | null;
  weekly_window_start?: string | null;
  daily_reset_at?: string | null;
  weekly_reset_at?: string | null;
  models?: AliasUsageRow[];
}

export interface StatusResponse {
  enabled: boolean;
  state_file: string;
  key_count: number;
}

export interface CodexAuthAccount {
  id: string;
  label?: string;
  name?: string;
  email?: string;
  status?: string;
  disabled?: boolean;
  unavailable?: boolean;
  priority?: number;
  source: "host" | "recent" | "host+recent";
}

export interface CodexSchedulerDiagnostics {
  scheduler_pick_count: number;
  last_scheduler_pick_at?: string;
}

export interface CodexAuthListResponse {
  available: boolean;
  source: "host" | "recent" | "unavailable";
  accounts: CodexAuthAccount[];
  error?: string;
  diagnostics?: CodexSchedulerDiagnostics;
}
