#!/usr/bin/env python3
"""Assemble repositories, software packages and data archives for the catalogue.

Sources
  <output>/repos/github_search.csv   GitHub search hits (search_github.py)
  <output>/repos/paper_links.csv     links found in catalogued papers (mine_links.py)
  <output>/seeds/local_urls.csv      links in FALCONAge / 300BCG / 300OB
  configuration, repos section       package search terms and known packages

Metadata
  GitHub REST API (/repos/{owner}/{repo}) for repositories not in the search
  results, and to follow renames; CRAN (crandb), Bioconductor (release
  packages.json; first month with downloads in the Bioconductor download
  stats dates the first release), PyPI (first upload), Zenodo (records
  linked from papers). R packages that live only on GitHub (BioAge,
  dnaMethyAge, ...) come in through the GitHub search.

Inclusion in progress.csv (everything is kept in repositories.csv):
  * linked from a catalogued paper, and either about aging measurement
    (config relevance pattern) or the own code (link in the abstract or in
    a "code is available at ..." sentence, under 500 stars) of a landmark or
    core paper;
  * linked from a local project, and relevant or the coefficient source of a
    FALCONAge registry clock;
  * a CRAN / Bioconductor / PyPI / Zenodo match;
  * a relevant GitHub search hit with at least `--min-stars` stars.
Generic tools a paper merely used (plotting, alignment, ...) are left out.

Outputs
  <output>/repos/repositories.csv            every repository / package seen
  <output>/repos/repositories_catalogue.csv  rows in progress.csv shape

Usage
  python/run.sh python python/pullrepos/build_repos.py [--min-stars 3]
"""

from __future__ import annotations

import argparse
import collections
import datetime as dt
import importlib.util
import os
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "common"))
from agelit import (config, REPOS, RESEARCH, SEEDS, Http, load_yaml, log,  # noqa: E402
                    make_uid, read_csv, today, write_csv)

_spec = importlib.util.spec_from_file_location(
    "build_progress", Path(__file__).resolve().parents[1] / "buildcatalog" / "build_progress.py")
bp = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(bp)

GH = "https://api.github.com/repos/"
REPO_SOURCES = ["github", "cran", "bioconductor", "pypi", "zenodo"]
VENUE_KIND = {"GitHub": "github", "CRAN": "cran", "Bioconductor": "bioconductor", "PyPI": "pypi", "Zenodo": "zenodo"}
# the paper's own code: the sentence must refer to this study's code ("code
# for this study is available", "code availability", "our scripts"), not just
# say that a tool it used "is publicly available at github.com/..."
AVAIL = re.compile(r"(?i)\b(code|scripts?|software|analys[ie]s|pipeline|implementation|data)\b[^.]{0,60}"
                   r"\b(for|of|from|used in|underlying|supporting|to reproduce|to replicate|accompanying)\s+"
                   r"(this|the present|the current|our)\s+(study|work|paper|article|manuscript|analys[ie]s|results|findings)"
                   r"|\b(code|data and code|software) availability\b"
                   r"|\bour (code|scripts|software|package|pipeline|implementation|repository|model|tool)\b"
                   r"|\b(we|authors) (have )?(made|make|provide|release|deposit|share)[^.]{0,40}\b(code|scripts|software|package|implementation)")
OWN_CODE_MAX_STARS = 500  # a paper's own analysis code is rarely a popular general-purpose tool
RECHECK_DAYS = 30
ALL_COLUMNS = ["uid", "kind", "name", "url", "description", "created", "updated", "stars",
               "language", "license", "topics", "archived", "linked_from_papers",
               "linked_from_local", "search_queries", "included", "reason", "doi", "checked_on"]


