import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { NearItem, NearScreen, ROWS_AHEAD } from "@/hooks/nearScreen";

/** Stands in for the browser's IntersectionObserver so a test can "scroll". */
class FakeObserver {
  static live: FakeObserver[] = [];
  watched: Element[] = [];
  constructor(
    public cb: IntersectionObserverCallback,
    public options: IntersectionObserverInit,
  ) {
    FakeObserver.live.push(this);
  }
  observe(el: Element) {
    this.watched.push(el);
  }
  unobserve(el: Element) {
    this.watched = this.watched.filter((w) => w !== el);
  }
  disconnect() {
    this.watched = [];
    FakeObserver.live = FakeObserver.live.filter((o) => o !== this);
  }
  /** Reports whether the tile with this label is within the margin. */
  static report(label: string, near: boolean) {
    for (const o of [...FakeObserver.live])
      for (const el of o.watched)
        if (el.getAttribute("data-label") === label)
          o.cb([{ target: el, isIntersecting: near } as never], o as never);
  }
}

function Grid() {
  return (
    <NearScreen>
      <ul>
        {["a", "b"].map((label) => (
          <NearItem key={label} className="poster-item">
            {(near) => <span data-testid={label}>{near ? "poster" : "empty"}</span>}
          </NearItem>
        ))}
      </ul>
    </NearScreen>
  );
}

// NearItem puts the label on the child; the observer watches the li around it.
function label(el: Element) {
  el.parentElement?.setAttribute("data-label", el.getAttribute("data-testid") ?? "");
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  FakeObserver.live = [];
});

describe("NearScreen", () => {
  it("draws posters as tiles come near the screen and lets go of far ones", () => {
    vi.stubGlobal("IntersectionObserver", FakeObserver);
    render(<Grid />);
    label(screen.getByTestId("a"));
    label(screen.getByTestId("b"));
    expect(screen.getByTestId("a").textContent).toBe("empty");

    act(() => FakeObserver.report("a", true));
    expect(screen.getByTestId("a").textContent).toBe("poster");
    expect(screen.getByTestId("a").parentElement?.className).toContain("near");
    expect(screen.getByTestId("b").textContent).toBe("empty");

    act(() => FakeObserver.report("a", false));
    expect(screen.getByTestId("a").textContent).toBe("empty");
  });

  it("looks the given number of rows ahead, above and below", () => {
    vi.stubGlobal("IntersectionObserver", FakeObserver);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      height: 300,
    } as DOMRect);
    render(<Grid />);
    // jsdom has no layout, so the row gap reads as 0.
    expect(FakeObserver.live[0]?.options.rootMargin).toBe(`${300 * ROWS_AHEAD}px 0px`);
    vi.restoreAllMocks();
  });

  it("stops watching when the grid goes away", () => {
    vi.stubGlobal("IntersectionObserver", FakeObserver);
    const { unmount } = render(<Grid />);
    expect(FakeObserver.live).toHaveLength(1);
    unmount();
    expect(FakeObserver.live).toHaveLength(0);
  });

  it("draws every poster where the browser can't tell what is near", () => {
    vi.stubGlobal("IntersectionObserver", undefined);
    render(<Grid />);
    expect(screen.getByTestId("a").textContent).toBe("poster");
    expect(screen.getByTestId("b").textContent).toBe("poster");
  });
});
