"""Crossref and DataCite lookups by DOI, normalised to the record-store schema."""

from __future__ import annotations

from urllib.parse import quote

from agelit import CONTACT, Http, clean_text, make_uid, norm_doi

CROSSREF = "https://api.crossref.org/works/"
DATACITE = "https://api.datacite.org/dois/"

TYPE_MAP = {"journal-article": "article", "posted-content": "preprint",
            "proceedings-article": "conference paper", "book-chapter": "book chapter",
            "book": "book", "dataset": "dataset", "report": "report",
            "dissertation": "thesis", "peer-review": "peer review",
            "reference-entry": "reference entry"}


def http() -> Http:
    ua = "litSearch/2.0" + (f" (mailto:{CONTACT})" if CONTACT else "")
    return Http(min_interval=0.1, headers={"User-Agent": ua})


def _date(parts) -> str:
    try:
        p = parts["date-parts"][0]
    except (KeyError, IndexError, TypeError):
        return ""
    if not p or p[0] is None:
        return ""
    y = p[0]
    m = p[1] if len(p) > 1 else 1
    d = p[2] if len(p) > 2 else 1
    return f"{y:04d}-{m:02d}-{d:02d}"


def crossref(h: Http, doi: str) -> dict | None:
    r = h.get(CROSSREF + quote(doi, safe="/"), params={"mailto": CONTACT} if CONTACT else None)
    if r is None or r.status_code != 200:
        return None
    m = r.json().get("message", {})
    dates = [_date(m.get(k)) for k in ("published-online", "published-print", "posted", "issued", "published")]
    dates = [d for d in dates if d]
    authors = []
    for a in m.get("author", []) or []:
        if a.get("family"):
            authors.append(f"{a['family']} {''.join(x[0] for x in (a.get('given') or '').replace('-', ' ').split() if x)}".strip())
        elif a.get("name"):
            authors.append(a["name"])
    ctype = m.get("type", "")
    venue = (m.get("container-title") or [""])[0]
    inst = m.get("institution")
    if not venue and inst:
        venue = (inst[0] if isinstance(inst, list) else inst).get("name", "")
    if not venue and ctype == "posted-content":
        venue = m.get("publisher", "") or "preprint"
    lic = [x.get("URL", "") for x in m.get("license", []) or []]
    links = [x.get("URL") for x in m.get("link", []) or [] if x.get("content-type") == "application/pdf"]
    rec = {
        "epmc_id": "", "source_db": "CROSSREF",
        "doi": norm_doi(m.get("DOI") or doi), "pmid": "", "pmcid": "",
        "title": clean_text((m.get("title") or [""])[0]),
        "authors": ", ".join(authors),
        "journal": clean_text(venue),
        "volume": m.get("volume", ""), "issue": m.get("issue", ""), "pages": m.get("page", ""),
        "pub_date": min(dates) if dates else "",
        "pub_year": (min(dates)[:4] if dates else ""),
        "pub_types": TYPE_MAP.get(ctype, ctype),
        "is_preprint": ctype == "posted-content",
        "cited_by": m.get("is-referenced-by-count", 0) or 0,
        "license": lic[0] if lic else "",
        "fulltext_urls": links,
        "abstract": clean_text(m.get("abstract")),
        "publisher": m.get("publisher", ""),
    }
    rec["uid"] = make_uid(rec["doi"])
    return rec


def datacite(h: Http, doi: str) -> dict | None:
    r = h.get(DATACITE + quote(doi, safe="/"))
    if r is None or r.status_code != 200:
        return None
    a = (r.json().get("data") or {}).get("attributes") or {}
    titles = a.get("titles") or [{}]
    creators = [c.get("name", "") for c in a.get("creators") or []]
    dates = {d.get("dateType"): d.get("date", "") for d in a.get("dates") or []}
    date = dates.get("Issued") or dates.get("Created") or (str(a.get("publicationYear") or "") + "-01-01")
    rtype = ((a.get("types") or {}).get("resourceTypeGeneral") or "").lower()
    desc = " ".join(d.get("description", "") for d in a.get("descriptions") or [] if d.get("descriptionType") == "Abstract")
    rec = {
        "epmc_id": "", "source_db": "DATACITE", "doi": norm_doi(a.get("doi") or doi),
        "pmid": "", "pmcid": "", "title": clean_text(titles[0].get("title", "")),
        "authors": ", ".join(creators), "journal": a.get("publisher", "") or "",
        "pub_date": date[:10], "pub_year": date[:4],
        "pub_types": rtype, "is_preprint": False, "abstract": clean_text(desc),
        "url": a.get("url", ""), "license": ((a.get("rightsList") or [{}])[0]).get("rightsUri", ""),
    }
    rec["uid"] = make_uid(rec["doi"])
    return rec
