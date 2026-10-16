"""Turn the extracted Radarr TestCases into Aerie's JSONL test tables."""
import json, os, re, sys
src, dst = sys.argv[1], sys.argv[2]
os.makedirs(dst, exist_ok=True)
def load(n): return json.load(open(os.path.join(src, n + ".json")))

def write(name, rows):
    with open(os.path.join(dst, name + ".jsonl"), "w", encoding="utf-8") as f:
        for r in rows: f.write(json.dumps(r, ensure_ascii=False) + "\n")
    print(name, len(rows))

# Quality: method -> Radarr quality name.
q = load("QualityParserFixture")
qmap = {
 "should_parse_ts": "TELESYNC", "should_parse_cam": "CAM", "should_parse_sdtv_quality": "SDTV",
 "should_parse_dvd_quality": "DVD", "should_parse_dvdr_quality": "DVD-R",
 "should_parse_webdl480p_quality": "WEBDL-480p", "should_parse_webrip480p_quality": "WEBRip-480p",
 "should_parse_bluray480p_quality": "Bluray-480p", "should_parse_hdtv720p_quality": "HDTV-720p",
 "should_parse_hdtv1080p_quality": "HDTV-1080p", "should_parse_hdtv2160p_quality": "HDTV-2160p",
 "should_parse_webdl720p_quality": "WEBDL-720p", "should_parse_webrip720p_quality": "WEBRip-720p",
 "should_parse_webdl1080p_quality": "WEBDL-1080p", "should_parse_webrip1080p_quality": "WEBRip-1080p",
 "should_parse_webdl2160p_quality": "WEBDL-2160p", "should_parse_webrip2160p_quality": "WEBRip-2160p",
 "should_parse_bluray720p_quality": "Bluray-720p", "should_parse_bluray1080p_quality": "Bluray-1080p",
 "should_parse_bluray2160p_quality": "Bluray-2160p", "should_parse_bluray576p_quality": "Bluray-576p",
 "should_parse_remux720p_as_bluray720p_quality": "Bluray-720p",
 "should_parse_remux1080p_quality": "Remux-1080p", "should_parse_remux2160p_quality": "Remux-2160p",
 "should_parse_brdisk_1080p_quality": "BR-DISK", "should_parse_raw_quality": "Raw-HD",
 "quality_parse": "Unknown",
}
rows = []
for m, quality in qmap.items():
    for a in q[m]:
        r = {"name": a[0], "quality": quality}
        if len(a) > 1 and isinstance(a[1], bool): r["proper"] = a[1]
        rows.append(r)
for a in q["should_be_able_to_parse_repack"]:
    rows.append({"name": a[0], "revision": a[2]})
write("quality", rows)

p = load("ParserFixture")
rows = []
for a in p["should_parse_movie_title"] + p["should_parse_movie_folder_name"]:
    rows.append({"name": a[0], "title": a[1]})
for a in p["should_parse_anime_movie_title"]:
    rows.append({"name": a[0], "title": a[1], "group": a[2], "year": a[3]})
for a in p["should_parse_anime_movie_title_without_year"]:
    rows.append({"name": a[0], "title": a[1], "group": a[2]})
for a in p["should_parse_german_movie"]:
    r = {"name": a[0], "title": a[1], "year": a[3]}
    if a[2]: r["edition"] = a[2]
    rows.append(r)
for a in p["should_parse_movie_alternative_titles"]:
    rows.append({"name": a[0], "title": a[1][0] if isinstance(a[1], list) else a[1]})
for a in p["should_parse_movie_year"]:
    rows.append({"name": a[0], "year": a[1]})
for a in p["should_parse_tmdb_id"]:
    rows.append({"name": a[0], "tmdbId": a[1]})
for a in p["should_parse_imdb_in_title"]:
    rows.append({"name": a[0], "imdbId": a[1]})
write("title", rows)

g = load("ReleaseGroupParserFixture")
rows = []
for m, cases in g.items():
    for a in cases:
        rows.append({"name": a[0], "group": a[1] if len(a) > 1 and isinstance(a[1], str) else ""})
write("group", rows)

e = load("EditionParserFixture")
rows = [{"name": a[0], "edition": a[1]} for a in e["should_parse_edition"]]
rows += [{"name": a[0], "edition": ""} for a in e["should_not_parse_edition"]]
write("edition", rows)

lang = load("LanguageParserFixture")
rows = []
for m, cases in lang.items():
    mm = re.fullmatch(r"should_parse_language_(\w+)", m)
    if not mm: continue
    want = {"french_english": ["French", "English"], "ukrainian_multi": ["Ukrainian"],
            "polish_subbed": [], "unknown": [], "spanish_latino": ["Spanish (Latino)"],
            "brazilian_portuguese": ["Portuguese (Brazil)"]}.get(mm.group(1), [mm.group(1).capitalize()])
    for a in cases:
        rows.append({"name": a[0], "languages": want})
for a in p["should_not_parse_wrong_language_in_title"]:
    rows.append({"name": a[0], "languages": []})
write("language", rows)
