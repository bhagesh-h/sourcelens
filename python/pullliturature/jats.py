"""JATS XML (PMC / Europe PMC / bioRxiv) to Markdown and plain text.

Keeps what a reader or a text-mining step needs: front matter, abstract,
section structure, paragraphs, lists, figure and table captions, simple tables
as pipe tables, data-availability and acknowledgement sections, and the
reference list with DOIs. Math is kept as its text content.
"""

from __future__ import annotations

import re

from lxml import etree

BLOCK = {"p", "sec", "list", "fig", "table-wrap", "disp-formula", "boxed-text",
         "def-list", "disp-quote", "statement", "supplementary-material",
         "ref-list", "fn-group", "ack", "app", "app-group", "glossary"}


def _ln(el) -> str:
    t = el.tag
    return t.split("}", 1)[-1] if isinstance(t, str) else ""


def _inline(el) -> str:
    """Text of an element with light Markdown for emphasis and links."""
    parts = [el.text or ""]
    for ch in el:
        name = _ln(ch)
        inner = _inline(ch)
        if name in ("bold",):
            parts.append(f"**{inner.strip()}**" if inner.strip() else "")
        elif name in ("italic",):
            parts.append(f"*{inner.strip()}*" if inner.strip() else "")
        elif name in ("sup",):
            parts.append(f"^{inner}^" if inner else "")
        elif name in ("sub",):
            parts.append(f"~{inner}~" if inner else "")
        elif name == "ext-link":
            href = ch.get("{http://www.w3.org/1999/xlink}href") or ""
            txt = inner.strip() or href
            parts.append(f"[{txt}]({href})" if href and href != txt else txt)
        elif name in ("uri",):
            href = ch.get("{http://www.w3.org/1999/xlink}href") or inner.strip()
            parts.append(href)
        elif name in ("xref",):
            parts.append(inner)
        elif name in ("inline-formula", "mml:math", "math", "tex-math"):
            parts.append(" " + " ".join(inner.split()) + " ")
        elif name in ("break",):
            parts.append(" ")
        elif name in BLOCK:
            parts.append(" " + inner + " ")
        else:
            parts.append(inner)
        parts.append(ch.tail or "")
    return "".join(parts)


def _clean(s: str) -> str:
    return re.sub(r"[ \t\r\f\v]+", " ", re.sub(r"\s*\n\s*", " ", s)).strip()


def _table(tw) -> list[str]:
    out = []
    label = _clean(_inline(tw.find("label"))) if tw.find("label") is not None else ""
    cap = tw.find("caption")
    captxt = _clean(" ".join(_inline(c) for c in cap)) if cap is not None else ""
    out.append(f"**{label}** {captxt}".strip() if label or captxt else "")
    tbl = tw.find(".//table")
    if tbl is not None:
        rows = []
        for tr in tbl.iter("tr"):
            cells = [_clean(_inline(c)).replace("|", "\\|") for c in tr if _ln(c) in ("td", "th")]
            if cells:
                rows.append(cells)
        if rows:
            w = max(len(r) for r in rows)
            rows = [r + [""] * (w - len(r)) for r in rows]
            out.append("")
            out.append("| " + " | ".join(rows[0]) + " |")
            out.append("|" + "---|" * w)
            for r in rows[1:]:
                out.append("| " + " | ".join(r) + " |")
    for fn in tw.findall(".//table-wrap-foot//p"):
        out.append("")
        out.append("_" + _clean(_inline(fn)) + "_")
    return [x for x in out if x is not None]


