import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DevelopImg } from "./DevelopImg";

describe("DevelopImg", () => {
  it("stays clear until the picture arrives, then develops", () => {
    render(<DevelopImg src="/poster.jpg" alt="Poster" className="size-full" />);
    const img = screen.getByAltText("Poster");
    expect(img).toHaveClass("develop", "size-full");
    expect(img).not.toHaveAttribute("data-developed");
    fireEvent.load(img);
    expect(img).toHaveAttribute("data-developed");
  });

  it("doesn't leave a clear box when the picture fails", () => {
    render(<DevelopImg src="/missing.jpg" alt="Missing" />);
    const img = screen.getByAltText("Missing");
    fireEvent.error(img);
    expect(img).toHaveAttribute("data-developed");
  });
});
