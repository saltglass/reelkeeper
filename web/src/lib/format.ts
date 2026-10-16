import type { LabelPart } from "@/api/client";

/** Bytes as "512 B", "1.5 KB", "48 MB", "1.2 TB". */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

/** Minutes as "2h 13m" or "47m". */
export function formatRuntime(minutes: number): string {
  if (minutes <= 0) return "";
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h > 0 ? `${h}h ${String(m).padStart(2, "0")}m` : `${m}m`;
}

/** Channel count as "7.1", "5.1", "2.0". */
export function channelLayout(n?: number): string {
  if (!n) return "";
  return (
    ({ 1: "1.0", 2: "2.0", 3: "2.1", 6: "5.1", 7: "6.1", 8: "7.1" } as Record<number, string>)[n] ??
    `${n}ch`
  );
}

/** The facts a version's label can show (see versionLabel). */
export type LabelFacts = {
  profileName: string;
  quality: string;
  resolution: number;
  hdr: string[];
  videoCodec: string;
  audioCodec: string;
  audioChannels: number;
  releaseGroup: string;
  edition: string;
  size: number;
};

/**
 * The pieces of a version's label, in display order, for the parts chosen in
 * Settings → General → Labels. Unknown facts are left out rather than shown
 * as blanks, so the result can be empty.
 */
export function versionLabel(v: LabelFacts, parts: readonly LabelPart[]): string[] {
  const piece: Record<LabelPart, () => (string | undefined)[]> = {
    profile: () => [v.profileName],
    quality: () => [v.quality || (v.resolution ? `${v.resolution}p` : "")],
    hdr: () => v.hdr,
    videoCodec: () => [v.videoCodec],
    audio: () => [`${v.audioCodec} ${channelLayout(v.audioChannels)}`.trim()],
    releaseGroup: () => [v.releaseGroup],
    edition: () => [v.edition],
    size: () => [v.size > 0 ? formatBytes(v.size) : ""],
  };
  return labelParts
    .filter((p) => parts.includes(p))
    .flatMap((p) => piece[p]())
    .filter((s): s is string => Boolean(s));
}

/** Every label part, in the order labels show them. */
export const labelParts: readonly LabelPart[] = [
  "profile",
  "quality",
  "hdr",
  "videoCodec",
  "audio",
  "releaseGroup",
  "edition",
  "size",
];

/** What labels show until the setting loads (and by default). */
export const defaultLabelParts: readonly LabelPart[] = ["profile", "quality", "hdr"];

/** A short quality label for a version: "2160p · DV · HDR10" or "Bluray-1080p". */
export function qualityLabel(v: { quality: string; resolution: number; hdr: string[] }): string {
  const base = v.quality || (v.resolution ? `${v.resolution}p` : "");
  return [base, ...v.hdr].filter(Boolean).join(" · ");
}

/** Language codes as names: "eng" → "English". Unknown codes are shown as is. */
const languages: Record<string, string> = {
  eng: "English",
  fre: "French",
  fra: "French",
  ger: "German",
  deu: "German",
  spa: "Spanish",
  ita: "Italian",
  jpn: "Japanese",
  kor: "Korean",
  chi: "Chinese",
  zho: "Chinese",
  rus: "Russian",
  por: "Portuguese",
  dut: "Dutch",
  nld: "Dutch",
  swe: "Swedish",
  nor: "Norwegian",
  dan: "Danish",
  fin: "Finnish",
  pol: "Polish",
  cze: "Czech",
  ces: "Czech",
  hun: "Hungarian",
  tur: "Turkish",
  gre: "Greek",
  ell: "Greek",
  heb: "Hebrew",
  ara: "Arabic",
  hin: "Hindi",
  tha: "Thai",
  ukr: "Ukrainian",
  rum: "Romanian",
  ron: "Romanian",
  vie: "Vietnamese",
  ind: "Indonesian",
  may: "Malay",
  ice: "Icelandic",
};
export function languageName(code?: string): string {
  if (!code) return "";
  return languages[code] ?? code;
}
