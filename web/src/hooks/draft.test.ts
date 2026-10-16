import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useDraft } from "./draft";

const eq = (a: { n: number }, b: { n: number }) => a.n === b.n;

describe("useDraft", () => {
  it("follows server changes until the user edits, then keeps the edits", () => {
    const { result, rerender } = renderHook(({ data }) => useDraft(data, eq), {
      initialProps: { data: { n: 1 } },
    });
    expect(result.current.draft).toEqual({ n: 1 });
    expect(result.current.dirty).toBe(false);

    rerender({ data: { n: 2 } }); // saved elsewhere
    expect(result.current.draft).toEqual({ n: 2 });

    act(() => result.current.setDraft({ n: 5 }));
    expect(result.current.dirty).toBe(true);

    rerender({ data: { n: 3 } }); // changed elsewhere while editing
    expect(result.current.draft).toEqual({ n: 5 });

    act(() => result.current.reset());
    expect(result.current.draft).toEqual({ n: 3 });
    expect(result.current.dirty).toBe(false);

    act(() => result.current.setDraft({ n: 9 }));
    act(() => result.current.commit({ n: 9 }));
    expect(result.current.dirty).toBe(false);
  });
});
