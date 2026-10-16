import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetToastsForTests, toast, useToasts } from "@/hooks/toast";

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  resetToastsForTests();
  vi.useRealTimers();
});

describe("toast", () => {
  it("shows a notice, then it leaves after about 3 seconds", () => {
    const { result } = renderHook(() => useToasts());
    act(() => toast("Saved"));
    expect(result.current.map((t) => t.text)).toEqual(["Saved"]);
    act(() => vi.advanceTimersByTime(3100));
    expect(result.current[0]?.leaving).toBe(true);
    act(() => vi.advanceTimersByTime(400));
    expect(result.current).toEqual([]);
  });

  it("keeps problems on screen for longer", () => {
    const { result } = renderHook(() => useToasts());
    act(() => toast("Download failed", "problem"));
    act(() => vi.advanceTimersByTime(5000));
    expect(result.current[0]?.leaving).toBe(false);
    act(() => vi.advanceTimersByTime(1100));
    expect(result.current[0]?.leaving).toBe(true);
  });

  it("shows at most three at once, dropping the oldest", () => {
    const { result } = renderHook(() => useToasts());
    act(() => {
      for (const n of ["a", "b", "c", "d"]) toast(n);
    });
    expect(result.current.map((t) => t.text)).toEqual(["b", "c", "d"]);
  });
});
