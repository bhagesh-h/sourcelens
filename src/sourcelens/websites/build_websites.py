#!/usr/bin/env python3
"""Verify and date the websites, databases and data resources of the topic.

1. Every site in the websites section of the configuration is fetched; the first URL that answers
   is kept, with the page's own <title> and meta description.
2. Domains that several catalogued papers link to (candidate_websites.csv,
   written by src/sourcelens/pullrepos/mine_links.py) are fetched as well and kept
   when their home page matches websites.relevance (or repos.relevance) of
   the configuration.
3. Each kept site is dated by its earliest Internet Archive snapshot, which
   places it in the chronology of progress.csv.

Outputs
  <catalogue>/websites/websites.csv            every site checked, with status
  <catalogue>/websites/websites_catalogue.csv  reachable sites, progress.csv shape

Usage
  sourcelens __step websites [--min-papers 3]
"""

from __future__ import annotations

import argparse
import concurrent.futures as cf
import datetime as dt
import html
import re
import threading

from sourcelens import __version__
from sourcelens.buildcatalog import build_progress as bp  # noqa: E402
from sourcelens.common.agelit import WEBSITES, Http, config, config_root, log, make_uid, read_csv, today, write_csv

UA = f"Mozilla/5.0 (X11; Linux x86_64) sourcelens/{__version__}"
COLUMNS = ["name", "type", "url", "status", "page_title", "page_description", "first_archived",
           "source", "n_papers", "included", "note", "related_doi", "checked_on"]
RECHECK_DAYS = 30


def fetch(h: Http, url: str) -> tuple[int, str, str, str]:
    """(status, final url, title, description); status 0 on network failure."""
    r = h.get(url, timeout=40, tries=2, ok=(200,), allow_404=False)
    if r is None:
        return 0, url, "", ""
    if r.status_code != 200:
        return r.status_code, url, "", ""
    # servers that omit the charset get decoded as ISO-8859-1 by requests,
    # which garbles non-Latin titles; trust the detected encoding instead
    if (r.encoding or "").lower() in ("", "iso-8859-1"):
        r.encoding = r.apparent_encoding or "utf-8"
    text = r.text[:400_000]
    t = re.search(r"<title[^>]*>(.*?)</title>", text, re.I | re.S)
    d = re.search(r'<meta[^>]+name=["\']description["\'][^>]+content=["\']([^"\']*)', text, re.I) or \
        re.search(r'<meta[^>]+property=["\']og:description["\'][^>]+content=["\']([^"\']*)', text, re.I)
    clean = lambda s: " ".join(html.unescape(s).split())[:300] if s else ""  # noqa: E731
    return 200, r.url, clean(t.group(1) if t else ""), clean(d.group(1) if d else "")


