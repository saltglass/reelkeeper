import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useLiveEvents } from "@/hooks/events";
import { resetToastsForTests, useToasts } from "@/hooks/toast";

/** Stands in for the browser's EventSource so a test can send events. */
class FakeEventSource {
  static last: FakeEventSource;
  handlers = new Map<string, (e: MessageEvent) => void>();
  constructor(public url: string) {
    FakeEventSource.last = this;
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void) {
    this.handlers.set(type, fn);
  }
  close() {}
  send(type: string, data: unknown) {
    this.handlers.get(type)?.({ data: JSON.stringify(data) } as MessageEvent);
  }
}

function Probe() {
  useLiveEvents(true);
  const toasts = useToasts();
  return <p data-testid="out">{toasts.map((t) => `${t.kind}:${t.text}`).join("|")}</p>;
}

afterEach(() => {
  vi.unstubAllGlobals();
  resetToastsForTests();
});

describe("live events", () => {
  it("turns a notice into a pop-up, with a warning look for problems", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const { getByTestId } = render(
      <QueryClientProvider client={new QueryClient()}>
        <Probe />
      </QueryClientProvider>,
    );
    act(() =>
      FakeEventSource.last.send("notice", {
        kind: "imported",
        message: "Dune imported",
        problem: false,
      }),
    );
    act(() =>
      FakeEventSource.last.send("notice", {
        kind: "grab-failed",
        message: "Grab failed",
        problem: true,
      }),
    );
    expect(getByTestId("out").textContent).toBe("ok:Dune imported|problem:Grab failed");
  });

  it("refreshes the backups list when the daily backup finishes", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const qc = new QueryClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    render(
      <QueryClientProvider client={qc}>
        <Probe />
      </QueryClientProvider>,
    );
    const job = { id: 1, kind: "backup.scheduled", status: "running" };
    act(() => FakeEventSource.last.send("task.updated", job));
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: ["backups"] });
    act(() => FakeEventSource.last.send("task.updated", { ...job, status: "succeeded" }));
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["backups"] });
  });
});
