"""Local seeds: DOIs and links found in the reference folders listed under
`references` in the catalogue's configuration.

Every text file is scanned for DOIs and code/website URLs, with the line they
sit on as context. A folder with a clock registry (registry/data/clocks.yaml,
plus evidence.yaml beside it and docs/references.yml) or literature manifests
(literature/manifest*.tsv) is also read field by field.

Outputs
  <catalogue>/seeds/registry_entries.csv   one row per registry entry
  <catalogue>/seeds/local_seeds.csv        doi x role x source file, with context
  <catalogue>/seeds/local_urls.csv         repository / package / website URLs

Roles, strongest first: registry_origin, registry_reference, benchmark,
methods_reference, cohort_paper, mentioned.
"""

from __future__ import annotations

import csv
import os
import re
from pathlib import Path

from sourcelens.common.agelit import DOI_RE, SEEDS, config_root, load_yaml, log, norm_doi, write_csv

TEXT_SUFFIXES = {".md", ".qmd", ".rmd", ".yml", ".yaml", ".r", ".py", ".tsv",
                 ".csv", ".cff", ".bib", ".txt", ".json", ".html", ".rd"}
SKIP_DIRS = {".git", "__pycache__", ".ruff_cache", "node_modules", "_site",
             "output", "renv", ".venv", "site-packages", "logs"}
SKIP_NAME = re.compile(r"^fulltext|^full_text|\.fulltext\.", re.I)
MAX_BYTES = 5_000_000
URL_RE = re.compile(r"https?://[^\s\"'<>)\]}|,`]+", re.I)
# code and data hosts; seeds.keep_url_domains in the configuration adds more
CODE_DATA_HOSTS = (r"github\.com|gitlab\.|bitbucket\.org|zenodo\.org|figshare\.com|osf\.io|"
                   r"bioconductor\.org|cran\.r-project\.org|pypi\.org|huggingface\.co|ncbi\.nlm\.nih\.gov/geo")

SEED_COLUMNS = ["doi", "role", "source_repo", "source_file", "registry_id",
                "registry_year", "data_type", "species", "context"]
