"""The run report: reports/runreport_<stamp>.html, one self-contained page.

Written after every update, retry, download and dry run (and by `sourcelens
report`): the logo, the date, time and version, the run's steps, the
catalogue's numbers and charts, and a searchable, filterable table of every
row. The page is the shared template sourcelens/assets/report.html (the same
file as cmd/sourcelens/assets/report.html, used by the Go twin report.go)
with the data embedded as gzip-compressed, base64-encoded JSON.
"""

from __future__ import annotations

import base64
import collections
import datetime as dt
import gzip
import html
import json
from importlib import resources
from pathlib import Path

from sourcelens import __version__
from sourcelens.buildcatalog import overview
from sourcelens.common import agelit
from sourcelens.pullliturature import attachments

COLUMNS = ["date", "added_on", "year", "resource_type", "tier", "category", "title", "authors", "venue", "doi",
           "url", "cited_by", "open_access", "license", "fulltext_status", "fulltext_reason", "fulltext_pdf",
           "fulltext_md", "attachments", "code_links", "modality", "entities", "species", "summary", "summary_from",
           "keywords", "fulltext_sources", "pending", "found_by", "related", "details", "uid", "notes", "user_tags"]
CUT = {"summary": 500, "keywords": 300, "code_links": 300, "details": 200, "entities": 300}


def asset(name: str) -> bytes:
    return resources.files("sourcelens").joinpath(f"assets/{name}").read_bytes()


def last_line(path: Path) -> str:
    try:
        with open(path, "rb") as fh:
            fh.seek(0, 2)
            fh.seek(max(0, fh.tell() - 4000))
            lines = [line for line in fh.read().decode("utf-8", "replace").splitlines() if line.strip()]
        return lines[-1].strip()[:150] if lines else ""
    except OSError:
        return ""


def steps(logdir: Path | None) -> list[dict]:
    """The steps of a run from its logs/cli_<stamp>/summary.txt."""
    out = []
    if logdir is None or not (logdir / "summary.txt").is_file():
        return out
    for line in (logdir / "summary.txt").read_text(encoding="utf-8").splitlines():
        parts = line.split("\t")
        if len(parts) < 4:
            continue
        out.append({"name": parts[0], "ok": parts[1] == "rc=0", "minutes": parts[2].replace(" min", ""),
                    "last": last_line(logdir / parts[3])})
    return out


def attachment_stats() -> list[dict]:
    """Per file extension: files known and files downloaded, most files first."""
    total: collections.Counter = collections.Counter()
    ok: collections.Counter = collections.Counter()
    for rows in attachments.read_index().values():
        for a in rows:
            if a.get("file"):
                total[a.get("ext") or "?"] += 1
                if a.get("status") == "ok":
                    ok[a.get("ext") or "?"] += 1
    return [{"ext": e, "total": n, "ok": ok.get(e, 0)}
            for e, n in sorted(total.items(), key=lambda kv: (-kv[1], kv[0]))]


def data(label: str, started: dt.datetime, logdir: Path | None, command: str, topic: str, rc: int,
         impl: str, finished: dt.datetime) -> dict:
    rows = overview.rows()
    st = steps(logdir)
    failed = sum(1 for s in st if not s["ok"])
    outcome = "completed" if rc == 0 and not failed else (
        f"{failed} step{'s' if failed != 1 else ''} failed" if failed else "failed")
    meta = {"title": f"sourcelens run report: {topic}", "topic": topic, "label": label, "command": command,
            "folder": str(agelit.RESEARCH), "version": __version__, "impl": impl,
            "started": started.strftime("%Y-%m-%d %H:%M:%S"), "finished": finished.strftime("%Y-%m-%d %H:%M:%S"),
            "minutes": f"{(finished - started).total_seconds() / 60:.1f}", "outcome": outcome,
            "run_date": started.strftime("%Y-%m-%d"),
            "logs": agelit.research_rel(logdir) if logdir is not None and logdir.is_dir() else "",
            "summary_max": CUT["summary"]}
    table = [[(r.get(c) or "")[:CUT[c]] if c in CUT else (r.get(c) or "") for c in COLUMNS] for r in rows]
    return {"meta": meta, "steps": st, "columns": COLUMNS, "rows": table, "attachments": attachment_stats()}


def page(d: dict) -> str:
    raw = json.dumps(d, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    payload = base64.b64encode(gzip.compress(raw, compresslevel=9, mtime=0)).decode("ascii")
    logo = "data:image/png;base64," + base64.b64encode(asset("logo.png")).decode("ascii")
    return (asset("report.html").decode("utf-8")
            .replace("__SL_TITLE__", html.escape(d["meta"]["title"]))
            .replace("__SL_LOGO__", logo)
            .replace("__SL_DATA__", payload))


def write(label: str, stamp: str, started: dt.datetime, logdir: Path | None, command: str, topic: str,
          rc: int, impl: str) -> Path:
    d = data(label, started, logdir, command, topic, rc, impl, dt.datetime.now())
    path = agelit.RESEARCH / "reports" / f"runreport_{stamp}.html"
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    with open(tmp, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(page(d))
    tmp.replace(path)
    return path
