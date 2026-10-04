#!/usr/bin/env python3
"""Search Europe PMC for every query group in the search section of the configuration.

Europe PMC indexes PubMed/MEDLINE, PMC and preprints (bioRxiv, medRxiv,
Research Square, SSRN, arXiv, ...), so one search covers journals and
preprints.

Two stages, because Europe PMC returns identifier lists quickly but full
records slowly (a 100-record core page can take 30 s, and larger pages time
out server-side):

1. idlist harvest per group: every hit's Europe PMC id and source.
2. details for hits not yet in the record store: PubMed efetch for MEDLINE
   records (fast, 200 per request) and Europe PMC core records, with a page
   size that shrinks on timeouts, for preprints and other non-PubMed sources.

Outputs
  <catalogue>/corpus/records.jsonl.gz     the shared record store (merged)
  <catalogue>/corpus/search_hits.csv      uid x group x source, cumulative
  <catalogue>/raw/europepmc/<run date>/counts.json

Usage
  sourcelens __step europepmc [--groups g1,g2] [--scope focused]
"""

from __future__ import annotations

import argparse
import time

import requests

from sourcelens.common import hits as hitlog  # noqa: E402
from sourcelens.common import pubmed  # noqa: E402
from sourcelens.common.agelit import (  # noqa: E402
    RAW,
    Http,
    clean_text,
    config,
    log,
    make_uid,
    norm_doi,
    norm_pmcid,
    norm_pmid,
    search_window,
    today,
    write_json,
)
from sourcelens.common.store import Store  # noqa: E402
from sourcelens.common.terms import epmc_term  # noqa: E402

API = "https://www.ebi.ac.uk/europepmc/webservices/rest/searchPOST"


def phrase_block(terms: list[str]) -> str:
    return "(" + " OR ".join(epmc_term(t) for t in terms) + ")"


def build_query(group: dict, start: str, end: str, sources: str = "nonmed") -> str:
    q = phrase_block(group["terms"])
    if group.get("require_any"):
        q += " AND " + phrase_block(group["require_any"])
    q = f"{q} AND FIRST_PDATE:[{start} TO {end}]"
    # PubMed already covers MEDLINE; by default Europe PMC is asked only for
    # what PubMed cannot give: preprints, PMC-only and other non-MEDLINE records
    return q + " AND NOT SRC:MED" if sources == "nonmed" else q


def normalise(r: dict) -> dict:
    ji = r.get("journalInfo") or {}
    journal = (ji.get("journal") or {}).get("title") or ""
    book = r.get("bookOrReportDetails") or {}
    source = r.get("source", "")
    if source == "PPR" and not journal:
        journal = book.get("publisher") or "preprint"
    pubtypes = (r.get("pubTypeList") or {}).get("pubType") or []
    if isinstance(pubtypes, str):
        pubtypes = [pubtypes]
    urls = [u.get("url") for u in (r.get("fullTextUrlList") or {}).get("fullTextUrl", [])
            if u.get("url")]
    kws = (r.get("keywordList") or {}).get("keyword") or []
    mesh = (r.get("meshHeadingList") or {}).get("meshHeading") or []
    grants = (r.get("grantsList") or {}).get("grant") or []
    rec = {
        "epmc_id": r.get("id", ""),
        "source_db": source,
        "doi": norm_doi(r.get("doi")),
        "pmid": norm_pmid(r.get("pmid")),
        "pmcid": norm_pmcid(r.get("pmcid")),
        "title": clean_text(r.get("title")),
        "authors": clean_text(r.get("authorString")),
        "journal": clean_text(journal),
        "volume": ji.get("volume", ""),
        "issue": ji.get("issue", ""),
        "pages": r.get("pageInfo", ""),
        "pub_date": r.get("firstPublicationDate", "") or "",
        "pub_year": str(r.get("pubYear", "") or ""),
        "pub_types": "; ".join(pubtypes),
        "is_preprint": source == "PPR",
        "is_open_access": r.get("isOpenAccess") == "Y",
        "in_pmc": r.get("inPMC") == "Y",
        "has_pdf": r.get("hasPDF") == "Y",
        "cited_by": r.get("citedByCount", 0) or 0,
        "license": r.get("license", "") or "",
        "fulltext_urls": urls,
        "language": r.get("language", "") or "",
        "abstract": clean_text(r.get("abstractText")),
        "keywords": "; ".join(kws),
        "mesh": "; ".join(m.get("descriptorName", "") for m in mesh),
        "funders": "; ".join(sorted({g.get("agency", "") for g in grants if g.get("agency")})),
    }
    rec["uid"] = make_uid(rec["doi"], rec["pmid"], rec["pmcid"], rec["epmc_id"])
    return rec


def page(http: Http, query: str, result_type: str, size: int, cursor: str) -> tuple[dict | None, int]:
    """One page: (json, 200) or (None, HTTP status; 0 for a network error)."""
    http._wait(API)
    try:
        r = http.s.post(API, data={"query": query, "format": "json", "pageSize": size,
                                   "resultType": result_type, "cursorMark": cursor,
                                   "synonym": "false"}, timeout=75)
    except requests.RequestException:
        return None, 0
    if r.status_code == 200:
        try:
            return r.json(), 200
        except ValueError:
            return None, 0
    return None, r.status_code


class EuropePMCUnavailable(RuntimeError):
    pass


