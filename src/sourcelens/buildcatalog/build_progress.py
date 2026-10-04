#!/usr/bin/env python3
"""Build <catalogue>/progress.csv: every resource, oldest first.

Merges the record store (articles and preprints from the literature searches
and the local seeds), repositories, packages and websites into one catalogue,
annotated with the classify section of the configuration.

INCREMENTAL BY DESIGN. progress.csv is never rebuilt from scratch:
  * a row whose uid is already in the file keeps its `added_on` date and
    its hand-edited columns (`notes`, `user_tags`) untouched;
  * a resource not yet in the file is added with `added_on` = today;
  * a row that no longer comes out of the pipeline (search terms changed,
    source withdrew it) is kept and flagged in `status`, never deleted;
  * the file is then re-sorted by date, so it stays chronological.
Every run writes <catalogue>/changelog/added_<date>.csv with only the rows it
added, and appends one line to <catalogue>/changelog/runs.csv.

Outputs
  <catalogue>/progress.csv                     the catalogue (scope: focused + seeds)
  <catalogue>/corpus/broad_hits.csv            records matched only by broad terms
  <catalogue>/corpus/before_window.csv         seeds published before the window
  <catalogue>/corpus/offtopic_seeds.csv        local seeds outside the topic
  <catalogue>/corpus/references.csv            full citation fields for export
  <catalogue>/changelog/added_<date>.csv, runs.csv
  <catalogue>/summary/*.csv, stats.json        counts

Usage (normally run by sourcelens update)
  sourcelens __step catalogue
"""

from __future__ import annotations

import collections
import re
from pathlib import Path

from sourcelens.common.agelit import (  # noqa: E402
    CORPUS,
    FULLTEXT,
    REPOS,
    RESEARCH,
    SEEDS,
    WEBSITES,
    config,
    log,
    query_window,
    read_csv,
    today,
    write_csv,
    write_json,
)
from sourcelens.common.store import Store  # noqa: E402

PROGRESS = RESEARCH / "progress.csv"
CHANGELOG = RESEARCH / "changelog"
SUMMARY = RESEARCH / "summary"

COLUMNS = [
    "added_on", "date", "year", "resource_type", "tier", "category", "modality",
    "entities", "species", "title", "authors", "venue", "doi", "pmid", "pmcid",
    "url", "code_links", "related", "open_access", "license", "fulltext_status",
    "fulltext_pdf", "fulltext_md", "fulltext_txt", "metadata_file", "cited_by",
    "details", "matched_groups", "found_by", "local_refs", "status", "uid", "notes", "user_tags",
]
USER_COLUMNS = {"notes", "user_tags"}
KEEP_ON_UPDATE = {"added_on"} | USER_COLUMNS
TIER_ORDER = {"landmark": 0, "core": 1, "related": 2}


# --------------------------------------------------------------------------
# classification
# --------------------------------------------------------------------------