def first_archived(h: Http, url: str) -> str:
    bare = re.sub(r"^https?://(www\.)?", "", url).rstrip("/")
    r = h.get("https://web.archive.org/cdx/search/cdx",
              params={"url": bare, "output": "json", "limit": "1", "fl": "timestamp"}, timeout=60, tries=3)
    try:
        rows = r.json() if r is not None and r.status_code == 200 else []
    except ValueError:
        rows = []
    if len(rows) > 1:
        ts = rows[1][0]
        return f"{ts[:4]}-{ts[4:6]}-{ts[6:8]}"
    return ""


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--min-papers", type=int, default=3)
    ap.add_argument("--workers", type=int, default=8, help="candidate sites checked at once (all different domains)")
    args = ap.parse_args()
    cutoff = (dt.date.today() - dt.timedelta(days=RECHECK_DAYS)).isoformat()
    h = Http(min_interval=1.0, headers={"User-Agent": UA})
    clf = bp.Classifier(config("classify"))
    previous = {r["url"]: r for r in read_csv(WEBSITES / "websites.csv")}
    out = []

    # a site cited by papers is kept when its title or description matches
    # websites.relevance, else repos.relevance
    pattern = config("websites").get("relevance") or (config_root().get("repos") or {}).get("relevance") or "(?!)"
    relevant_rx = re.compile(pattern)
    for site in config("websites").get("sites") or []:
        row = {"name": site["name"], "type": site.get("type", "website"), "source": "curated",
               "note": site.get("note", ""), "related_doi": site.get("related_doi", "")}
        tried = []
        for u in site["urls"]:
            st, final, title, desc = fetch(h, u)
            tried.append({"url": u, "status": st, "page_title": title, "page_description": desc})
            if st == 200:
                break
        # the first alternative that answered, else the primary URL
        row.update(next((t for t in tried if t["status"] == 200), tried[0]))
        out.append(row)

    for row in out:
        row["checked_on"] = today()
    seen = {re.sub(r"^https?://(www\.)?", "", r["url"]).split("/")[0] for r in out}
    todo = []
    for c in read_csv(WEBSITES / "candidate_websites.csv"):
        if int(c["n_papers"]) < args.min_papers or c["domain"].replace("www.", "") in seen:
            continue
        home = f"https://{c['domain']}/"
        old = previous.get(home)
        if old and old.get("checked_on", "") >= cutoff:
            # checked recently: reuse instead of fetching every site on every run
            # values come back from the CSV as text; the checks below need numbers
            out.append({**old, "n_papers": c["n_papers"], "status": int(old.get("status") or 0)})
        else:
            todo.append((c, home))

    local = threading.local()

    def check(item):
        c, home = item
        if not hasattr(local, "h"):
            local.h = Http(min_interval=1.0, headers={"User-Agent": UA})
        st, final, title, desc = fetch(local.h, home)
        return {"name": title or c["domain"], "type": "website (linked from papers)", "url": home, "status": st,
                "page_title": title, "page_description": desc, "source": "paper links",
                "n_papers": c["n_papers"], "note": c["example_context"][:200], "checked_on": today()}

    log(f"{len(todo)} candidate sites to check ({len(out)} curated or recently checked)")
    with cf.ThreadPoolExecutor(max_workers=args.workers) as ex:
        out.extend(ex.map(check, todo))

    cat = []
    for row in out:
        curated = row["source"] == "curated"
        relevant = curated or bool(relevant_rx.search(f"{row['page_title']} {row['page_description']}"))
        old = previous.get(row["url"], {})
        link, availability = row["url"], ""
        if row["status"] == 200:
            row["included"] = relevant
        elif curated and row["status"] in (401, 403, 429):
            row["included"] = True
            availability = f"site refuses automated requests (HTTP {row['status']}); open in a browser"
        elif curated:
            # offline: keep it if the Internet Archive has a copy
            row["first_archived"] = old.get("first_archived") or first_archived(h, row["url"])
            row["included"] = bool(row["first_archived"])
            if row["included"]:
                link = f"https://web.archive.org/web/{row['first_archived'].replace('-', '')}/{row['url']}"
                availability = "offline; link points to the earliest Internet Archive copy"
        else:
            row["included"] = False
        if not row["included"]:
            continue
        row["first_archived"] = row.get("first_archived") or old.get("first_archived") or first_archived(h, row["url"])
        date = row["first_archived"]
        ann = clf.annotate({"title": row["name"], "abstract": f"{row['page_title']} {row['page_description']} {row.get('note', '')}"})
        cat.append({
            "date": date, "year": date[:4],
            "resource_type": "database" if "database" in row["type"] else
            ("web calculator" if "calculator" in row["type"] else "website"),
            "tier": "core" if row["source"] == "curated" else "related",
            "category": "software/resource", "modality": ann["modality"], "entities": ann["entities"],
            "species": ann["species"], "title": f"{row['name']}: {row['page_title']}" if row["page_title"]
            and row["page_title"].lower() != row["name"].lower() else row["name"],
            "venue": row["type"], "url": link, "related": row.get("related_doi", ""), "open_access": "yes",
            "found_by": "curated list (websites section of the configuration)" if row["source"] == "curated" else "linked from papers",
            "details": "; ".join(filter(None, [availability,
                                                f"first archived {date} (domain age; may predate its use for this topic)" if date
                                                else "undated (no archive snapshot)",
                                                f"cited by {row['n_papers']} catalogued papers" if row.get("n_papers") else "",
                                                row.get("note", "")[:200]])),
            "uid": make_uid(url=row["url"]),
        })
    write_csv(WEBSITES / "websites.csv", out, COLUMNS)
    write_csv(WEBSITES / "websites_catalogue.csv", sorted(cat, key=lambda r: r["date"] or "9999"), bp.COLUMNS)
    log(f"{len(out)} sites checked; {len(cat)} catalogued; unreachable: "
        f"{[r['name'] for r in out if r['source'] == 'curated' and r['status'] != 200]}")


if __name__ == "__main__":
    main()
