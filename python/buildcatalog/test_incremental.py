#!/usr/bin/env python3
"""Check the append-only contract of build_progress.py on a throwaway catalogue.

Builds a three-record catalogue in a temporary workspace, hand-edits it the
way a user would (notes, user_tags), then changes the inputs (one new
record, one record no longer matched) and rebuilds. Asserts that:

  * the new record is appended with today's added_on;
  * notes, user_tags and the original added_on of existing rows survive;
  * a record the pipeline no longer produces is kept and flagged;
  * the file stays sorted by date;
  * the day's changelog lists the new record.

Usage
  python/run.sh python python/buildcatalog/test_incremental.py
"""

from __future__ import annotations

import csv
import datetime as dt
import gzip
import importlib
import json
import os
import shutil
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve()
REAL_ROOT = HERE.parents[2]


def rec(uid_doi: str, date: str, title: str) -> dict:
    return {"uid": f"doi:{uid_doi}", "doi": uid_doi, "pmid": "", "pmcid": "", "title": title,
            "authors": "Doe J, Roe R", "journal": "Test J", "pub_date": date, "pub_year": date[:4],
            "pub_types": "Journal Article", "abstract": "We developed an epigenetic clock of biological age.",
            "sources": ["test"]}


def write_store(root: Path, records: list[dict]) -> None:
    p = root / "research/corpus/records.jsonl.gz"
    p.parent.mkdir(parents=True, exist_ok=True)
    with gzip.open(p, "wt", encoding="utf-8") as fh:
        for r in records:
            fh.write(json.dumps(r) + "\n")


def write_hits(root: Path, uids: list[str]) -> None:
    p = root / "research/corpus/search_hits.csv"
    with open(p, "w", newline="", encoding="utf-8") as fh:
        w = csv.writer(fh)
        w.writerow(["uid", "group", "scope", "source", "first_seen", "last_seen"])
        for u in uids:
            w.writerow([u, "epigenetic_clocks", "focused", "test", "2026-01-01", "2026-01-01"])


def read(root: Path) -> list[dict]:
    with open(root / "research/progress.csv", newline="", encoding="utf-8") as fh:
        return list(csv.DictReader(fh))


def live_config() -> Path:
    """The configuration in use (LITSEARCH_CONFIG, else the output folder's), else the template."""
    for p in (os.environ.get("LITSEARCH_CONFIG"),
              os.environ.get("AGING_RESEARCH") and Path(os.environ["AGING_RESEARCH"]) / "config" / "litsearch.yaml"):
        if p and Path(p).is_file():
            return Path(p)
    return REAL_ROOT / "config" / "litsearch.template.yaml"


def build(root: Path):
    os.environ["AGING_ROOT"] = str(root)
    os.environ["AGING_RESEARCH"] = str(root / "research")  # never the real output folder
    os.environ["LITSEARCH_CONFIG"] = str(root / "research" / "config" / "litsearch.yaml")
    for m in ("agelit", "store", "build_progress"):
        sys.modules.pop(m, None)
    sys.path.insert(0, str(REAL_ROOT / "python/common"))
    sys.path.insert(0, str(HERE.parent))
    importlib.import_module("build_progress").main()


def main() -> None:
    root = Path(tempfile.mkdtemp(prefix="aging_test_"))
    try:
        (root / "research" / "config").mkdir(parents=True)
        shutil.copy(live_config(), root / "research" / "config" / "litsearch.yaml")
        a = rec("10.1/a", "2015-03-01", "An epigenetic clock A")
        b = rec("10.1/b", "2019-06-01", "An epigenetic clock B")
        c = rec("10.1/c", "2021-09-01", "An epigenetic clock C")
        write_store(root, [a, b, c])
        write_hits(root, [a["uid"], b["uid"], c["uid"]])
        build(root)
        rows = read(root)
        assert [r["uid"] for r in rows] == [a["uid"], b["uid"], c["uid"]], rows

        # the user edits the catalogue
        for r in rows:
            if r["uid"] == a["uid"]:
                r["notes"], r["user_tags"], r["added_on"] = "read this", "must-read", "2026-01-15"
        with open(root / "research/progress.csv", "w", newline="", encoding="utf-8") as fh:
            w = csv.DictWriter(fh, fieldnames=list(rows[0].keys()))
            w.writeheader()
            w.writerows(rows)

        # new research appears; C drops out of the searches
        d = rec("10.1/d", "2017-01-20", "An epigenetic clock D")
        write_store(root, [a, b, c, d])
        write_hits(root, [a["uid"], b["uid"], d["uid"]])
        build(root)
        rows = read(root)
        by = {r["uid"]: r for r in rows}
        today = dt.date.today().isoformat()

        assert len(rows) == 4, f"expected 4 rows, got {len(rows)}"
        assert by[d["uid"]]["added_on"] == today, "new record must be appended with today's date"
        assert by[a["uid"]]["notes"] == "read this" and by[a["uid"]]["user_tags"] == "must-read", "user columns lost"
        assert by[a["uid"]]["added_on"] == "2026-01-15", "added_on of an existing row changed"
        assert by[c["uid"]]["status"].startswith("no longer matched"), "unmatched row must be kept and flagged"
        assert [r["date"] for r in rows] == sorted(r["date"] for r in rows), "not chronological"
        log = root / f"research/changelog/added_{today}.csv"
        with open(log, newline="", encoding="utf-8") as fh:
            added = {r["uid"] for r in csv.DictReader(fh)}
        assert d["uid"] in added, "changelog misses the new record"
        print("PASS: append-only contract holds "
              "(new row appended, user edits and added_on kept, unmatched row kept, chronological, changelog)")
    finally:
        shutil.rmtree(root, ignore_errors=True)


if __name__ == "__main__":
    main()
