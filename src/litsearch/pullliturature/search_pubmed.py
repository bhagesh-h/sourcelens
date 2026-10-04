#!/usr/bin/env python3
"""Cross-check the Europe PMC harvest against PubMed.

Runs every group of the search section of the configuration through NCBI E-utilities with [tiab]
phrase matching and records the PMIDs as hits (source = pubmed). Any PMID the
record store does not already hold is fetched with efetch. The two indexes
tokenise phrases differently, so each finds records the other misses; the
union goes forward.

Outputs
  <catalogue>/corpus/records.jsonl.gz     (merged)
  <catalogue>/corpus/search_hits.csv      (source = pubmed)
  <catalogue>/raw/pubmed/<run date>/counts.json

Usage
  litsearch __step pubmed [--groups g1,g2] [--scope focused]
"""

from __future__ import annotations

import argparse
import datetime as dt

from litsearch.common import hits as hitlog  # noqa: E402
from litsearch.common import pubmed  # noqa: E402
from litsearch.common.agelit import RAW, config, log, search_window, today, write_json  # noqa: E402
from litsearch.common.store import Store  # noqa: E402
from litsearch.common.terms import pubmed_term  # noqa: E402

ESEARCH_CAP = 9000


def block(terms: list[str]) -> str:
    return "(" + " OR ".join(pubmed_term(t) for t in terms) + ")"


def base_query(group: dict) -> str:
    q = block(group["terms"])
    if group.get("require_any"):
        q += " AND " + block(group["require_any"])
    return q


def dated(q: str, a: str, b: str) -> str:
    return f'{q} AND ("{a.replace("-", "/")}"[dp] : "{b.replace("-", "/")}"[dp])'


def split(a: str, b: str) -> list[tuple[str, str]]:
    """Halve a date window."""
    d0, d1 = dt.date.fromisoformat(a), dt.date.fromisoformat(b)
    mid = d0 + (d1 - d0) / 2
    return [(a, mid.isoformat()), ((mid + dt.timedelta(days=1)).isoformat(), b)]


def collect(h, q: str, a: str, b: str) -> set[str]:
    """PMIDs in [a, b], splitting the window until each piece fits esearch's cap."""
    n, _ = pubmed.esearch(h, dated(q, a, b))
    if n == 0:
        return set()
    if n <= ESEARCH_CAP or a == b:
        _, ids = pubmed.esearch(h, dated(q, a, b), retmax=ESEARCH_CAP + 999)
        return set(ids)
    out: set[str] = set()
    for c, d in split(a, b):
        out |= collect(h, q, c, d)
    return out


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--groups")
    ap.add_argument("--scope", choices=["focused", "broad", "all"], default="all")
    args = ap.parse_args()

    cfg = config("search")
    start, end = search_window()
    groups = cfg["groups"]
    if args.groups:
        groups = {k: v for k, v in groups.items() if k in args.groups.split(",")}
    if args.scope != "all":
        groups = {k: v for k, v in groups.items() if v["scope"] == args.scope}

    h = pubmed.http()
    store = Store()
    hits = hitlog.load()
    run = today()
    counts = {}

    for name, group in groups.items():
        q = base_query(group)
        total, _ = pubmed.esearch(h, dated(q, start, end))
        pmids = collect(h, q, start, end)
        missing = sorted(p for p in pmids if store.find({"pmid": p}) is None)
        log(f"{name}: PubMed count {total}, collected {len(pmids)}, {len(missing)} not in store")
        for rec in pubmed.efetch(h, missing):
            store.merge(rec, "pubmed")
        n_new = 0
        for p in pmids:
            uid = store.find({"pmid": p}) or f"pmid:{p}"
            n_new += hitlog.record(hits, uid, name, group["scope"], "pubmed", run)
        counts[name] = {"count": total, "collected": len(pmids), "fetched": len(missing),
                        "new_hits": n_new, "query": dated(q, start, end)}
        store.save()
        hitlog.save(hits)

    write_json(RAW / "pubmed" / run / "counts.json", {"window": [start, end], "groups": counts})
    log(f"done: store {len(store)} records")


if __name__ == "__main__":
    main()
