"""litsearch: collect the latest research on any topic (Python implementation).

The Go implementation (cmd/litsearch) has the same commands, flags, plans and
output text; keep them in step (see parity/check.sh). A stage runs its steps
as child processes ("python -m litsearch __step NAME ...") so that each step
has its own log file.
"""

from __future__ import annotations

import concurrent.futures as cf
import csv
import datetime as dt
import importlib
import os
import re
import subprocess
import sys
import threading
import time
from collections import Counter
from pathlib import Path

from litsearch import __version__
from litsearch.common import agelit
from litsearch.common.flags import parse_flags
from litsearch.common.settings import (
    ProjectError,
    cmd_config,
    cmd_list,
    default_topic,
    extend_start,
    github_token_from_cli,
    resolve_project,
    topic_flag,
)
from litsearch.common.terms import parse_topic

IMPL = "python"

# ---------------------------------------------------------------------------
# vocabulary (identical in cmd/litsearch/main.go)
# ---------------------------------------------------------------------------
SOURCES = [
    ("pubmed", "PubMed / MEDLINE search (NCBI E-utilities)"),
    ("europepmc", "Europe PMC search (preprints, PMC-only records) and Europe PMC full-text XML"),
    ("arxiv", "arXiv search and arXiv PDFs"),
    ("openalex", "OpenAlex search (all fields of research), citation counts, exact dates, open-access PDF links"),
    ("local", "DOIs and links in the reference folders listed in the configuration"),
    ("pmc", "PMC open-access bucket on AWS (XML, text, PDF)"),
    ("biorxiv", "bioRxiv / medRxiv JATS and PDF, and preprint to journal links"),
    ("unpaywall", "open-access PDFs from publishers and repositories (needs contact_email)"),
    ("github", "GitHub repository search and metadata"),
    ("cran", "CRAN packages"),
    ("bioconductor", "Bioconductor packages"),
    ("pypi", "PyPI packages"),
    ("zenodo", "Zenodo records linked from papers"),
    ("websites", "curated websites, databases, calculators; sites cited by papers"),
]
SOURCE_GROUPS = {
    "literature": ["pubmed", "europepmc", "arxiv", "openalex", "local"],
    "fulltext": ["pmc", "biorxiv", "europepmc", "arxiv", "openalex", "unpaywall"],
    "repos": ["github", "cran", "bioconductor", "pypi", "zenodo"],
    "packages": ["cran", "bioconductor", "pypi"],
}
TYPES = [
    ("papers", "literature metadata: articles, reviews, preprints (search and enrichment)"),
    ("pdf", "full-text PDF files"),
    ("md", "full-text Markdown"),
    ("txt", "full-text plain text"),
    ("xml", "full-text source XML (JATS)"),
    ("repo", "code repositories and software packages"),
    ("website", "websites, databases, web calculators"),
]
TYPE_GROUPS = {"fulltext": ["pdf", "md", "txt", "xml"], "repos": ["repo"], "repository": ["repo"],
               "package": ["repo"], "packages": ["repo"], "websites": ["website"],
               "paper": ["papers"], "metadata": ["papers"]}
PAPER_TYPES = ["article", "review", "preprint", "report", "thesis", "conference paper", "book chapter"]
PAPER_SET = set(PAPER_TYPES)
FT_FORMATS = ["pdf", "md", "txt", "xml"]
FT_SOURCES = ["pmc", "biorxiv", "europepmc", "arxiv", "openalex", "unpaywall"]
REPO_SOURCES = ["github", "cran", "bioconductor", "pypi", "zenodo"]
SEARCH_SOURCES = ["pubmed", "europepmc", "arxiv", "openalex"]

# step name -> module with main()
STEPS = {
    "seeds": "litsearch.localseeds.extract_seeds",
    "pubmed": "litsearch.pullliturature.search_pubmed",
    "europepmc": "litsearch.pullliturature.search_europepmc",
    "arxiv": "litsearch.pullliturature.search_arxiv",
    "openalex-search": "litsearch.pullliturature.search_openalex",
    "github-search": "litsearch.pullrepos.search_github",
    "resolve-seeds": "litsearch.pullliturature.resolve_seeds",
    "catalogue": "litsearch.buildcatalog.build_progress",
    "openalex": "litsearch.pullliturature.enrich_openalex",
    "preprint-links": "litsearch.pullliturature.enrich_preprints",
    "fulltext": "litsearch.pullliturature.fetch_fulltext",
    "links": "litsearch.pullrepos.mine_links",
    "repositories": "litsearch.pullrepos.build_repos",
    "websites": "litsearch.websites.build_websites",
    "summary": "litsearch.buildcatalog.summarise",
}

