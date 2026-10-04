"""Search OpenAlex for every group of the search section of the configuration.

OpenAlex indexes works from every field of research, so it is what makes
topics outside biomedicine work. Each group's terms are matched in title and
abstract within the search window, newest first, up to search.max_results
works per group.

Outputs
  <catalogue>/corpus/records.jsonl.gz   (merged; source = openalex)
  <catalogue>/corpus/search_hits.csv    (source = openalex)
  <catalogue>/raw/openalex/<run date>/counts.json

Usage (normally run by litsearch update)
  litsearch __step openalex-search [--groups a,b] [--scope focused|broad|all]
"""

from __future__ import annotations

import argparse
import os
import re

from litsearch.common import agelit
from litsearch.common import hits as hitlog
from litsearch.common.agelit import (
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
from litsearch.common.store import Store
from litsearch.common.terms import openalex_term, word_matcher

WORKS = "https://api.openalex.org/works"
SELECT = ("id,doi,title,display_name,publication_date,publication_year,type,authorships,"
          "primary_location,ids,abstract_inverted_index,cited_by_count,open_access,biblio,language,best_oa_location")

# OpenAlex work types and how the catalogue records them; others are skipped
TYPES = {"article": "Journal Article", "review": "Review", "preprint": "preprint", "book-chapter": "book chapter",
         "book": "book chapter", "dissertation": "thesis", "dataset": "dataset", "report": "report",
         "editorial": "Editorial", "letter": "Letter", "erratum": "Erratum", "retraction": "Retraction",
         "standard": "report", "other": ""}


def query(group: dict) -> str:
    """The group's terms (OR), and its require_any terms when given."""
    def any_of(terms: list[str]) -> str:
        q = [openalex_term(t) for t in terms]
        return q[0] if len(q) == 1 else "(" + " OR ".join(q) + ")"
    q = any_of(group["terms"])
    if group.get("require_any"):
        q += " AND " + any_of(group["require_any"])
    return q


def abstract(idx: dict | None) -> str:
    """Rebuild an abstract from OpenAlex's inverted index."""
    words = sorted((p, w) for w, ps in (idx or {}).items() for p in (ps or []) if isinstance(p, int))
    return clean_text(" ".join(w for _, w in words))


def work(w: dict) -> dict | None:
    """An OpenAlex work as a store record (None for types that are not research)."""
    typ = w.get("type") or ""
    if typ not in TYPES:
        return None  # paratext, peer-review, grant, ...
    pt = TYPES[typ]
    loc = w.get("primary_location") or {}
    src = loc.get("source") or {}
    if typ == "article" and src.get("type") == "conference":
        pt = "conference paper"
    ids = w.get("ids") or {}
    names = [n for n in ((a.get("author") or {}).get("display_name") for a in w.get("authorships") or []) if n]
    oa = w.get("open_access") or {}
    urls = []
    if oa.get("oa_url"):
        urls.append(oa["oa_url"])
    best_pdf = (w.get("best_oa_location") or {}).get("pdf_url") or ""
    if best_pdf and (not urls or urls[0] != best_pdf):
        urls.append(best_pdf)
    bib = w.get("biblio") or {}
    pages = bib.get("first_page") or ""
    if bib.get("last_page") and bib["last_page"] != pages:
        pages += "-" + bib["last_page"]
    date = w.get("publication_date") or ""
    year = str(w.get("publication_year") or "") or date[:4]
    is_oa = oa.get("is_oa") is True
    rec = {
        "source_db": "OPENALEX", "openalex_id": w.get("id") or "", "doi": norm_doi(w.get("doi")),
        "pmid": norm_pmid(ids.get("pmid")), "pmcid": norm_pmcid(ids.get("pmcid")),
        "title": clean_text(w.get("title") or w.get("display_name") or ""), "authors": ", ".join(names),
        "journal": clean_text(src.get("display_name") or ""), "volume": bib.get("volume") or "",
        "issue": bib.get("issue") or "", "pages": pages, "pub_date": date, "pub_year": year, "pub_types": pt,
        "is_preprint": typ == "preprint", "is_open_access": is_oa, "cited_by": int(w.get("cited_by_count") or 0),
        "license": loc.get("license") or "", "fulltext_urls": urls, "language": w.get("language") or "",
        "abstract": abstract(w.get("abstract_inverted_index")),
        "url": loc.get("landing_page_url") or w.get("id") or "",
        "oa_pdf_url": best_pdf or ((loc.get("pdf_url") or "") if is_oa else ""),
    }
    if not rec["title"]:
        return None
    link = "" if (rec["doi"] or rec["pmid"] or rec["pmcid"]) else (w.get("id") or "")
    rec["uid"] = make_uid(rec["doi"], rec["pmid"], rec["pmcid"], "", link)
    return rec


_KEY = re.compile(r"[\W_]+")


def work_key(r: dict) -> str:
    """Works with the same title, venue and year are versions of one item
    (Zenodo concept and version DOIs, figshare .v2, repository copies)."""
    t = _KEY.sub(" ", (r.get("title") or "").lower()).strip()
    if not t:
        return ""
    year = str(r.get("pub_year") or r.get("pub_date") or "")[:4]
    return f"{t}|{(r.get('journal') or '').strip().lower()}|{year}"


def max_results() -> int:
    try:
        n = int(config("search").get("max_results") or 0)
    except (TypeError, ValueError):
        n = 0
    return n if n > 0 else 5000


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--groups")
    ap.add_argument("--scope", choices=["focused", "broad", "all"], default="all")
    args = ap.parse_args()

    groups = config("search")["groups"]
    if args.groups:
        groups = {k: v for k, v in groups.items() if k in args.groups.split(",")}
    if args.scope != "all":
        groups = {k: v for k, v in groups.items() if v["scope"] == args.scope}
    start, end = search_window()
    limit = max_results()
    h = Http(min_interval=0.12)
    store = Store()
    hits = hitlog.load()
    run = today()
    log(f"record store: {len(store)} records; window {start} .. {end}; at most {limit} works per group")
    seen: dict[str, str] = {}  # work_key -> uid of the version already stored
    for r in store.values():
        k = work_key(r)
        if k and k not in seen:
            seen[k] = r["uid"]
    counts = {}
    for name, group in groups.items():
        q = query(group)
        # OpenAlex stems words ("base" also finds "based"); keep a work only when the
        # terms appear as whole words in its title or abstract. Works whose abstract
        # OpenAlex does not provide are kept: they matched its full index.
        match = word_matcher(group["terms"])
        req = word_matcher(group["require_any"]) if group.get("require_any") else None
        dropped = versions = 0
        flt = f"title_and_abstract.search:{q},from_publication_date:{start},to_publication_date:{end}"
        cursor, total, collected, n_new = "*", 0, 0, 0
        while cursor and collected < limit:
            params = {"filter": flt, "per-page": "200", "cursor": cursor, "sort": "publication_date:desc",
                      "select": SELECT}
            if agelit.CONTACT:
                params["mailto"] = agelit.CONTACT
            if os.environ.get("OPENALEX_API_KEY"):
                params["api_key"] = os.environ["OPENALEX_API_KEY"]
            r = h.get(WORKS, params=params, timeout=120)
            if r is None or r.status_code != 200:
                status = 0 if r is None else r.status_code
                log(f"{name}: OpenAlex request failed (HTTP {status}); keeping {collected} works")
                break
            j = r.json()
            meta = j.get("meta") or {}
            total = int(meta.get("count") or 0)
            results = j.get("results") or []
            for x in results:
                rec = work(x)
                if rec is None:
                    continue
                text = f"{rec['title']} {rec['abstract']}"
                if rec["abstract"] and (not match(text) or (req and not req(text))):
                    dropped += 1
                    continue
                if store.find(rec) is None:
                    first = seen.get(work_key(rec)) if work_key(rec) else None
                    if first:
                        # another version of a stored work: count the hit for that one
                        versions += 1
                        n_new += hitlog.record(hits, first, name, group["scope"], "openalex", run)
                        continue
                uid = store.merge(rec, "openalex")
                k = work_key(rec)
                if k and k not in seen:
                    seen[k] = uid
                n_new += hitlog.record(hits, uid, name, group["scope"], "openalex", run)
                collected += 1
            cursor = meta.get("next_cursor") or ""
            if not results:
                break
        if total > collected >= limit:
            log(f"{name}: OpenAlex has {total} works, kept the newest {collected} (search.max_results)")
        log(f"{name}: OpenAlex count {total}, collected {collected}, {n_new} new hit rows, "
            f"{dropped} dropped (terms not in title or abstract), {versions} other versions of stored works")
        counts[name] = {"count": total, "collected": collected, "new_hits": n_new, "query": q}
        store.save()
        hitlog.save(hits)
    write_json(agelit.RAW / "openalex" / run / "counts.json",
               {"window": [start, end], "groups": counts, "max_results": str(limit)})
    log(f"done: store {len(store)} records")


if __name__ == "__main__":
    main()
