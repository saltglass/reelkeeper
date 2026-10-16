import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BATCH, useInBatches } from "@/hooks/batches";

/** Stands in for the browser's IntersectionObserver so a test can "scroll". */
class FakeObserver {
  static live: FakeObserver[] = [];
  constructor(public cb: IntersectionObserverCallback) {}
  observe() {
    FakeObserver.live.push(this);
  }
  disconnect() {
    FakeObserver.live = FakeObserver.live.filter((o) => o !== this);
  }
  static reachEnd() {
    for (const o of [...FakeObserver.live])
      o.cb([{ isIntersecting: true } as IntersectionObserverEntry], o as never);
  }
}

function List({ n, view }: { n: number; view: string }) {
  const items = Array.from({ length: n }, (_, i) => i);
  const { visible, more } = useInBatches(items, view);
  return (
    <>
      <p data-testid="count">{visible.length}</p>
      {more}
    </>
  );
}

const count = () => Number(screen.getByTestId("count").textContent);

afterEach(() => {
  vi.unstubAllGlobals();
  FakeObserver.live = [];
});

describe("useInBatches", () => {
  it("draws one batch, then another each time the end comes near", () => {
    vi.stubGlobal("IntersectionObserver", FakeObserver);
    render(<List n={300} view="a" />);
    expect(count()).toBe(BATCH);
    act(() => FakeObserver.reachEnd());
    expect(count()).toBe(BATCH * 2);
    act(() => FakeObserver.reachEnd());
    expect(count()).toBe(300);
    // Everything is drawn, so nothing is watched any more.
    expect(FakeObserver.live).toHaveLength(0);
  });

  it("starts again from the first batch for a new filter or sort", () => {
    vi.stubGlobal("IntersectionObserver", FakeObserver);
    const { rerender } = render(<List n={300} view="a" />);
    act(() => FakeObserver.reachEnd());
    expect(count()).toBe(BATCH * 2);
    rerender(<List n={300} view="b" />);
    expect(count()).toBe(BATCH);
  });

  it("draws short lists whole", () => {
    vi.stubGlobal("IntersectionObserver", FakeObserver);
    render(<List n={BATCH - 1} view="a" />);
    expect(count()).toBe(BATCH - 1);
    expect(FakeObserver.live).toHaveLength(0);
  });

  it("draws everything where the browser can't tell when the end is near", () => {
    vi.stubGlobal("IntersectionObserver", undefined);
    render(<List n={500} view="a" />);
    expect(count()).toBe(500);
  });
});
