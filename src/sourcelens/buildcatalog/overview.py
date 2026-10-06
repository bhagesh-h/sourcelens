"""Overview rows: the catalogue with what each row is about and what is pending.

Used by the dry run (reports/dryrun_<stamp>.csv) and the run report. One row
per progress.csv row, with these columns added:

  summary           the abstract; else the start of the downloaded full text;
                    else a repository's or website's description
  summary_from      abstract | fulltext | description
  keywords          author keywords and MeSH headings
  fulltext_sources  where an open full text may come from
  pending           what the next update would download: full text,
                    attachments ("(retry)" when an earlier attempt failed)

The Go twin is cmd/sourcelens/overview.go.
"""

from __future__ import annotations

import datetime as dt

from sourcelens.common import agelit
from sourcelens.common.store import Store
from sourcelens.pullliturature import attachments

EXTRA = ["summary", "summary_from", "keywords", "fulltext_sources", "pending"]
SUMMARY_MAX = 2000
PAPER_TYPES = {"article", "review", "preprint", "report", "thesis", "conference paper", "book chapter"}


def collapse(text: str) -> str:
    return " ".join((text or "").split())


def _items(v) -> list[str]:
    if isinstance(v, list):
        return [str(x).strip() for x in v if str(x).strip()]
    if isinstance(v, str):
        return [x.strip() for x in v.split(";") if x.strip()]
    return []


def fulltext_excerpt(row: dict) -> str:
    for col in ("fulltext_txt", "fulltext_md"):
        rel = row.get(col) or ""
        if not rel:
            continue
        p = agelit.research_file(rel)
        try:
            with open(p, "rb") as fh:
                raw = fh.read(20000)
        except OSError:
            continue
        text = collapse(raw.decode("utf-8", "ignore"))
        if text:
            return text[:SUMMARY_MAX]
    return ""


def descriptions() -> dict[str, str]:
    """uid -> description of repositories, packages and websites."""
    out: dict[str, str] = {}
    for r in agelit.read_csv(agelit.REPOS / "repositories.csv"):
        if r.get("uid") and r.get("description"):
            out[r["uid"]] = collapse(r["description"])
    for r in agelit.read_csv(agelit.WEBSITES / "websites.csv"):
        if not r.get("url"):
            continue
        text = collapse(" ".join(x for x in (r.get("page_title"), r.get("page_description"), r.get("note")) if x))
        if text:
            try:
                out.setdefault(agelit.make_uid(url=r["url"]), text)
            except ValueError:
                pass
    return out


def fulltext_sources(rec: dict) -> str:
    doi = (rec.get("doi") or "").lower()
    out = []
    if rec.get("pmcid"):
        out.append("pmc")
    if doi.startswith("10.1101/"):
        out.append("biorxiv")
    if any(str(e).startswith("PPR/") for e in rec.get("epmc_ids") or []):
        out.append("europepmc")
    if doi.startswith("10.48550/arxiv."):
        out.append("arxiv")
    if doi:
        out.append("unpaywall")
    if rec.get("oa_pdf_url"):
        out.append("openalex")
    return ",".join(out)


def pending(row: dict, idx: dict | None, atts: list[dict], cutoff: str) -> str:
    if row.get("resource_type") not in PAPER_TYPES:
        return ""
    out = []
    status = (idx or {}).get("status", "")
    if idx is None:
        out.append("full text")
    elif status in ("partial", "deferred", "error") or (status == "none" and idx.get("checked_on", "") < cutoff):
        out.append("full text (retry)")
    if row.get("pmcid") and status not in ("none", "removed"):
        if any(a.get("status") == "failed" for a in atts):
            out.append("attachments (retry)")
        elif not (idx or {}).get("attachments") or any(a.get("status") == "listed" and
                                                        a.get("reason") != "extension not selected" for a in atts):
            out.append("attachments")
    return "; ".join(out)


def rows(progress: list[dict] | None = None) -> list[dict]:
    """The overview rows of the current catalogue (progress.csv columns + EXTRA)."""
    progress = agelit.read_csv(agelit.RESEARCH / "progress.csv") if progress is None else progress
    store = Store(agelit.CORPUS / "records.jsonl.gz")
    index = {r["uid"]: r for r in agelit.read_csv(agelit.FULLTEXT / "fulltext_index.csv")}
    atts = attachments.read_index()
    desc = descriptions()
    cutoff = (dt.date.today() - dt.timedelta(days=30)).isoformat()
    out = []
    for r in progress:
        uid = r.get("uid", "")
        rec = store.get(uid) or {}
        summary, frm = collapse(rec.get("abstract") or "")[:SUMMARY_MAX], "abstract"
        if not summary:
            summary, frm = fulltext_excerpt(r), "fulltext"
        if not summary:
            summary, frm = desc.get(uid, "")[:SUMMARY_MAX], "description"
        keywords = "; ".join(dict.fromkeys(_items(rec.get("keywords")) + _items(rec.get("mesh"))))
        is_paper = r.get("resource_type") in PAPER_TYPES
        out.append({**r, "summary": summary, "summary_from": frm if summary else "", "keywords": keywords,
                    "fulltext_sources": fulltext_sources(rec) if is_paper else "",
                    "pending": pending(r, index.get(uid), atts.get(uid, []), cutoff)})
    return out
