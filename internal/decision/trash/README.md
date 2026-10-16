# Custom formats from TRaSH Guides

These custom formats are copied unchanged from TRaSH Guides
(https://github.com/TRaSH-Guides/Guides, `docs/json/radarr/cf/`, commit
d6a23d6137f8549a66ade74846faf8bdf94abf2f 2026-10-07), under the MIT license in `LICENSE` (Copyright (c) 2021 TRaSH).

The simple profile editor builds on them (see `../simple.go`): each
feature, such as "Dolby Vision" or "Low-quality groups", is one or more of
these formats, and choosing prefer, avoid or block gives them a score.

To update, copy the same files again from that folder and run the tests.
