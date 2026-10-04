#!/usr/bin/env python3
"""Download open-access full texts as PDF, Markdown and plain text, with metadata.

For every article row in <catalogue>/progress.csv (optionally limited by tier) the
following sources are tried, open-access copies only:

1. PMC Open Access on AWS (s3://pmc-oa-opendata): JATS XML, PMC's plain
   text, the PDF, and PMC's per-article JSON (licence, retraction flag).
2. bioRxiv / medRxiv API: JATS XML and PDF of the latest preprint version.
3. Europe PMC fullTextXML: PMC articles missing from (1) and other preprints
   with a full text in Europe PMC (PPR ids).
4. arXiv: the PDF of arXiv preprints found by search_arxiv.py.
5. Unpaywall: the best open-access PDF location for the DOI (publisher OA,
   repositories, arXiv, ...); needs a contact email.
6. OpenAlex: the open-access PDF link OpenAlex recorded for the work.

XML is converted to Markdown by jats.py; a PDF without XML is converted with
PyMuPDF and pymupdf4llm when installed (pip install litsearch[pdf]), else
with poppler (pdftotext, pdftohtml).

Layout, one folder per record:
  <catalogue>/fulltext/<year>/<uid slug>/
      metadata.json   the catalogue record + where every file came from
      paper.pdf       when an open PDF exists
      paper.md        Markdown (from JATS when available, else from the PDF)
      paper.txt       plain text
      paper.jats.xml  source XML when available
  <catalogue>/fulltext/fulltext_index.csv   one row per record tried

Re-running skips records already complete and retries records with nothing
after --retry-days days (access changes: embargoes lift, preprints get PMC
copies).

Usage
  litsearch __step fulltext \
      [--tiers landmark,core] [--limit 500] [--workers 6] [--no-pdf]
"""

from __future__ import annotations

import argparse
import concurrent.futures as cf
import datetime as dt
import hashlib
import html
import json
import re
import shutil
import subprocess
import threading
import time
import traceback
from pathlib import Path
from urllib.parse import quote

import requests
from lxml import etree

from litsearch import __version__
from litsearch.common.agelit import (  # noqa: E402
    CONTACT,
    FULLTEXT,
    RESEARCH,
    Http,
    log,
    read_csv,
    research_rel,
    search_window,
    search_window_overridden,
    slug,
    today,
    write_csv,
    write_json,
)
from litsearch.common.store import Store  # noqa: E402
from litsearch.pullliturature import jats  # noqa: E402

S3 = "https://pmc-oa-opendata.s3.amazonaws.com"
EPMC = "https://www.ebi.ac.uk/europepmc/webservices/rest"
UNPAYWALL = "https://api.unpaywall.org/v2/"
BROWSER_UA = f"Mozilla/5.0 (X11; Linux x86_64) litsearch/{__version__}" + (f" (mailto:{CONTACT})" if CONTACT else "")
INDEX = FULLTEXT / "fulltext_index.csv"
INDEX_COLUMNS = ["uid", "status", "source", "license", "has_pdf", "has_md", "has_txt",
                 "has_xml", "folder", "pdf_url", "checked_on", "note"]
ARTICLE_TYPES = {"article", "review", "preprint", "book chapter", "conference paper", "thesis", "report"}
MAX_PDF = 150_000_000

_local = threading.local()
_host_lock = threading.Lock()
_host_next: dict[str, float] = {}
# seconds between requests to one host, shared by all worker threads
HOST_INTERVAL = {"pmc-oa-opendata.s3.amazonaws.com": 0.02, "api.unpaywall.org": 0.1,
                 "api.biorxiv.org": 0.5, "www.ebi.ac.uk": 0.25,
                 # bioRxiv / medRxiv answer 429 to anything faster
                 "www.biorxiv.org": 6.0, "www.medrxiv.org": 6.0, "arxiv.org": 3.0}
# bioRxiv / medRxiv answer bursts with 429 and Retry-After of several minutes.
# Rather than park worker threads on that, the host is marked blocked and its
# records are recorded as "deferred", which the next run retries.
PREPRINT_HOSTS = {"www.biorxiv.org", "www.medrxiv.org"}
_blocked: dict[str, float] = {}
DEFAULT_INTERVAL = 1.0


