#!/usr/bin/env python3
"""Query the catalogue (Python implementation of `litSearch query`).

Examples
  # GrimAge papers on proteomics since 2022, core tier
  litSearch query --text GrimAge --modality proteomic --from 2022 --tier core

  # which catalogued papers mention DunedinPACE anywhere in their full text
  litSearch query --fulltext "DunedinPACE" --type article,preprint

  # repositories for brain age, most cited first, to a CSV
  litSearch query --type repository --text "brain age" --out brain_age_repos.csv

  # rows added by the latest run; papers by DOI; titles in the last 6 months
  litSearch query --added-since 2026-10-01
  litSearch query --doi 10.18632/aging.101414,10.7554/elife.73420
  litSearch query --title "epigenetic clock" --range 6m

Filters combine with AND; comma-separated values within one filter combine
with OR (see python/query/catalog.py). The Go implementation offers the same
flags and prints the same lines.
"""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "common"))
import catalog  # noqa: E402
from agelit import read_csv, write_csv  # noqa: E402
from flags import parse_flags  # noqa: E402

SHOW = ["date", "resource_type", "tier", "category", "cited_by", "title", "doi", "url"]
SPEC = {**catalog.FILTER_SPEC, "limit": (50, int), "out": ("", str)}

HELP = """litSearch query [filters] [--limit N] [--out FILE]: filter the catalogue

  litSearch query --text GrimAge --modality proteomic --from 2022 --tier core
  litSearch query --fulltext DunedinPACE --type article,preprint
  litSearch query --doi 10.18632/aging.101414,10.7554/elife.73420
  litSearch query --title "epigenetic clock" --range 6m --out recent.csv

""" + catalog.FILTER_HELP + """  --limit N           rows printed (default 50; --out gets all)
  --out FILE          CSV of all matching rows; relative paths go to <output folder>/exports/
"""


def run(argv: list[str]) -> int:
    try:
        o = parse_flags(argv, SPEC, HELP)
        rows = read_csv(catalog.PROGRESS)
        cols = list(rows[0].keys()) if rows else []
        out, hits = catalog.select(o, rows)
    except ValueError as exc:
        print(f"litSearch: {exc}", file=sys.stderr)
        return 2
    print(f"{len(out)} matching rows")
    for r in out[:o.limit]:
        print(" | ".join((r.get(k) or "")[:110 if k == "title" else 40] for k in SHOW))
        if hits.get(r["uid"]):
            print(f"      … {hits[r['uid']]} …")
    if o.out:
        path = catalog.out_path(o.out)
        write_csv(path, out, cols)
        print(f"wrote {path}")
    return 0


if __name__ == "__main__":
    sys.exit(run(sys.argv[1:]))
