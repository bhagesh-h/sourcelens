#!/usr/bin/env python3
"""Link bioRxiv / medRxiv preprints to their published versions.

A preprint and the journal article it became are separate rows in
progress.csv (different DOIs, dates and often titles). The bioRxiv API
records the journal DOI of every preprint that has been published; this step
asks it for each catalogued 10.1101 DOI so build_progress.py can cross-link
the pair in the `related` column.

Incremental: a preprint already linked is not asked again; one still
unpublished is re-checked after --recheck-days days.

Output
  <output>/corpus/preprint_links.csv  preprint_doi, server, published_doi, published_date, checked_on

Usage
  python/run.sh python python/pullliturature/enrich_preprints.py
"""

from __future__ import annotations

import argparse
import datetime as dt
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "common"))
from agelit import CORPUS, RESEARCH, Http, log, norm_doi, read_csv, today, write_csv  # noqa: E402

OUT = CORPUS / "preprint_links.csv"
COLUMNS = ["preprint_doi", "server", "published_doi", "published_date", "checked_on"]


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--recheck-days", type=int, default=30)
    args = ap.parse_args()
    dois = sorted({r["doi"] for r in read_csv(RESEARCH / "progress.csv") if r.get("doi", "").startswith("10.1101/")})
    old = {r["preprint_doi"]: r for r in read_csv(OUT)}
    cutoff = (dt.date.today() - dt.timedelta(days=args.recheck_days)).isoformat()
    todo = [d for d in dois if d not in old or (not old[d]["published_doi"] and old[d]["checked_on"] < cutoff)]
    log(f"{len(dois)} bioRxiv/medRxiv DOIs; {len(todo)} to check")
    h = Http(min_interval=0.4)
    for i, d in enumerate(todo, 1):
        row = {"preprint_doi": d, "server": "", "published_doi": "", "published_date": "", "checked_on": today()}
        for server in ("biorxiv", "medrxiv"):
            r = h.get(f"https://api.biorxiv.org/details/{server}/{d}", tries=3)
            coll = (r.json() or {}).get("collection") if r is not None and r.status_code == 200 else None
            if coll:
                row["server"] = server
                pub = next((c.get("published") for c in reversed(coll) if c.get("published") not in (None, "", "NA")), "")
                row["published_doi"] = norm_doi(pub)
                break
        if row["published_doi"]:
            r = h.get(f"https://api.biorxiv.org/pubs/{row['server']}/{d}", tries=2)
            coll = (r.json() or {}).get("collection") if r is not None and r.status_code == 200 else None
            if coll:
                row["published_date"] = coll[0].get("published_date", "")
        old[d] = row
        if i % 200 == 0:
            write_csv(OUT, sorted(old.values(), key=lambda x: x["preprint_doi"]), COLUMNS)
            log(f"  {i}/{len(todo)}")
    write_csv(OUT, sorted(old.values(), key=lambda x: x["preprint_doi"]), COLUMNS)
    n = sum(1 for r in old.values() if r["published_doi"])
    log(f"{len(old)} preprints checked; {n} have a published version")


if __name__ == "__main__":
    main()
