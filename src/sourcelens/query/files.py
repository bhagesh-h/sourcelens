"""`sourcelens files`: list, copy, move or delete the files of a catalogue.

Files are the full texts (paper.pdf, paper.md, paper.txt, paper.jats.xml) and
the attachments (figures, tables, supplementary files) of the catalogue's
papers. Pick the papers with any query filter (or --in a CSV), and the files
with --ext, --name, --kind and --status. Then list them, save the list (--out),
copy them (--copy-to), move them (--move-to) or delete them (--delete --yes).

Moved and deleted files are recorded in fulltext/fulltext_index.csv,
fulltext/attachments_index.csv, the paper's metadata.json and progress.csv, so
updates do not download them again. The Go twin is cmd/sourcelens/files.go.
"""

from __future__ import annotations

import json
import re
import shutil
import sys
from pathlib import Path

from sourcelens.common import agelit
from sourcelens.common.agelit import read_csv, research_file, slug, today, write_csv, write_json
from sourcelens.common.flags import parse_flags
from sourcelens.common.settings import ProjectError, resolve_project
from sourcelens.pullliturature import attachments
from sourcelens.query import catalog

FILE_COLUMNS = ["uid", "title", "kind", "file", "ext", "bytes", "status", "path", "label", "caption", "url"]
PAPER_FILES = [("pdf", "paper.pdf"), ("md", "paper.md"), ("txt", "paper.txt"), ("xml", "paper.jats.xml")]
KINDS = {"paper", "figure", "table", "supplementary"}
SPEC = {**catalog.FILTER_SPEC, "name": ("", str), "kind": ("", str), "status": ("ok", str),
        "copy-to": ("", str), "move-to": ("", str), "delete": (False, bool), "yes": (False, bool),
        "flat": (False, bool), "limit": (50, int), "out": ("", str)}

HELP = """sourcelens files [filters] [file options] [action]: list, copy, move or delete
the full texts and attachments of a catalogue

  sourcelens files --ext xlsx,csv                             every downloaded spreadsheet
  sourcelens files --kind figure --title "epigenetic clock"   figures of matching papers
  sourcelens files --ext pptx,docx --copy-to ~/slides
  sourcelens files --in exports/picked.csv --kind paper --ext pdf --copy-to ~/to-read --flat
  sourcelens files --status listed --ext xlsx                 spreadsheets offered but not downloaded
  sourcelens files --kind figure --range 5y --move-to /data/figures
  sourcelens files --ext txt --delete --yes

""" + catalog.PROJECT_HELP + "\n" + catalog.FILTER_HELP + """
files
  --ext LIST          file extensions: pdf,md,txt,xml,jpg,png,xlsx,csv,docx,pptx,zip,...
  --name RE           regex over the file name, label and caption
  --kind LIST         paper (full texts), figure, table, supplementary; attachment = the last three
  --status LIST       ok (on disk, default), listed (offered, not downloaded), skipped,
                      failed, moved, deleted; all for every file

action (one; default: list the files)
  --limit N           files printed (default 50; --out gets all)
  --out FILE          CSV of all matching files; relative paths go to <catalogue>/exports/
  --copy-to DIR       copy the files to DIR/<paper>/<file> (attachments in DIR/<paper>/attachments/)
  --move-to DIR       the same, then remove them from the catalogue; updates will not fetch them again
  --delete            delete the files from the catalogue (needs --yes); updates will not fetch them again
  --yes               confirm --delete
  --flat              copy or move to DIR/<paper>__<file>, without subfolders
"""


def plural(n: int, word: str) -> str:
    return f"{n} {word}{'' if n == 1 else 's'}"


