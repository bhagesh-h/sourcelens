"""NCBI E-utilities: esearch for PMIDs, efetch + parse for full PubMed records."""

from __future__ import annotations

import os

from lxml import etree

from agelit import Http, clean_text, log, make_uid, norm_doi, norm_pmcid, norm_pmid

EUTILS = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils"
MONTHS = {m: i for i, m in enumerate(
    ["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"], 1)}


def http() -> Http:
    return Http(min_interval=0.12 if os.environ.get("NCBI_API_KEY") else 0.36)


def _key(d: dict) -> dict:
    if os.environ.get("NCBI_API_KEY"):
        d["api_key"] = os.environ["NCBI_API_KEY"]
    return d


def esearch(h: Http, term: str, retmax: int = 0) -> tuple[int, list[str]]:
    r = h.post(f"{EUTILS}/esearch.fcgi",
               data=_key({"db": "pubmed", "term": term, "retmode": "json", "retmax": retmax}))
    if r is None:
        raise RuntimeError("esearch failed")
    js = r.json()["esearchresult"]
    return int(js.get("count", 0)), js.get("idlist", [])


def _text(el) -> str:
    return clean_text("".join(el.itertext())) if el is not None else ""


def _date(y, m, d) -> str:
    m = m or "1"
    m = MONTHS.get(m[:3].lower()) or (int(m) if str(m).isdigit() else 1)
    d = d if d and str(d).isdigit() else "1"
    return f"{y}-{int(m):02d}-{int(d):02d}"


def pub_date(art) -> str:
    """Earliest of the electronic ArticleDate and the journal issue date."""
    cands = []
    for ad in art.findall(".//Article/ArticleDate"):
        if ad.findtext("Year"):
            cands.append(_date(ad.findtext("Year"), ad.findtext("Month"), ad.findtext("Day")))
    pd_ = art.find(".//Article/Journal/JournalIssue/PubDate")
    if pd_ is not None:
        y = pd_.findtext("Year")
        m = pd_.findtext("Month")
        if not y and pd_.findtext("MedlineDate"):
            md = pd_.findtext("MedlineDate")
            y, m = md[:4], (md[5:8] if len(md) > 7 else None)
        if y and y.isdigit():
            cands.append(_date(y, m, pd_.findtext("Day")))
    return min(cands) if cands else ""


def parse(xml: bytes) -> list[dict]:
    out = []
    root = etree.fromstring(xml)
    for art in root.findall("PubmedArticle"):
        pmid = art.findtext(".//MedlineCitation/PMID")
        a = art.find(".//MedlineCitation/Article")
        if a is None:
            continue
        doi = pmcid = ""
        for aid in art.findall(".//PubmedData/ArticleIdList/ArticleId"):
            if aid.get("IdType") == "doi" and not doi:
                doi = aid.text or ""
            elif aid.get("IdType") == "pmc":
                pmcid = aid.text or ""
        if not doi:
            for el in a.findall("ELocationID"):
                if el.get("EIdType") == "doi":
                    doi = el.text or ""
        authors = []
        for au in a.findall("AuthorList/Author"):
            if au.findtext("CollectiveName"):
                authors.append(au.findtext("CollectiveName"))
            else:
                authors.append(f"{au.findtext('LastName') or ''} {au.findtext('Initials') or ''}".strip())
        abstract = " ".join(
            ((p.get("Label") + ": ") if p.get("Label") else "") + _text(p)
            for p in a.findall("Abstract/AbstractText"))
        date = pub_date(art)
        pubtypes = [_text(p) for p in art.findall(".//PublicationTypeList/PublicationType")]
        rec = {
            "epmc_id": pmid, "source_db": "MED",
            "doi": norm_doi(doi), "pmid": norm_pmid(pmid), "pmcid": norm_pmcid(pmcid),
            "title": _text(a.find("ArticleTitle")),
            "authors": ", ".join(authors),
            "journal": a.findtext("Journal/Title") or "",
            "volume": a.findtext("Journal/JournalIssue/Volume") or "",
            "issue": a.findtext("Journal/JournalIssue/Issue") or "",
            "pages": a.findtext("Pagination/MedlinePgn") or "",
            "pub_date": date, "pub_year": date[:4],
            "pub_types": "; ".join(pubtypes),
            "is_preprint": "Preprint" in pubtypes,
            "in_pmc": bool(pmcid),
            "language": a.findtext("Language") or "",
            "abstract": abstract,
            "keywords": "; ".join(_text(k) for k in art.findall(".//KeywordList/Keyword")),
            "mesh": "; ".join(_text(m.find("DescriptorName"))
                              for m in art.findall(".//MeshHeadingList/MeshHeading")),
            "funders": "; ".join(sorted({g.findtext("Agency") or ""
                                         for g in art.findall(".//GrantList/Grant")} - {""})),
            "retracted": any(p in ("Retracted Publication", "Retraction of Publication") for p in pubtypes),
        }
        rec["uid"] = make_uid(rec["doi"], rec["pmid"], rec["pmcid"])
        out.append(rec)
    return out


def efetch(h: Http, pmids: list[str], batch: int = 200) -> list[dict]:
    out = []
    for i in range(0, len(pmids), batch):
        chunk = pmids[i:i + batch]
        r = h.post(f"{EUTILS}/efetch.fcgi",
                   data=_key({"db": "pubmed", "id": ",".join(chunk), "retmode": "xml"}), timeout=180)
        if r is None:
            log(f"efetch failed for {len(chunk)} PMIDs starting {chunk[0]}")
            continue
        try:
            out.extend(parse(r.content))
        except etree.XMLSyntaxError as exc:
            log(f"efetch parse error ({exc}) for batch starting {chunk[0]}")
        if (i // batch) % 10 == 9 or i + batch >= len(pmids):
            log(f"  efetch {min(i + batch, len(pmids))}/{len(pmids)}")
    return out