def _block(el, depth: int, lines: list[str]) -> None:
    name = _ln(el)
    if name == "sec" or name in ("ack", "app", "glossary", "boxed-text"):
        title = el.find("title")
        label = el.find("label")
        head = " ".join(_clean(_inline(x)) for x in (label, title) if x is not None).strip()
        if not head and name == "ack":
            head = "Acknowledgements"
        if head:
            lines += ["", "#" * min(depth + 1, 6) + " " + head, ""]
        for ch in el:
            if _ln(ch) not in ("title", "label"):
                _block(ch, depth + 1, lines)
    elif name == "p":
        txt = _clean(_inline(el))
        if txt:
            lines += [txt, ""]
    elif name == "list":
        ordered = el.get("list-type") in ("order", "number", "arabic", "alpha-lower", "roman-lower")
        for i, it in enumerate(el.findall("list-item"), 1):
            txt = _clean(" ".join(_inline(x) for x in it))
            if txt:
                lines.append(f"{i}. {txt}" if ordered else f"- {txt}")
        lines.append("")
    elif name == "fig":
        label = _clean(_inline(el.find("label"))) if el.find("label") is not None else "Figure"
        cap = el.find("caption")
        captxt = _clean(" ".join(_inline(c) for c in cap)) if cap is not None else ""
        lines += [f"**{label}.** {captxt}".strip(), ""]
    elif name == "table-wrap":
        lines += _table(el) + [""]
    elif name == "disp-formula":
        lines += ["", "    " + _clean(_inline(el)), ""]
    elif name == "supplementary-material":
        label = _clean(_inline(el.find("label"))) if el.find("label") is not None else "Supplementary material"
        cap = el.find("caption")
        captxt = _clean(" ".join(_inline(c) for c in cap)) if cap is not None else ""
        lines += [f"*{label}* {captxt}".strip(), ""]
    elif name in ("fn-group",):
        for fn in el.iter():
            if _ln(fn) == "p":
                t = _clean(_inline(fn))
                if t:
                    lines += [t, ""]
    elif name in ("title", "label", "object-id"):
        return
    else:
        # unknown container: descend, or keep its text if it has no block children
        if any(_ln(c) in BLOCK for c in el):
            for ch in el:
                _block(ch, depth, lines)
        else:
            t = _clean(_inline(el))
            if t:
                lines += [t, ""]


def _refs(back) -> list[str]:
    out = []
    for i, ref in enumerate(back.iter("{*}ref") if back is not None else [], 1):
        cit = None
        for tag in ("mixed-citation", "element-citation", "citation", "nlm-citation"):
            cit = ref.find(f".//{tag}")
            if cit is not None:
                break
        if cit is None:
            continue
        # A citation made only of tagged fields with no punctuation between
        # them (element-citation, and many publishers' mixed-citation) would
        # run together as text, so it is rebuilt field by field.
        has_glue = bool((cit.text or "").strip()) or any((c.tail or "").strip() for c in cit)
        if _ln(cit) == "element-citation" or not has_glue:
            names = [f"{n.findtext('surname') or ''} {n.findtext('given-names') or ''}".strip()
                     for n in cit.iter("name")]
            collab = [_clean(_inline(c)) for c in cit.iter("collab")]
            names = names + collab
            who = ", ".join(names[:6]) + (" et al." if len(names) > 6 else "")
            bits = [who]
            for tag in ("article-title", "chapter-title", "source", "year", "volume"):
                v = cit.find(tag)
                if v is not None:
                    bits.append(_clean(_inline(v)))
            fp, lp = cit.findtext("fpage"), cit.findtext("lpage")
            if fp:
                bits.append(f"{fp}-{lp}" if lp else fp)
            txt = ". ".join(b for b in bits if b)
            pmid = cit.find("pub-id[@pub-id-type='pmid']")
            if pmid is not None and pmid.text:
                txt += f" PMID:{pmid.text.strip()}"
        else:
            txt = _clean(_inline(cit))
        doi = cit.find("pub-id[@pub-id-type='doi']")
        if doi is not None and doi.text and doi.text not in txt:
            txt += f" doi:{doi.text.strip()}"
        label = ref.findtext("label") or str(i)
        out.append(f"{label}. {txt}")
    return out