def collect(rows: list[dict]) -> list[dict]:
    """Every file (full text or attachment) of the given catalogue rows."""
    index = {r["uid"]: r for r in read_csv(agelit.FULLTEXT / "fulltext_index.csv")}
    atts = attachments.read_index()
    out = []
    for r in rows:
        uid, title = r.get("uid", ""), r.get("title", "")
        folder = (index.get(uid) or {}).get("folder", "")
        if folder:
            for ext, name in PAPER_FILES:
                rel = f"{folder}/{name}"
                p = research_file(rel)
                if p.is_file():
                    out.append({"uid": uid, "title": title, "kind": "paper", "file": name, "ext": ext,
                                "bytes": p.stat().st_size, "status": "ok", "path": rel, "label": "",
                                "caption": "", "url": ""})
        for a in atts.get(uid, []):
            if a.get("file"):
                out.append({"uid": uid, "title": title, "kind": a.get("kind", ""), "file": a["file"],
                            "ext": a.get("ext", ""), "bytes": a.get("bytes", ""), "status": a.get("status", ""),
                            "path": a.get("path", ""), "label": a.get("label", ""), "caption": a.get("caption", ""),
                            "url": a.get("url", "")})
    return out


def pick(files: list[dict], o) -> list[dict]:
    exts = {e.strip().lower().lstrip(".") for e in o.ext.split(",") if e.strip()}
    kinds = {k.strip().lower() for k in o.kind.split(",") if k.strip()}
    if "attachment" in kinds or "attachments" in kinds:
        kinds |= {"figure", "table", "supplementary"}
    unknown = kinds - KINDS - {"attachment", "attachments"}
    if unknown:
        raise ValueError(f"--kind must be paper, figure, table, supplementary or attachment, not {sorted(unknown)[0]}")
    statuses = {x.strip().lower() for x in o.status.split(",") if x.strip()}
    try:
        nrx = re.compile(o.name, re.I) if o.name else None
    except re.error:
        raise ValueError("invalid regular expression for --name") from None
    out = []
    for f in files:
        if exts and f["ext"].lower() not in exts:
            continue
        if kinds and f["kind"] not in kinds:
            continue
        if "all" not in statuses and f["status"] not in statuses:
            continue
        if nrx and not nrx.search(" ".join([f["file"], f["label"], f["caption"]])):
            continue
        out.append(f)
    return out


def destination(base: Path, f: dict, flat: bool) -> Path:
    paper = slug(f["uid"])
    if flat:
        return base / f"{paper}__{f['file']}"
    if f["kind"] == "paper":
        return base / paper / f["file"]
    return base / paper / "attachments" / f["file"]


def record(done: list[dict], action: str, where: str) -> None:
    """Moved or deleted files: indexes, metadata.json and progress.csv say so."""
    from sourcelens.buildcatalog.build_progress import COLUMNS
    from sourcelens.pullliturature.fetch_fulltext import INDEX_COLUMNS
    status = "moved" if action == "move" else "deleted"
    reason = f"moved to {where} by sourcelens files" if action == "move" else "deleted by sourcelens files"
    by_uid: dict[str, list[dict]] = {}
    for f in done:
        by_uid.setdefault(f["uid"], []).append(f)

    atts = attachments.read_index()
    index = {r["uid"]: r for r in read_csv(agelit.FULLTEXT / "fulltext_index.csv")}
    for uid, fs in by_uid.items():
        names = {f["file"]: f for f in fs if f["kind"] != "paper"}
        for a in atts.get(uid, []):
            if a.get("file") in names:
                a.update(status=status, path=names[a["file"]].get("dest", ""), reason=reason, checked_on=today())
        idx = index.get(uid)
        if idx and idx.get("folder"):
            folder = research_file(idx["folder"])
            for ext, name in PAPER_FILES:
                idx[f"has_{ext}"] = (folder / name).is_file()
            if not any(idx[f"has_{ext}"] for ext, _ in PAPER_FILES) and any(f["kind"] == "paper" for f in fs):
                idx.update(status="removed", reason=reason)
            if idx.get("attachments") and uid in atts:
                idx["attachments"] = f"{sum(1 for a in atts[uid] if a.get('status') == 'ok')}/" \
                                     f"{sum(1 for a in atts[uid] if a.get('file'))}"
            meta_path = folder / "metadata.json"
            if meta_path.is_file():
                try:
                    meta = json.loads(meta_path.read_text(encoding="utf-8"))
                except ValueError:
                    meta = None
                if meta is not None:
                    ft = meta.setdefault("fulltext", {})
                    gone = {f["file"] for f in fs if f["kind"] == "paper"}
                    ft["files"] = {k: v for k, v in (ft.get("files") or {}).items() if k not in gone}
                    for a in ft.get("attachments") or []:
                        if a.get("file") in names:
                            a["status"] = status
                    if idx.get("status") == "removed":
                        ft.update(status="removed", reason=reason)
                    write_json(meta_path, meta)
    attachments.write_index(atts)
    write_csv(agelit.FULLTEXT / "fulltext_index.csv", sorted(index.values(), key=lambda x: x["uid"]), INDEX_COLUMNS)

    progress = read_csv(agelit.RESEARCH / "progress.csv")
    cols = {"pdf": "fulltext_pdf", "md": "fulltext_md", "txt": "fulltext_txt"}
    for r in progress:
        fs = by_uid.get(r.get("uid", ""))
        if not fs:
            continue
        for f in fs:
            if f["kind"] == "paper" and f["ext"] in cols:
                r[cols[f["ext"]]] = ""
        idx = index.get(r["uid"]) or {}
        if idx.get("status") == "removed":
            r["fulltext_status"], r["fulltext_reason"] = "removed", reason
        r["attachments"] = attachments.summary(atts.get(r["uid"], []))
    write_csv(agelit.RESEARCH / "progress.csv", progress, COLUMNS)