def gh_http() -> Http:
    headers = {"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
    if os.environ.get("GITHUB_TOKEN"):
        headers["Authorization"] = f"Bearer {os.environ['GITHUB_TOKEN']}"
    return Http(min_interval=0.75 if os.environ.get("GITHUB_TOKEN") else 60, headers=headers)


def repo_key(url: str) -> str | None:
    m = re.match(r"https?://(www\.)?github\.com/([\w.-]+)/([\w.-]+)", url, re.I)
    return f"{m.group(2)}/{re.sub(r'.git$', '', m.group(3))}".lower() if m else None


def gh_repo(h: Http, full: str) -> dict | None:
    r = h.get(GH + full)
    if r is None or r.status_code != 200:
        return None
    j = r.json()
    return {"full_name": j["full_name"], "url": j["html_url"], "description": j.get("description") or "",
            "created": (j.get("created_at") or "")[:10], "updated": (j.get("pushed_at") or "")[:10],
            "stars": j.get("stargazers_count", 0), "language": j.get("language") or "",
            "license": (j.get("license") or {}).get("spdx_id") or "",
            "topics": "; ".join(j.get("topics") or []), "archived": j.get("archived", False),
            "fork": j.get("fork", False)}


def bioc_first_month(h: Http, pkg: str) -> str:
    r = h.get(f"https://bioconductor.org/packages/stats/bioc/{pkg}/{pkg}_stats.tab")
    if r is None or r.status_code != 200:
        return ""
    months = {m: i for i, m in enumerate(["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug",
                                           "Sep", "Oct", "Nov", "Dec"], 1)}
    first = ""
    for line in r.text.splitlines()[1:]:
        p = line.split("\t")
        if len(p) >= 4 and p[1] in months and p[3].strip().isdigit() and int(p[3]) > 0:
            d = f"{p[0]}-{months[p[1]]:02d}-01"
            first = d if not first or d < first else first
    return first


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--min-stars", type=int, default=3)
    ap.add_argument("--sources", default=",".join(REPO_SOURCES),
                    help="which to refresh: github, cran, bioconductor, pypi, zenodo (default all); "
                         "rows of the others are carried over from the last build unchanged")
    args = ap.parse_args()
    srcs = {x.strip().lower() for x in args.sources.split(",") if x.strip()}
    if not srcs <= set(REPO_SOURCES):
        ap.error(f"sources must be within {REPO_SOURCES}")
    cfg = config("repos")
    pkg_rx = re.compile(cfg["package_terms"])
    clf = bp.Classifier(config("classify"))

    progress = {r["uid"]: r for r in read_csv(RESEARCH / "progress.csv")}
    items: dict[str, dict] = {}

    # ---- GitHub: search hits --------------------------------------------
    rel = re.compile(cfg["relevance"])
    for r in (read_csv(REPOS / "github_search.csv") if "github" in srcs else []):
        k = r["full_name"].lower()
        # relevance is re-evaluated with the current config, not taken from
        # the search run, so tightening the pattern takes effect immediately
        relevant = bool(rel.search(" ".join([r["full_name"].split("/")[-1].replace("-", " "),
                                             r["description"], r["topics"].replace("-", " ")])))
        items[f"gh:{k}"] = {"kind": "github", "name": r["full_name"], "url": r["url"],
                            "description": r["description"], "created": r["created_at"][:10],
                            "updated": r["pushed_at"][:10], "stars": int(r["stars"] or 0),
                            "language": r["language"], "license": r["license"], "topics": r["topics"],
                            "archived": r["archived"], "fork": r["fork"],
                            "search_queries": r["queries"], "relevant": relevant,
                            "papers": set(), "local": set()}

    # ---- links from papers and from the local projects ------------------
    other_links = []
    for r in read_csv(REPOS / "paper_links.csv"):
        if r["uid"] not in progress:
            continue
        k = repo_key(r["url"]) if "github" in srcs else None
        if k:
            it = items.setdefault(f"gh:{k}", {"kind": "github", "name": k, "url": r["url"], "papers": set(),
                                              "local": set(), "search_queries": ""})
            it["papers"].add(r["uid"])
            # a link in the abstract or in a "code is available at" sentence is
            # the paper's own code; a bare mention is usually a tool it used
            if r["where"] == "abstract" or AVAIL.search(r.get("context", "")):
                it["own_code"] = True
        elif r["kind"] in ("zenodo", "cran", "bioconductor", "pypi", "gitlab", "bitbucket", "huggingface",
                           "figshare", "osf", "codeocean", "shiny"):
            other_links.append(r)
    for r in read_csv(SEEDS / "local_urls.csv"):
        k = repo_key(r["url"]) if "github" in srcs else None
        if k:
            it = items.setdefault(f"gh:{k}", {"kind": "github", "name": k, "url": r["url"], "papers": set(),
                                              "local": set(), "search_queries": ""})
            it["local"].add(r["source_repo"])
            if r["source_file"].endswith("registry/data/clocks.yaml"):
                it["registry"] = True

    # ---- fill GitHub metadata for linked repos not seen in search -------
    h = gh_http()
    # GitHub metadata fetched in the last RECHECK_DAYS is reused
    cutoff = (dt.date.today() - dt.timedelta(days=RECHECK_DAYS)).isoformat()
    cached = {r["name"].lower(): r for r in read_csv(REPOS / "repositories.csv")
              if r.get("kind") == "github" and (r.get("checked_on") or today()) >= cutoff}
    for k, v in items.items():
        c = cached.get(v["name"].lower()) or cached.get(k[3:])
        if v["kind"] == "github" and not v.get("created") and c:
            if c.get("created"):
                v.update({x: c.get(x, "") for x in ("url", "description", "created", "updated", "language",
                                                    "license", "topics", "archived")})
                v["stars"] = int(c.get("stars") or 0)
                v["name"] = c["name"]
                v["checked_on"] = c.get("checked_on") or today()
            elif c.get("reason") == "repository gone":
                v["missing"] = True
                v["checked_on"] = c.get("checked_on") or today()
    need = [k for k, v in items.items()
            if v["kind"] == "github" and not v.get("created") and not v.get("missing")]
    log(f"GitHub: {len(items)} repositories; fetching metadata for {len(need)}")
    for i, k in enumerate(need, 1):
        meta = gh_repo(h, items[k]["name"])
        if meta:
            items[k].update({x: meta[x] for x in ("url", "description", "created", "updated", "stars",
                                                  "language", "license", "topics", "archived", "fork")})
            items[k]["name"] = meta["full_name"]
        else:
            items[k]["missing"] = True
        if i % 200 == 0:
            log(f"  {i}/{len(need)}")
    for v in items.values():
        if v["kind"] == "github" and "relevant" not in v:
            v["relevant"] = bool(rel.search(" ".join([v["name"].split("/")[-1].replace("-", " "),
                                                      v.get("description", ""),
                                                      v.get("topics", "").replace("-", " ")])))

    # ---- packages ---------------------------------------------------------
    ph = Http(min_interval=0.2)
    known = cfg.get("known_packages", {})
    # CRAN
    r = ph.get("https://crandb.r-pkg.org/-/desc") if "cran" in srcs else None
    cran = r.json() if r is not None and r.status_code == 200 else {}
    cran_hits = {p for p, d in cran.items() if pkg_rx.search(f"{d.get('title', '')}")} | \
                {p for p in known.get("cran", []) if p in cran}
    cran_hits |= {re.sub(r".*/package=|.*/packages/", "", x["url"]).split("/")[0].split("&")[0]
                  for x in other_links if x["kind"] == "cran"} & set(cran)
    for p in sorted(cran_hits):
        rr = ph.get(f"https://crandb.r-pkg.org/{p}/all")
        if rr is None or rr.status_code != 200:
            continue
        j = rr.json()
        times = j.get("timeline") or {}
        latest = (j.get("versions") or {}).get(j.get("latest", ""), {})
        items[f"cran:{p}"] = {"kind": "cran", "name": p, "url": f"https://cran.r-project.org/package={p}",
                              "description": f"{latest.get('Title', '')}. {latest.get('Description', '')}",
                              "created": min(times.values())[:10] if times else "",
                              "updated": max(times.values())[:10] if times else "",
                              "license": latest.get("License", ""), "papers": set(), "local": set(),
                              "relevant": True, "search_queries": "CRAN title match"}
    # Bioconductor
    bioc, ver = {}, ""
    if "bioconductor" in srcs:
        rr = ph.get("https://bioconductor.org/config.yaml")
        ver = re.search(r'release_version:\s*"([\d.]+)"', rr.text).group(1) if rr is not None else "3.22"
        rr = ph.get(f"https://bioconductor.org/packages/json/{ver}/bioc/packages.json")
        bioc = rr.json() if rr is not None and rr.status_code == 200 else {}
    for p, d in bioc.items():
        text = f"{d.get('Title', '')} {d.get('Description', '')}"
        if pkg_rx.search(text) or p in known.get("bioconductor", []):
            items[f"bioc:{p}"] = {"kind": "bioconductor", "name": p,
                                  "url": f"https://bioconductor.org/packages/{p}",
                                  "description": " ".join(text.split()),
                                  "created": bioc_first_month(ph, p),
                                  "updated": (d.get("git_last_commit_date") or "")[:10],
                                  "license": d.get("License", ""), "papers": set(), "local": set(),
                                  "relevant": True, "search_queries": f"Bioconductor {ver}"}
    # PyPI
    pypi_names = set(known.get("pypi", []))
    pypi_names |= {re.sub(r".*pypi\.org/project/", "", x["url"]).strip("/").split("/")[0]
                   for x in other_links if x["kind"] == "pypi"}
    if "pypi" not in srcs:
        pypi_names = set()
    for p in sorted(pypi_names):
        rr = ph.get(f"https://pypi.org/pypi/{p}/json")
        if rr is None or rr.status_code != 200:
            continue
        j = rr.json()
        ups = [f["upload_time"] for files in (j.get("releases") or {}).values() for f in files]
        info = j.get("info") or {}
        items[f"pypi:{p.lower()}"] = {"kind": "pypi", "name": info.get("name", p),
                                      "url": f"https://pypi.org/project/{info.get('name', p)}/",
                                      "description": info.get("summary") or "",
                                      "created": min(ups)[:10] if ups else "", "updated": max(ups)[:10] if ups else "",
                                      "license": info.get("license") or "", "papers": set(), "local": set(),
                                      "relevant": True, "search_queries": "known / linked"}
    # Zenodo records linked from papers
    for x in other_links:
        if x["kind"] != "zenodo" or "zenodo" not in srcs:
            continue
        m = re.search(r"zenodo\.org/(?:records?|deposit)/(\d+)", x["url"]) or re.search(r"zenodo\.(\d+)", x["url"])
        if not m:
            continue
        key = f"zenodo:{m.group(1)}"
        if key not in items:
            rr = ph.get(f"https://zenodo.org/api/records/{m.group(1)}")
            if rr is None or rr.status_code != 200:
                continue
            j = rr.json()
            md = j.get("metadata") or {}
            items[key] = {"kind": "zenodo", "name": md.get("title", key), "url": j.get("links", {}).get("html", x["url"]),
                          "description": re.sub(r"<[^>]+>", " ", md.get("description") or "")[:600],
                          "created": (j.get("created") or md.get("publication_date") or "")[:10],
                          "updated": (j.get("updated") or "")[:10], "doi": j.get("doi", ""),
                          "license": (md.get("license") or {}).get("id", ""),
                          "resource": (md.get("resource_type") or {}).get("type", ""),
                          "papers": set(), "local": set(), "relevant": True, "search_queries": ""}
        items[key]["papers"].add(x["uid"])

    # ---- decide inclusion, write -------------------------------------------
    all_rows, cat_rows = [], []
    for key, it in items.items():
        if it.get("missing") or str(it.get("fork")) == "True":
            reason, inc = ("repository gone" if it.get("missing") else "fork"), False
        elif it["papers"] and (it.get("relevant") or
                               (it.get("own_code") and int(it.get("stars") or 0) < OWN_CODE_MAX_STARS
                                and any(progress.get(u, {}).get("tier") in ("landmark", "core") for u in it["papers"]))):
            reason, inc = "linked from catalogued paper", True
        elif it["local"] and (it.get("relevant") or it.get("registry")):
            reason, inc = "linked from local project", True
        elif it["kind"] in ("cran", "bioconductor", "pypi", "zenodo"):
            reason, inc = "package index match", True
        elif it.get("relevant") and int(it.get("stars") or 0) >= args.min_stars:
            reason, inc = f"relevant search hit, >= {args.min_stars} stars", True
        else:
            reason, inc = "search hit below inclusion bar", False
        uid = make_uid(url=it["url"])
        papers = sorted(it["papers"])
        dois = [progress[u]["doi"] for u in papers if progress.get(u, {}).get("doi")]
        all_rows.append({"uid": uid, "kind": it["kind"], "name": it["name"], "url": it["url"],
                         "description": it.get("description", ""), "created": it.get("created", ""),
                         "updated": it.get("updated", ""), "stars": it.get("stars", ""),
                         "language": it.get("language", ""), "license": it.get("license", ""),
                         "topics": it.get("topics", ""), "archived": it.get("archived", ""),
                         "linked_from_papers": "; ".join(papers), "linked_from_local": "; ".join(sorted(it["local"])),
                         "search_queries": it.get("search_queries", ""), "included": inc, "reason": reason,
                         "doi": it.get("doi", ""), "checked_on": it.get("checked_on") or today()})
        if not inc:
            continue
        ann = clf.annotate({"title": it["name"], "abstract": f"{it.get('description', '')} {it.get('topics', '')}"})
        linked_tiers = {progress[u]["tier"] for u in papers if u in progress}
        tier = "core" if (linked_tiers & {"landmark", "core"} or it["local"]
                          or int(it.get("stars") or 0) >= 20 or it["kind"] in ("cran", "bioconductor", "pypi")) \
            else "related"
        rtype = {"github": "repository", "gitlab": "repository",
                 "cran": "software package", "bioconductor": "software package", "pypi": "software package",
                 "zenodo": "dataset" if it.get("resource") == "dataset" else "archive"}.get(it["kind"], "repository")
        venue = {"github": "GitHub", "cran": "CRAN", "bioconductor": "Bioconductor", "pypi": "PyPI",
                 "zenodo": "Zenodo"}.get(it["kind"], it["kind"])
        details = [f"stars={it['stars']}" if it.get("stars") not in (None, "") else "",
                   f"language={it['language']}" if it.get("language") else "",
                   f"last_update={it['updated']}" if it.get("updated") else "",
                   "archived" if str(it.get("archived")) == "True" else "",
                   ]
        date = it.get("created", "")
        found = {"linked from catalogued paper": "paper link", "linked from local project": "local seed"}.get(reason, "")
        cat_rows.append({
            "date": date, "year": date[:4], "resource_type": rtype, "tier": tier,
            "category": "software/resource", "modality": ann["modality"], "clocks": ann["clocks"],
            "species": ann["species"],
            "title": f"{it['name']}: {it.get('description', '')}".strip().rstrip(":")[:500],
            "authors": it["name"].split("/")[0] if "/" in it["name"] else "", "venue": venue,
            "doi": it.get("doi", ""), "url": it["url"], "related": "; ".join(dois[:20]),
            "license": it.get("license", ""), "open_access": "yes",
            "matched_groups": it.get("search_queries", ""),
            "found_by": "; ".join(filter(None, [found, "github search" if it.get("search_queries") and it["kind"] == "github" else "",
                                                "package index" if it["kind"] in ("cran", "bioconductor", "pypi") else ""])),
            "local_refs": "; ".join(sorted(it["local"])), "details": "; ".join(filter(None, details)),
            "uid": uid,
        })
    # renamed repositories reached through old links resolve to the same uid
    seen_uid: set[str] = set()
    all_rows = [r for r in all_rows if not (r["uid"] in seen_uid or seen_uid.add(r["uid"]))]
    seen_uid = set()
    cat_rows = [r for r in cat_rows if not (r["uid"] in seen_uid or seen_uid.add(r["uid"]))]
    # sources not refreshed this run keep their rows from the last build
    skipped = set(REPO_SOURCES) - srcs
    if skipped:
        done = {r["uid"] for r in all_rows}
        all_rows += [r for r in read_csv(REPOS / "repositories.csv")
                     if r.get("kind") in skipped and r["uid"] not in done]
        done = {r["uid"] for r in cat_rows}
        cat_rows += [r for r in read_csv(REPOS / "repositories_catalogue.csv")
                     if VENUE_KIND.get(r.get("venue", "")) in skipped and r["uid"] not in done]
    write_csv(REPOS / "repositories.csv", sorted(all_rows, key=lambda r: (r["kind"], r["name"].lower())), ALL_COLUMNS)
    write_csv(REPOS / "repositories_catalogue.csv", sorted(cat_rows, key=lambda r: r["date"] or "9999"),
              bp.COLUMNS)
    log(f"{len(all_rows)} repositories/packages seen; {len(cat_rows)} catalogued "
        f"{dict(collections.Counter(r['resource_type'] for r in cat_rows))}")


if __name__ == "__main__":
    main()
