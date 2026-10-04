"""Reference formatting for `litSearch export` (Python implementation).

The Go implementation (go/refs.go) mirrors every rule here; for the same rows
both must print byte-identical text. Formats are chosen with three-letter
codes:

  APA  APA 7th            AMA  AMA 11th          MLA  MLA 9th
  CHI  Chicago author-date HAR  Harvard (Cite Them Right)
  VAN  Vancouver / ICMJE  IEE  IEEE              NAT  Nature
  BIB  BibTeX             RIS  RIS (Zotero, Mendeley, EndNote import)
  ENW  EndNote tagged     CSL  CSL-JSON

Author strings come from references.csv in PubMed style ("Belsky DW, Caspi
A") or as full names ("Daniel W Belsky"); collective names are kept literal.
Italics and small caps cannot be expressed in plain text and are omitted.
"""

from __future__ import annotations

import json
import re

CODES = ["APA", "AMA", "MLA", "CHI", "HAR", "VAN", "IEE", "NAT", "BIB", "RIS", "ENW", "CSL"]
DESCRIPTIONS = {
    "APA": "APA 7th edition", "AMA": "AMA Manual of Style 11th edition", "MLA": "MLA 9th edition",
    "CHI": "Chicago 17th, author-date", "HAR": "Harvard (Cite Them Right)", "VAN": "Vancouver / ICMJE (NLM)",
    "IEE": "IEEE", "NAT": "Nature", "BIB": "BibTeX", "RIS": "RIS (Zotero, Mendeley, EndNote, RefWorks)",
    "ENW": "EndNote tagged (.enw)", "CSL": "CSL-JSON (Zotero, pandoc, citeproc)",
}
ALIASES = {"IEEE": "IEE", "VANCOUVER": "VAN", "BIBTEX": "BIB", "ENDNOTE": "ENW", "CSL-JSON": "CSL",
           "JSON": "CSL", "CHICAGO": "CHI", "HARVARD": "HAR", "NATURE": "NAT"}
EXT = {"APA": ".apa.txt", "AMA": ".ama.txt", "MLA": ".mla.txt", "CHI": ".chi.txt", "HAR": ".har.txt",
       "VAN": ".van.txt", "IEE": ".iee.txt", "NAT": ".nat.txt", "BIB": ".bib", "RIS": ".ris",
       "ENW": ".enw", "CSL": ".json"}

COLLECTIVE = re.compile(r"(?i)\b(consortium|group|study|investigators|collaboration|network|initiative|"
                        r"team|committee|society|project|cohort|alliance|working)\b")
INITIALS = re.compile(r"^(.+?) ([A-Z]{1,4})$")
# fallback fields when a row is missing from references.csv
REF_COLUMNS = ["uid", "resource_type", "date", "year", "authors", "title", "venue", "doi", "pmid", "pmcid", "url"]


def parse_codes(spec: str) -> list[str]:
    out = []
    for tok in spec.split(","):
        t = tok.strip().upper()
        if not t:
            continue
        t = ALIASES.get(t, t)
        if t not in CODES:
            raise ValueError(f"unknown format {tok.strip()!r}; use {', '.join(CODES)}")
        if t not in out:
            out.append(t)
    return out


# ---------------------------------------------------------------------------
# authors
# ---------------------------------------------------------------------------

def parse_authors(s: str) -> list[dict]:
    """[{family, initials: [..]} | {literal}] from a comma-separated author string."""
    out = []
    for tok in (s or "").strip().rstrip(".").split(","):
        t = " ".join(tok.split())
        if not t or t.lower() in ("et al", "et al."):
            continue
        if COLLECTIVE.search(t):
            out.append({"literal": t})
            continue
        m = INITIALS.match(t)
        if m:
            out.append({"family": m.group(1), "initials": list(m.group(2))})
            continue
        parts = t.split(" ")
        if len(parts) == 1:
            out.append({"literal": t})
            continue
        inits = []
        for g in parts[:-1]:
            for piece in g.split("-"):
                c = piece.strip(".")
                if c:
                    inits.append(c[0].upper())
        out.append({"family": parts[-1], "initials": inits})
    return out


def _dots(a: dict, sep: str = " ") -> str:
    return sep.join(i + "." for i in a.get("initials", []))


