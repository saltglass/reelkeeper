import { describe, expect, it } from "vitest";
import { formatBytes, formatDuration } from "./StatusPage";

describe("status formatting", () => {
  it("formats bytes", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(48 * 1024 * 1024)).toBe("48 MB");
  });
  it("formats durations", () => {
    expect(formatDuration(42)).toBe("42s");
    expect(formatDuration(3 * 60)).toBe("3m");
    expect(formatDuration(2 * 3600 + 5 * 60)).toBe("2h 5m");
    expect(formatDuration(3 * 86400 + 4 * 3600)).toBe("3d 4h");
  });
});
