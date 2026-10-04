#!/usr/bin/env python3
"""Search arXiv for every term of the focused groups in the search section of the configuration.

Europe PMC indexes only part of arXiv, and brain-age, retinal-age, ECG-age and
other imaging or machine-learning clocks are often posted to cs.CV, eess.IV
or cs.LG only. Terms are sent eight at a time as phrase searches over title
and abstract (the API allows one request every 3 seconds) and every hit is
re-checked for an exact phrase; a group with require_any terms keeps a hit
only if its title or abstract also contains one of them. Re-runs look back
only to the last complete run (<output>/raw/arxiv/state.json) minus 14 days.

Outputs
  <output>/corpus/records.jsonl.gz   (merged; source_db ARXIV, DOI 10.48550/arXiv.<id>)
  <output>/corpus/search_hits.csv    (source = arxiv)

Usage
  python/run.sh python python/pullliturature/search_arxiv.py [--groups g1,g2]
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import re
import sys
from pathlib import Path

from lxml import etree

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "common"))
import hits as hitlog  # noqa: E402
from agelit import (config, RAW, Http, clean_text, load_yaml, log, make_uid,  # noqa: E402
                    search_window, search_window_overridden, today)
from store import Store  # noqa: E402

API = "https://export.arxiv.org/api/query"
NS = {"a": "http://www.w3.org/2005/Atom", "arxiv": "http://arxiv.org/schemas/atom"}


def search(h: Http, terms: list[str], start: str, end: str) -> list[dict]:
    """Newest-first search for any of `terms` in title or abstract, back to `start`."""
    q = " OR ".join(f'ti:"{t}" OR abs:"{t}"' for t in terms)
    out, offset = [], 0
    while True:
        r = h.get(API, params={"search_query": q, "start": offset, "max_results": 200,
                               "sortBy": "submittedDate", "sortOrder": "descending"}, timeout=120)
        if r is None or r.status_code != 200:
            break
        root = etree.fromstring(r.content)
        entries = root.findall("a:entry", NS)
        stop = False
        for e in entries:
            published = (e.findtext("a:published", namespaces=NS) or "")[:10]
            if published < start:
                stop = True
                continue
            if published > end:
                continue
            aid = re.sub(r"v\d+$", "", (e.findtext("a:id", namespaces=NS) or "").rsplit("/abs/", 1)[-1])
            journal_doi = e.findtext("arxiv:doi", namespaces=NS) or ""
            cats = [c.get("term") for c in e.findall("a:category", NS)]
            rec = {
                "epmc_id": aid, "source_db": "ARXIV",
                "doi": f"10.48550/arxiv.{aid}".lower(), "pmid": "", "pmcid": "",
                "title": clean_text(e.findtext("a:title", namespaces=NS)),
                "authors": ", ".join(clean_text(a.findtext("a:name", namespaces=NS))
                                     for a in e.findall("a:author", NS)),
                "journal": "arXiv (" + ", ".join(cats[:3]) + ")",
                "pub_date": published, "pub_year": published[:4],
                "pub_types": "preprint", "is_preprint": True, "is_open_access": True,
                "abstract": clean_text(e.findtext("a:summary", namespaces=NS)),
                "keywords": "; ".join(cats),
                "fulltext_urls": [f"https://arxiv.org/pdf/{aid}"],
                "related_doi": journal_doi.lower(),
            }
            rec["uid"] = make_uid(rec["doi"])
            out.append(rec)
        if stop or len(entries) < 200:
            break
        offset += 200
    return out


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--groups")
    ap.add_argument("--full", action="store_true", help="search the whole window, not just since the last run")
    args = ap.parse_args()
    cfg = config("search")
    start, end = search_window()
    explicit = search_window_overridden()  # a range given on the CLI wins
    groups = {k: v for k, v in cfg["groups"].items() if v["scope"] == "focused"}
    if args.groups:
        groups = {k: v for k, v in groups.items() if k in args.groups.split(",")}
    # arXiv allows one request every 3 s and answers bursts with 429, so a
    # re-run only looks back to the last complete run (with a 14-day overlap
    # for late-indexed submissions); --full re-scans everything
    state_file = RAW / "arxiv" / "state.json"
    state = json.loads(state_file.read_text()) if state_file.exists() else {}
    if state.get("last_complete_run") and not (args.full or args.groups or explicit):
        since = (dt.date.fromisoformat(state["last_complete_run"]) - dt.timedelta(days=14)).isoformat()
        start = max(start, since)
    log(f"arXiv window {start} .. {end}")

    h = Http(min_interval=3.1)
    store = Store()
    hits = hitlog.load()
    run = today()
    for name, g in groups.items():
        req = re.compile(r"(?<![\w-])(" + "|".join(re.escape(t) for t in g["require_any"]) + r")(?![\w-])", re.I) \
            if g.get("require_any") else None
        kept = 0
        terms = g["terms"]
        for i in range(0, len(terms), 8):  # several terms per request
            chunk = terms[i:i + 8]
            phrase = re.compile(r"(?<![\w-])(" + "|".join(re.escape(t) for t in chunk) + r")(?![\w-])", re.I)
            for rec in search(h, chunk, start, end):
                text = f"{rec['title']} {rec['abstract']}"
                # arXiv matches words, not strictly phrases; re-check the phrases
                if not phrase.search(text) or (req and not req.search(text)):
                    continue
                uid = store.merge(rec, "arxiv")
                kept += hitlog.record(hits, uid, name, "focused", "arxiv", run)
        log(f"{name}: {kept} new arXiv hits")
        store.save()
        hitlog.save(hits)
    if not (args.groups or explicit):
        state_file.parent.mkdir(parents=True, exist_ok=True)
        state_file.write_text(json.dumps({"last_complete_run": run, "window": [start, end]}))
    log(f"done: store {len(store)} records")


if __name__ == "__main__":
    main()