def convert(xml: bytes) -> tuple[str, dict]:
    """Return (markdown, front-matter dict) for a JATS article."""
    parser = etree.XMLParser(recover=True, huge_tree=True, resolve_entities=False, no_network=True)
    root = etree.fromstring(xml, parser)
    # PMC OAI / Europe PMC wrappers put <article> inside other elements
    art = root if _ln(root) == "article" else next((e for e in root.iter() if _ln(e) == "article"), root)
    for e in art.iter():  # drop namespaces from tag names for simple lookups
        if isinstance(e.tag, str) and "}" in e.tag and not e.tag.endswith("}math"):
            e.tag = e.tag.split("}", 1)[1]
    front = art.find("front")
    meta = {}
    lines: list[str] = []
    am = front.find("article-meta") if front is not None else None
    if am is not None:
        t = am.find(".//article-title")
        meta["title"] = _clean(_inline(t)) if t is not None else ""
        authors = []
        for c in am.findall(".//contrib[@contrib-type='author']"):
            n = c.find(".//name")
            if n is not None:
                authors.append(f"{n.findtext('given-names') or ''} {n.findtext('surname') or ''}".strip())
            elif c.find("collab") is not None:
                authors.append(_clean(_inline(c.find("collab"))))
        meta["authors"] = authors
        for aid in am.findall("article-id"):
            meta[aid.get("pub-id-type", "id")] = (aid.text or "").strip()
        j = front.find(".//journal-title")
        meta["journal"] = _clean(_inline(j)) if j is not None else ""
        lic = am.find(".//license")
        if lic is not None:
            meta["license"] = lic.get("{http://www.w3.org/1999/xlink}href") or _clean(_inline(lic))[:300]
        kws = [_clean(_inline(k)) for k in am.findall(".//kwd")]
        meta["keywords"] = kws
    lines.append(f"# {meta.get('title', '')}".rstrip())
    lines.append("")
    if meta.get("authors"):
        lines += [", ".join(meta["authors"]), ""]
    bits = [meta.get("journal", "")]
    if meta.get("doi"):
        bits.append(f"doi:{meta['doi']}")
    if meta.get("pmcid") or meta.get("pmc"):
        bits.append(meta.get("pmcid") or "PMC" + meta.get("pmc", "").lstrip("PMC"))
    if meta.get("license"):
        bits.append(f"license: {meta['license']}")
    lines += [" | ".join(b for b in bits if b), ""]
    if am is not None:
        for ab in am.findall("abstract"):
            kind = ab.get("abstract-type")
            if kind in ("toc", "teaser"):
                continue
            head = "Abstract" if not kind else f"Abstract ({kind})"
            lines += [f"## {head}", ""]
            for ch in ab:
                _block(ch, 2, lines)
        if meta.get("keywords"):
            lines += ["**Keywords:** " + "; ".join(meta["keywords"]), ""]
    body = art.find("body")
    if body is not None:
        for ch in body:
            _block(ch, 1, lines)
    back = art.find("back")
    if back is not None:
        for ch in back:
            if _ln(ch) != "ref-list":
                _block(ch, 1, lines)
        refs = _refs(back)
        if refs:
            lines += ["", "## References", ""] + refs
    md = re.sub(r"\n{3,}", "\n\n", "\n".join(lines)).strip() + "\n"
    return md, meta


def md_to_text(md: str) -> str:
    """Plain text from the Markdown above: drop markup, keep structure as blank lines."""
    t = re.sub(r"^#+\s*", "", md, flags=re.M)
    t = re.sub(r"\[([^\]]*)\]\(([^)]*)\)", r"\1 (\2)", t)
    t = re.sub(r"\*\*|__|(?<!\w)\*(?!\s)|(?<!\s)\*(?!\w)|\^|~", "", t)
    t = re.sub(r"^\|?-{3,}.*$", "", t, flags=re.M)
    t = t.replace(" | ", "\t").replace("| ", "").replace(" |", "")
    return re.sub(r"\n{3,}", "\n\n", t).strip() + "\n"
