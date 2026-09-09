// Limit-input parsing shared by the bulk and single-key forms.
//
// A blank field is ambiguous by nature, so the caller picks the meaning:
//   - "omit" (bulk): blank = leave this limit unchanged on every selected key.
//   - "zero" (single form, whose labels read "empty = unlimited"): blank = 0.
// Anything that is not a finite, non-negative number is rejected before the
// request is sent, so NaN / Infinity can never silently become a null limit.
import type { KeyWriteRequest } from "./types";

export interface LimitInputs {
  daily?: number;
  weekly?: number;
  rpm?: number;
}

export interface LimitFields {
  daily: string;
  weekly: string;
  rpm: string;
}

export interface LimitParseResult {
  ok: boolean;
  values?: LimitInputs;
  invalid?: (keyof LimitFields)[];
}

export type BlankMode = "omit" | "zero";

function parseField(raw: string, integer: boolean): { ok: boolean; value?: number } {
  const trimmed = raw.trim();
  if (trimmed === "") return { ok: true };
  const n = Number(trimmed);
  if (!Number.isFinite(n) || n < 0) return { ok: false };
  if (integer && !Number.isInteger(n)) return { ok: false };
  return { ok: true, value: n };
}

export function parseLimits(input: LimitFields, blank: BlankMode = "omit"): LimitParseResult {
  const daily = parseField(input.daily, false);
  const weekly = parseField(input.weekly, false);
  const rpm = parseField(input.rpm, true);
  const invalid: (keyof LimitFields)[] = [];
  if (!daily.ok) invalid.push("daily");
  if (!weekly.ok) invalid.push("weekly");
  if (!rpm.ok) invalid.push("rpm");
  if (invalid.length) return { ok: false, invalid };
  const values: LimitInputs = {};
  if (daily.value !== undefined) values.daily = daily.value;
  else if (blank === "zero") values.daily = 0;
  if (weekly.value !== undefined) values.weekly = weekly.value;
  else if (blank === "zero") values.weekly = 0;
  if (rpm.value !== undefined) values.rpm = rpm.value;
  else if (blank === "zero") values.rpm = 0;
  return { ok: true, values };
}

export function hasAnyLimit(values: LimitInputs): boolean {
  return values.daily !== undefined || values.weekly !== undefined || values.rpm !== undefined;
}

// Build a PATCH/POST body that only carries the limits the operator actually
// entered, so blank fields are left untouched instead of being reset to 0.
export function limitPatch(id: string, values: LimitInputs): KeyWriteRequest {
  const req: KeyWriteRequest = { id };
  if (values.daily !== undefined) req.daily_limit_usd = values.daily;
  if (values.weekly !== undefined) req.weekly_limit_usd = values.weekly;
  if (values.rpm !== undefined) req.rpm = values.rpm;
  return req;
}
