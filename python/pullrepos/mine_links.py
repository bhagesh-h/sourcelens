#!/usr/bin/env python3
"""Mine code, data and website links from catalogued papers.

Scans, for every article row in <output>/progress.csv, its abstract (record
store) and its downloaded full text (paper.md, else paper.txt). Links to
code and data hosts become paper_links.csv (paper -> repository); every other
external link is counted in candidate_websites.csv for website curation.

Outputs
  <output>/repos/paper_links.csv            uid, url, kind, where, context
  <output>/websites/candidate_websites.csv  domain/url counts across papers

Usage
  python/run.sh python python/pullrepos/mine_links.py
"""

from __future__ import annotations

import collections
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "common"))
from agelit import (REPOS, RESEARCH, WEBSITES, log, read_csv,  # noqa: E402
                    research_file, write_csv)
from store import Store  # noqa: E402

URL_RE = re.compile(r"(?:https?://|www\.)[^\s<>\"'`\]\[{}|\\^]+", re.I)
# "github.com/owner/repo" written without a scheme
BARE_RE = re.compile(r"(?<![\w/.])((?:github\.com|gitlab\.com|bitbucket\.org)/[\w.-]+/[\w.-]+)", re.I)
CODE_HOSTS = {
    "github.com": "github", "gitlab.com": "gitlab", "bitbucket.org": "bitbucket",
    "sourceforge.net": "sourceforge", "zenodo.org": "zenodo", "figshare.com": "figshare",
    "osf.io": "osf", "cran.r-project.org": "cran", "bioconductor.org": "bioconductor",
    "pypi.org": "pypi", "huggingface.co": "huggingface", "codeocean.com": "codeocean",
    "shinyapps.io": "shiny", "datadryad.org": "dryad", "synapse.org": "synapse",
    "github.io": "github-pages", "r-universe.dev": "r-universe", "readthedocs.io": "docs",
    "kaggle.com": "kaggle", "mendeley.com/datasets": "mendeley-data",
}
# never websites in their own right
SKIP_DOMAINS = re.compile(
    r"(^|\.)(doi\.org|dx\.doi\.org|creativecommons\.org|orcid\.org|w3\.org|crossmark|"
    r"ncbi\.nlm\.nih\.gov|nih\.gov|europepmc\.org|ebi\.ac\.uk|pubmed|scholar\.google|"
    r"elsevier\.com|sciencedirect\.com|springer\.com|springernature|nature\.com|wiley\.com|"
    r"tandfonline|oup\.com|academic\.oup|biomedcentral|frontiersin|mdpi\.com|plos\.org|"
    r"cell\.com|sagepub|karger|bmj\.com|jamanetwork|thelancet|nejm|aging-us\.com|"
    r"biorxiv\.org|medrxiv\.org|arxiv\.org|researchsquare|ssrn\.com|elifesciences|pnas\.org|"
    r"science\.org|sciencemag|acs\.org|rsc\.org|ieee\.org|acm\.org|jstor|clinicaltrials\.gov|"
    r"who\.int|twitter\.com|x\.com|facebook\.com|linkedin\.com|youtube\.com|example\.com|"
    r"mailto|localhost|apa\.org|annualreviews|cambridge\.org|lww\.com|jci\.org|embopress|"
    r"physiology\.org|ahajournals|diabetesjournals|genome\.cshlp|cshlp|epigeneticsandchromatin|"
    r"doi:|hdl\.handle\.net|identifiers\.org|isrctn|chictr|anzctr|clinicaltrialsregister|"
    # generic tools, link shorteners, registries and vendors, not aging resources
    r"biorender|bit\.ly|tinyurl|goo\.gl|genome\.ucsc|illumina\.com|babraham|crd\.york|prospero|"
    r"umin\.ac\.jp|python\.org|r-project\.org$|rstudio|posit\.co|anaconda|qualtrics|redcap|"
    r"surveymonkey|graphpad|mathworks|microsoft|google\.com/(forms|maps)|apple\.com|ensembl\.org|"
    r"geneontology|kegg\.jp|string-db|uniprot|reactome|gtexportal|gwas\.mrcieu|opengwas|"
    r"covidence|amegroups|data\.bris\.ac\.uk|agrf\.org\.au|disgenet|proteinatlas)", re.I)
REPO_PATH = re.compile(r"^(github\.com|gitlab\.com|bitbucket\.org)/([\w.-]+)/([\w.-]+)", re.I)
COLUMNS = ["uid", "url", "kind", "where", "context"]
ARTICLE_TYPES = {"article", "review", "preprint", "book chapter", "conference paper", "thesis", "report"}


