"""Attachments of a paper: figures, tables and supplementary files.

Open-access articles in the PMC bucket (s3://pmc-oa-opendata) come with every
file of the article package: figure images and supplementary material such as
spreadsheets, slides, documents and archives. The bucket listing names them
and gives their sizes; the article's JATS XML gives each its label and caption.

Downloaded files go to <paper folder>/attachments/<file>. Every file offered,
downloaded or not, is one row of <catalogue>/fulltext/attachments_index.csv:

  status   listed (available, not downloaded), ok (downloaded), skipped (too
           large), failed (download failed), moved, deleted (by sourcelens files),
           none (the paper has no attachments: a row without a file)

The Go twin is cmd/sourcelens/attachments.go.
"""

from __future__ import annotations

import collections
import re
from pathlib import Path

from lxml import etree

from sourcelens.common import agelit

COLUMNS = ["uid", "file", "ext", "kind", "label", "caption", "bytes", "status", "path", "url",
           "checked_on", "reason"]
IMAGE_EXTS = {"bmp", "eps", "gif", "jpeg", "jpg", "png", "svg", "tif", "tiff", "webp"}
TARGETS = {"fig": "figure", "table-wrap": "table", "supplementary-material": "supplementary"}
HREF_TAGS = {"graphic", "media", "inline-graphic", "inline-supplementary-material", "supplementary-material"}
XLINK = "{http://www.w3.org/1999/xlink}href"
CAPTION_MAX = 300


def index_path() -> Path:
    return agelit.FULLTEXT / "attachments_index.csv"


def read_index() -> dict[str, list[dict]]:
    """uid -> its attachment rows, in file order."""
    out: dict[str, list[dict]] = collections.defaultdict(list)
    for r in agelit.read_csv(index_path()):
        out[r["uid"]].append(r)
    return out


def write_index(rows_by_uid: dict[str, list[dict]]) -> None:
    rows = [r for uid in sorted(rows_by_uid) for r in rows_by_uid[uid]]
    agelit.write_csv(index_path(), rows, COLUMNS)


def file_ext(name: str) -> str:
    base = name.rsplit("/", 1)[-1]
    return base.rsplit(".", 1)[-1].lower() if "." in base else ""


def stem(name: str) -> str:
    base = name.rsplit("/", 1)[-1]
    return (base.rsplit(".", 1)[0] if "." in base else base).lower()


def _text(el) -> str:
    return " ".join(" ".join(el.itertext()).split())


def captions(xml: bytes | None) -> dict[str, tuple[str, str, str]]:
    """File reference (lower case) -> (kind, label, caption) from the article XML.

    <fig>, <table-wrap> and <supplementary-material> name their files with
    xlink:href on themselves or on <graphic> / <media> inside, with or without
    the extension. Each reference is keyed as written and without its last
    extension; the first element (document order) naming a key wins.
    """
    if not xml:
        return {}
    parser = etree.XMLParser(recover=True, huge_tree=True, resolve_entities=False, no_network=True)
    try:
        root = etree.fromstring(xml, parser)
    except etree.XMLSyntaxError:
        return {}
    if root is None:
        return {}
    out: dict[str, tuple[str, str, str]] = {}
    for el in root.iter():
        if not isinstance(el.tag, str):
            continue
        tag = etree.QName(el).localname
        if tag not in TARGETS:
            continue
        label_el = next((k for k in el if isinstance(k.tag, str) and etree.QName(k).localname == "label"), None)
        cap_el = next((k for k in el if isinstance(k.tag, str) and etree.QName(k).localname == "caption"), None)
        label = _text(label_el) if label_el is not None else ""
        caption = _text(cap_el)[:CAPTION_MAX] if cap_el is not None else ""
        for d in el.iter():
            if not isinstance(d.tag, str) or etree.QName(d).localname not in HREF_TAGS:
                continue
            href = d.get(XLINK) or ""
            if not href:
                continue
            for key in (href.rsplit("/", 1)[-1].lower(), stem(href)):
                if key not in out:
                    out[key] = (TARGETS[tag], label, caption)
    return out


def describe(name: str, caps: dict) -> tuple[str, str, str]:
    """(kind, label, caption) of a file: from the XML, else figure / supplementary by extension."""
    for key in (name.rsplit("/", 1)[-1].lower(), stem(name)):
        if key in caps:
            return caps[key]
    return ("figure" if file_ext(name) in IMAGE_EXTS else "supplementary"), "", ""


def summary(rows: list[dict]) -> str:
    """The attachments column: "downloaded/available: ext count, ..." ("" when unknown)."""
    if not rows:
        return ""
    files = [r for r in rows if r.get("file")]
    ok = sum(1 for r in files if r.get("status") == "ok")
    exts = collections.Counter(r.get("ext") or "?" for r in files)
    head = f"{ok}/{len(files)}"
    return head + (": " + ", ".join(f"{e} {n}" for e, n in sorted(exts.items())) if exts else "")


def summary_exts(value: str) -> set[str]:
    """Extensions named in an attachments column value ("2/5: jpg 2, pdf 2, xlsx 1")."""
    if ":" not in value:
        return set()
    return {p.strip().split(" ")[0].lower() for p in value.split(":", 1)[1].split(",") if p.strip()}


def human_bytes(n) -> str:
    try:
        n = int(n)
    except (TypeError, ValueError):
        return ""
    for unit in ("B", "KB", "MB", "GB"):
        if n < 1024 or unit == "GB":
            return f"{n:.0f} {unit}" if unit == "B" else f"{n:.1f} {unit}"
        n /= 1024
    return ""


_SAFE = re.compile(r"[^A-Za-z0-9._-]+")


def safe_name(name: str) -> str:
    """A file name from the bucket, safe to write (no folders, no odd characters)."""
    base = name.rsplit("/", 1)[-1]
    return _SAFE.sub("_", base).strip("._") or "file"
