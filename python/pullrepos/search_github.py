#!/usr/bin/env python3
"""Search GitHub for repositories on the topic (repos section of the configuration).

Runs every query in the repos section of the configuration through the repository search API and
keeps hits whose name, description or topics match the relevance pattern.
Uses GITHUB_TOKEN when python/run.sh passes one (30 searches/min instead of
10); the token is only read from the environment.

Output
  <output>/repos/github_search.csv   full_name, query hits, first/last seen

Usage
  python/run.sh python python/pullrepos/search_github.py
"""

from __future__ import annotations

import os
import re
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "common"))
from agelit import (config, REPOS, Http, load_yaml, log, read_csv,  # noqa: E402
                    search_window, search_window_overridden, today, write_csv)

API = "https://api.github.com/search/repositories"
OUT = REPOS / "github_search.csv"
COLUMNS = ["full_name", "url", "description", "topics", "stars", "created_at", "pushed_at",
           "language", "license", "fork", "archived", "queries", "relevant", "first_seen", "last_seen"]


def gh_http() -> Http:
    headers = {"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
    if os.environ.get("GITHUB_TOKEN"):
        headers["Authorization"] = f"Bearer {os.environ['GITHUB_TOKEN']}"
    return Http(min_interval=2.1 if os.environ.get("GITHUB_TOKEN") else 6.5, headers=headers)


def search(h: Http, q: str, created: str = "") -> list[dict]:
    out = []
    query = q if q.startswith("topic:") else f'"{q}" in:name,description,topics,readme'
    if created:
        query += f" created:{created}"
    for page in range(1, 11):
        r = h.get(API, params={"q": query, "per_page": 100, "page": page, "sort": "stars"}, ok=(200,))
        if r is None:
            break
        if r.status_code == 403:  # secondary rate limit
            wait = int(r.headers.get("Retry-After") or 60)
            log(f"rate limited; sleeping {wait}s")
            time.sleep(wait)
            continue
        items = r.json().get("items", [])
        out.extend(items)
        if len(items) < 100:
            break
    return out


def main() -> None:
    cfg = config("repos")
    rel = re.compile(cfg["relevance"])
    h = gh_http()
    log(f"GitHub token: {'yes' if os.environ.get('GITHUB_TOKEN') else 'no (slow, 10 searches/min)'}")
    rows = {r["full_name"].lower(): r for r in read_csv(OUT)}
    run = today()
    # a date range given on the CLI limits repositories by creation date
    created = "{}..{}".format(*search_window()) if search_window_overridden() else ""
    if created:
        log(f"repositories created {created}")
    for q in cfg["github_queries"]:
        items = search(h, q, created)
        kept = 0
        for it in items:
            key = it["full_name"].lower()
            text = " ".join([it["name"], it.get("description") or "", " ".join(it.get("topics") or [])])
            relevant = bool(rel.search(text))
            row = rows.get(key) or {"full_name": it["full_name"], "queries": "", "first_seen": run}
            row.update({
                "url": it["html_url"], "description": (it.get("description") or "").replace("\n", " "),
                "topics": "; ".join(it.get("topics") or []), "stars": it.get("stargazers_count", 0),
                "created_at": it.get("created_at", ""), "pushed_at": it.get("pushed_at", ""),
                "language": it.get("language") or "",
                "license": ((it.get("license") or {}).get("spdx_id") or ""),
                "fork": it.get("fork", False), "archived": it.get("archived", False),
                "relevant": relevant, "last_seen": run,
            })
            qs = set(filter(None, row["queries"].split("; "))) | {q}
            row["queries"] = "; ".join(sorted(qs))
            rows[key] = row
            kept += relevant
        log(f"{q!r}: {len(items)} hits, {kept} relevant")
        write_csv(OUT, sorted(rows.values(), key=lambda r: r["full_name"].lower()), COLUMNS)
    n_rel = sum(1 for r in rows.values() if str(r["relevant"]) == "True")
    log(f"done: {len(rows)} repositories, {n_rel} relevant")


if __name__ == "__main__":
    main()
