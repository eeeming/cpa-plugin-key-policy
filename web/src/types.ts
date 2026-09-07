export interface UsageSummary {
  daily_usd: number;
  weekly_usd: number;
  daily_limit_usd: number;
  weekly_limit_usd: number;
  daily_reset_at?: string;
  weekly_reset_at?: string;
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
}

export interface StatusResponse {
  enabled: boolean;
  state_file: string;
  key_count: number;
}
