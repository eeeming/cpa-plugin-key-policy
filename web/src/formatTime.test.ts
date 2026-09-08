import { describe, expect, it } from "vitest";
import { formatResetAt, formatResetShort } from "./formatTime";

describe("formatResetAt", () => {
  it("returns null for missing or sentinel dates", () => {
    expect(formatResetAt(undefined, "zh-CN")).toBeNull();
    expect(formatResetAt(null, "zh-CN")).toBeNull();
    expect(formatResetAt("", "en")).toBeNull();
    expect(formatResetAt("0001-01-01T00:00:00Z", "en")).toBeNull();
  });

  it("renders a real timestamp in the requested locale", () => {
    const got = formatResetAt("2026-09-09T15:04:00Z", "en-US");
    expect(got).toBeTruthy();
    expect(got).toMatch(/2026/);
    expect(got).toMatch(/\d{2}:\d{2}/);
  });
});

describe("formatResetShort", () => {
  it("returns MM/DD HH:mm in local time", () => {
    const got = formatResetShort("2026-09-15T01:26:00Z");
    expect(got).toMatch(/^\d{2}\/\d{2} \d{2}:\d{2}$/);
  });
});
