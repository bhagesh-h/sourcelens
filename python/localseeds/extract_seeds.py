#!/usr/bin/env python3
"""Harvest seed references from the three local aging projects.

The repositories are mounted read-only under /refs by python/run.sh:

  /refs/FALCONAge  clock registry (161 clocks, each with its origin paper),
                   documentation bibliography, published-association evidence
  /refs/300BCG     curated literature manifests (cohort papers + aging-clock
                   methods references) and analysis notes
  /refs/300OB      prior-art notes, lipid / methylation clock notes

Structured sources are parsed field by field; every other text file is scanned
for DOIs and code/website URLs with the line they sit on as context.

Outputs
  <output>/seeds/falconage_clocks.csv   one row per registry clock
  <output>/seeds/local_seeds.csv        doi x role x source file, with context
  <output>/seeds/local_urls.csv         repository / package / website URLs

Roles, strongest first: clock_origin, clock_reference, benchmark,
methods_reference, cohort_paper, mentioned.
"""

from __future__ import annotations

import csv
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "common"))
from agelit import REFS, SEEDS, DOI_RE, load_yaml, log, norm_doi, write_csv  # noqa: E402

TEXT_SUFFIXES = {".md", ".qmd", ".rmd", ".yml", ".yaml", ".r", ".py", ".tsv",
                 ".csv", ".cff", ".bib", ".txt", ".json", ".html", ".rd"}
SKIP_DIRS = {".git", "__pycache__", ".ruff_cache", "node_modules", "_site",
             "output", "renv", ".venv", "site-packages", "logs"}
# full texts of papers: their reference lists would flood the seeds with
# unrelated DOIs
SKIP_PATH_PARTS = ("300BCG/literature/0", "300BCG/literature/1", "/papers/")
SKIP_NAME = re.compile(r"^fulltext|^full_text|\.fulltext\.", re.I)
MAX_BYTES = 5_000_000
URL_RE = re.compile(r"https?://[^\s\"'<>)\]}|,`]+", re.I)
URL_KEEP = re.compile(
    r"github\.com|gitlab\.|bitbucket\.org|zenodo\.org|figshare\.com|osf\.io|"
    r"bioconductor\.org|cran\.r-project\.org|pypi\.org|huggingface\.co|"
    r"clockfoundation|shinyapps\.io|dnamage|clockbase|biolearn|"
    r"computage|agingbiomarkers|biomarkersofaging|genomics\.senescence|"
    r"ngdc\.cncb\.ac\.cn|ncbi\.nlm\.nih\.gov/geo|synapse\.org|ukbiobank", re.I)

SEED_COLUMNS = ["doi", "role", "source_repo", "source_file", "clock_name",
                "clock_year", "data_type", "species", "context"]
URL_COLUMNS = ["url", "kind", "source_repo", "source_file", "context"]
CLOCK_COLUMNS = ["clock_id", "name", "year", "species", "data_type", "generation",
                 "tissue", "platform", "predicts", "unit", "scale_type",
                 "model_type", "population", "n_features", "availability",
                 "doi", "citation", "notes", "coefficient_url"]


def as_list(v) -> str:
    if isinstance(v, list):
        return "; ".join(str(x) for x in v)
    return "" if v is None else str(v)


def url_kind(u: str) -> str:
    ul = u.lower()
    for key, kind in (("github.com", "github"), ("gitlab.", "gitlab"),
                      ("bitbucket.org", "bitbucket"), ("zenodo.org", "zenodo"),
                      ("figshare.com", "figshare"), ("osf.io", "osf"),
                      ("bioconductor.org", "bioconductor"),
                      ("cran.r-project.org", "cran"), ("pypi.org", "pypi"),
                      ("huggingface.co", "huggingface"),
                      ("ncbi.nlm.nih.gov/geo", "geo")):
        if key in ul:
            return kind
    return "website"


def falconage(seeds: list, urls: list) -> list[dict]:
    base = REFS / "FALCONAge"
    reg = base / "python/src/falconage/registry/data/clocks.yaml"
    clocks = []
    if reg.exists():
        for cid, c in load_yaml(reg)["clocks"].items():
            src = c.get("coefficient_source") or {}
            row = {"clock_id": cid, "name": c.get("name", cid), "year": c.get("year"),
                   "species": c.get("species"), "data_type": c.get("data_type"),
                   "generation": c.get("generation"), "tissue": as_list(c.get("tissue")),
                   "platform": as_list(c.get("platform")),
                   "predicts": as_list(c.get("predicts")), "unit": as_list(c.get("unit")),
                   "scale_type": c.get("scale_type"), "model_type": c.get("model_type"),
                   "population": c.get("population"), "n_features": c.get("n_features"),
                   "availability": c.get("availability"), "doi": norm_doi(c.get("doi")),
                   "citation": c.get("citation"), "notes": c.get("notes"),
                   "coefficient_url": src.get("url") or ""}
            clocks.append(row)
            if row["doi"]:
                seeds.append({"doi": row["doi"], "role": "clock_origin",
                              "source_repo": "FALCONAge",
                              "source_file": str(reg.relative_to(REFS)),
                              "clock_name": cid, "clock_year": row["year"],
                              "data_type": row["data_type"], "species": row["species"],
                              "context": row["citation"]})
            if row["coefficient_url"]:
                urls.append({"url": row["coefficient_url"], "kind": url_kind(row["coefficient_url"]),
                             "source_repo": "FALCONAge", "source_file": str(reg.relative_to(REFS)),
                             "context": f"coefficient source of clock {cid}"})
        log(f"FALCONAge registry: {len(clocks)} clocks")

    refs = base / "docs/references.yml"
    if refs.exists():
        n = 0
        for g in load_yaml(refs).get("groups", []):
            for e in g.get("entries", []):
                d = norm_doi(e.get("doi") or e.get("text"))
                if d:
                    seeds.append({"doi": d, "role": "clock_reference", "source_repo": "FALCONAge",
                                  "source_file": str(refs.relative_to(REFS)),
                                  "context": f"[{g.get('title')}] {e.get('text')}"})
                    n += 1
        log(f"FALCONAge references.yml: {n} DOIs")

    ev = base / "python/src/falconage/registry/data/evidence.yaml"
    if ev.exists():
        for key, s in (load_yaml(ev).get("sources") or {}).items():
            d = norm_doi(s.get("doi"))
            if d:
                seeds.append({"doi": d, "role": "benchmark", "source_repo": "FALCONAge",
                              "source_file": str(ev.relative_to(REFS)),
                              "context": " ".join(str(s.get("citation", "")).split())})
    return clocks


