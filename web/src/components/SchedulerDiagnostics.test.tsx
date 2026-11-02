import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { listCodexAuths } from "../api/keys";
import { _resetLocale } from "../i18n";
import SchedulerDiagnostics from "./SchedulerDiagnostics";

vi.mock("../api/keys", () => ({ listCodexAuths: vi.fn() }));
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let root: Root;
let container: HTMLDivElement;

beforeEach(() => {
  vi.clearAllMocks();
  _resetLocale("en");
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  _resetLocale("zh-CN");
});

describe("observed scheduler diagnostics", () => {
  it("renders only count and the browser-local timestamp, retaining an ISO dateTime", async () => {
    const localTime = new Intl.DateTimeFormat(undefined, {
      year: "numeric", month: "numeric", day: "numeric", hour: "numeric", minute: "numeric", second: "numeric",
    }).format(new Date("2026-11-02T07:08:09+08:00"));
    vi.mocked(listCodexAuths).mockResolvedValue({ available: false, source: "unavailable", accounts: [], diagnostics: { scheduler_pick_count: 12, last_scheduler_pick_at: "2026-11-02T07:08:09+08:00" } });
    await act(async () => root.render(<SchedulerDiagnostics />));
    expect(container.querySelectorAll("dt")).toHaveLength(2);
    expect(container.querySelector("dd")?.textContent).toBe("12");
    expect(container.querySelector("time")?.textContent).toBe(localTime);
    expect(container.querySelector("time")?.textContent).not.toBe(container.querySelector("time")?.dateTime);
    expect(container.querySelector("time")?.dateTime).toBe("2026-11-01T23:08:09.000Z");
    expect(container.querySelector("[role='note']")).toBeNull();
  });

  it("keeps zero observations quiet and handles invalid timestamps safely", async () => {
    vi.mocked(listCodexAuths).mockResolvedValue({ available: false, source: "unavailable", accounts: [], diagnostics: { scheduler_pick_count: 0, last_scheduler_pick_at: "invalid" } });
    await act(async () => root.render(<SchedulerDiagnostics />));
    expect(container.querySelector("dd")?.textContent).toBe("0");
    expect(container.querySelector("time")).toBeNull();
    expect(container.querySelector(".scheduler-diagnostics-note")).toBeNull();
  });

  it("shows loading and errors and allows refreshing the observed values", async () => {
    let rejectRequest!: (error: Error) => void;
    vi.mocked(listCodexAuths).mockReturnValueOnce(new Promise((_, reject) => { rejectRequest = reject; }));
    await act(async () => root.render(<SchedulerDiagnostics />));
    expect(container.querySelector("button")?.disabled).toBe(true);
    expect(container.querySelector("[aria-busy='true']")).not.toBeNull();
    await act(async () => rejectRequest(new Error("directory request failed")));
    expect(container.querySelector("[role='alert']")?.textContent).toContain("directory request failed");
    expect(container.querySelector("button")?.disabled).toBe(false);
    vi.mocked(listCodexAuths).mockResolvedValueOnce({ available: true, source: "host", accounts: [], diagnostics: { scheduler_pick_count: 3 } });
    await act(async () => container.querySelector("button")!.click());
    expect(container.querySelector("dd")?.textContent).toBe("3");
    expect(container.querySelector("[role='alert']")).toBeNull();
  });
});