def _shared_wait(url: str) -> None:
    host = url.split("/")[2].lower()
    gap = HOST_INTERVAL.get(host, DEFAULT_INTERVAL)
    with _host_lock:
        now = time.time()
        slot = max(now, _host_next.get(host, 0.0))
        _host_next[host] = slot + gap
    if slot > now:
        time.sleep(slot - now)


def http() -> Http:
    if not hasattr(_local, "h"):
        _local.h = Http(headers={"User-Agent": BROWSER_UA})
        _local.h._wait = _shared_wait
    return _local.h


def sha256(b: bytes) -> str:
    return hashlib.sha256(b).hexdigest()


# --------------------------------------------------------------------------
# sources
# --------------------------------------------------------------------------

def pmc_s3(pmcid: str) -> dict | None:
    """Latest version's JSON record in the PMC open-data bucket.

    None when the article is not in the open-access subset; {"_error": ...}
    when the bucket could not be asked.
    """
    r = http().get(S3 + "/", params={"list-type": "2", "prefix": pmcid + ".", "max-keys": "100"})
    if r is None or r.status_code != 200:
        return {"_error": f"listing failed ({getattr(r, 'status_code', 'network')})"}
    keys = re.findall(r"<Key>([^<]+)</Key>", r.text)
    vers = sorted({int(m.group(1)) for k in keys
                   if (m := re.match(rf"{pmcid}\.(\d+)/{pmcid}\.\d+\.json$", k))})
    if not vers:
        return None
    v = vers[-1]
    j = http().get(f"{S3}/{pmcid}.{v}/{pmcid}.{v}.json")
    if j is None or j.status_code != 200:
        return None
    meta = j.json()
    meta["_version"] = v
    return meta


def s3_url(s3uri: str | None) -> str | None:
    if not s3uri:
        return None
    path = s3uri.replace("s3://pmc-oa-opendata/", "").split("?", 1)[0]
    return f"{S3}/{path}"


def get_bytes(url: str, want_pdf: bool = False) -> bytes | None:
    host = url.split("/")[2].lower()
    if host in PREPRINT_HOSTS:
        if time.time() < _blocked.get(host, 0.0):
            _local.deferred = True
            return None
        _shared_wait(url)
        try:
            r = http().s.get(url, timeout=180)
        except requests.RequestException:
            return None
        if r.status_code == 429:
            ra = r.headers.get("Retry-After")
            _blocked[host] = time.time() + (float(ra) if ra and ra.isdigit() else 300.0)
            _local.deferred = True
            log(f"{host} rate-limited; deferring its downloads for {ra or 300}s")
            return None
    elif host == "pmc-oa-opendata.s3.amazonaws.com":
        r = http().get(url, timeout=180, tries=3)
    else:
        # publisher / repository hosts: dead or slow links are common, so fail
        # fast; the record is retried on a later run
        r = http().get(url, timeout=60, tries=2)
    if r is None or r.status_code != 200:
        return None
    b = r.content
    if want_pdf and (not b.startswith(b"%PDF") or len(b) > MAX_PDF):
        return None
    return b


def epmc_xml(ext_id: str) -> bytes | None:
    r = http().get(f"{EPMC}/{ext_id}/fullTextXML", tries=2)
    if r is None or r.status_code != 200 or b"<body" not in r.content:
        return None
    return r.content


def biorxiv(doi: str) -> dict | None:
    """Latest version of a bioRxiv / medRxiv preprint from their API."""
    for server in ("biorxiv", "medrxiv"):
        r = http().get(f"https://api.biorxiv.org/details/{server}/{doi}", tries=3)
        if r is None or r.status_code != 200:
            continue
        coll = (r.json() or {}).get("collection") or []
        if coll:
            v = max(coll, key=lambda c: int(c.get("version") or 0))
            v["_server"] = server
            return v
    return None


def unpaywall(doi: str) -> dict | None:
    r = http().get(UNPAYWALL + quote(doi, safe="/"), params={"email": CONTACT}, tries=3)
    if r is None or r.status_code != 200:
        return None
    return r.json()