class Classifier:
    def __init__(self, cfg: dict):
        f = re.I
        self.modality = {k: re.compile(v, f) for k, v in (cfg.get("modality") or {}).items()}
        self.species = {k: re.compile(v, f) for k, v in (cfg.get("species") or {}).items()}
        self.category = []
        for rule in cfg.get("category") or []:
            self.category.append({
                "name": rule["name"],
                "pub_types": re.compile(rule["pub_types"], f) if rule.get("pub_types") else None,
                "title": re.compile(rule["title"], f) if rule.get("title") else None,
                "text": re.compile(rule["text"], f) if rule.get("text") else None,
            })
        self.core_title = re.compile(cfg["core_title_terms"], f) if cfg.get("core_title_terms") else None
        # catalogues configured before 1.0 name registry roles clock_*
        roles = set(cfg.get("landmark_roles") or [])
        self.landmark_roles = roles | {r.replace("clock_", "registry_", 1) for r in roles}
        self.default_category = cfg.get("default_category") or "application/association"
        self.origin_category = cfg.get("origin_category") or "method development"
        self.core_categories = list(cfg.get("core_categories") or
                                    [self.origin_category, "benchmark/comparison", "review", "software/resource"])
        entities = cfg.get("entities")
        if entities is None:
            entities = cfg.get("clock_names") or {}  # name used before 1.0
        self.entities = {k: re.compile(r"(?<![\w-])(?:" + v + r")(?![\w-])" if not v.startswith("(?i)")
                                       else "(?i)(?<![\\w-])(?:" + v[4:] + ")(?![\\w-])")
                         for k, v in (entities or {}).items()}

    def annotate(self, rec: dict) -> dict:
        title = rec.get("title") or ""
        text = " ".join(str(rec.get(k) or "") for k in ("title", "abstract", "keywords", "mesh"))
        pubtypes = rec.get("pub_types") or ""
        mods = [k for k, rx in self.modality.items() if rx.search(text)]
        sp = [k for k, rx in self.species.items() if rx.search(text) and k != "human"]
        if ("human" in self.species and self.species["human"].search(text)) or "Humans" in (rec.get("mesh") or ""):
            sp = ["human"] + sp
        cat = self.default_category
        for rule in self.category:
            if ((rule["pub_types"] and rule["pub_types"].search(pubtypes))
                    or (rule["title"] and rule["title"].search(title))
                    or (rule["text"] and rule["text"].search(text))):
                cat = rule["name"]
                break
        ents = [k for k, rx in self.entities.items() if rx.search(text)]
        return {"modality": "; ".join(mods), "species": "; ".join(sp), "category": cat,
                "entities": "; ".join(ents),
                "_core_title": bool(self.core_title and self.core_title.search(title))}


# --------------------------------------------------------------------------
# rows
# --------------------------------------------------------------------------

def resource_type(rec: dict, category: str) -> str:
    pt = (rec.get("pub_types") or "").lower()
    if rec.get("is_preprint") or rec.get("source_db") == "PPR" or "preprint" in pt:
        return "preprint"
    if category == "review":
        return "review"
    for key in ("dataset", "software", "book chapter", "conference paper", "thesis", "report"):
        if key in pt:
            return key
    return "article"


def best_date(rec: dict) -> str:
    d = (rec.get("pub_date") or "").strip()
    if re.fullmatch(r"\d{4}-\d{2}-\d{2}", d):
        return d
    y = (rec.get("pub_year") or d[:4]).strip()
    return f"{y}-01-01" if re.fullmatch(r"\d{4}", y) else ""


SERVER_NAMES = {"biorxiv": "bioRxiv", "medrxiv": "medRxiv", "research square": "Research Square",
                "preprints.org": "Preprints.org", "ssrn": "SSRN", "psyarxiv": "PsyArXiv"}


def venue_name(v: str) -> str:
    """One spelling per preprint server ("bioRxiv : the preprint server for biology" -> "bioRxiv")."""
    low = (v or "").strip().lower()
    for key, name in SERVER_NAMES.items():
        if low.startswith(key):
            return name
    return v


def first_authors(a: str, n: int = 3) -> str:
    parts = [p.strip() for p in (a or "").rstrip(".").split(",") if p.strip()]
    return ", ".join(parts[:n]) + (" et al." if len(parts) > n else "")