MAIN_HELP = """litsearch {version}: collect the latest research on any topic

usage
  litsearch "TOPIC" [options]     search, download and catalogue a topic (same as update --topic)
  litsearch COMMAND [options]

commands
  update    search, download and rebuild a catalogue (the default topic without --topic)
  retry     download again the full texts that were deferred, partial or failed
  query     filter a catalogue
  export    write catalogue entries as references (APA AMA MLA CHI HAR VAN IEE NAT BIB RIS ENW CSL)
  status    size, full-text coverage, last runs and failures of a catalogue
  list      the catalogues in the output folder
  config    show or change the settings of this machine (output folder, contact email, keys)
  sources   the values --sources, --types, --paper-types and --format accept
  test      check the append-only contract on a throwaway catalogue
  version   print the version

examples
  litsearch "CRISPR base editing"                 last 12 months, every source
  litsearch "graph neural networks" --range 3y    a longer window
  litsearch update                                the default topic
  litsearch query --topic "CRISPR base editing" --range 1m
  litsearch export --topic "CRISPR base editing" --format BIB --out crispr.bib

litsearch COMMAND --help shows the options of a command.
"""

PROJECT_HELP = """catalogue
  --topic TEXT        the topic; words are all required, commas separate alternatives,
                      "double quotes" keep a phrase (default: the default topic)
  --dir DIR           use the catalogue in DIR instead of the output folder
"""

UPDATE_HELP = """litsearch update [options]: search, download and rebuild a catalogue

  litsearch update                                 the default topic, configured window
  litsearch "CRISPR base editing"                  a new topic: last 12 months
  litsearch "CRISPR base editing" --range 5y       a new topic: last 5 years
  litsearch update --range 1y                      only work published in the last year
  litsearch update --types papers --range 6m       metadata only, last 6 months
  litsearch update --from 2020 --to 2022-06        exact window (year, month or day)
  litsearch update --types pdf,md --sources pmc,unpaywall --range 2y
  litsearch update --types repo,website            repositories, packages, websites only
  litsearch update --sources pubmed,arxiv --dry-run

""" + PROJECT_HELP + """
options
  --sources LIST      sources or groups (default all; see: litsearch sources)
  --types LIST        all | papers,pdf,md,txt,xml,repo,website (default all)
  --paper-types LIST  paper types to download full texts for (default all)
  --range SPAN        span back from --to: 1d 7d 2w 1m 6m 1y 2y 10y
  --from WHEN         YYYY, YYYY-MM, YYYY-MM-DD, today or a span (2y)
  --to WHEN           YYYY, YYYY-MM, YYYY-MM-DD or today (default today)
  --scope S           search groups: focused | broad | all (default all)
  --tiers LIST        catalogue tiers for full texts (default landmark,core,related)
  --workers N         parallel full-text downloads (default 12)
  --parallel N        steps run at the same time within a stage (default 5)
  --min-stars N       GitHub search hits need this many stars (default 3)
  --heartbeat SEC     seconds between progress lines (default 120)
  --stop-on-error     stop after the first failed stage
  --dry-run           print the plan and a full-text estimate, then exit

A range limits the searches (publication date), the full-text downloads
(publication date) and the GitHub search (creation date). It never removes
anything from progress.csv, which only grows. A new topic starts 12 months
back unless --range or --from says otherwise; asking later for older work
extends its window.
"""

RETRY_HELP = """litsearch retry [options]: download again the full texts whose last attempt
was deferred (rate limit), partial or failed, then rebuild

""" + PROJECT_HELP + """
options
  --status LIST   last full-text statuses to try again (default deferred,partial,error)
  --types LIST    formats to keep: pdf,md,txt,xml (default all four)
  --sources LIST  full-text sources (default pmc,biorxiv,europepmc,arxiv,openalex,unpaywall)
  --workers N     parallel downloads (default 12)
  --dry-run       count the records and exit
"""

STATUS_HELP = """litsearch status [--topic TEXT | --dir DIR]: size, full-text coverage, last
builds, the last run and its failures, and whether an update is running
"""

