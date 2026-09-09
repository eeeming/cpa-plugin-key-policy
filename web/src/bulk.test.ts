import { describe, it, expect } from "vitest";
import { parseLimits, hasAnyLimit, limitPatch } from "./bulk";

describe("parseLimits", () => {
  it("omits blank fields so they are left unchanged", () => {
    const got = parseLimits({ daily: "", weekly: "5", rpm: "  " });
    expect(got.ok).toBe(true);
    expect(got.values).toEqual({ weekly: 5 });
  });

  it("keeps an explicit 0 as 'unlimited'", () => {
    expect(parseLimits({ daily: "0", weekly: "", rpm: "" }).values).toEqual({ daily: 0 });
  });

  it("accepts decimals for USD and integers for rpm", () => {
    expect(parseLimits({ daily: "1.25", weekly: "", rpm: "" }).values).toEqual({ daily: 1.25 });
    expect(parseLimits({ daily: "", weekly: "", rpm: "60" }).values).toEqual({ rpm: 60 });
    expect(parseLimits({ daily: "", weekly: "", rpm: "1.5" }).ok).toBe(false);
  });

  it("rejects negative, non-numeric, and non-finite input", () => {
    expect(parseLimits({ daily: "abc", weekly: "", rpm: "" }).ok).toBe(false);
    expect(parseLimits({ daily: "-1", weekly: "", rpm: "" }).ok).toBe(false);
    expect(parseLimits({ daily: "1e999", weekly: "", rpm: "" }).ok).toBe(false);
  });

  it("reports which fields were invalid", () => {
    expect(parseLimits({ daily: "x", weekly: "y", rpm: "" }).invalid).toEqual(["daily", "weekly"]);
  });

  it("treats blank fields as 0 in the single-key form (blank = unlimited)", () => {
    expect(parseLimits({ daily: "", weekly: "2", rpm: "" }, "zero").values).toEqual({
      daily: 0,
      weekly: 2,
      rpm: 0,
    });
  });
});

describe("limitPatch", () => {
  it("only carries the limits that were provided", () => {
    expect(limitPatch("a", { rpm: 3 })).toEqual({ id: "a", rpm: 3 });
    expect(limitPatch("a", { daily: 0, weekly: 2 })).toEqual({
      id: "a",
      daily_limit_usd: 0,
      weekly_limit_usd: 2,
    });
    expect(limitPatch("a", {})).toEqual({ id: "a" });
  });

  it("hasAnyLimit distinguishes empty from zero", () => {
    expect(hasAnyLimit({})).toBe(false);
    expect(hasAnyLimit({ daily: 0 })).toBe(true);
  });
});