def article_row(rec, ann, groups, found_by, seeds, ft, links, cites) -> dict:
    uid = rec["uid"]
    doi = rec.get("doi") or ""
    url = f"https://doi.org/{doi}" if doi else (
        f"https://pubmed.ncbi.nlm.nih.gov/{rec['pmid']}/" if rec.get("pmid") else rec.get("url", ""))
    f = ft.get(uid, {})
    folder = f.get("folder", "")

    def fp(name: str, flag: str) -> str:
        return f"{folder}/{name}" if folder and str(f.get(flag)) == "True" else ""

    seed_info = seeds.get(doi, {})
    cited = max(int(rec.get("cited_by") or 0), int(cites.get(uid, 0) or 0))
    oa = "yes" if (rec.get("is_open_access") or f.get("status") in ("ok", "partial")) else (
        "unknown" if not f else "no")
    date = best_date(rec)
    return {
        "date": date, "year": date[:4], "resource_type": resource_type(rec, ann["category"]),
        "category": ann["category"], "modality": ann["modality"],
        "entities": "; ".join(filter(None, [seed_info.get("registry_ids", ""), ann["entities"]])),
        "species": ann["species"], "title": rec.get("title", ""),
        "authors": first_authors(rec.get("authors", "")), "venue": venue_name(rec.get("journal", "")),
        "doi": doi, "pmid": rec.get("pmid", ""), "pmcid": rec.get("pmcid", ""), "url": url,
        "code_links": "; ".join(links.get(uid, [])), "related": "",
        "open_access": oa, "license": f.get("license") or rec.get("license", ""),
        "fulltext_status": f.get("status", ""),
        "fulltext_pdf": fp("paper.pdf", "has_pdf"), "fulltext_md": fp("paper.md", "has_md"),
        "fulltext_txt": fp("paper.txt", "has_txt"),
        "metadata_file": f"{folder}/metadata.json" if folder else "",
        "cited_by": cited or "", "matched_groups": "; ".join(sorted(groups)),
        "found_by": "; ".join(sorted(found_by)), "local_refs": seed_info.get("refs", ""),
        "uid": uid,
    }


def load_seeds() -> dict:
    """doi -> {roles, refs, registry_ids} from the reference folders."""
    out: dict[str, dict] = {}
    for s in read_csv(SEEDS / "local_seeds.csv"):
        d = out.setdefault(s["doi"], {"roles": set(), "refs": set(), "ids": {}})
        role = s["role"].replace("clock_", "registry_", 1)  # seeds written before 1.0
        d["roles"].add(role)
        d["refs"].add(f"{s['source_repo']}:{role}")
        rid = s.get("registry_id") or s.get("clock_name") or ""
        if rid:
            d["ids"].setdefault(s["source_repo"], set()).add(rid)
    for d in out.values():
        d["refs"] = "; ".join(sorted(d["refs"]))
        d["registry_ids"] = "; ".join(f"{repo} registry: " + ", ".join(sorted(ids))
                                      for repo, ids in sorted(d["ids"].items()))
    return out


def extra_rows(path: Path) -> list[dict]:
    """Rows produced by the repository / website harvesters, already in catalogue shape."""
    return [r for r in read_csv(path) if r.get("uid")]


# --------------------------------------------------------------------------
# main
# --------------------------------------------------------------------------

