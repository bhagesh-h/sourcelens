#!/usr/bin/env python3
"""Render <catalogue>/summary/findings.md from <catalogue>/progress.csv.

Regenerated on every run so the numbers in it always match the catalogue:
growth per year, data layers per year, origin papers of registry entries, when
each named entity first appears in a catalogued title, the most-cited work
overall and per data layer, the most-starred repositories, websites, and
full-text coverage.

Usage (normally run by sourcelens update)
  sourcelens __step summary
"""

from __future__ import annotations

import collections
import re

from sourcelens.buildcatalog.build_progress import Classifier
from sourcelens.common.agelit import RESEARCH, config, config_root, log, read_csv, today
from sourcelens.common.settings import default_topic

OUT = RESEARCH / "summary" / "findings.md"
PAPERS = {"article", "review", "preprint", "report", "book chapter", "conference paper", "thesis", "dataset"}
REGISTRY = re.compile(r"([^;]*) registry: ([^;]*)")


def summary_modalities(classify: dict) -> list[str]:
    """classify.summary_modalities, else every modality rule, in order."""
    return list(classify.get("summary_modalities") or []) or list((classify.get("modality") or {}).keys())


def md_table(header: list[str], rows: list[list]) -> list[str]:
    out = ["| " + " | ".join(header) + " |", "|" + "---|" * len(header)]
    for r in rows:
        out.append("| " + " | ".join(str(x).replace("|", "/").replace("\n", " ") for x in r) + " |")
    return out + [""]


def cite(r: dict) -> str:
    link = f"[{r['doi']}](https://doi.org/{r['doi']})" if r.get("doi") else r.get("url", "")
    return f"{r['title'][:120]} ({r['authors'].split(',')[0] if r.get('authors') else ''}, {r['year']}) {link}"