PROJECT_SPEC = {"topic": ("", str), "dir": ("", str)}


def fail(msg: str) -> int:
    print(f"litsearch: {msg}", file=sys.stderr)
    return 2


def expand(spec: str, vocab: list[str], groups: dict, what: str) -> list[str]:
    out: list[str] = []
    for tok in (t.strip().lower() for t in spec.split(",")):
        if not tok:
            continue
        items = vocab if tok == "all" else groups.get(tok, [tok])
        for it in items:
            if it not in vocab:
                raise ValueError(f'unknown {what} "{tok}" (see: litsearch sources)')
            if it not in out:
                out.append(it)
    return out


_lock = threading.Lock()


def say(msg: str) -> None:
    with _lock:
        print(f"[{dt.datetime.now():%H:%M:%S}] {msg}", flush=True)


def read_rows(path: Path) -> list[dict]:
    if not path.exists():
        return []
    with open(path, newline="", encoding="utf-8") as fh:
        return list(csv.DictReader(fh))


def rpath(*parts: str) -> Path:
    return agelit.RESEARCH.joinpath(*parts)


def counts(rows: list[dict], col: str) -> str:
    c = Counter((r.get(col) or "-") for r in rows)
    return ", ".join(f"{k}: {v}" for k, v in sorted(c.items(), key=lambda kv: (-kv[1], kv[0])))


# ---------------------------------------------------------------------------
# plan (identical in cmd/litsearch/main.go)
# ---------------------------------------------------------------------------
class Step:
    def __init__(self, name: str, *args: str):
        self.name, self.args = name, [a for a in args if a != ""]

    def __str__(self) -> str:
        return " ".join([self.name, *self.args])

    @property
    def kind(self) -> str:
        return re.sub(r"-\d+$", "", self.name)


class Opts:
    pass


def config_search_sources() -> list[str]:
    """search.sources of the configuration (pubmed, europepmc and arxiv when absent)."""
    s = agelit.config("search").get("sources") or []
    if not s:
        return ["pubmed", "europepmc", "arxiv"]
    return [x for x in SEARCH_SOURCES if x in s]


def plan(o) -> list[list[Step]]:
    src, types = o.sources, o.types
    papers = "papers" in types

    def search(name: str) -> bool:
        return papers and name in src and name in o.search_sources

    formats = [f for f in FT_FORMATS if f in types]
    fts = [s for s in FT_SOURCES if s in src]
    rs = [s for s in REPO_SOURCES if s in src]
    want_repo = "repo" in types and bool(rs)
    want_web = "website" in types and "websites" in src
    stages: list[list[Step]] = []
    s1 = []
    if papers and "local" in src:
        s1.append(Step("seeds"))
    if search("pubmed"):
        s1.append(Step("pubmed", "--scope", o.scope))
    if search("europepmc"):
        s1.append(Step("europepmc", "--scope", o.scope))
    if search("arxiv") and o.scope != "broad":
        s1.append(Step("arxiv"))
    if search("openalex"):
        s1.append(Step("openalex-search", "--scope", o.scope))
    if want_repo and "github" in src:
        s1.append(Step("github-search"))
    stages.append(s1)
    if papers and "local" in src:
        stages.append([Step("resolve-seeds")])
    stages.append([Step("catalogue-1")])
    s3 = []
    if papers and "openalex" in src:
        s3.append(Step("openalex"))
    if papers and "biorxiv" in src:
        s3.append(Step("preprint-links"))
    if formats and fts:
        s3.append(Step("fulltext", "--formats", ",".join(formats), "--sources", ",".join(fts),
                       "--types", ",".join(o.paper_types), "--tiers", o.tiers, "--workers", str(o.workers)))
    stages.append(s3)
    if s3:
        stages.append([Step("catalogue-2")])
    if want_repo or want_web:
        stages.append([Step("links")])
    s5 = []
    if want_repo:
        s5.append(Step("repositories", "--sources", ",".join(rs), "--min-stars", str(o.min_stars)))
    if want_web:
        s5.append(Step("websites"))
    stages.append(s5)
    if s5:
        stages.append([Step("catalogue-3")])
    stages.append([Step("summary")])
    return [s for s in stages if s]


