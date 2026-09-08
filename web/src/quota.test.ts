import { describe, expect, it } from "vitest";
import { formatUsd, quotaAmountLabel, quotaProgress } from "./quota";

describe("formatUsd", () => {
  it("always uses four decimal places", () => {
    expect(formatUsd(0.5)).toBe("$0.5000");
    expect(formatUsd(0.7408)).toBe("$0.7408");
    expect(formatUsd(10)).toBe("$10.0000");
    expect(formatUsd(0)).toBe("$0.0000");
  });
});

describe("quotaAmountLabel", () => {
  it("shows used / limit, or just used when unlimited", () => {
    expect(quotaAmountLabel(0.7408, 10)).toBe("$0.7408 / $10.0000");
    expect(quotaAmountLabel(0.8, 0)).toBe("$0.8000");
  });
});


describe("quotaProgress", () => {
  it("treats non-positive limit as unlimited", () => {
    expect(quotaProgress(12, 0)).toEqual({ pct: null, fill: 0, tone: "none" });
  });

  it("caps the bar at 100 while showing over 100%", () => {
    expect(quotaProgress(1.5, 1)).toEqual({ pct: 150, fill: 100, tone: "over" });
  });

  it("stays ok at 95% and warns just below the cap", () => {
    expect(quotaProgress(0.95, 1).tone).toBe("ok");
    expect(quotaProgress(0.97, 1).tone).toBe("warn");
    expect(quotaProgress(0.5, 1).tone).toBe("ok");
    expect(quotaProgress(0.5, 1).pct).toBe(50);
  });
});