def cursor_all(http: Http, query: str, result_type: str, size: int, max_size: int):
    """Yield result pages.

    Two failure modes need opposite responses:
      * HTTP 500 or a client timeout: the page was too heavy for the current
        load (the server gives up after ~30 s). Halve the page size and set a
        ceiling below it, so the timeout is not paid again on every page; the
        ceiling lifts after 25 clean pages.
      * 502/503/504/429: the service is down or throttling. Keep the page
        size and back off (30 s doubling to 8 min); after ~30 min in total
        raise EuropePMCUnavailable so the caller can skip the group.
    """
    cursor, shrinks, ceiling, clean, waited, backoff = "*", 0, max_size + 1, 0, 0, 30
    while True:
        js, status = page(http, query, result_type, size, cursor)
        if js is None:
            if status in (502, 503, 504, 429):
                if waited > 1800:
                    raise EuropePMCUnavailable(f"HTTP {status} for {waited}s")
                log(f"  Europe PMC HTTP {status}; waiting {backoff}s")
                time.sleep(backoff)
                waited += backoff
                backoff = min(backoff * 2, 480)
                continue
            shrinks += 1
            if shrinks > 12:
                raise EuropePMCUnavailable(f"pages keep failing (last HTTP {status})")
            ceiling, size, clean = size, max(10, size // 2), 0
            log(f"  page failed (HTTP {status}); page size -> {size}")
            continue
        shrinks, waited, backoff = 0, 0, 30
        clean += 1
        yield js
        results = (js.get("resultList") or {}).get("result") or []
        nxt = js.get("nextCursorMark")
        if not results or not nxt or nxt == cursor:
            return
        cursor = nxt
        if clean >= 25:
            ceiling, clean = max_size + 1, 0
        if size * 2 < ceiling:
            size = min(max_size, size * 2)


def harvest_ids(http: Http, query: str) -> tuple[int, list[dict]]:
    total, out = None, []
    for js in cursor_all(http, query, "idlist", 250, 1000):
        if total is None:
            total = js.get("hitCount", 0)
        out.extend((js.get("resultList") or {}).get("result") or [])
    return total or 0, out


def fetch_core(http: Http, source: str, ids: list[str], store: Store) -> None:
    for i in range(0, len(ids), 100):
        chunk = ids[i:i + 100]
        q = "(" + " OR ".join(f"EXT_ID:{x}" for x in chunk) + f") AND SRC:{source}"
        try:
            for js in cursor_all(http, q, "core", 100, 100):
                for raw in (js.get("resultList") or {}).get("result") or []:
                    try:
                        store.merge(normalise(raw), "europepmc")
                    except ValueError:
                        pass
        except EuropePMCUnavailable as exc:
            log(f"  core {source}: chunk skipped ({exc}); next run retries it")
        log(f"  core {source}: {min(i + 100, len(ids))}/{len(ids)}")


def id_key(r: dict) -> dict:
    return {"pmid": r.get("pmid", ""), "pmcid": r.get("pmcid", ""), "doi": r.get("doi", ""),
            "epmc_ids": [f"{r.get('source')}/{r.get('id')}"]}


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--groups", help="comma-separated subset of groups to run")
    ap.add_argument("--scope", choices=["focused", "broad", "all"], default="all")
    ap.add_argument("--sources", choices=["nonmed", "all"], default=None,
                    help="nonmed (default from config): skip MEDLINE, which search_pubmed.py covers")
    args = ap.parse_args()

    cfg = config("search")
    sources = args.sources or cfg.get("europepmc_sources", "nonmed")
    start, end = search_window()
    groups = cfg["groups"]
    if args.groups:
        groups = {k: v for k, v in groups.items() if k in args.groups.split(",")}
    if args.scope != "all":
        groups = {k: v for k, v in groups.items() if v["scope"] == args.scope}

    store = Store()
    log(f"record store: {len(store)} records; window {start} .. {end}")
    hits = hitlog.load()
    run = today()
    counts: dict = {}
    http = Http(min_interval=0.25)
    ph = pubmed.http()

    for name, group in groups.items():
        query = build_query(group, start, end, sources)
        try:
            total, ids = harvest_ids(http, query)
        except EuropePMCUnavailable as exc:
            # not fatal: the next run fills this group in (hits are cumulative)
            log(f"{name}: SKIPPED, Europe PMC unavailable ({exc})")
            counts[name] = {"error": str(exc), "query": query}
            continue
        log(f"{name}: {total} hits, {len(ids)} ids")

        missing = [r for r in ids if store.find(id_key(r)) is None]
        med = sorted({r["pmid"] if r.get("pmid") else r["id"] for r in missing if r.get("source") == "MED"})
        other: dict[str, list[str]] = {}
        for r in missing:
            if r.get("source") != "MED":
                other.setdefault(r["source"], []).append(r["id"])
        log(f"  new: {len(med)} MEDLINE -> efetch, {sum(map(len, other.values()))} other -> core")
        for rec in pubmed.efetch(ph, med):
            store.merge(rec, "pubmed")
        still = [r for r in missing if r.get("source") == "MED" and store.find(id_key(r)) is None]
        for r in still:  # efetch did not return them (rare); try Europe PMC
            other.setdefault("MED", []).append(r["id"])
        for src, lst in other.items():
            fetch_core(http, src, lst, store)

        n_new = 0
        for r in ids:
            uid = store.find(id_key(r))
            if uid is None:
                try:
                    uid = make_uid(r.get("doi"), r.get("pmid"), r.get("pmcid"), f"{r.get('source')}{r.get('id')}")
                except ValueError:
                    continue
            n_new += hitlog.record(hits, uid, name, group["scope"], "europepmc", run)
        counts[name] = {"hitCount": total, "ids": len(ids), "new_hits": n_new, "query": query}
        log(f"  {n_new} new hit rows; store now {len(store)}")
        # checkpoint after every group so an interrupted run keeps its work
        store.save()
        hitlog.save(hits)

    write_json(RAW / "europepmc" / run / "counts.json", {"window": [start, end], "groups": counts})
    log(f"done: store {len(store)} records, {len(hits)} hit rows")


if __name__ == "__main__":
    main()