def run(argv: list[str]) -> int:
    try:
        o = parse_flags(argv, SPEC, HELP)
        resolve_project(o.topic, o.dir, False)
        actions = [a for a in (o.copy_to and "copy", o.move_to and "move", o.delete and "delete") if a]
        if len(actions) > 1:
            raise ValueError("choose one of --copy-to, --move-to and --delete")
        file_ext = o.ext
        o.ext = ""  # --ext picks files here, not papers with attachments of that extension
        rows, _ = catalog.input_rows(o)
        sel, _ = catalog.select(o, rows)
        o.ext = file_ext
        files = pick(collect(sel), o)
    except (ValueError, ProjectError) as exc:
        print(f"sourcelens: {exc}", file=sys.stderr)
        return 2
    total = sum(catalog._int(f["bytes"]) for f in files)
    print(f"{plural(len(files), 'file')} ({attachments.human_bytes(total) or '0 B'}) of {len(sel)} matching papers")
    for f in files[:o.limit]:
        print(" | ".join([f["kind"][:13], f["ext"][:5], (attachments.human_bytes(f["bytes"]) or "-"),
                          f["status"], f["file"][:60], (f["title"] or "")[:70]]))
    if o.out:
        path = catalog.out_path(o.out)
        write_csv(path, files, FILE_COLUMNS)
        print(f"wrote {path}")
    action = actions[0] if actions else ""
    if not action:
        return 0
    todo = [f for f in files if f["status"] == "ok" and f["path"] and research_file(f["path"]).is_file()]
    if action == "delete" and not o.yes:
        print(f"{plural(len(todo), 'file')} would be deleted from the catalogue; add --yes to delete them")
        return 1
    lock = None
    if action in ("move", "delete"):
        lock = agelit.acquire_pipeline_lock("sourcelens files")
        if lock is None:
            print("sourcelens: an update is running; try again later", file=sys.stderr)
            return 2
    try:
        base = Path(o.copy_to or o.move_to).expanduser().resolve() if action != "delete" else None
        done = []
        for f in todo:
            src = research_file(f["path"])
            if action == "delete":
                src.unlink()
            else:
                dest = destination(base, f, o.flat)
                dest.parent.mkdir(parents=True, exist_ok=True)
                if action == "copy":
                    shutil.copy2(src, dest)
                else:
                    shutil.move(str(src), str(dest))
                f["dest"] = dest.as_posix()
            done.append(f)
        if base is not None and done:
            write_csv(base / "sourcelens_files.csv", [{**f, "path": f.get("dest", "")} for f in done], FILE_COLUMNS)
        if action in ("move", "delete") and done:
            record(done, action, base.as_posix() if base else "")
    finally:
        if lock is not None:
            lock.close()
    verb = {"copy": "copied to", "move": "moved to", "delete": "deleted"}[action]
    print(f"{plural(len(done), 'file')} {verb}" + (f" {base}" if base else ""))
    return 0