def clean_url(u: str) -> str:
    u = u.strip().rstrip(".,;:)]}>'\"*_")
    u = re.sub(r"^www\.", "https://www.", u, flags=re.I)
    u = re.sub(r"^http://", "https://", u, flags=re.I)
    # split "https://github.com/a/bhttps://..." glued by PDF extraction
    u = re.split(r"(?<=.)https?://", u)[0]
    return u


def normalise(u: str) -> tuple[str, str, str]:
    """Return (canonical url, kind, domain)."""
    bare = re.sub(r"^https?://(www\.)?", "", u, flags=re.I)
    domain = bare.split("/", 1)[0].lower()
    m = REPO_PATH.match(bare)
    if m:
        host, owner, repo = m.group(1).lower(), m.group(2), re.sub(r"\.git$", "", m.group(3))
        if repo.lower() in ("issues", "pulls", "releases", "blob", "tree", "wiki", "archive", "raw"):
            return f"https://{host}/{owner}", CODE_HOSTS[host] + "-user", domain
        return f"https://{host}/{owner}/{repo}", CODE_HOSTS[host], domain
    for host, kind in CODE_HOSTS.items():
        if host in bare.lower():
            return "https://" + bare, kind, domain
    return "https://" + bare, "website", domain


def extract(text: str) -> list[tuple[str, str]]:
    out = []
    for rx in (URL_RE, BARE_RE):
        for m in rx.finditer(text):
            raw = clean_url(m.group(1) if rx is BARE_RE else m.group(0))
            if len(raw) < 12:
                continue
            s = max(0, m.start() - 120)
            ctx = " ".join(text[s:m.end() + 80].split())
            out.append((raw, ctx))
    return out


def main() -> None:
    rows = [r for r in read_csv(RESEARCH / "progress.csv") if r.get("resource_type") in ARTICLE_TYPES]
    store = Store()
    links: dict[tuple, dict] = {}
    web = collections.defaultdict(lambda: {"papers": set(), "urls": collections.Counter(), "context": ""})
    n_ft = 0
    for r in rows:
        uid = r["uid"]
        rec = store.get(uid) or {}
        sources = [("abstract", rec.get("abstract") or "")]
        folder = research_file(r["fulltext_md"]) if r.get("fulltext_md") else None
        if folder is None and r.get("fulltext_txt"):
            folder = research_file(r["fulltext_txt"])
        if folder is not None and folder.exists():
            txt = folder.read_text(encoding="utf-8", errors="ignore")
            # the reference list cites other papers' code; stop before it
            cut = re.search(r"^#+\s*References\b|^References\s*$|^REFERENCES\s*$", txt, re.M)
            sources.append(("fulltext", txt[:cut.start()] if cut else txt))
            n_ft += 1
        for where, text in sources:
            for raw, ctx in extract(text):
                url, kind, domain = normalise(raw)
                if SKIP_DOMAINS.search(domain) or SKIP_DOMAINS.search(url.split("/", 3)[2] if url.count("/") > 2 else url):
                    continue
                if kind == "website":
                    w = web[domain]
                    w["papers"].add(uid)
                    w["urls"][url] += 1
                    w["context"] = w["context"] or ctx[:300]
                    continue
                key = (uid, url)
                if key not in links:
                    links[key] = {"uid": uid, "url": url, "kind": kind, "where": where, "context": ctx[:400]}
    write_csv(REPOS / "paper_links.csv", sorted(links.values(), key=lambda x: (x["uid"], x["url"])), COLUMNS)
    cand = [{"domain": d, "n_papers": len(v["papers"]), "top_url": v["urls"].most_common(1)[0][0],
             "n_urls": len(v["urls"]), "example_context": v["context"],
             "example_papers": "; ".join(sorted(v["papers"])[:5])}
            for d, v in web.items()]
    cand.sort(key=lambda x: -x["n_papers"])
    write_csv(WEBSITES / "candidate_websites.csv", cand,
              ["domain", "n_papers", "top_url", "n_urls", "example_context", "example_papers"])
    kinds = collections.Counter(v["kind"] for v in links.values())
    log(f"{len(rows)} articles ({n_ft} with full text): {len(links)} code/data links {dict(kinds)}; "
        f"{len(cand)} candidate website domains")


if __name__ == "__main__":
    main()
