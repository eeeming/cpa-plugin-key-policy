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
