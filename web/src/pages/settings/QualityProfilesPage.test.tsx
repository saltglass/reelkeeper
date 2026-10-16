import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const hd = {
  id: 1,
  name: "HD",
  slug: "hd",
  appliesTo: "both",
  mediaFolders: 2,
  movies: 14,
  series: 0,
};
const remux = {
  id: 2,
  name: "Remux",
  slug: "remux",
  appliesTo: "movie",
  mediaFolders: 0,
  movies: 0,
  series: 0,
};

function open(extra: Parameters<typeof renderApp>[1] = {}) {
  return renderApp("/settings/profiles", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /profiles": json([hd, remux]),
    "GET /profiles/options": json({
      presets: [
        { id: "hd", name: "HD", description: "1080p from web or Blu-ray.", settings: {} },
        { id: "remux", name: "Remux", description: "Untouched Blu-ray.", settings: {} },
      ],
    }),
    ...extra,
  });
}

describe("quality profiles page", () => {
  it("lists profiles with what they are for and how much they are used", async () => {
    open();
    expect(await screen.findByText("2 media folders · 14 movies")).toBeInTheDocument();
    expect(screen.getByText("Not used yet")).toBeInTheDocument();
    expect(screen.getByText("Movies and TV")).toBeInTheDocument();
    const remuxCard = screen.getByText("Not used yet").closest("div.flex-wrap") as HTMLElement;
    expect(within(remuxCard).getByText("Movies")).toBeInTheDocument();
    // A profile in use can't be deleted, and the page says why.
    expect(screen.getByRole("button", { name: "Delete HD" })).toBeDisabled();
    expect(
      screen.getByText(
        /Used by 2 media folders and 14 movies\. To delete it, switch those media folders/,
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete Remux" })).toBeEnabled();
  });

  it("adds a profile and shows a field error from the server", async () => {
    const sent: unknown[] = [];
    open({
      "POST /profiles": async (req) => {
        const body = await req.json();
        sent.push(body);
        if (body.name === "hd") {
          return json(
            {
              code: "invalid",
              message: "Some fields need attention.",
              fields: { name: "There is already a profile called HD." },
            },
            422,
          );
        }
        return json({ ...remux, id: 3, name: body.name, slug: "4k" });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Add a profile" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Name"), "hd");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add profile" }));
    expect(
      await within(dialog).findByText("There is already a profile called HD."),
    ).toBeInTheDocument();

    await userEvent.clear(within(dialog).getByLabelText("Name"));
    await userEvent.type(within(dialog).getByLabelText("Name"), "4K");
    await userEvent.selectOptions(within(dialog).getByLabelText("Used for"), "both");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add profile" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(sent[1]).toEqual({ name: "4K", appliesTo: "both", preset: "hd" });
  });

  it("starts a new profile from the chosen preset", async () => {
    const sent: unknown[] = [];
    open({
      "POST /profiles": async (req) => {
        const body = await req.json();
        sent.push(body);
        return json({ ...remux, id: 3, name: body.name, slug: "x" });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Add a profile" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByLabelText(/HD/)).toBeChecked();
    await userEvent.click(within(dialog).getByLabelText(/Untouched Blu-ray/));
    await userEvent.type(within(dialog).getByLabelText("Name"), "Remux 2");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add profile" }));
    await waitFor(() =>
      expect(sent).toEqual([{ name: "Remux 2", appliesTo: "movie", preset: "remux" }]),
    );
  });

  it("links each profile to its settings", async () => {
    open();
    const links = await screen.findAllByRole("link", { name: /Edit settings/ });
    expect(links[0]).toHaveAttribute("href", "/settings/profiles/1");
  });

  it("renames a profile", async () => {
    const sent: unknown[] = [];
    open({
      "PUT /profiles/1": async (req) => {
        const body = await req.json();
        sent.push(body);
        return json({ ...hd, ...body });
      },
    });
    const card = (await screen.findByText("2 media folders · 14 movies")).closest("div.flex-wrap");
    await userEvent.click(within(card as HTMLElement).getByRole("button", { name: "Edit" }));
    const dialog = await screen.findByRole("dialog");
    const name = within(dialog).getByLabelText("Name");
    expect(name).toHaveValue("HD");
    await userEvent.clear(name);
    await userEvent.type(name, "High def");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(sent).toEqual([{ name: "High def", appliesTo: "both" }]));
  });

  it("shows the server's reason when a delete is refused", async () => {
    open({
      // The page thinks Remux is unused, but another tab just started using it.
      "DELETE /profiles/2": json(
        {
          code: "invalid",
          message: "Some fields need attention.",
          fields: { profile: "Remux is used by 1 media folder. Give them another profile first." },
        },
        422,
      ),
    });
    await userEvent.click(await screen.findByRole("button", { name: "Delete Remux" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete profile" }));
    expect(
      await within(dialog).findByText(
        "Remux is used by 1 media folder. Give them another profile first.",
      ),
    ).toBeInTheDocument();
  });

  it("deletes an unused profile after confirming", async () => {
    let deleted = false;
    open({
      "GET /profiles": () => json(deleted ? [hd] : [hd, remux]),
      "DELETE /profiles/2": () => {
        deleted = true;
        return new Response(null, { status: 204 });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Delete Remux" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Delete Remux?")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete profile" }));
    await waitFor(() => expect(screen.queryByText("Remux")).not.toBeInTheDocument());
    expect(deleted).toBe(true);
  });
});
