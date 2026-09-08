import { describe, it, expect } from "vitest";
import {
  toggleId,
  selectAll,
  invertSelection,
  normalizeRect,
  rectsIntersect,
  idsHitByMarquee,
  mergeSelection,
} from "./selection";
import zh from "./i18n/locales/zh-CN.json";
import en from "./i18n/locales/en.json";

describe("toggleId / selectAll / invertSelection", () => {
  it("toggles a card id in and out of the selection", () => {
    expect(toggleId([], "a")).toEqual(["a"]);
    expect(toggleId(["a"], "a")).toEqual([]);
    expect(toggleId(["a"], "b")).toEqual(["a", "b"]);
  });

  it("select-all copies the current list of ids", () => {
    expect(selectAll(["a", "b", "c"])).toEqual(["a", "b", "c"]);
  });

  it("invert-selection keeps ids that were not selected", () => {
    expect(invertSelection(["a"], ["a", "b", "c"])).toEqual(["b", "c"]);
    expect(invertSelection(["a", "b", "c"], ["a", "b", "c"])).toEqual([]);
  });

  it("merges marquee hits into the existing selection without duplicates", () => {
    expect(mergeSelection(["a"], ["a", "b"])).toEqual(["a", "b"]);
  });
});

describe("marquee rectangle hit test", () => {
  const cards = [
    { id: "a", rect: { x: 0, y: 0, w: 100, h: 80 } },
    { id: "b", rect: { x: 120, y: 0, w: 100, h: 80 } },
    { id: "c", rect: { x: 0, y: 100, w: 100, h: 80 } },
  ];

  it("normalizes inverted drag coordinates", () => {
    expect(normalizeRect(50, 50, 10, 20)).toEqual({ x: 10, y: 20, w: 40, h: 30 });
  });

  it("detects intersecting rectangles", () => {
    expect(rectsIntersect({ x: 0, y: 0, w: 10, h: 10 }, { x: 9, y: 9, w: 5, h: 5 })).toBe(true);
    expect(rectsIntersect({ x: 0, y: 0, w: 10, h: 10 }, { x: 10, y: 0, w: 5, h: 5 })).toBe(false);
  });

  it("returns ids whose cards intersect the drag rectangle", () => {
    const marquee = normalizeRect(10, 10, 150, 40);
    expect(idsHitByMarquee(marquee, cards)).toEqual(["a", "b"]);
  });

  it("does not hit cards below a top-row marquee", () => {
    const marquee = normalizeRect(0, 0, 200, 50);
    expect(idsHitByMarquee(marquee, cards)).toEqual(["a", "b"]);
  });
});

describe("bulk toolbar copy", () => {
  it("ships 全选 / 反选 / 设置限额 / 重置 in zh-CN and English", () => {
    expect(zh.keys.selectAll).toBe("全选");
    expect(zh.keys.invert).toBe("反选");
    expect(zh.keys.setLimits).toBe("设置限额");
    expect(zh.keys.reset).toBe("重置");
    expect(zh.keys.disable).toBe("停用");
    expect(zh.keys.unbind).toBe("解绑");
    expect(en.keys.selectAll).toBe("Select all");
    expect(en.keys.reset).toBe("Reset");
    expect(en.keys.resetOneConfirm).toContain("Limits stay the same");
  });
});