def main() -> None:
    cfg = config("classify")
    clf = Classifier(cfg)
    start, end = query_window()
    store = Store()
    log(f"store: {len(store)} records; window {start} .. {end}")

    groups: dict[str, set] = collections.defaultdict(set)
    scopes: dict[str, set] = collections.defaultdict(set)
    found: dict[str, set] = collections.defaultdict(set)
    for h in read_csv(CORPUS / "search_hits.csv"):
        uid = store.find({"uid": h["uid"]}) or h["uid"]
        groups[uid].add(h["group"])
        scopes[uid].add(h["scope"])
        found[uid].add(h["source"])

    seeds = load_seeds()
    ft = {r["uid"]: r for r in read_csv(FULLTEXT / "fulltext_index.csv")}
    links: dict[str, list] = collections.defaultdict(list)
    for r in read_csv(REPOS / "paper_links.csv"):
        if r["url"] not in links[r["uid"]]:
            links[r["uid"]].append(r["url"])
    openalex = {r["uid"]: r for r in read_csv(CORPUS / "citations.csv")}
    cites = {u: r.get("cited_by", 0) for u, r in openalex.items()}
    # preprint <-> published version, both directions
    pp_links: dict[str, str] = {}
    for r in read_csv(CORPUS / "preprint_links.csv"):
        if r.get("published_doi"):
            pp_links[r["preprint_doi"]] = f"published as {r['published_doi']}"
            pp_links[r["published_doi"]] = f"preprint {r['preprint_doi']}"

    rows, broad, pre, offtopic = {}, [], [], []
    for rec in store.values():
        uid = rec["uid"]
        ann = clf.annotate(rec)
        doi = rec.get("doi", "")
        sd = seeds.get(doi) if doi else None
        roles = sd["roles"] if sd else set()
        is_landmark = bool(roles & clf.landmark_roles)
        focused = "focused" in scopes.get(uid, set())
        fb = set(found.get(uid, set()))
        if sd:
            fb.add("local seed")
        if "registry_origin" in roles:
            # the paper that introduced a registry entry is development work by definition
            ann = {**ann, "category": clf.origin_category}
        row = article_row(rec, ann, groups.get(uid, set()), fb, seeds, ft, links, cites)
        # the indexes give some records only a year (stored YYYY-01-01);
        # OpenAlex's exact date replaces it when it falls in the same year
        oa_date = openalex.get(uid, {}).get("publication_date", "")
        if row["date"].endswith("-01-01") and oa_date[:4] == row["date"][:4] and oa_date != row["date"]:
            row["date"] = oa_date
        if doi in pp_links:
            row["related"] = pp_links[doi]
        elif rec.get("related_doi"):  # arXiv entries that name their journal version
            row["related"] = f"published as {rec['related_doi']}"
        if not row["date"]:
            row["status"] = "undated"
        in_window = not row["date"] or start <= row["date"] <= end
        if is_landmark:
            row["tier"] = "landmark"
        elif focused and (ann["_core_title"] or ann["category"] in clf.core_categories):
            row["tier"] = "core"
        elif focused:
            row["tier"] = "related"
        elif sd and ann["_core_title"]:
            row["tier"] = "related"
        else:
            row["tier"] = ""
        if row["tier"] and not in_window:
            pre.append(row)
        elif row["tier"]:
            rows[uid] = row
        elif sd:
            offtopic.append(row)
        elif "broad" in scopes.get(uid, set()):
            broad.append(row)

    for path in (REPOS / "repositories_catalogue.csv", WEBSITES / "websites_catalogue.csv"):
        for r in extra_rows(path):
            rows[r["uid"]] = {k: r.get(k, "") for k in COLUMNS if k not in KEEP_ON_UPDATE}

    # ---- incremental merge with the existing progress.csv -----------------
    existing = {r["uid"]: r for r in read_csv(PROGRESS)}
    run = today()
    added = []
    merged = {}
    for uid, new in rows.items():
        old = existing.get(uid)
        if old is None:
            new = {**new, "added_on": run}
            added.append(new)
        else:
            new = {**new, **{k: old.get(k, "") for k in KEEP_ON_UPDATE}}
        new.setdefault("status", "")
        merged[uid] = new
    kept = 0
    for uid, old in existing.items():
        if uid not in merged:
            old["status"] = "no longer matched by pipeline (kept)"
            merged[uid] = old
            kept += 1

    def sort_key(r):
        return (r.get("date") or "9999", TIER_ORDER.get(r.get("tier"), 9), r.get("title", "").lower())

    final = sorted(merged.values(), key=sort_key)
    write_csv(PROGRESS, final, COLUMNS)
    CHANGELOG.mkdir(parents=True, exist_ok=True)
    if added:
        # update_all.sh builds several times a day; the day's file collects
        # every pass's additions rather than keeping only the last pass
        day_file = CHANGELOG / f"added_{run}.csv"
        today_rows = {r["uid"]: r for r in read_csv(day_file)}
        today_rows.update({r["uid"]: r for r in added})
        write_csv(day_file, sorted(today_rows.values(), key=sort_key), COLUMNS)
    runs = read_csv(CHANGELOG / "runs.csv")
    runs.append({"run_date": run, "window_end": end, "rows_total": len(final), "rows_added": len(added),
                 "rows_kept_unmatched": kept, "broad_only": len(broad), "store_records": len(store)})
    write_csv(CHANGELOG / "runs.csv", runs, list(runs[-1].keys()))

    write_references(final, store)
    write_csv(CORPUS / "broad_hits.csv", sorted(broad, key=sort_key), COLUMNS)
    write_csv(CORPUS / "before_window.csv", sorted(pre, key=sort_key), COLUMNS)
    write_csv(CORPUS / "offtopic_seeds.csv", sorted(offtopic, key=sort_key), COLUMNS)
    summarise(final)
    log(f"progress.csv: {len(final)} rows ({len(added)} added this run, {kept} kept though unmatched); "
        f"broad-only {len(broad)}, before the window {len(pre)}, off-topic seeds {len(offtopic)}")


