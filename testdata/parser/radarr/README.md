# Release names from Radarr's parser tests

These tables are adapted from Radarr's own parser tests
(`src/NzbDrone.Core.Test/ParserTests/*.cs` in
https://github.com/Radarr/Radarr, branch `develop`, commit
`0a097deb192a6b72b84c23dc18c2c0d68a0678fe`, 2026-10-04).
Radarr is licensed under GPL-3.0, as is Aerie (see `LICENSE`).
Copyright of the test cases remains with the Radarr contributors.

Each line is one JSON object: the release `name` plus what Radarr expects
the parser to find. `internal/parser/radarr_test.go` checks Aerie's parser
against them.

| File | Fields | From |
| --- | --- | --- |
| `quality.jsonl` | `quality` (Radarr's quality name, e.g. `Bluray-1080p`), `proper`, `revision` | QualityParserFixture.cs |
| `title.jsonl` | `title`, `year`, `group`, `edition`, `tmdbId`, `imdbId` | ParserFixture.cs |
| `group.jsonl` | `group` (`""` = no group) | ReleaseGroupParserFixture.cs |
| `edition.jsonl` | `edition` (`""` = none) | EditionParserFixture.cs |
| `language.jsonl` | `languages` the name must contain (`[]` = none named) | LanguageParserFixture.cs, ParserFixture.cs |

Subtitle-file languages, hashed anime releases and "crap" names were left
out: Aerie doesn't need them.

To refresh, download those `.cs` files into a folder and run (Python 3):

    for f in *.cs; do python3 -I convert/cases.py "$f" > "${f%.cs}.json"; done
    python3 -I convert/tojsonl.py <that folder> testdata/parser/radarr

then update the commit above.
