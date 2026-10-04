"""<catalogue>/corpus/search_hits.csv: which search group found which record.

Several searchers (PubMed, Europe PMC, arXiv) can run at the same time, and
each writes the whole file. save() therefore takes an exclusive lock,
re-reads the file and merges: rows from other processes are kept, and for a
row both know, first_seen is the earliest and last_seen the latest.
"""

from __future__ import annotations

import fcntl

from sourcelens.common.agelit import CORPUS, read_csv, write_csv

HITS = CORPUS / "search_hits.csv"
COLUMNS = ["uid", "group", "scope", "source", "first_seen", "last_seen"]


def key(row: dict) -> tuple:
    return (row["uid"], row["group"], row["source"])


def load() -> dict[tuple, dict]:
    return {key(r): r for r in read_csv(HITS)}


def record(hits: dict, uid: str, group: str, scope: str, source: str, run: str) -> bool:
    """Add or refresh one hit in memory; True if it is new."""
    k = (uid, group, source)
    if k in hits:
        hits[k]["last_seen"] = run
        return False
    hits[k] = {"uid": uid, "group": group, "scope": scope, "source": source,
               "first_seen": run, "last_seen": run}
    return True


def save(hits: dict) -> None:
    HITS.parent.mkdir(parents=True, exist_ok=True)
    with open(HITS.with_name(".search_hits.lock"), "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        merged = load()
        for k, row in hits.items():
            old = merged.get(k)
            if old is None:
                merged[k] = row
            else:
                old["first_seen"] = min(old["first_seen"], row["first_seen"])
                old["last_seen"] = max(old["last_seen"], row["last_seen"])
        write_csv(HITS, sorted(merged.values(), key=lambda r: key(r)), COLUMNS)