# ---------------------------------------------------------------------------
# run
# ---------------------------------------------------------------------------
def last_line(path: Path) -> str:
    try:
        with open(path, "rb") as fh:
            fh.seek(0, 2)
            fh.seek(max(0, fh.tell() - 4000))
            lines = [line for line in fh.read().decode("utf-8", "replace").splitlines() if line.strip()]
        return lines[-1].strip()[:150] if lines else ""
    except OSError:
        return ""


def run_step(step: Step, log: Path, env: dict) -> tuple[Step, int, float, Path]:
    t0 = time.time()
    say(f"start  {step.name:<15} {step}")
    with open(log, "w") as fh:
        rc = subprocess.call([sys.executable, "-m", "litsearch", "__step", step.kind, *step.args],
                             stdout=fh, stderr=subprocess.STDOUT, cwd=agelit.RESEARCH, env=env)
    dur = time.time() - t0
    say(f"{'done ' if rc == 0 else 'FAILED'} {step.name:<15} {dur / 60:5.1f} min  {last_line(log)}")
    return step, rc, dur, log


def run_plan(stages: list[list[Step]], o, label: str) -> int:
    stamp = dt.datetime.now().strftime("%Y-%m-%d_%H%M%S")
    logdir = rpath("logs", f"cli_{stamp}")
    logdir.mkdir(parents=True, exist_ok=True)
    lines = [f"command {label}", f"version {__version__}", f"window {o.start or 'config start'} .. {o.end or 'today'}",
             f"sources {','.join(o.sources)}", f"types {','.join(o.types)}",
             f"paper-types {','.join(o.paper_types)}"]
    lines += [f"stage {i}: " + " | ".join(str(s) for s in st) for i, st in enumerate(stages, 1)]
    (logdir / "plan.txt").write_text("\n".join(lines) + "\n")
    # the configuration this run used, kept with its logs
    if agelit.CONFIG_FILE.is_file():
        (logdir / "litsearch.yaml").write_bytes(agelit.CONFIG_FILE.read_bytes())
    say(f"logs in {logdir}")
    env = dict(os.environ)
    if o.start or o.end:
        env["LITSEARCH_SEARCH_START"] = o.start or agelit.query_window()[0]
        env["LITSEARCH_SEARCH_END"] = o.end or dt.date.today().isoformat()

    running: dict[str, Path] = {}
    stop = threading.Event()

    def heartbeat():
        while not stop.wait(o.heartbeat):
            for name, log in list(running.items()):
                say(f"  ...  {name:<15} {last_line(log)}")

    if o.heartbeat > 0:
        threading.Thread(target=heartbeat, daemon=True).start()
    results, n, t_all, failed = [], 0, time.time(), False
    try:
        for i, stage in enumerate(stages, 1):
            say(f"== stage {i}/{len(stages)}: {', '.join(s.name for s in stage)}")
            with cf.ThreadPoolExecutor(max_workers=max(1, o.parallel)) as ex:
                futs = []
                for s in stage:
                    n += 1
                    log = logdir / f"{n:02d}_{s.name}.log"
                    running[s.name] = log
                    fut = ex.submit(run_step, s, log, env)
                    # the heartbeat reports running steps only
                    fut.add_done_callback(lambda _f, name=s.name: running.pop(name, None))
                    futs.append(fut)
                stage_res = [f.result() for f in futs]
            for s, rc, dur, log in stage_res:
                running.pop(s.name, None)
                results.append((s, rc, dur, log))
                failed = failed or rc != 0
            if failed and o.stop_on_error:
                break
    finally:
        stop.set()
    (logdir / "summary.txt").write_text("".join(
        f"{s.name}\trc={rc}\t{dur / 60:.1f} min\t{log.name}\n" for s, rc, dur, log in results))
    say(f"finished in {(time.time() - t_all) / 60:.1f} min")
    today = dt.date.today().isoformat()
    added = rpath("changelog", f"added_{today}.csv")
    if added.exists():
        say(f"rows added today: {len(read_rows(added))} (changelog/added_{today}.csv)")
    for s, rc, _, log in results:
        if rc != 0:
            say(f"FAILED step {s.name}: see {log}")
    return 1 if failed else 0


def newest(limit: int) -> None:
    """The most recent rows added today: what a run found."""
    rows = read_rows(rpath("changelog", f"added_{dt.date.today().isoformat()}.csv"))
    if not rows:
        return
    rows.sort(key=lambda r: r.get("date") or "", reverse=True)
    print(f"\nnewest additions ({min(limit, len(rows))} of {len(rows)} added today):")
    for r in rows[:limit]:
        print(f"  {r.get('date', ''):<10} {(r.get('resource_type') or '')[:16]:<16} {(r.get('title') or '')[:100]}")


