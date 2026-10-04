#!/usr/bin/env python3
"""Citation counts, exact publication dates and OA status from OpenAlex.

Looks up every DOI in <catalogue>/progress.csv, 50 per request, through the
OpenAlex works filter (not the free-text search, which is rate-limited for
anonymous use). Refreshed on every run because citation counts change.

build_progress.py uses the result to fill `cited_by` and to sharpen dates
that the indexes only give to the year (stored as YYYY-01-01).

Output
  <catalogue>/corpus/citations.csv   uid, doi, cited_by, publication_date, oa_status, oa_url, openalex_id

Usage
  litsearch __step openalex
"""

from __future__ import annotations

import os

from litsearch.common.agelit import (  # noqa: E402
    CONTACT,
    CORPUS,
    RESEARCH,
    Http,
    log,
    norm_doi,
    read_csv,
    today,
    write_csv,
)

API = "https://api.openalex.org/works"
COLUMNS = ["uid", "doi", "cited_by", "publication_date", "oa_status", "oa_url", "openalex_id", "checked_on"]


def main() -> None:
    rows = [r for r in read_csv(RESEARCH / "progress.csv") if r.get("doi")]
    by_doi = {r["doi"].lower(): r["uid"] for r in rows}
    old = {r["uid"]: r for r in read_csv(CORPUS / "citations.csv")}
    h = Http(min_interval=0.15)
    params_base = {"select": "doi,cited_by_count,publication_date,open_access,id", "per_page": 50}
    if CONTACT:
        params_base["mailto"] = CONTACT
    if os.environ.get("OPENALEX_API_KEY"):
        params_base["api_key"] = os.environ["OPENALEX_API_KEY"]
    dois = sorted(by_doi)
    out = dict(old)
    for i in range(0, len(dois), 50):
        # OpenAlex filters split on '|' and ',' so DOIs containing either are skipped
        chunk = [d for d in dois[i:i + 50] if "|" not in d and "," not in d]
        r = h.get(API, params={**params_base, "filter": "doi:" + "|".join(chunk)})
        if r is None or r.status_code != 200:
            log(f"OpenAlex failed at {i}")
            continue
        for w in r.json().get("results", []):
            d = norm_doi(w.get("doi"))
            uid = by_doi.get(d)
            if not uid:
                continue
            oa = w.get("open_access") or {}
            out[uid] = {"uid": uid, "doi": d, "cited_by": w.get("cited_by_count", 0),
                        "publication_date": w.get("publication_date", ""), "oa_status": oa.get("oa_status", ""),
                        "oa_url": oa.get("oa_url", "") or "", "openalex_id": w.get("id", ""),
                        "checked_on": today()}
        if (i // 50) % 50 == 49:
            log(f"  {i + 50}/{len(dois)}")
    write_csv(CORPUS / "citations.csv", sorted(out.values(), key=lambda r: r["uid"]), COLUMNS)
    log(f"{len(out)} records with OpenAlex data ({len(dois)} DOIs looked up)")


if __name__ == "__main__":
    main()