def n_inverted(a: dict) -> str:       # Family, A. A.
    if "literal" in a:
        return a["literal"]
    return f"{a['family']}, {_dots(a)}" if a["initials"] else a["family"]


def n_harvard(a: dict) -> str:        # Family, A.A.
    if "literal" in a:
        return a["literal"]
    return f"{a['family']}, {_dots(a, '')}" if a["initials"] else a["family"]


def n_compact(a: dict) -> str:        # Family AA
    if "literal" in a:
        return a["literal"]
    return f"{a['family']} {''.join(a['initials'])}" if a["initials"] else a["family"]


def n_forward(a: dict) -> str:        # A. A. Family
    if "literal" in a:
        return a["literal"]
    return f"{_dots(a)} {a['family']}" if a["initials"] else a["family"]


def list_apa(au: list[dict]) -> str:
    n = [n_inverted(a) for a in au]
    if not n:
        return ""
    if len(n) == 1:
        return n[0]
    if len(n) == 2:
        return f"{n[0]}, & {n[1]}"
    if len(n) <= 20:
        return ", ".join(n[:-1]) + f", & {n[-1]}"
    return ", ".join(n[:19]) + f", . . . {n[-1]}"


def list_ama(au: list[dict]) -> str:
    n = [n_compact(a) for a in au]
    return ", ".join(n) if len(n) <= 6 else ", ".join(n[:3]) + ", et al"


def list_van(au: list[dict]) -> str:
    n = [n_compact(a) for a in au]
    return ", ".join(n) if len(n) <= 6 else ", ".join(n[:6]) + ", et al."


def list_mla(au: list[dict]) -> str:
    if not au:
        return ""
    if len(au) == 1:
        return n_inverted(au[0])
    if len(au) == 2:
        return f"{n_inverted(au[0])}, and {n_forward(au[1])}"
    return f"{n_inverted(au[0])}, et al."


def list_har(au: list[dict]) -> str:
    n = [n_harvard(a) for a in au]
    if not n:
        return ""
    if len(n) == 1:
        return n[0]
    if len(n) <= 3:
        return ", ".join(n[:-1]) + f" and {n[-1]}"
    return f"{n[0]} et al."


def list_chi(au: list[dict]) -> str:
    if not au:
        return ""
    n = [n_inverted(au[0])] + [n_forward(a) for a in au[1:]]
    if len(n) == 1:
        return n[0]
    if len(n) == 2:
        return f"{n[0]}, and {n[1]}"
    if len(n) <= 10:
        return ", ".join(n[:-1]) + f", and {n[-1]}"
    return ", ".join(n[:7]) + ", et al."


def list_iee(au: list[dict]) -> str:
    n = [n_forward(a) for a in au]
    if not n:
        return ""
    if len(n) == 1:
        return n[0]
    if len(n) == 2:
        return f"{n[0]} and {n[1]}"
    if len(n) <= 6:
        return ", ".join(n[:-1]) + f", and {n[-1]}"
    return f"{n[0]} et al."


def list_nat(au: list[dict]) -> str:
    n = [n_inverted(a) for a in au]
    if not n:
        return ""
    if len(n) == 1:
        return n[0]
    if len(n) == 2:
        return f"{n[0]} & {n[1]}"
    if len(n) <= 5:
        return ", ".join(n[:-1]) + f" & {n[-1]}"
    return f"{n[0]} et al."


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

def bare(t: str) -> str:
    """Title without a trailing full stop."""
    t = " ".join((t or "").split())
    return t[:-1] if t.endswith(".") else t


def sentence(t: str) -> str:
    """Text ending in . ? or !"""
    t = " ".join((t or "").split())
    if not t:
        return ""
    return t if t[-1] in ".?!" else t + "."


def with_dot(t: str) -> str:
    return t if t.endswith(".") else t + "."


def link(r: dict) -> str:
    return f"https://doi.org/{r['doi']}" if r.get("doi") else r.get("url", "")


def year(r: dict) -> str:
    return r.get("year") or "n.d."


# ---------------------------------------------------------------------------
# text styles: one line per reference
# ---------------------------------------------------------------------------

