#!/usr/bin/env python3
"""Resolve every seed DOI from the local projects into the record store.

Seeds come from python/localseeds/extract_seeds.py (FALCONAge clock registry
and bibliography, 300BCG manifests, notes in all three projects). A seed the
searches already found is only tagged; any other is resolved, in order, via
PubMed (by DOI), Europe PMC (by DOI, catches preprints), Crossref, and
DataCite (Zenodo, figshare and other data/software DOIs).

Outputs
  <output>/corpus/records.jsonl.gz       (merged, source = seed:<how>)
  <output>/seeds/seed_resolution.csv     doi, how it was resolved, uid, title

Usage
  python/run.sh python python/pullliturature/resolve_seeds.py
"""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "common"))
import crossref  # noqa: E402
import pubmed  # noqa: E402
from agelit import SEEDS, Http, log, read_csv, write_csv  # noqa: E402
from store import Store  # noqa: E402

sys.path.insert(0, str(Path(__file__).resolve().parent))
from search_europepmc import EuropePMCUnavailable, cursor_all, normalise  # noqa: E402

COLUMNS = ["doi", "resolved_via", "uid", "pub_date", "title", "roles"]


def main() -> None:
    seeds = read_csv(SEEDS / "local_seeds.csv")
    roles: dict[str, set] = {}
    for s in seeds:
        roles.setdefault(s["doi"], set()).add(s["role"])
    store = Store()
    old = {r["doi"]: r for r in read_csv(SEEDS / "seed_resolution.csv")}
    out = {}
    todo = []
    for doi in sorted(roles):
        uid = store.find({"doi": doi})
        if uid:
            out[doi] = {"doi": doi, "resolved_via": old.get(doi, {}).get("resolved_via") or "search", "uid": uid}
        else:
            todo.append(doi)
    log(f"{len(roles)} seed DOIs; {len(roles) - len(todo)} already in store; resolving {len(todo)}")

    # 1. PubMed by DOI
    ph = pubmed.http()
    pmids = []
    for i in range(0, len(todo), 40):
        chunk = todo[i:i + 40]
        term = " OR ".join(f'"{d}"[doi]' for d in chunk)
        _, ids = pubmed.esearch(ph, term, retmax=200)
        pmids += ids
    for rec in pubmed.efetch(ph, sorted(set(pmids))):
        store.merge(rec, "seed:pubmed")
    todo2 = []
    for d in todo:
        uid = store.find({"doi": d})
        if uid:
            out[d] = {"doi": d, "resolved_via": "pubmed", "uid": uid}
        else:
            todo2.append(d)
    log(f"PubMed resolved {len(todo) - len(todo2)}; {len(todo2)} left")

    # 2. Europe PMC by DOI (preprints, non-MEDLINE journals)
    eh = Http(min_interval=0.25)
    for i in range(0, len(todo2), 25):
        chunk = todo2[i:i + 25]
        q = " OR ".join(f'DOI:"{d}"' for d in chunk)
        try:
            for js in cursor_all(eh, q, "core", 25, 25):
                for raw in (js.get("resultList") or {}).get("result") or []:
                    try:
                        store.merge(normalise(raw), "seed:europepmc")
                    except ValueError:
                        pass
        except EuropePMCUnavailable as exc:
            log(f"Europe PMC unavailable ({exc}); falling through to Crossref")
            break
    todo3 = []
    for d in todo2:
        uid = store.find({"doi": d})
        if uid:
            out[d] = {"doi": d, "resolved_via": "europepmc", "uid": uid}
        else:
            todo3.append(d)
    log(f"Europe PMC resolved {len(todo2) - len(todo3)}; {len(todo3)} left")

    # 3. Crossref, then 4. DataCite
    ch = crossref.http()
    for d in todo3:
        rec, via = crossref.crossref(ch, d), "crossref"
        if rec is None:
            rec, via = crossref.datacite(ch, d), "datacite"
        if rec is None:
            out[d] = {"doi": d, "resolved_via": "unresolved", "uid": ""}
            continue
        uid = store.merge(rec, f"seed:{via}")
        out[d] = {"doi": d, "resolved_via": via, "uid": uid}
    store.save()

    rows = []
    for d, row in out.items():
        rec = store.get(row["uid"]) if row["uid"] else None
        row["pub_date"] = (rec or {}).get("pub_date", "")
        row["title"] = (rec or {}).get("title", "")
        row["roles"] = "; ".join(sorted(roles[d]))
        rows.append(row)
    write_csv(SEEDS / "seed_resolution.csv", sorted(rows, key=lambda r: r["doi"]), COLUMNS)
    via = {}
    for r in rows:
        via[r["resolved_via"]] = via.get(r["resolved_via"], 0) + 1
    log(f"seed resolution: {via}")


if __name__ == "__main__":
    main()