REF_COLUMNS = ["uid", "resource_type", "date", "year", "authors", "title", "venue", "volume", "issue",
               "pages", "doi", "pmid", "pmcid", "url"]


def write_references(rows: list[dict], store: Store) -> None:
    """<catalogue>/corpus/references.csv: full citation fields for every catalogue row.

    progress.csv keeps only the first three authors; reference export (both
    sourcelens export) needs all of them plus volume / issue / pages, so they are
    written here once per build instead of reading the record store.
    """
    out = []
    for r in rows:
        rec = store.get(r["uid"]) or {}
        out.append({
            "uid": r["uid"], "resource_type": r.get("resource_type", ""), "date": r.get("date", ""),
            "year": r.get("year", ""),
            "authors": (rec.get("authors") or r.get("authors", "")).strip().rstrip("."),
            "title": r.get("title", ""), "venue": r.get("venue", ""),
            "volume": rec.get("volume", "") or "", "issue": rec.get("issue", "") or "",
            "pages": rec.get("pages", "") or "", "doi": r.get("doi", ""), "pmid": r.get("pmid", ""),
            "pmcid": r.get("pmcid", ""), "url": r.get("url", ""),
        })
    write_csv(CORPUS / "references.csv", out, REF_COLUMNS)


def summarise(rows: list[dict]) -> None:
    SUMMARY.mkdir(parents=True, exist_ok=True)
    by_year = collections.Counter((r["year"], r["resource_type"]) for r in rows)
    years = sorted({y for y, _ in by_year})
    types = sorted({t for _, t in by_year})
    write_csv(SUMMARY / "by_year_type.csv",
              [{"year": y, **{t: by_year.get((y, t), 0) for t in types}} for y in years], ["year"] + types)
    mods = collections.Counter()
    ymods = collections.Counter()
    for r in rows:
        for m in filter(None, (x.strip() for x in r.get("modality", "").split(";"))):
            mods[m] += 1
            ymods[(r["year"], m)] += 1
    mlist = [m for m, _ in mods.most_common()]
    write_csv(SUMMARY / "by_year_modality.csv",
              [{"year": y, **{m: ymods.get((y, m), 0) for m in mlist}} for y in years], ["year"] + mlist)
    cats = collections.Counter(r["category"] for r in rows)
    tiers = collections.Counter(r["tier"] for r in rows)
    ents = collections.Counter()
    for r in rows:
        for c in filter(None, (x.strip() for x in re.sub(r"^[^;]* registry: [^;]*;?", "", r.get("entities") or "").split(";"))):
            ents[c] += 1
    ft = collections.Counter(r.get("fulltext_status") or "not tried" for r in rows)
    write_json(SUMMARY / "stats.json", {
        "rows": len(rows), "tiers": dict(tiers), "categories": dict(cats.most_common()),
        "modalities": dict(mods.most_common()), "entity_mentions": dict(ents.most_common(60)),
        "fulltext": dict(ft), "resource_types": dict(collections.Counter(r["resource_type"] for r in rows)),
    })


if __name__ == "__main__":
    main()