def pdf_tools() -> str:
    """Which PDF converter is available."""
    try:
        import fitz  # noqa: F401  PyMuPDF
        import pymupdf4llm  # noqa: F401
        return "PyMuPDF (pymupdf4llm)"
    except ImportError:
        pass
    missing = [t for t in ("pdftotext", "pdftohtml") if not shutil.which(t)]
    if not missing:
        return "poppler (pdftotext, pdftohtml)"
    return "unavailable: install poppler-utils (" + ", ".join(missing) + " not found)"


_FONT = re.compile(r'<fontspec\b[^>]*\bid="([^"]*)"[^>]*\bsize="([^"]*)"')
_TEXT = re.compile(r'<text\b([^>]*)>(.*?)</text>', re.S)
_ATTR = re.compile(r'(\w+)="([^"]*)"')
_BOLD = re.compile(r"</?b>")
_HYPHEN = re.compile(r"(\w)- (\w)")


def poppler_text(path: Path) -> str:
    def run(*args) -> str:
        try:
            return subprocess.run(["pdftotext", *args, str(path), "-"], capture_output=True,
                                  timeout=300).stdout.decode("utf-8", "replace")
        except (OSError, subprocess.SubprocessError):
            return ""
    txt = run("-enc", "UTF-8")
    return txt if txt.strip() else run("-layout", "-enc", "UTF-8")


def poppler_markdown(path: Path) -> str:
    """Headings from font size, bold from <b>, paragraphs from vertical gaps (pdftohtml -xml)."""
    try:
        out = subprocess.run(["pdftohtml", "-xml", "-i", "-q", "-stdout", "-enc", "UTF-8", str(path)],
                             capture_output=True, timeout=300)
    except (OSError, subprocess.SubprocessError):
        return ""
    if out.returncode != 0:
        return ""
    xml_text = out.stdout.decode("utf-8", "replace")
    pages = []
    for chunk in re.split(r"<page\b", xml_text)[1:]:
        fonts = {m.group(1): float(m.group(2) or 0) for m in _FONT.finditer(chunk)}
        texts = []
        for m in _TEXT.finditer(chunk):
            a = dict(_ATTR.findall(m.group(1)))
            texts.append((float(a.get("top") or 0), float(a.get("height") or 0), a.get("font", ""), m.group(2)))
        pages.append((fonts, texts))
    sizes, weight = {}, {}
    for fonts, texts in pages:
        sizes.update(fonts)
        for _, _, font, inner in texts:
            sz = sizes.get(font, 0.0)
            weight[sz] = weight.get(sz, 0) + len(inner.encode("utf-8"))
    body, bw = 0.0, -1
    for s, w in weight.items():
        if w > bw or (w == bw and s < body):
            body, bw = s, w
    lines: list[str] = []
    para: list[str] = []

    def flush():
        if para:
            lines.extend([_HYPHEN.sub(r"\1\2", " ".join(para)), ""])
            para.clear()

    for _fonts, texts in pages:
        last_top, last_h = -1.0, 0.0
        for top, height, font, raw in texts:
            is_bold = "<b>" in raw
            txt = html.unescape(re.sub(r"<[^>]+>", "", _BOLD.sub("", raw))).strip()
            if not txt:
                continue
            size = sizes.get(font, 0.0)
            if body > 0 and size >= body * 1.6:
                flush()
                lines.extend(["# " + txt, ""])
            elif body > 0 and size >= body * 1.2:
                flush()
                lines.extend(["## " + txt, ""])
            else:
                if last_top >= 0 and (top - last_top > 1.8 * max(last_h, 1) or top < last_top):
                    flush()
                if is_bold and not para and len(txt) < 120:
                    txt = "**" + txt + "**"
                para.append(txt)
            last_top, last_h = top, height
        flush()
    return re.sub(r"\n{3,}", "\n\n", "\n".join(lines)).strip() + "\n"


