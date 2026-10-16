import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

function open() {
  const saved: unknown[] = [];
  const app = renderApp("/settings/general", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /settings/metadata": json({ tmdbApiKeySet: true, language: "en-US" }),
    "GET /settings/labels": json({ parts: ["profile", "quality", "hdr"] }),
    "PUT /settings/labels": async (req) => {
      const body = await req.json();
      saved.push(body);
      return json(body);
    },
  });
  return { ...app, saved };
}

describe("labels settings", () => {
  it("previews the choice live and saves it in display order", async () => {
    const { saved } = open();
    const [remux] = await screen.findAllByTestId("label-preview");
    if (!remux) throw new Error("no preview");
    expect(within(remux).getByText("Remux · Remux-2160p · DV · HDR10")).toBeInTheDocument();

    await userEvent.click(screen.getByLabelText(/^Size/));
    await userEvent.click(screen.getByLabelText(/^Audio/));
    await userEvent.click(screen.getByLabelText(/^Quality profile/));
    expect(
      within(remux).getByText("Remux-2160p · DV · HDR10 · TrueHD 7.1 · 62 GB"),
    ).toBeInTheDocument();

    const form = screen.getByRole("form", { name: "Labels" });
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));
    await within(form).findByRole("button", { name: "Saved" });
    expect(saved).toEqual([{ parts: ["quality", "hdr", "audio", "size"] }]);
  });

  it("says when a choice leaves no label", async () => {
    open();
    await screen.findAllByTestId("label-preview");
    for (const name of ["Quality profile", "Quality", "HDR and Dolby Vision"]) {
      await userEvent.click(screen.getByLabelText(name));
    }
    expect(screen.getAllByText("No label")).toHaveLength(2);
  });
});
