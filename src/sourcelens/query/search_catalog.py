"""`sourcelens query`: filter a catalogue and print or save the rows.

Filters combine with AND; comma-separated values within one filter combine
with OR (see catalog.py). The Go implementation offers the same flags and
prints the same lines.
"""

from __future__ import annotations

import sys

from sourcelens.common.agelit import write_csv
from sourcelens.common.flags import parse_flags
from sourcelens.common.settings import ProjectError, resolve_project
from sourcelens.query import catalog

SHOW = ["date", "resource_type", "tier", "category", "cited_by", "title", "doi", "url"]
SPEC = {**catalog.FILTER_SPEC, "limit": (50, int), "out": ("", str)}

HELP = """sourcelens query [filters] [--limit N] [--out FILE]: filter a catalogue

  sourcelens query --range 1m                                  what was published last month
  sourcelens query --topic "CRISPR base editing" --type review
  sourcelens query --text GrimAge --modality proteomic --from 2022 --tier core
  sourcelens query --fulltext DunedinPACE --type article,preprint
  sourcelens query --doi 10.18632/aging.101414,10.7554/elife.73420
  sourcelens query --title "epigenetic clock" --range 6m --out recent.csv
  sourcelens query --in reports/dryrun_<stamp>.csv --summary "single.cell" --ext xlsx --out picked.csv

""" + catalog.PROJECT_HELP + "\n" + catalog.FILTER_HELP + """  --limit N           rows printed (default 50; --out gets all)
  --out FILE          CSV of all matching rows; relative paths go to <catalogue>/exports/
"""


def run(argv: list[str]) -> int:
    try:
        o = parse_flags(argv, SPEC, HELP)
        resolve_project(o.topic, o.dir, False)
        rows, cols = catalog.input_rows(o)
        out, hits = catalog.select(o, rows)
    except (ValueError, ProjectError) as exc:
        print(f"sourcelens: {exc}", file=sys.stderr)
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