def fmt_apa(r: dict, au: list[dict]) -> str:
    a = list_apa(au)
    src = r.get("venue", "")
    if r.get("volume"):
        src += f", {r['volume']}"
    if r.get("issue"):
        src += f"({r['issue']})"
    if r.get("pages"):
        src += f", {r['pages']}"
    parts = [f"{a} ({year(r)}).", sentence(r.get("title", ""))] if a else \
        [sentence(r.get("title", "")), f"({year(r)})."]
    if src:
        parts.append(with_dot(src.lstrip(", ")))
    if link(r):
        parts.append(link(r))
    return " ".join(p for p in parts if p)


def _ama_like(r: dict, a: str) -> str:
    parts = []
    if a:
        parts.append(with_dot(a))
    parts.append(sentence(r.get("title", "")))
    if r.get("venue"):
        parts.append(sentence(r["venue"]))
    yv = year(r)
    if r.get("volume"):
        yv += f";{r['volume']}"
    if r.get("issue"):
        yv += f"({r['issue']})"
    if r.get("pages"):
        yv += f":{r['pages']}"
    parts.append(yv + ".")
    if r.get("doi"):
        parts.append(f"doi:{r['doi']}")
    elif r.get("url"):
        parts.append(r["url"])
    return " ".join(p for p in parts if p)


def fmt_ama(r: dict, au: list[dict]) -> str:
    return _ama_like(r, list_ama(au))


def fmt_van(r: dict, au: list[dict]) -> str:
    return _ama_like(r, list_van(au))


def fmt_mla(r: dict, au: list[dict]) -> str:
    parts = []
    a = list_mla(au)
    if a:
        parts.append(with_dot(a))
    parts.append(f'"{sentence(r.get("title", ""))}"')
    src = r.get("venue", "")
    if r.get("volume"):
        src += f", vol. {r['volume']}"
    if r.get("issue"):
        src += f", no. {r['issue']}"
    src = f"{src}, {year(r)}" if src else year(r)
    if r.get("pages"):
        src += f", pp. {r['pages']}"
    parts.append(src + ".")
    if link(r):
        parts.append(link(r) + ".")
    return " ".join(parts)


def fmt_har(r: dict, au: list[dict]) -> str:
    a = list_har(au)
    t = bare(r.get("title", ""))
    head = f"{a} ({year(r)}) '{t}'" if a else f"{t} ({year(r)})"
    src = r.get("venue", "")
    if r.get("volume"):
        src += f", {r['volume']}"
    if r.get("issue"):
        src += f"({r['issue']})"
    if r.get("pages"):
        src += f", pp. {r['pages']}"
    out = f"{head}, {src.lstrip(', ')}." if src else f"{head}."
    if r.get("doi"):
        out += f" doi:{r['doi']}."
    elif r.get("url"):
        out += f" Available at: {r['url']}."
    return out


def fmt_chi(r: dict, au: list[dict]) -> str:
    parts = []
    a = list_chi(au)
    if a:
        parts.append(with_dot(a))
    parts.append(f"{year(r)}.")
    parts.append(f'"{sentence(r.get("title", ""))}"')
    src = r.get("venue", "")
    if r.get("volume"):
        src += f" {r['volume']}"
    if r.get("issue"):
        src += f" ({r['issue']})"
    if r.get("pages"):
        src += f": {r['pages']}"
    src = src.strip()
    if src:
        parts.append(src + ".")
    if link(r):
        parts.append(link(r) + ".")
    return " ".join(parts)


def fmt_iee(r: dict, au: list[dict], n: int) -> str:
    a = list_iee(au)
    s = f"[{n}] " + (f"{a}, " if a else "") + f'"{bare(r.get("title", ""))},"'
    src = [r.get("venue", "")]
    if r.get("volume"):
        src.append(f"vol. {r['volume']}")
    if r.get("issue"):
        src.append(f"no. {r['issue']}")
    if r.get("pages"):
        src.append(f"pp. {r['pages']}")
    src.append(year(r))
    s += " " + ", ".join(p for p in src if p)
    if r.get("doi"):
        s += f", doi: {r['doi']}"
    elif r.get("url"):
        s += f". [Online]. Available: {r['url']}"
    return s + "."


