import { describe, it, expect } from "vitest";
import { quotaStatus } from "./keys";
import type { KeyPublic } from "../types";

function key(over: Partial<KeyPublic> = {}): KeyPublic {
  const usage = {
    daily_usd: 0,
    weekly_usd: 0,
    daily_limit_usd: 1,
    weekly_limit_usd: 10,
    ...(over.usage ?? {}),
  };
  return {
    id: "a",
    name: "a",
    enabled: true,
    key_preview: "sk-...a",
    rpm: 0,
    daily_limit_usd: 1,
    weekly_limit_usd: 10,
    ...over,
    usage,
  };
}

describe("quotaStatus", () => {
  it("reports disabled, daily, weekly, and ok", () => {
    expect(quotaStatus(key({ enabled: false }))).toBe("disabled");
    expect(quotaStatus(key({ usage: { daily_usd: 1, weekly_usd: 0, daily_limit_usd: 1, weekly_limit_usd: 10 } }))).toBe("daily");
    expect(quotaStatus(key({ usage: { daily_usd: 0, weekly_usd: 10, daily_limit_usd: 1, weekly_limit_usd: 10 } }))).toBe("weekly");
    expect(quotaStatus(key())).toBe("ok");
  });

  it("names the daily cap when both are over, matching backend order", () => {
    expect(
      quotaStatus(key({ usage: { daily_usd: 2, weekly_usd: 20, daily_limit_usd: 1, weekly_limit_usd: 10 } })),
    ).toBe("daily");
  });

  it("ignores limits of 0 (unlimited)", () => {
    expect(
      quotaStatus(key({ usage: { daily_usd: 99, weekly_usd: 99, daily_limit_usd: 0, weekly_limit_usd: 0 } })),
    ).toBe("ok");
  });
});