def pdf_to_text(path: Path) -> tuple[str, str]:
    """(Markdown, text) of a PDF: PyMuPDF when installed (pip install litsearch[pdf]), else poppler."""
    try:
        import fitz  # PyMuPDF
        import pymupdf4llm
    except ImportError:
        return poppler_markdown(path), poppler_text(path)
    try:
        with fitz.open(path) as doc:
            txt = "\n\n".join(page.get_text() for page in doc)
    except Exception:  # malformed PDFs (bad font encodings) can break PyMuPDF
        txt = ""
    if not txt.strip():
        try:  # poppler as a second opinion
            txt = subprocess.run(["pdftotext", "-layout", str(path), "-"], capture_output=True,
                                 timeout=300).stdout.decode("utf-8", "replace")
        except (OSError, subprocess.SubprocessError):
            txt = ""
    try:
        md = pymupdf4llm.to_markdown(str(path), show_progress=False)
    except Exception:  # pymupdf4llm fails on some malformed PDFs; text still stands
        md = ""
    return md, txt


# --------------------------------------------------------------------------
# one record
# --------------------------------------------------------------------------

FORMAT_FILES = {"pdf": "paper.pdf", "md": "paper.md", "txt": "paper.txt", "xml": "paper.jats.xml"}
ALL_FORMATS = set(FORMAT_FILES)
ALL_SOURCES = {"pmc", "biorxiv", "europepmc", "arxiv", "openalex", "unpaywall"}


def complete(present: set[str], formats: set[str]) -> bool:
    """A record is complete when the requested text formats are there.

    With the default (all formats) that means md + txt, as before: PDF and XML
    are kept when a source offers them but are not required. When only pdf or
    xml is requested, that format itself is required.
    """
    core = formats & {"md", "txt"}
    return core <= present if core else formats <= present


def index_row(uid: str, folder: Path, ft: dict) -> dict:
    files = ft.get("files", {})
    on_disk = {k: (folder / f).exists() for k, f in FORMAT_FILES.items()}
    return {"uid": uid, "status": ft.get("status", ""), "source": ft.get("source", ""),
            "license": ft.get("license", ""), "has_pdf": on_disk["pdf"], "has_md": on_disk["md"],
            "has_txt": on_disk["txt"], "has_xml": on_disk["xml"],
            "folder": research_rel(folder),
            "pdf_url": files.get("paper.pdf", {}).get("url", ""),
            "checked_on": (ft.get("fetched_on") or today())[:10], "note": ""}


def folder_for(rec: dict, row: dict) -> Path:
    year = (rec.get("pub_date") or row.get("date") or "0000")[:4] or "0000"
    return FULLTEXT / year / slug(rec["uid"])


def from_disk(rec: dict, row: dict, formats: set[str] = ALL_FORMATS) -> dict | None:
    """Index row from an earlier complete download, so a lost index costs nothing."""
    meta = folder_for(rec, row) / "metadata.json"
    if not meta.exists():
        return None
    try:
        ft = json.loads(meta.read_text(encoding="utf-8")).get("fulltext", {})
    except ValueError:
        return None
    files = ft.get("files", {})
    present = {k for k, f in FORMAT_FILES.items() if (meta.parent / f).exists()}
    if ft.get("status") != "ok" or not all((meta.parent / f).exists() for f in files) \
            or not complete(present, formats):
        return None
    return index_row(rec["uid"], meta.parent, ft)


