import { describe, expect, it } from "vitest";
import { type LabelFacts, versionLabel } from "./format";

const remux: LabelFacts = {
  profileName: "Remux",
  quality: "Remux-2160p",
  resolution: 2160,
  hdr: ["DV", "HDR10"],
  videoCodec: "H.265",
  audioCodec: "TrueHD",
  audioChannels: 8,
  releaseGroup: "FraMeSToR",
  edition: "Director's Cut",
  size: 62 * 1024 ** 3,
};

describe("versionLabel", () => {
  it("shows the default parts like the labels always did", () => {
    expect(versionLabel(remux, ["profile", "quality", "hdr"])).toEqual([
      "Remux",
      "Remux-2160p",
      "DV",
      "HDR10",
    ]);
  });

  it("always uses display order, whatever order parts are given in", () => {
    expect(versionLabel(remux, ["size", "audio", "edition", "videoCodec", "releaseGroup"])).toEqual(
      ["H.265", "TrueHD 7.1", "FraMeSToR", "Director's Cut", "62 GB"],
    );
  });

  it("leaves out facts that aren't known", () => {
    const bare: LabelFacts = {
      ...remux,
      quality: "",
      hdr: [],
      videoCodec: "",
      audioCodec: "",
      audioChannels: 0,
      releaseGroup: "",
      edition: "",
      size: 0,
    };
    expect(versionLabel(bare, ["quality", "hdr", "videoCodec", "audio", "size"])).toEqual([
      "2160p",
    ]);
    expect(versionLabel({ ...bare, resolution: 0 }, ["quality", "audio"])).toEqual([]);
  });

  it("is empty when nothing is chosen", () => {
    expect(versionLabel(remux, [])).toEqual([]);
  });
});
