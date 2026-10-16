import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import { afterEach } from "vitest";

afterEach(() => cleanup());

// findBy… waits up to 5 s instead of 1 s. The CI runner is a busy VM on a
// slow disk, where the first page a test file opens (its route is loaded
// lazily) can take over a second; a pass still returns as soon as the
// element appears.
configure({ asyncUtilTimeout: 5000 });

// jsdom lacks ResizeObserver, which Radix components use to measure.
if (!("ResizeObserver" in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

// jsdom doesn't implement scrolling; the router's scroll restoration calls it.
window.scrollTo = () => {};
