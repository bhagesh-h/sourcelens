#!/usr/bin/env python3
"""Export catalogue entries as references (Python implementation of `litSearch export`).

Select rows with the same filters as `litSearch query` (date window, title,
DOIs, tier, type, ...) and write them in one or more reference formats,
chosen with three-letter codes:

  APA AMA MLA CHI HAR VAN IEE NAT   text styles, one reference per line
  BIB RIS ENW CSL                   BibTeX, RIS, EndNote tagged, CSL-JSON

Examples
  litSearch export --from 2024 --type article,review --format APA
  litSearch export --doi 10.18632/aging.101414,10.7554/elife.73420 --format APA,BIB,RIS
  litSearch export --title "GrimAge|DunedinPACE" --range 2y --format VAN --out refs/grim
  litSearch export --doi @dois.txt --format CSL --out refs.json

--out: a file name (one format), or a prefix that gets one file per format
(refs/grim.apa.txt, refs/grim.bib, ...). Without --out the references are
printed. Output order follows --sort (default: date).
"""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "common"))
import catalog  # noqa: E402
import refs  # noqa: E402
from agelit import read_csv  # noqa: E402
from flags import parse_flags  # noqa: E402

SPEC = {**catalog.FILTER_SPEC, "format": ("APA", str), "limit": (0, int), "out": ("", str)}

HELP = """litSearch export [filters] --format CODES [--out PATH]: write references

  litSearch export --from 2024 --type article,review --format APA
  litSearch export --doi 10.18632/aging.101414,10.7554/elife.73420 --format APA,BIB,RIS
  litSearch export --title "GrimAge|DunedinPACE" --range 2y --format VAN --out grim
  litSearch export --doi @dois.txt --format CSL --out refs.json

formats (three-letter codes, comma list)
  APA AMA MLA CHI HAR VAN IEE NAT   text styles, one reference per line
  BIB RIS ENW CSL                   BibTeX, RIS, EndNote tagged, CSL-JSON

""" + catalog.FILTER_HELP + """  --format CODES      default APA
  --limit N           at most N references (default all)
  --out PATH          a file (one format) or a prefix (one file per format);
                      relative paths go to <output folder>/exports/
"""


def run(argv: list[str]) -> int:
    try:
        o = parse_flags(argv, SPEC, HELP)
        codes = refs.parse_codes(o.format)
        rows, _ = catalog.select(o)
    except ValueError as exc:
        print(f"litSearch: {exc}", file=sys.stderr)
        return 2
    if o.limit:
        rows = rows[:o.limit]
    full = {r["uid"]: r for r in read_csv(catalog.REFERENCES)}
    items = [full.get(r["uid"]) or {k: r.get(k) or "" for k in refs.REF_COLUMNS} for r in rows]
    print(f"{len(items)} references", file=sys.stderr)
    if not o.out:
        for c in codes:
            if len(codes) > 1:
                print(f"==> {c} <==")
            sys.stdout.write(refs.render(c, items))
        return 0
    out = catalog.out_path(o.out)
    for c in codes:
        path = out if len(codes) == 1 and out.suffix else out.with_name(out.name + refs.EXT[c])
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(refs.render(c, items), encoding="utf-8")
        print(f"wrote {path}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(run(sys.argv[1:]))
