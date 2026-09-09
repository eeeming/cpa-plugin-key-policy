import { describe, it, expect } from "vitest";
import { errText } from "./error";

describe("errText", () => {
  it("prefers the plugin error envelope message over the axios status text", () => {
    const error = {
      response: {
        data: { error: { code: "invalid_policy", message: 'key hash already bound to "k-a"' } },
      },
      message: "Request failed with status code 400",
    };
    expect(errText(error, "fallback")).toBe('key hash already bound to "k-a" (invalid_policy)');
  });

  it("falls back to the code, then the axios message, then the caller fallback", () => {
    expect(errText({ response: { data: { error: { code: "not_found" } } } }, "fb")).toBe("not_found");
    expect(errText({ message: "Network Error" }, "fb")).toBe("Network Error");
    expect(errText(undefined, "fb")).toBe("fb");
    expect(errText({}, "fb")).toBe("fb");
    expect(errText(null, "fb")).toBe("fb");
  });

  it("accepts a plain string, a string error field, and a top-level message", () => {
    expect(errText("boom", "fb")).toBe("boom");
    expect(errText({ response: { data: { error: "plus_sync_failed" } } }, "fb")).toBe("plus_sync_failed");
    expect(errText({ response: { data: { message: "bad gateway" } } }, "fb")).toBe("bad gateway");
  });

  it("ignores empty/blank candidates", () => {
    expect(errText({ response: { data: { error: { message: "  " } } }, message: "  " }, "fb")).toBe("fb");
  });
});