def bcg(seeds: list) -> None:
    lit = REFS / "300BCG/literature"
    for name, role in (("manifest_methods.tsv", "methods_reference"),
                       ("manifest_local.tsv", "methods_reference"),
                       ("manifest.tsv", "cohort_paper")):
        p = lit / name
        if not p.exists():
            continue
        with open(p, newline="", encoding="utf-8") as fh:
            rows = list(csv.DictReader(fh, delimiter="\t"))
        for r in rows:
            d = norm_doi(r.get("doi"))
            if d:
                topic = r.get("topic", "")
                # only the aging-clock topics of the methods lists are clock
                # references; EWAS / normalisation / deconvolution methods are not
                if role == "methods_reference" and not (topic.startswith("clocks") or "aging-clocks" in topic):
                    row_role = "methods_other"
                else:
                    row_role = role
                ctx = f"[{topic}] {r.get('title', '')}"
                if r.get("question"):
                    ctx += f" | {r['question']}"
                seeds.append({"doi": d, "role": row_role, "source_repo": "300BCG",
                              "source_file": str(p.relative_to(REFS)), "context": ctx})
        log(f"300BCG {name}: {len(rows)} rows")


def scan_text(seeds: list, urls: list) -> None:
    for repo in ("FALCONAge", "300BCG", "300OB"):
        base = REFS / repo
        if not base.exists():
            log(f"missing {base}; skipped")
            continue
        n_doi = n_url = 0
        for p in sorted(base.rglob("*")):  # sorted: same order as the Go implementation
            if not p.is_file() or p.suffix.lower() not in TEXT_SUFFIXES:
                continue
            rel = p.relative_to(REFS)
            if (set(rel.parts) & SKIP_DIRS or any(s in str(rel) for s in SKIP_PATH_PARTS)
                    or SKIP_NAME.search(p.name)):
                continue
            if p.stat().st_size > MAX_BYTES:
                continue
            try:
                lines = p.read_text(encoding="utf-8", errors="ignore").splitlines()
            except OSError:
                continue
            for line in lines:
                for m in DOI_RE.finditer(line):
                    d = norm_doi(m.group(0))
                    if d:
                        seeds.append({"doi": d, "role": "mentioned", "source_repo": repo,
                                      "source_file": str(rel), "context": line.strip()[:400]})
                        n_doi += 1
                for m in URL_RE.finditer(line):
                    u = m.group(0).rstrip(".;:*_")
                    if URL_KEEP.search(u) and "doi.org" not in u:
                        urls.append({"url": u, "kind": url_kind(u), "source_repo": repo,
                                     "source_file": str(rel), "context": line.strip()[:300]})
                        n_url += 1
        log(f"{repo}: {n_doi} DOI mentions, {n_url} code/website URL mentions")


def main() -> None:
    seeds: list[dict] = []
    urls: list[dict] = []
    clocks = falconage(seeds, urls)
    bcg(seeds)
    scan_text(seeds, urls)

    # one row per (doi, role, file, clock); keep the first context seen
    uniq = {}
    for s in seeds:
        uniq.setdefault((s["doi"], s["role"], s["source_file"], s.get("clock_name", "")), s)
    seeds = sorted(uniq.values(), key=lambda s: (s["doi"], s["role"], s["source_file"]))
    uu = {}
    for u in urls:
        uu.setdefault((u["url"], u["source_file"]), u)
    urls = sorted(uu.values(), key=lambda u: (u["kind"], u["url"]))

    write_csv(SEEDS / "falconage_clocks.csv", clocks, CLOCK_COLUMNS)
    write_csv(SEEDS / "local_seeds.csv", seeds, SEED_COLUMNS)
    write_csv(SEEDS / "local_urls.csv", urls, URL_COLUMNS)
    log(f"{len({s['doi'] for s in seeds})} unique DOIs in {len(seeds)} seed rows; "
        f"{len({u['url'] for u in urls})} unique URLs")


if __name__ == "__main__":
    main()
