"""Catalogue filters shared by `litSearch query` and `litSearch export`.

The Go implementation (go/query.go) mirrors this module option for option;
the two must select the same rows for the same flags.

Filters combine with AND; comma-separated values within one filter combine
with OR.
  --text RE        regex (case-insensitive) over title, clocks, venue, matched groups, details
  --title RE       regex (case-insensitive) over the title only
  --doi LIST       DOIs (comma list, or @file with one per line); doi.org prefixes are ignored
  --uid LIST       catalogue uids (comma list or @file)
  --from/--to      2024 | 2024-03 | 2024-03-15 | today | a span back from today (7d, 1m, 2y)
  --range SPAN     span back from --to (or today): 1d 7d 2w 1m 6m 1y 10y
  --tier --type --category        exact values
  --modality --clock --species    substrings
  --added-since DATE, --has-fulltext, --fulltext RE (in the downloaded md/txt)
  --sort date|cited_by|title|author   (cited_by: descending)
"""

from __future__ import annotations

import datetime as dt
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "common"))
from agelit import RESEARCH, norm_doi, parse_when, read_csv, research_file  # noqa: E402

PROGRESS = RESEARCH / "progress.csv"
EXPORTS = RESEARCH / "exports"
REFERENCES = RESEARCH / "corpus" / "references.csv"
SORTS = ("date", "cited_by", "title", "author")

# flag spec for flags.parse_flags (same names and defaults as go/query.go)
FILTER_SPEC = {"text": ("", str), "title": ("", str), "doi": ("", str), "uid": ("", str),
               "from": ("", str), "to": ("", str), "range": ("", str), "tier": ("", str), "type": ("", str),
               "category": ("", str), "modality": ("", str), "clock": ("", str), "species": ("", str),
               "added-since": ("", str), "has-fulltext": (False, bool), "fulltext": ("", str),
               "sort": ("date", str)}

FILTER_HELP = """filters (AND between filters, OR within a comma list)
  --text RE           regex over title, clocks, venue, matched groups, details
  --title RE          regex over the title
  --doi LIST          DOIs: comma list or @file (one per line)
  --uid LIST          catalogue uids: comma list or @file
  --from WHEN         YYYY, YYYY-MM, YYYY-MM-DD, today or a span (2y)
  --to WHEN           YYYY, YYYY-MM, YYYY-MM-DD or today
  --range SPAN        span back from --to: 1d 7d 2w 1m 6m 1y 10y
  --tier LIST         landmark,core,related
  --type LIST         article,review,preprint,repository,software package,website,...
  --category LIST     e.g. clock development,benchmark/comparison
  --modality LIST     substrings, e.g. DNA methylation,proteomic
  --clock LIST        substrings, e.g. GrimAge,DunedinPACE
  --species LIST      substrings, e.g. mouse
  --added-since DATE  rows whose added_on >= DATE
  --has-fulltext      only rows with a downloaded Markdown full text
  --fulltext RE       regex searched in the downloaded paper.md / paper.txt
  --sort KEY          date | cited_by | title | author (default date)
"""


def _list(spec: str) -> list[str]:
    if not spec:
        return []
    if spec.startswith("@"):
        try:
            with open(spec[1:], encoding="utf-8") as fh:
                return [l.strip() for l in fh.read().splitlines() if l.strip()]
        except OSError:
            raise ValueError(f"cannot read {spec[1:]}") from None
    return [x.strip() for x in spec.split(",") if x.strip()]


def window(o) -> tuple[str, str]:
    """(--from, or --range back from --to / today; --to); "" when not given."""
    today = dt.date.today()
    hi = parse_when(o.to, end=True) if o.to else ""
    lo = ""
    if o.range:
        ref = dt.date.fromisoformat(hi) if hi else today
        shift = ref - today
        lo = (dt.date.fromisoformat(parse_when(o.range)) + shift).isoformat()
    if getattr(o, "from"):
        lo = parse_when(getattr(o, "from"))
    return lo, hi


def out_path(p: str) -> Path:
    """--out: absolute paths as given, relative ones under <output folder>/exports/."""
    q = Path(p)
    return q if q.is_absolute() else EXPORTS / q


def _int(v) -> int:
    try:
        return int(str(v or "").strip())
    except ValueError:
        return 0


def first_author_key(r: dict) -> str:
    a = (r.get("authors") or "").split(",")[0].strip()
    return (a.split(" ")[0] if a else r.get("title", "")).lower()


def _compile(flag: str, pattern: str):
    if not pattern:
        return None
    try:
        return re.compile(pattern, re.I)
    except re.error:
        raise ValueError(f"invalid regular expression for --{flag}") from None


def select(o, rows: list[dict] | None = None) -> tuple[list[dict], dict]:
    """(rows of progress.csv, or `rows`, that pass every filter, sorted; {uid: --fulltext context}).

    Raises ValueError with the message to print for a bad option value.
    """
    if o.sort not in SORTS:
        raise ValueError("--sort must be date, cited_by, title or author")
    rows = read_csv(PROGRESS) if rows is None else rows

    def any_in(value: str, wanted: str) -> bool:
        return not wanted or any(w.strip().lower() in value.lower() for w in wanted.split(","))

    def exact_in(value: str, wanted: str) -> bool:
        return not wanted or value.lower() in {w.strip().lower() for w in wanted.split(",")}

    rx = _compile("text", o.text)
    trx = _compile("title", o.title)
    frx = _compile("fulltext", o.fulltext)
    dois = {norm_doi(d) for d in _list(o.doi)} - {""}
    uids = set(_list(o.uid))
    lo, hi = window(o)
    lo, hi = lo or "0000", hi or "9999"
    hits: dict[str, str] = {}
    out = []
    for r in rows:
        if not (lo <= (r.get("date") or "0000") <= hi):
            continue
        if dois and (r.get("doi") or "").lower() not in dois:
            continue
        if uids and r.get("uid", "") not in uids:
            continue
        if not (exact_in(r.get("tier") or "", o.tier) and exact_in(r.get("resource_type") or "", o.type)
                and exact_in(r.get("category") or "", o.category) and any_in(r.get("modality") or "", o.modality)
                and any_in(r.get("clocks") or "", o.clock) and any_in(r.get("species") or "", o.species)):
            continue
        if o.added_since and (r.get("added_on") or "") < o.added_since:
            continue
        if o.has_fulltext and not r.get("fulltext_md"):
            continue
        if trx and not trx.search(r.get("title") or ""):
            continue
        if rx and not rx.search(" ".join(r.get(k) or "" for k in ("title", "clocks", "venue", "matched_groups", "details"))):
            continue
        if frx:
            p = r.get("fulltext_md") or r.get("fulltext_txt")
            if not p or not research_file(p).is_file():
                continue
            text = research_file(p).read_bytes().decode("utf-8", errors="ignore")
            m = frx.search(text)
            if not m:
                continue
            hits[r["uid"]] = " ".join(text[max(0, m.start() - 80):m.end() + 80].split())
        out.append(r)

    if o.sort == "cited_by":
        out.sort(key=lambda r: -_int(r.get("cited_by")))
    elif o.sort == "author":
        out.sort(key=first_author_key)
    else:
        out.sort(key=lambda r: r.get(o.sort) or "")
    return out, hits