def fmt_nat(r: dict, au: list[dict], n: int) -> str:
    a = list_nat(au)
    s = f"{n}. " + (f"{a} " if a else "") + sentence(r.get("title", ""))
    src = r.get("venue", "")
    if r.get("volume"):
        src += f" {r['volume']}"
    if r.get("pages"):
        src += f", {r['pages']}"
    src = src.strip()
    s += f" {src} ({year(r)})." if src else f" ({year(r)})."
    if link(r):
        s += f" {link(r)}"
    return s


# ---------------------------------------------------------------------------
# exchange formats
# ---------------------------------------------------------------------------

ARTICLE = {"article", "review"}


def kind(r: dict) -> str:
    t = r.get("resource_type", "")
    if t in ARTICLE:
        return "article"
    if t == "preprint":
        return "preprint"
    if t in ("repository", "software package", "software"):
        return "software"
    if t in ("website", "database", "web calculator"):
        return "web"
    if t in ("dataset", "archive"):
        return "data"
    return "other"


def _ascii_word(s: str) -> str:
    return re.sub(r"[^A-Za-z0-9]", "", s or "").lower()


def bib_key(r: dict, au: list[dict], used: dict) -> str:
    first = au[0] if au else {}
    fam = _ascii_word(first.get("family") or first.get("literal", "").split(" ")[0])
    words = [w for w in (_ascii_word(x) for x in (r.get("title") or "").split()) if w]
    key = (fam or "ref") + (r.get("year") or "nd") + (words[0] if words else "")
    n = used.get(key, 0)
    used[key] = n + 1
    return key if n == 0 else key + "abcdefghijklmnopqrstuvwxyz"[min(n - 1, 25)]


def bib_escape(s: str) -> str:
    for a, b in (("&", r"\&"), ("%", r"\%"), ("#", r"\#"), ("_", r"\_")):
        s = s.replace(a, b)
    return s


def fmt_bib(r: dict, au: list[dict], used: dict) -> str:
    k = kind(r)
    typ = "article" if k == "article" else "misc"
    fields = []
    if au:
        fields.append(("author", " and ".join("{" + a["literal"] + "}" if "literal" in a else n_inverted(a) for a in au)))
    fields.append(("title", "{" + bib_escape(bare(r.get("title", ""))) + "}"))
    if r.get("venue"):
        fields.append(("journal" if typ == "article" else "howpublished", bib_escape(r["venue"])))
    if r.get("year"):
        fields.append(("year", r["year"]))
    for src, dst in (("volume", "volume"), ("issue", "number")):
        if r.get(src):
            fields.append((dst, r[src]))
    if r.get("pages"):
        fields.append(("pages", r["pages"].replace("-", "--")))
    if r.get("doi"):
        fields.append(("doi", r["doi"]))
    if link(r):
        fields.append(("url", link(r)))
    if r.get("pmid"):
        fields.append(("pmid", r["pmid"]))
    if typ == "misc" and r.get("resource_type"):
        fields.append(("note", r["resource_type"]))
    body = ",\n".join(f"  {k_} = {{{v}}}" for k_, v in fields)
    return f"@{typ}{{{bib_key(r, au, used)},\n{body}\n}}"


RIS_TYPE = {"article": "JOUR", "preprint": "UNPB", "software": "COMP", "web": "ELEC", "data": "DATA", "other": "GEN"}
ENW_TYPE = {"article": "Journal Article", "preprint": "Unpublished Work", "software": "Computer Program",
            "web": "Web Page", "data": "Dataset", "other": "Generic"}
CSL_TYPE = {"article": "article-journal", "preprint": "article", "software": "software", "web": "webpage",
            "data": "dataset", "other": "document"}


def _pages(p: str) -> tuple[str, str]:
    bits = [b for b in re.split(r"[-–]", p or "") if b.strip()]
    if not bits:
        return "", ""
    return bits[0].strip(), (bits[-1].strip() if len(bits) > 1 else "")