# ---------------------------------------------------------------------------
# estimate (identical in cmd/litsearch/main.go)
# ---------------------------------------------------------------------------
def estimate_fulltext(o, formats: list[str]) -> tuple[int, int]:
    rows = read_rows(rpath("progress.csv"))
    idx = {r["uid"]: r for r in read_rows(rpath("fulltext", "fulltext_index.csv"))}
    tiers = o.tiers.split(",")
    cutoff = (dt.date.today() - dt.timedelta(days=30)).isoformat()
    lo, hi = o.start or "0000", o.end or "9999"
    todo = scope = 0
    for r in rows:
        if r.get("resource_type") not in PAPER_SET or r["resource_type"] not in o.paper_types \
                or r.get("tier") not in tiers:
            continue
        if (o.start or o.end) and not (lo <= (r.get("date") or "0000") <= hi):
            continue
        scope += 1
        p = idx.get(r["uid"])
        if p is None or p["status"] in ("partial", "deferred", "error"):
            todo += 1
        elif p["status"] == "none" and p.get("checked_on", "") < cutoff:
            todo += 1
        elif p["status"] == "ok" and p.get("checked_on", "") < cutoff and \
                any(p.get(f"has_{f}") != "True" for f in formats):
            todo += 1
    return todo, scope


# ---------------------------------------------------------------------------
# commands
# ---------------------------------------------------------------------------
def cmd_update(argv: list[str]) -> int:
    from litsearch.query.catalog import window
    spec = {**PROJECT_SPEC, "sources": ("all", str), "types": ("all", str),
            "paper-types": (",".join(PAPER_TYPES), str),
            "range": ("", str), "from": ("", str), "to": ("", str), "scope": ("all", str),
            "tiers": ("landmark,core,related", str), "workers": (12, int), "parallel": (5, int),
            "min-stars": (3, int), "heartbeat": (120, int), "stop-on-error": (False, bool),
            "dry-run": (False, bool)}
    try:
        o = parse_flags(argv, spec, UPDATE_HELP)
        o.sources = expand(o.sources, [s for s, _ in SOURCES], SOURCE_GROUPS, "source")
        o.types = expand(o.types, [t for t, _ in TYPES], TYPE_GROUPS, "type")
        o.paper_types = [t.strip() for t in o.paper_types.split(",") if t.strip()]
        for t in o.paper_types:
            if t not in PAPER_SET:
                raise ValueError(f'unknown paper type "{t}" (one of: {", ".join(PAPER_TYPES)})')
        if o.scope not in ("focused", "broad", "all"):
            raise ValueError("--scope must be focused, broad or all")
        if o.topic and not parse_topic(o.topic):
            raise ValueError("--topic needs at least one word")
        lo, hi = window(o)
    except ValueError as exc:
        return fail(str(exc))
    try:
        p = resolve_project(o.topic, o.dir, not o.dry_run, lo)
    except ProjectError as exc:
        if o.dry_run and not exc.project.config.is_file():
            t = o.topic or default_topic()
            print(f"litsearch update (dry run)\n  catalogue: {exc.project.dir} (not created yet)\n"
                  f"  topic    : {t}\n  terms    : {' | '.join(parse_topic(t))}")
            return 0
        return fail(str(exc))
    if p.created:
        say(f'new catalogue for "{p.topic}" in {p.dir}')
    if not o.dry_run and extend_start(p, lo):
        say(f"catalogue window now starts {lo}")
    o.search_sources = config_search_sources()
    o.start, o.end = lo, hi
    if o.start and not o.end:
        o.end = dt.date.today().isoformat()
    if o.end and not o.start:
        o.start = agelit.query_window()[0]  # the configured start date
    if "github" in o.sources:
        github_token_from_cli()
    stages = plan(o)
    print(f"litsearch update\n  topic  : {p.topic}\n  folder : {p.dir}\n"
          f"  window : {o.start or 'config start'} .. {o.end or 'today'}\n"
          f"  sources: {', '.join(o.sources)}\n  types  : {', '.join(o.types)}\n"
          f"  paper types (full text): {', '.join(o.paper_types)}\n"
          f"  scope  : {o.scope}   full-text tiers: {o.tiers}   workers: {o.workers}   parallel steps: {o.parallel}")
    for i, st in enumerate(stages, 1):
        print(f"  stage {i}: " + " | ".join(s.name for s in st))
    if o.dry_run:
        for i, st in enumerate(stages, 1):
            for s in st:
                print(f"    {i}. {s}")
        formats = [f for f in FT_FORMATS if f in o.types]
        if formats:
            todo, scope = estimate_fulltext(o, formats)
            print(f"  estimate: {todo} of {scope} catalogued papers in scope would be tried for full text "
                  f"(plus whatever the searches add)")
        return 0
    lock = agelit.acquire_pipeline_lock("litsearch update")
    if lock is None:
        held = agelit.pipeline_lock_path().read_text().strip() if agelit.pipeline_lock_path().exists() else ""
        return fail(f"another update is running ({held}); try again later")
    try:
        rc = run_plan(stages, o, "update")
    finally:
        lock.close()
    newest(10)
    print(f"\ncatalogue: {rpath('progress.csv')}")
    return rc