def fetch_one(rec: dict, row: dict, want_pdf: bool = True, formats: set[str] = ALL_FORMATS,
              sources: set[str] = ALL_SOURCES) -> dict:
    uid = rec["uid"]
    _local.deferred = False
    folder = folder_for(rec, row)
    folder.mkdir(parents=True, exist_ok=True)
    files: dict[str, dict] = {}
    prov = {"sources_tried": [], "formats": sorted(formats), "sources": sorted(sources)}
    lic = ""
    source = ""
    xml = None
    keep = {FORMAT_FILES[f] for f in formats}
    # a PDF is fetched when asked for, or as the fallback route to md / txt
    want_pdf = want_pdf and bool(formats & {"pdf", "md", "txt"})
    pdf_tmp = folder / ".paper.tmp.pdf"

    def save(name: str, data: bytes | str, url: str = "", via: str = "") -> None:
        b = data.encode("utf-8") if isinstance(data, str) else data
        if name == "paper.pdf" and name not in keep:
            pdf_tmp.write_bytes(b)  # only to derive md / txt; removed below
            files["_pdf_tmp"] = {"url": url, "via": via}
            return
        if name not in keep:
            return
        (folder / name).write_bytes(b)
        files[name] = {"bytes": len(b), "sha256": sha256(b), "url": url, "via": via}

    def have_pdf() -> bool:
        return "paper.pdf" in files or "_pdf_tmp" in files

    pmcid = rec.get("pmcid") or ""
    s3_error = False
    if pmcid and "pmc" in sources:
        prov["sources_tried"].append("pmc_s3")
        m = pmc_s3(pmcid)
        s3_error = bool(m and m.get("_error"))
        if m and not s3_error:
            prov["pmc_s3"] = {k: m.get(k) for k in ("pmcid", "_version", "license_code", "is_pmc_openaccess",
                                                   "is_manuscript", "is_retracted", "citation")}
            lic = m.get("license_code") or lic
            source = "pmc_s3"
            u = s3_url(m.get("xml_url"))
            if u and (b := get_bytes(u)):
                xml = b
                save("paper.jats.xml", b, u, "pmc_s3")
            u = s3_url(m.get("text_url"))
            if u and (b := get_bytes(u)):
                save("paper.txt", b, u, "pmc_s3")
            u = s3_url(m.get("pdf_url"))
            if want_pdf and u and (b := get_bytes(u, want_pdf=True)):
                save("paper.pdf", b, u, "pmc_s3")

    doi = rec.get("doi") or ""
    if "biorxiv" in sources and doi.startswith("10.1101/") and (xml is None or (want_pdf and not have_pdf())):
        prov["sources_tried"].append("biorxiv")
        v = biorxiv(doi)
        if v:
            server = v["_server"]
            prov["biorxiv"] = {k: v.get(k) for k in ("server", "version", "date", "license", "published")}
            lic = lic or v.get("license", "")
            if xml is None and v.get("jatsxml") and (b := get_bytes(v["jatsxml"])) and b"<article" in b[:5000]:
                xml = b
                source = source or server
                save("paper.jats.xml", b, v["jatsxml"], server)
            pdf = f"https://www.{server}.org/content/{doi}v{v.get('version', 1)}.full.pdf"
            if want_pdf and not have_pdf() and (b := get_bytes(pdf, want_pdf=True)):
                save("paper.pdf", b, pdf, server)
                source = source or server

    if xml is None and "europepmc" in sources:
        # Europe PMC serves the same open-access subset as the PMC bucket, so a
        # PMC article is only asked for there when the bucket could not be read
        ppr = next((e.split("/", 1)[1] for e in rec.get("epmc_ids") or [] if e.startswith("PPR/")), "")
        ext = ppr or (pmcid if pmcid and (s3_error or "pmc" not in sources) else "")
        if ext:
            prov["sources_tried"].append("europepmc_xml")
            b = epmc_xml(ext)
            if b:
                xml = b
                source = source or "europepmc"
                save("paper.jats.xml", b, f"{EPMC}/{ext}/fullTextXML", "europepmc")

    if "arxiv" in sources and want_pdf and not have_pdf() and doi.startswith("10.48550/arxiv."):
        prov["sources_tried"].append("arxiv")
        aid = doi.split("arxiv.", 1)[1]
        u = f"https://arxiv.org/pdf/{aid}"
        if b := get_bytes(u, want_pdf=True):
            save("paper.pdf", b, u, "arxiv")
            lic = lic or "arXiv (see abstract page for licence)"
            source = source or "arxiv"

    # Unpaywall needs a contact address (litsearch config set contact_email ...)
    if "unpaywall" in sources and CONTACT and want_pdf and not have_pdf() and rec.get("doi"):
        prov["sources_tried"].append("unpaywall")
        up = unpaywall(rec["doi"])
        if up:
            prov["unpaywall"] = {"is_oa": up.get("is_oa"), "oa_status": up.get("oa_status"),
                                 "best_oa_url": (up.get("best_oa_location") or {}).get("url")}
            locs = [up.get("best_oa_location") or {}] + (up.get("oa_locations") or [])
            seen = set()
            for loc in locs:
                u = loc.get("url_for_pdf")
                if not u or u in seen:
                    continue
                seen.add(u)
                b = get_bytes(u, want_pdf=True)
                if b:
                    save("paper.pdf", b, u, f"unpaywall:{loc.get('host_type', '')}")
                    lic = lic or loc.get("license") or ""
                    source = source or "unpaywall"
                    break

    # OpenAlex's open-access PDF link (works from any field; no contact address needed)
    if "openalex" in sources and want_pdf and not have_pdf() and rec.get("oa_pdf_url"):
        prov["sources_tried"].append("openalex")
        b = get_bytes(rec["oa_pdf_url"], want_pdf=True)
        if b:
            save("paper.pdf", b, rec["oa_pdf_url"], "openalex")
            lic = lic or rec.get("license") or ""
            source = source or "openalex"

    if xml is not None and keep & {"paper.md", "paper.txt"}:
        try:
            md, fm = jats.convert(xml)
            if len(md) > 1500:
                save("paper.md", md, via="jats->md")
                if "paper.txt" not in files:
                    save("paper.txt", jats.md_to_text(md), via="jats->txt")
                lic = lic or fm.get("license", "")
        except etree.XMLSyntaxError as exc:
            prov["xml_error"] = str(exc)
    need_text = {"paper.md", "paper.txt"} & keep - files.keys()
    if have_pdf() and need_text:
        md, txt = pdf_to_text(folder / "paper.pdf" if "paper.pdf" in files else pdf_tmp)
        how = "poppler" if pdf_tools().startswith("poppler") else "pymupdf"
        if "paper.md" in need_text and md.strip():
            save("paper.md", md, via=f"pdf->md ({how})")
        if "paper.txt" in need_text and txt.strip():
            save("paper.txt", txt, via=f"pdf->txt ({how})")
    files.pop("_pdf_tmp", None)
    pdf_tmp.unlink(missing_ok=True)

    # files from earlier runs stay listed while they are still on disk
    old_meta = folder / "metadata.json"
    if old_meta.exists():
        try:
            old_files = json.loads(old_meta.read_text(encoding="utf-8")).get("fulltext", {}).get("files", {})
            files = {**{k: v for k, v in old_files.items() if (folder / k).exists()}, **files}
        except ValueError:
            pass
    present = {k for k, f in FORMAT_FILES.items() if f in files}
    status = "ok" if complete(present, formats) else ("partial" if files else "none")
    if status != "ok" and getattr(_local, "deferred", False):
        status = "deferred"  # a source was rate-limited; retried on the next run
    # metadata.json is written even when nothing was retrievable, so every
    # record tried has an inspectable file saying what was attempted
    meta = {"uid": uid, "record": rec, "catalogue_row": {k: row.get(k, "") for k in
                                                         ("tier", "category", "modality", "title", "date")},
            "fulltext": {"status": status, "source": source, "license": lic, "files": files,
                         "fetched_on": dt.datetime.now().isoformat(timespec="seconds"), **prov}}
    write_json(folder / "metadata.json", meta)
    return index_row(uid, folder, meta["fulltext"])


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--tiers", default="landmark,core,related")
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--workers", type=int, default=6)
    ap.add_argument("--no-pdf", action="store_true")
    ap.add_argument("--retry-days", type=int, default=30)
    ap.add_argument("--force", action="store_true")
    ap.add_argument("--uids", help="comma-separated uids to (re)fetch")
    ap.add_argument("--formats", default="pdf,md,txt,xml",
                    help="files to keep: any of pdf, md, txt, xml (default all)")
    ap.add_argument("--sources", default=",".join(sorted(ALL_SOURCES)),
                    help="full-text sources to use: pmc, biorxiv, europepmc, arxiv, openalex, unpaywall (default all)")
    ap.add_argument("--types", dest="rtypes", default=",".join(sorted(ARTICLE_TYPES)),
                    help="catalogue resource types to download for (default: all paper types)")
    ap.add_argument("--only-status", help="re-try only records whose last status is one of these, "
                                          "e.g. deferred,partial,error")
    ap.add_argument("--from", dest="date_from", help="only records published on/after this date")
    ap.add_argument("--to", dest="date_to", help="only records published on/before this date")
    args = ap.parse_args()
    formats = {f.strip().lower() for f in args.formats.split(",") if f.strip()}
    sources = {x.strip().lower() for x in args.sources.split(",") if x.strip()}
    if not formats <= ALL_FORMATS or not sources <= ALL_SOURCES:
        ap.error(f"formats must be within {sorted(ALL_FORMATS)}, sources within {sorted(ALL_SOURCES)}")
    if "unpaywall" in sources and not CONTACT:
        log("Unpaywall skipped: no contact email (litsearch config set contact_email you@example.org)")
    if pdf_tools().startswith("unavailable"):
        log(f"PDF to text/Markdown {pdf_tools()}; PDFs are kept")
    date_from, date_to = args.date_from, args.date_to
    if not (date_from or date_to) and search_window_overridden():
        date_from, date_to = search_window()  # range given on the CLI

    rows = read_csv(RESEARCH / "progress.csv")
    tiers = args.tiers.split(",")
    order = {t: i for i, t in enumerate(["landmark", "core", "related"])}
    rtypes = {t.strip() for t in args.rtypes.split(",") if t.strip()} & ARTICLE_TYPES
    rows = [r for r in rows if r.get("resource_type") in rtypes and r.get("tier") in tiers]
    if args.uids:
        want = set(args.uids.split(","))
        rows = [r for r in rows if r["uid"] in want]
    if date_from or date_to:
        lo, hi = date_from or "0000", date_to or "9999"
        rows = [r for r in rows if lo <= (r.get("date") or "0000") <= hi]
    rows.sort(key=lambda r: (order.get(r["tier"], 9), -int(r.get("cited_by") or 0)))

    index = {r["uid"]: r for r in read_csv(INDEX)}
    if args.only_status:
        wanted = {x.strip() for x in args.only_status.split(",")}
        rows = [r for r in rows if index.get(r["uid"], {}).get("status") in wanted]
        args.force = True  # these are re-tries by definition
    cutoff = (dt.date.today() - dt.timedelta(days=args.retry_days)).isoformat()
    todo = []
    for r in rows:
        prev = index.get(r["uid"])
        if args.force or args.uids or prev is None:
            todo.append(r)
        elif prev["status"] in ("partial", "deferred", "error") or (prev["status"] == "none" and prev["checked_on"] < cutoff):
            todo.append(r)
        elif prev["status"] == "ok" and prev.get("checked_on", "") < cutoff:
            # complete for the formats asked earlier; ask again only if a format
            # requested now is missing and the last attempt is old enough
            have = {f for f in ALL_FORMATS if str(prev.get(f"has_{f}")) == "True"}
            if not complete(have, formats) or (formats - have and formats != ALL_FORMATS):
                todo.append(r)
    if args.limit:
        todo = todo[:args.limit]
    store = Store()
    log(f"{len(rows)} article rows in tiers {tiers}; {len(todo)} to fetch; "
        f"formats {sorted(formats)}; sources {sorted(sources)}"
        + (f"; dates {date_from or '…'} .. {date_to or '…'}" if (date_from or date_to) else ""))

    done = 0
    with cf.ThreadPoolExecutor(max_workers=args.workers) as ex:
        futs = {}
        resumed = 0
        for r in todo:
            rec = store.get(r["uid"])
            if rec is None:
                continue
            if not args.force and (prev := from_disk(rec, r, formats)):
                index[r["uid"]] = prev
                resumed += 1
                continue
            futs[ex.submit(fetch_one, rec, r, not args.no_pdf, formats, sources)] = r["uid"]
        log(f"{resumed} already complete on disk; {len(futs)} to download")
        for fut in cf.as_completed(futs):
            uid = futs[fut]
            try:
                index[uid] = fut.result()
            except Exception as exc:  # one bad record must not stop the batch
                index[uid] = {"uid": uid, "status": "error", "checked_on": today(),
                              "note": f"{exc.__class__.__name__}: {exc}"[:300]}
                log(f"error on {uid}: {exc}\n{traceback.format_exc(limit=8)}")
            done += 1
            if done % 25 == 0:
                write_csv(INDEX, sorted(index.values(), key=lambda x: x["uid"]), INDEX_COLUMNS)
                st = {}
                for v in index.values():
                    st[v["status"]] = st.get(v["status"], 0) + 1
                log(f"  {done}/{len(futs)} {st}")
    write_csv(INDEX, sorted(index.values(), key=lambda x: x["uid"]), INDEX_COLUMNS)
    st = {}
    for v in index.values():
        st[v["status"]] = st.get(v["status"], 0) + 1
    log(f"done: {st}")


if __name__ == "__main__":
    main()
