"""The dry run's table: every catalogue row with what it is about and what is pending.

Run by `sourcelens update --dry-run` after the searches, which add metadata
only. It lists the attachments of open-access PMC articles (one request per
article not listed before; nothing is downloaded) and writes
<catalogue>/reports/dryrun_<stamp>.csv: the progress.csv columns plus summary,
summary_from, keywords, fulltext_sources and pending (see overview.py).

Usage
  sourcelens __step dryrun --stamp YYYY_MM_DD_HH_MM_SS [--workers 8]

The Go twin is cmd/sourcelens/dryrun.go.
"""

from __future__ import annotations

import argparse
import collections
import concurrent.futures as cf
from pathlib import Path

from sourcelens.buildcatalog import overview
from sourcelens.buildcatalog.build_progress import COLUMNS
from sourcelens.common import agelit
from sourcelens.common.agelit import log, read_csv, research_rel, today, write_csv
from sourcelens.pullliturature import attachments
from sourcelens.pullliturature import fetch_fulltext as ft

COLUMNS_OUT = COLUMNS + overview.EXTRA


def list_one(uid: str, pmcid: str, folder: Path) -> list[dict] | None:
    """Attachment rows of one article from the bucket listing (None: the bucket could not be read)."""
    lst = ft.pmc_listing(pmcid)
    if lst is not None and lst.get("_error"):
        return None
    if lst is None:
        return [ft.none_row(uid, "not in the PMC open-access subset")]
    xml = folder / "paper.jats.xml"
    caps = attachments.captions(xml.read_bytes() if xml.exists() else None)
    rows = []
    for name, size in ft.pmc_media(pmcid, lst):
        fname = attachments.safe_name(name)
        kind, label, caption = attachments.describe(name, caps)
        target = folder / "attachments" / fname
        on_disk = target.exists()
        rows.append({"uid": uid, "file": fname, "ext": attachments.file_ext(fname), "kind": kind, "label": label,
                     "caption": caption, "bytes": size if size >= 0 else "",
                     "status": "ok" if on_disk else "listed", "path": research_rel(target) if on_disk else "",
                     "url": f"{ft.S3}/{pmcid}.{lst['version']}/{ft.quote(name)}", "checked_on": today(),
                     "reason": ""})
    return rows or [ft.none_row(uid, "no attachments")]


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--stamp", required=True)
    ap.add_argument("--workers", type=int, default=8)
    args = ap.parse_args()

    progress = read_csv(agelit.RESEARCH / "progress.csv")
    store_rows = {r["uid"]: r for r in progress}
    atts = attachments.read_index()
    todo = [r for r in progress if r.get("pmcid") and r["uid"] not in atts
            and r.get("resource_type") in overview.PAPER_TYPES]
    log(f"{len(progress)} catalogue rows; listing the attachments of {len(todo)} PMC articles")
    if todo:
        store = ft.Store()
        done = 0
        with cf.ThreadPoolExecutor(max_workers=max(1, args.workers)) as ex:
            futs = {}
            for r in todo:
                rec = store.get(r["uid"]) or {"uid": r["uid"], "pmcid": r["pmcid"]}
                futs[ex.submit(list_one, r["uid"], r["pmcid"], ft.folder_for(rec, store_rows[r["uid"]]))] = r["uid"]
            for fut in cf.as_completed(futs):
                rows = fut.result()
                if rows:
                    atts[futs[fut]] = rows
                done += 1
                if done % 200 == 0:
                    attachments.write_index(atts)
                    log(f"  {done}/{len(futs)} listed")
        attachments.write_index(atts)

    out = overview.rows(progress)
    path = agelit.RESEARCH / "reports" / f"dryrun_{args.stamp}.csv"
    write_csv(path, out, COLUMNS_OUT)
    files = [a for rows in atts.values() for a in rows if a.get("file")]
    by_ext = collections.Counter(a["ext"] or "?" for a in files)
    log(f"attachments known: {len(files)} files in {sum(1 for v in atts.values() if any(a.get('file') for a in v))} "
        f"papers; " + ", ".join(f"{e} {n}" for e, n in by_ext.most_common(8)))
    log(f"wrote {research_rel(path)}: {len(out)} rows")


if __name__ == "__main__":
    main()