def cmd_retry(argv: list[str]) -> int:
    spec = {**PROJECT_SPEC, "status": ("deferred,partial,error", str), "types": ("pdf,md,txt,xml", str),
            "sources": (",".join(FT_SOURCES), str), "workers": (12, int), "dry-run": (False, bool)}
    try:
        o = parse_flags(argv, spec, RETRY_HELP)
        formats = expand(o.types, FT_FORMATS, {"fulltext": FT_FORMATS}, "format")
        fsrc = expand(o.sources, FT_SOURCES, {"fulltext": FT_SOURCES}, "full-text source")
        resolve_project(o.topic, o.dir, False)
    except (ValueError, ProjectError) as exc:
        return fail(str(exc))
    want = o.status.split(",")
    n = sum(1 for r in read_rows(rpath("fulltext", "fulltext_index.csv")) if r.get("status") in want)
    print(f"litsearch retry: {n} records with status {o.status}")
    if o.dry_run or n == 0:
        return 0
    o.sources, o.types, o.paper_types = fsrc, formats, PAPER_TYPES
    o.start = o.end = ""
    o.parallel, o.heartbeat, o.stop_on_error = 1, 120, False
    stages = [[Step("fulltext", "--only-status", o.status, "--formats", ",".join(formats),
                    "--sources", ",".join(fsrc), "--workers", str(o.workers))],
              [Step("catalogue-1")], [Step("summary")]]
    lock = agelit.acquire_pipeline_lock("litsearch retry")
    if lock is None:
        return fail("another update is running; try again later")
    try:
        return run_plan(stages, o, "retry")
    finally:
        lock.close()


def cmd_status(argv: list[str]) -> int:
    try:
        o = parse_flags(argv, PROJECT_SPEC, STATUS_HELP)
        p = resolve_project(o.topic, o.dir, False)
    except (ValueError, ProjectError) as exc:
        return fail(str(exc))
    print(f"topic: {p.topic}\nfolder: {p.dir}")
    rows = read_rows(rpath("progress.csv"))
    if not rows:
        print(f"no progress.csv yet; run: litsearch update{topic_flag(o.topic)}")
        return 0
    print(f"progress.csv: {len(rows)} rows, {rows[0]['date']} .. {rows[-1]['date']}")
    for col in ("resource_type", "tier", "fulltext_status"):
        print(f"  {col:<16}{counts(rows, col)}")
    idx = read_rows(rpath("fulltext", "fulltext_index.csv"))
    if idx:
        print(f"full-text index  {counts(idx, 'status')}")
        seen = {r["uid"] for r in idx}
        waiting = sum(1 for r in rows if r.get("resource_type") in PAPER_SET and r["uid"] not in seen)
        print(f"  papers not yet tried for full text: {waiting}   (try deferred/partial again: litsearch retry)")
    runs = read_rows(rpath("changelog", "runs.csv"))
    if runs:
        print("last builds:")
        for r in runs[-5:]:
            print(f"  {r['run_date']}: {r['rows_total']} rows, +{r['rows_added']} added")
    dirs = sorted(rpath("logs").glob("cli_*"))
    if dirs:
        last = dirs[-1]
        print(f"last run: {last.relative_to(agelit.RESEARCH)}")
        plan_file = last / "plan.txt"
        if plan_file.exists():
            for line in plan_file.read_text().splitlines():
                if line.startswith(("command", "window")):
                    print("  " + line)
        summ = last / "summary.txt"
        if summ.exists():
            for line in summ.read_text().strip().splitlines():
                mark = "ok    " if "rc=0" in line else "FAILED"
                print(f"  {mark} {line.replace(chr(9), '  ')}")
        else:
            print("  (no summary.txt: still running or interrupted)")
    lockf = agelit.pipeline_lock_path()
    if lockf.exists():
        held = agelit.acquire_pipeline_lock("status probe")
        if held is None:
            print(f"update running now: {lockf.read_text().strip()}")
        else:
            held.close()
    return 0