def fmt_ris(r: dict, au: list[dict]) -> str:
    lines = [f"TY  - {RIS_TYPE[kind(r)]}"]
    lines += [f"AU  - {n_inverted(a)}" for a in au]
    lines.append(f"TI  - {bare(r.get('title', ''))}")
    if r.get("venue"):
        lines.append(f"T2  - {r['venue']}")
    if r.get("year"):
        lines.append(f"PY  - {r['year']}")
    if re.fullmatch(r"\d{4}-\d{2}-\d{2}", r.get("date", "")):
        lines.append(f"DA  - {r['date'].replace('-', '/')}")
    if r.get("volume"):
        lines.append(f"VL  - {r['volume']}")
    if r.get("issue"):
        lines.append(f"IS  - {r['issue']}")
    sp, ep = _pages(r.get("pages", ""))
    if sp:
        lines.append(f"SP  - {sp}")
    if ep:
        lines.append(f"EP  - {ep}")
    if r.get("doi"):
        lines.append(f"DO  - {r['doi']}")
    if link(r):
        lines.append(f"UR  - {link(r)}")
    if r.get("pmid"):
        lines.append(f"AN  - {r['pmid']}")
    lines.append("ER  - ")
    return "\n".join(lines)


def fmt_enw(r: dict, au: list[dict]) -> str:
    k = kind(r)
    lines = [f"%0 {ENW_TYPE[k]}"]
    lines += [f"%A {n_inverted(a)}" for a in au]
    lines.append(f"%T {bare(r.get('title', ''))}")
    if r.get("venue"):
        lines.append(("%J " if k == "article" else "%B ") + r["venue"])
    if r.get("year"):
        lines.append(f"%D {r['year']}")
    for tag, f in (("%V", "volume"), ("%N", "issue"), ("%P", "pages"), ("%R", "doi")):
        if r.get(f):
            lines.append(f"{tag} {r[f]}")
    if link(r):
        lines.append(f"%U {link(r)}")
    if r.get("pmid"):
        lines.append(f"%M {r['pmid']}")
    return "\n".join(lines)


def csl_item(r: dict, au: list[dict]) -> dict:
    it = {"id": r["uid"], "type": CSL_TYPE[kind(r)], "title": bare(r.get("title", ""))}
    if au:
        it["author"] = [{"literal": a["literal"]} if "literal" in a else
                        ({"family": a["family"], "given": _dots(a)} if a["initials"] else {"family": a["family"]})
                        for a in au]
    if r.get("venue"):
        it["container-title"] = r["venue"]
    d = r.get("date", "")
    if re.fullmatch(r"\d{4}-\d{2}-\d{2}", d):
        it["issued"] = {"date-parts": [[int(d[:4]), int(d[5:7]), int(d[8:10])]]}
    elif r.get("year", "").isdigit():
        it["issued"] = {"date-parts": [[int(r["year"])]]}
    for src, dst in (("volume", "volume"), ("issue", "issue"), ("pages", "page"), ("doi", "DOI"), ("pmid", "PMID")):
        if r.get(src):
            it[dst] = r[src]
    if link(r):
        it["URL"] = link(r)
    return it


# ---------------------------------------------------------------------------
# render a list of references in one format
# ---------------------------------------------------------------------------

def render(code: str, refs: list[dict]) -> str:
    """refs: rows of references.csv (in output order). Returns the full text, ending in a newline."""
    used: dict = {}
    out = []
    for i, r in enumerate(refs, 1):
        au = parse_authors(r.get("authors", ""))
        if code == "APA":
            out.append(fmt_apa(r, au))
        elif code == "AMA":
            out.append(f"{i}. " + fmt_ama(r, au))
        elif code == "VAN":
            out.append(f"{i}. " + fmt_van(r, au))
        elif code == "MLA":
            out.append(fmt_mla(r, au))
        elif code == "HAR":
            out.append(fmt_har(r, au))
        elif code == "CHI":
            out.append(fmt_chi(r, au))
        elif code == "IEE":
            out.append(fmt_iee(r, au, i))
        elif code == "NAT":
            out.append(fmt_nat(r, au, i))
        elif code == "BIB":
            out.append(fmt_bib(r, au, used))
        elif code == "RIS":
            out.append(fmt_ris(r, au))
        elif code == "ENW":
            out.append(fmt_enw(r, au))
    if code == "CSL":
        items = [csl_item(r, parse_authors(r.get("authors", ""))) for r in refs]
        return json.dumps(items, indent=2, ensure_ascii=False, sort_keys=True) + "\n"
    sep = "\n\n" if code in ("BIB", "RIS", "ENW") else "\n"
    return sep.join(out) + "\n" if out else ""