def main() -> None:
    rows = read_csv(RESEARCH / "progress.csv")
    papers = [r for r in rows if r["resource_type"] in PAPERS]
    repos = [r for r in rows if r["resource_type"] in ("repository", "software package")]
    sites = [r for r in rows if r["resource_type"] in ("website", "database", "web calculator")]
    years = sorted({r["year"] for r in papers if r["year"]})
    classify = config("classify")
    modalities = summary_modalities(classify)
    clf = Classifier(classify)
    topic = config_root().get("topic") or default_topic()
    L = [f"# Findings: {topic}", "",
         f"_Generated {today()} by `sourcelens` (summary step) from `progress.csv` "
         f"({len(rows)} rows: {len(papers)} papers/preprints/reviews, {len(repos)} repositories and packages, "
         f"{len(sites)} websites and databases). Classification is rule-based (classify section of the configuration); "
         f"treat counts as indicative._", ""]

    # growth
    L += ["## Publications per year", ""]
    by = collections.Counter((r["year"], r["resource_type"]) for r in papers)
    tiers = collections.Counter((r["year"], r["tier"]) for r in papers)
    types = ["article", "review", "preprint"]
    L += md_table(["year"] + types + ["landmark", "core", "related", "total"],
                  [[y] + [by.get((y, t), 0) for t in types] + [tiers.get((y, t), 0) for t in ("landmark", "core", "related")]
                   + [sum(v for (yy, _), v in by.items() if yy == y)] for y in years])

    # modality per year
    if modalities:
        L += ["## Data layers over time (core + landmark papers)", "",
              "A paper can use several layers, so rows do not sum to the totals above.", ""]
        cm = collections.Counter()
        for r in papers:
            if r["tier"] in ("core", "landmark"):
                for m in (x.strip() for x in r["modality"].split(";")):
                    if m:
                        cm[(r["year"], m)] += 1
        L += md_table(["year"] + modalities, [[y] + [cm.get((y, m), 0) for m in modalities] for y in years])

    # origin papers of registry entries: the chronology of the field, which
    # title mentions alone cannot give
    origin = [r for r in rows if REGISTRY.search(r.get("entities") or "")]
    if origin:
        L += ["## Origin papers of registry entries, oldest first", "",
              "Papers that introduced an entry of a reference registry; the ids are the registry's identifiers.", ""]
        rows_o = []
        for r in sorted(origin, key=lambda r: r["date"] or "9999"):
            m = REGISTRY.search(r["entities"])
            rows_o.append([r["date"], (m.group(1).strip() + ": " + m.group(2))[:90], cite(r)])
        L += md_table(["date", "registry ids", "paper"], rows_o)

    # first appearance of named entities in titles
    entities = classify.get("entities")
    if entities is None:
        entities = classify.get("clock_names") or {}
    first = []
    for name, pat in entities.items():
        flags = 0
        if pat.startswith("(?i)"):
            pat, flags = pat[4:], re.I
        rx = re.compile(r"(?<![\w-])(?:" + pat + r")(?![\w-])", flags)
        hits = sorted((r for r in papers if rx.search(r["title"])), key=lambda r: r["date"] or "9999")
        if hits:
            n = sum(1 for r in papers if name in (r.get("entities") or ""))
            first.append([hits[0]["date"], name, n, cite(hits[0])])
    first.sort()
    if first:
        L += ["## When each named entity first appears in a catalogued title", "",
              "Earliest title mention within the catalogue window, not necessarily the origin paper; "
              "`papers` counts every catalogued paper whose title, abstract or keywords mention it.", ""]
        L += md_table(["first title mention", "entity", "papers", "earliest paper"], first)

    # most cited
    def num(r):
        try:
            return int(r.get("cited_by") or 0)
        except ValueError:
            return 0
    L += ["## Most-cited papers", ""]
    top = sorted(papers, key=num, reverse=True)[:30]
    L += md_table(["cited by", "type", "category", "paper"], [[num(r), r["resource_type"], r["category"], cite(r)] for r in top])
    if modalities:
        L += [f"## Most-cited {clf.origin_category} papers per data layer", ""]
    for m in modalities:
        sub = [r for r in papers if m in r["modality"] and r["category"] == clf.origin_category]
        if not sub:
            continue
        L += [f"**{m}** ({len(sub)} {clf.origin_category} papers)", ""]
        L += md_table(["cited by", "paper"], [[num(r), cite(r)] for r in sorted(sub, key=num, reverse=True)[:5]])

    # repositories
    def stars(r):
        m = re.search(r"stars=(\d+)", r.get("details", ""))
        return int(m.group(1)) if m else 0
    if repos:
        L += ["## Most-starred repositories", ""]
        L += md_table(["stars", "created", "repository"], [[stars(r), r["date"], f"[{r['title'][:100]}]({r['url']})"]
                                                            for r in sorted(repos, key=stars, reverse=True)[:30]])
    if sites:
        L += ["## Websites, databases and calculators", ""]
        L += md_table(["first archived", "type", "site"], [[r["date"] or "n/a", r["resource_type"], f"[{r['title'][:90]}]({r['url']})"]
                                                            for r in sorted(sites, key=lambda r: r["date"] or "9999")])

    # full text
    ft = collections.Counter(r["fulltext_status"] or "not tried" for r in papers)
    ftt = collections.Counter((r["tier"], r["fulltext_status"] or "not tried") for r in papers)
    L += ["## Full-text coverage", ""]
    L += md_table(["tier", "ok (md+txt)", "partial", "none (no open copy)", "not tried"],
                  [[t] + [ftt.get((t, s), 0) for s in ("ok", "partial", "none", "not tried")]
                   for t in ("landmark", "core", "related")])
    L += [f"PDFs on disk: {sum(1 for r in papers if r['fulltext_pdf'])}; Markdown: "
          f"{sum(1 for r in papers if r['fulltext_md'])}; plain text: {sum(1 for r in papers if r['fulltext_txt'])}.", ""]

    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text("\n".join(L), encoding="utf-8", newline="\n")
    log(f"wrote {OUT} ({len(L)} lines); full text {dict(ft)}")


if __name__ == "__main__":
    main()