def cmd_sources() -> int:
    from litsearch.query import refs
    print("--sources (comma list; default all)")
    for k, v in SOURCES:
        print(f"  {k:<13} {v}")
    print("  groups: literature = pubmed,europepmc,arxiv,openalex,local; "
          "fulltext = pmc,biorxiv,europepmc,arxiv,openalex,unpaywall;")
    print("          repos = github,cran,bioconductor,pypi,zenodo; packages = cran,bioconductor,pypi")
    print("  The literature sources searched for a topic are also limited by search.sources in its configuration.")
    print("\n--types (comma list; default all)")
    for k, v in TYPES:
        print(f"  {k:<13} {v}")
    print("  groups: fulltext = pdf,md,txt,xml; repos/package = repo; websites = website")
    print("\n--paper-types (full-text downloads; default all): " + ", ".join(PAPER_TYPES))
    print("\n--format (export; comma list; default APA)")
    for c in refs.CODES:
        print(f"  {c}  {refs.DESCRIPTIONS[c]}")
    print("\n--range: 1d 7d 2w 1m 6m 1y 2y 10y ...   --from/--to: 2024 | 2024-03 | 2024-03-15 | today")
    return 0


def run_step_now(args: list[str]) -> int:
    """Run one step in this process (the child side of run_step)."""
    if not args or args[0] not in STEPS:
        print(f"unknown step {args}", file=sys.stderr)
        return 2
    mod = importlib.import_module(STEPS[args[0]])
    sys.argv = [f"litsearch {args[0]}", *args[1:]]
    try:
        mod.main()
    except SystemExit as exc:
        return exc.code if isinstance(exc.code, int) else (0 if exc.code is None else 1)
    return 0


COMMANDS = ["update", "retry", "query", "export", "status", "list", "config", "sources", "test", "version", "help"]


def has_help(args: list[str]) -> bool:
    return any(a in ("-h", "--help", "-help") for a in args)


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    if not argv or argv[0] in ("help", "-h", "--help"):
        if len(argv) == 2 and argv[1] in COMMANDS and argv[1] != "help":
            return main([argv[1], "--help"])
        print(MAIN_HELP.format(version=__version__), end="")
        return 0
    cmd, args = argv[0], argv[1:]
    if cmd == "__step":
        return run_step_now(args)
    if cmd == "update":
        return cmd_update(args)
    if cmd == "retry":
        return cmd_retry(args)
    if cmd in ("query", "export"):
        from litsearch.query import export_refs, search_catalog
        mod = search_catalog if cmd == "query" else export_refs
        if has_help(args):
            print(mod.HELP, end="")
            return 0
        return mod.run(args)
    if cmd == "status":
        return cmd_status(args)
    if cmd == "list":
        return cmd_list(args)
    if cmd == "config":
        from litsearch.pullliturature.fetch_fulltext import pdf_tools
        return cmd_config(args, pdf_tools())
    if cmd == "sources":
        return cmd_sources()
    if cmd == "test":
        from litsearch.buildcatalog.test_incremental import run_test
        return run_test()
    if cmd in ("version", "--version", "-v"):
        print(f"litsearch {__version__} ({IMPL})")
        return 0
    # anything else that is not an option is a topic: litsearch "CRISPR base editing" [options]
    if not cmd.startswith("-"):
        words, rest = [], []
        for i, a in enumerate(argv):
            if a.startswith("-"):
                rest = argv[i:]
                break
            words.append(a)
        return cmd_update(["--topic", " ".join(words), *rest])
    print(f'litsearch: unknown option "{cmd}"\n', file=sys.stderr)
    print(MAIN_HELP.format(version=__version__), end="")
    return 2


def entry() -> None:
    """Console script entry point."""
    sys.exit(main())