URL_COLUMNS = ["url", "kind", "source_repo", "source_file", "context"]
REGISTRY_COLUMNS = ["clock_id", "name", "year", "species", "data_type", "generation",
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


class SeedConfig:
    """The `references` list and the `seeds` section of the configuration."""

    def __init__(self) -> None:
        root = config_root()
        seeds = root.get("seeds") or {}
        self.refs = [os.path.normpath(os.path.expanduser(str(r).strip()))
                     for r in (root.get("references") or []) if str(r).strip()]
        self.methods_topics = re.compile(seeds["methods_topics"]) if seeds.get("methods_topics") else None
        # full texts of papers: their reference lists would flood the seeds with unrelated DOIs
        self.skip_paths = ["/papers/"] + list(seeds.get("skip_paths") or [])
        keep = CODE_DATA_HOSTS + ("|" + seeds["keep_url_domains"] if seeds.get("keep_url_domains") else "")
        self.url_keep = re.compile(keep, re.I)


def find_file(base: Path, suffix: str) -> Path | None:
    """The first file under base whose path ends in suffix (walk order)."""
    for p in sorted(base.rglob("*")):
        if set(p.relative_to(base).parts[:-1]) & SKIP_DIRS:
            continue
        if p.is_file() and p.as_posix().endswith(suffix):
            return p
    return None


def registry(base: Path, seeds: list, urls: list) -> list[dict]:
    """A clock registry: registry/data/clocks.yaml (one entry per clock with its
    origin DOI and coefficient source), evidence.yaml beside it, and
    docs/references.yml at the folder root."""
    repo, parent = base.name, base.parent
    rel = lambda p: p.relative_to(parent).as_posix()  # noqa: E731
    entries = []
    reg = find_file(base, "registry/data/clocks.yaml")
    if reg is not None:
        for cid, c in (load_yaml(reg) or {}).get("clocks", {}).items():
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
            entries.append(row)
            if row["doi"]:
                seeds.append({"doi": row["doi"], "role": "registry_origin", "source_repo": repo,
                              "source_file": rel(reg), "registry_id": cid, "registry_year": row["year"],
                              "data_type": row["data_type"], "species": row["species"],
                              "context": row["citation"]})
            if row["coefficient_url"]:
                urls.append({"url": row["coefficient_url"], "kind": url_kind(row["coefficient_url"]),
                             "source_repo": repo, "source_file": rel(reg),
                             "context": f"coefficient source of {cid}"})
        log(f"{repo} registry: {len(entries)} entries")
        ev = reg.parent / "evidence.yaml"
        if ev.exists():
            for key, s in ((load_yaml(ev) or {}).get("sources") or {}).items():
                d = norm_doi(s.get("doi"))
                if d:
                    seeds.append({"doi": d, "role": "benchmark", "source_repo": repo, "source_file": rel(ev),
                                  "context": " ".join(str(s.get("citation", "")).split())})
    refs = base / "docs" / "references.yml"
    if refs.exists():
        n = 0
        for g in (load_yaml(refs) or {}).get("groups", []):
            for e in g.get("entries", []):
                d = norm_doi(e.get("doi") or e.get("text"))
                if d:
                    seeds.append({"doi": d, "role": "registry_reference", "source_repo": repo,
                                  "source_file": rel(refs), "context": f"[{g.get('title')}] {e.get('text')}"})
                    n += 1
        log(f"{repo} docs/references.yml: {n} DOIs")
    return entries


def manifests(base: Path, sc: SeedConfig, seeds: list) -> None:
    """Literature manifests: literature/manifest*.tsv with doi, topic, title and question columns."""
    repo = base.name
    for name, role in (("manifest_methods.tsv", "methods_reference"),
                       ("manifest_local.tsv", "methods_reference"),
                       ("manifest.tsv", "cohort_paper")):
        p = base / "literature" / name
        if not p.exists():
            continue
        with open(p, newline="", encoding="utf-8") as fh:
            rows = list(csv.DictReader(fh, delimiter="\t"))
        for r in rows:
            d = norm_doi(r.get("doi"))
            if d:
                topic = r.get("topic", "")
                row_role = role
                if role == "methods_reference" and sc.methods_topics is not None \
                        and not sc.methods_topics.search(topic):
                    row_role = "methods_other"
                ctx = f"[{topic}] {r.get('title', '')}"
                if r.get("question"):
                    ctx += f" | {r['question']}"
                seeds.append({"doi": d, "role": row_role, "source_repo": repo,
                              "source_file": f"{repo}/literature/{name}", "context": ctx})
        log(f"{repo} {name}: {len(rows)} rows")


def scan_text(base: Path, sc: SeedConfig, seeds: list, urls: list) -> None:
    """Every DOI and every code, data or kept website link in the text files of a folder."""
    repo, parent = base.name, base.parent
    n_doi = n_url = 0
    for p in sorted(base.rglob("*")):
        if not p.is_file() or p.suffix.lower() not in TEXT_SUFFIXES:
            continue
        if set(p.relative_to(base).parts[:-1]) & SKIP_DIRS:
            continue
        rel = p.relative_to(parent).as_posix()
        if any(s in rel for s in sc.skip_paths) or SKIP_NAME.search(p.name):
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
                                  "source_file": rel, "context": line.strip()[:400]})
                    n_doi += 1
            for m in URL_RE.finditer(line):
                u = m.group(0).rstrip(".;:*_")
                if sc.url_keep.search(u) and "doi.org" not in u:
                    urls.append({"url": u, "kind": url_kind(u), "source_repo": repo,
                                 "source_file": rel, "context": line.strip()[:300]})
                    n_url += 1
    log(f"{repo}: {n_doi} DOI mentions, {n_url} code/website URL mentions")


def main() -> None:
    seeds: list[dict] = []
    urls: list[dict] = []
    entries: list[dict] = []
    sc = SeedConfig()
    if not sc.refs:
        log("no reference folders in the configuration (references); nothing to read")
    for ref in sc.refs:
        base = Path(ref)
        if not base.exists():
            log(f"missing {base}; skipped")
            continue
        entries += registry(base, seeds, urls)
        manifests(base, sc, seeds)
        scan_text(base, sc, seeds, urls)

    # one row per (doi, role, file, registry id); keep the first context seen
    uniq = {}
    for s in seeds:
        uniq.setdefault((s["doi"], s["role"], s["source_file"], s.get("registry_id", "")), s)
    seeds = sorted(uniq.values(), key=lambda s: (s["doi"], s["role"], s["source_file"]))
    uu = {}
    for u in urls:
        uu.setdefault((u["url"], u["source_file"]), u)
    urls = sorted(uu.values(), key=lambda u: (u["kind"], u["url"]))

    write_csv(SEEDS / "registry_entries.csv", entries, REGISTRY_COLUMNS)
    write_csv(SEEDS / "local_seeds.csv", seeds, SEED_COLUMNS)
    write_csv(SEEDS / "local_urls.csv", urls, URL_COLUMNS)
    log(f"{len({s['doi'] for s in seeds})} unique DOIs in {len(seeds)} seed rows; "
        f"{len({u['url'] for u in urls})} unique URLs")


if __name__ == "__main__":
    main()
