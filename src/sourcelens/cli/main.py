"""sourcelens: collect the latest research on any topic (Python implementation).

The Go implementation (cmd/sourcelens) has the same commands, flags, plans and
output text; keep them in step (see parity/check.sh). A stage runs its steps
as child processes ("python -m sourcelens __step NAME ...") so that each step
has its own log file.
"""

from __future__ import annotations

import concurrent.futures as cf
import csv
import datetime as dt
import importlib
import os
import re
import shutil
import subprocess
import sys
import threading
import time
from collections import Counter
from pathlib import Path

from sourcelens import __version__
from sourcelens.common import agelit
from sourcelens.common.flags import parse_flags
from sourcelens.common.settings import (
    ProjectError,
    cmd_config,
    cmd_list,
    default_topic,
    extend_start,
    github_token_from_cli,
    resolve_project,
    topic_flag,
)
from sourcelens.common.terms import parse_topic

IMPL = "python"

# ---------------------------------------------------------------------------
# vocabulary (identical in cmd/sourcelens/main.go)
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
    ("attachments", "figures and supplementary files (spreadsheets, slides, documents) of open-access papers"),
    ("repo", "code repositories and software packages"),
    ("website", "websites, databases, web calculators"),
]
TYPE_GROUPS = {"fulltext": ["pdf", "md", "txt", "xml"], "repos": ["repo"], "repository": ["repo"],
               "package": ["repo"], "packages": ["repo"], "websites": ["website"],
               "paper": ["papers"], "metadata": ["papers"], "attachment": ["attachments"],
               "files": ["pdf", "md", "txt", "xml", "attachments"]}
PAPER_TYPES = ["article", "review", "preprint", "report", "thesis", "conference paper", "book chapter"]
PAPER_SET = set(PAPER_TYPES)
FT_FORMATS = ["pdf", "md", "txt", "xml", "attachments"]
METADATA_TYPES = ["papers", "repo", "website"]  # what a dry run fetches
FT_SOURCES = ["pmc", "biorxiv", "europepmc", "arxiv", "openalex", "unpaywall"]
REPO_SOURCES = ["github", "cran", "bioconductor", "pypi", "zenodo"]
SEARCH_SOURCES = ["pubmed", "europepmc", "arxiv", "openalex"]

# step name -> module with main()
STEPS = {
    "seeds": "sourcelens.localseeds.extract_seeds",
    "pubmed": "sourcelens.pullliturature.search_pubmed",
    "europepmc": "sourcelens.pullliturature.search_europepmc",
    "arxiv": "sourcelens.pullliturature.search_arxiv",
    "openalex-search": "sourcelens.pullliturature.search_openalex",
    "github-search": "sourcelens.pullrepos.search_github",
    "resolve-seeds": "sourcelens.pullliturature.resolve_seeds",
    "catalogue": "sourcelens.buildcatalog.build_progress",
    "openalex": "sourcelens.pullliturature.enrich_openalex",
    "preprint-links": "sourcelens.pullliturature.enrich_preprints",
    "fulltext": "sourcelens.pullliturature.fetch_fulltext",
    "links": "sourcelens.pullrepos.mine_links",
    "repositories": "sourcelens.pullrepos.build_repos",
    "websites": "sourcelens.websites.build_websites",
    "summary": "sourcelens.buildcatalog.summarise",
    "dryrun": "sourcelens.buildcatalog.dryrun",
}

MAIN_HELP = """sourcelens {version}: collect the latest research on any topic

usage
  sourcelens "TOPIC" [options]     search, download and catalogue a topic (same as update --topic)
  sourcelens COMMAND [options]

commands
  update    search, download and rebuild a catalogue (the default topic without --topic)
  download  download the full texts and attachments of the rows in a CSV (a filtered dry run)
  retry     download again the full texts that were deferred, partial or failed
  query     filter a catalogue or a dry-run table
  files     list, copy, move or delete downloaded files by extension, name or paper
  export    write catalogue entries as references (APA AMA MLA CHI HAR VAN IEE NAT BIB RIS ENW CSL)
  report    write the run report (searchable HTML) of a catalogue now
  status    size, full-text coverage, last runs and failures of a catalogue
  list      the catalogues in the output folder
  config    show or change the settings of this machine (output folder, contact email, keys)
  sources   the values --sources, --types, --paper-types and --format accept
  test      check the append-only contract on a throwaway catalogue
  version   print the version

examples
  sourcelens "CRISPR base editing"                 last 12 months, every source
  sourcelens "graph neural networks" --range 3y    a longer window
  sourcelens update                                the default topic
  sourcelens "CRISPR base editing" --dry-run       metadata and a table of what is there; no downloads
  sourcelens query --in reports/dryrun_<stamp>.csv --summary "off-target" --out picked.csv
  sourcelens download exports/picked.csv           download only the picked rows
  sourcelens query --topic "CRISPR base editing" --range 1m
  sourcelens export --topic "CRISPR base editing" --format BIB --out crispr.bib

sourcelens COMMAND --help shows the options of a command.
"""

PROJECT_HELP = """catalogue
  --topic TEXT        the topic; words are all required, commas separate alternatives,
                      "double quotes" keep a phrase (default: the default topic)
  --dir DIR           use the catalogue in DIR instead of the output folder
"""

UPDATE_HELP = """sourcelens update [options]: search, download and rebuild a catalogue

  sourcelens update                                 the default topic, configured window
  sourcelens "CRISPR base editing"                  a new topic: last 12 months
  sourcelens "CRISPR base editing" --range 5y       a new topic: last 5 years
  sourcelens update --range 1y                      only work published in the last year
  sourcelens update --types papers --range 6m       metadata only, last 6 months
  sourcelens update --from 2020 --to 2022-06        exact window (year, month or day)
  sourcelens update --types pdf,md --sources pmc,unpaywall --range 2y
  sourcelens update --types repo,website            repositories, packages, websites only
  sourcelens update --dry-run                       metadata only, then a table of what could be downloaded
  sourcelens update --types attachments --ext xlsx,csv,pptx   spreadsheets and slides of open papers
  sourcelens update --sources pubmed,arxiv --plan

""" + PROJECT_HELP + """
options
  --sources LIST      sources or groups (default all; see: sourcelens sources)
  --types LIST        all | papers,pdf,md,txt,xml,attachments,repo,website (default all)
  --paper-types LIST  paper types to download full texts for (default all)
  --range SPAN        span back from --to: 1d 7d 2w 1m 6m 1y 2y 10y
  --from WHEN         YYYY, YYYY-MM, YYYY-MM-DD, today or a span (2y)
  --to WHEN           YYYY, YYYY-MM, YYYY-MM-DD or today (default today)
  --scope S           search groups: focused | broad | all (default all)
  --tiers LIST        catalogue tiers for full texts (default landmark,core,related)
  --ext LIST          attachment file extensions to download, e.g. xlsx,csv,pptx (default all)
  --max-attachment-mb N   larger attachments are listed but not downloaded (default 100, 0: no limit)
  --workers N         parallel full-text downloads (default 12)
  --parallel N        steps run at the same time within a stage (default 5)
  --min-stars N       GitHub search hits need this many stars (default 3)
  --heartbeat SEC     progress lines every SEC seconds when the output is not a
                      terminal; a terminal shows a progress bar (default 120, 0: none)
  --stop-on-error     stop after the first failed stage
  --dry-run           search and fetch metadata only, download nothing, and write
                      reports/dryrun_<stamp>.csv: every row with its summary and what
                      could be downloaded; filter it with query --in, then download
  --plan              print the plan and a full-text estimate, then exit

Every run writes reports/runreport_<stamp>.html: a single page with the run's
numbers and a searchable, filterable table of the catalogue.

A range limits the searches (publication date), the full-text downloads
(publication date) and the GitHub search (creation date). It never removes
anything from progress.csv, which only grows. A new topic starts 12 months
back unless --range or --from says otherwise; asking later for older work
extends its window.
"""

RETRY_HELP = """sourcelens retry [options]: download again the full texts whose last attempt
was deferred (rate limit), partial or failed, then rebuild

""" + PROJECT_HELP + """
options
  --status LIST   last full-text statuses to try again (default deferred,partial,error)
  --types LIST    formats to keep: pdf,md,txt,xml,attachments (default all)
  --sources LIST  full-text sources (default pmc,biorxiv,europepmc,arxiv,openalex,unpaywall)
  --ext LIST      attachment file extensions to download (default all)
  --workers N     parallel downloads (default 12)
  --plan          count the records and exit
"""

DOWNLOAD_HELP = """sourcelens download FILE [options]: download the full texts and attachments
of the rows in FILE, then rebuild

FILE is a CSV with a uid or doi column: a dry-run table filtered with
sourcelens query --in ... --out FILE, or any query output. Relative paths are
looked up in the current folder, then in the catalogue and its exports/.

  sourcelens "CRISPR base editing" --dry-run
  sourcelens query --topic "CRISPR base editing" --in reports/dryrun_<stamp>.csv --summary prime --out prime.csv
  sourcelens download --topic "CRISPR base editing" exports/prime.csv
  sourcelens download picked.csv --types attachments --ext xlsx,csv

""" + PROJECT_HELP + """
options
  --types LIST    formats: pdf,md,txt,xml,attachments (default all)
  --sources LIST  full-text sources (default pmc,biorxiv,europepmc,arxiv,openalex,unpaywall)
  --ext LIST      attachment file extensions to download (default all)
  --max-attachment-mb N   larger attachments are listed but not downloaded (default 100, 0: no limit)
  --workers N     parallel downloads (default 12)
  --plan          count the rows and exit
"""

REPORT_HELP = """sourcelens report [--topic TEXT | --dir DIR]: write reports/runreport_<stamp>.html
for the catalogue as it is now (every update, retry, download and dry run writes one too)
"""

STATUS_HELP = """sourcelens status [--topic TEXT | --dir DIR]: size, full-text coverage, last
builds, the last run and its failures, and whether an update is running
"""

PROJECT_SPEC = {"topic": ("", str), "dir": ("", str)}


def fail(msg: str) -> int:
    print(f"sourcelens: {msg}", file=sys.stderr)
    return 2


def expand(spec: str, vocab: list[str], groups: dict, what: str) -> list[str]:
    out: list[str] = []
    for tok in (t.strip().lower() for t in spec.split(",")):
        if not tok:
            continue
        items = vocab if tok == "all" else groups.get(tok, [tok])
        for it in items:
            if it not in vocab:
                raise ValueError(f'unknown {what} "{tok}" (see: sourcelens sources)')
            if it not in out:
                out.append(it)
    return out


_lock = threading.Lock()
_status = ""  # the progress bar line kept below the messages (terminal only)


def say(msg: str) -> None:
    with _lock:
        clear = "\r\x1b[K" if _status else ""
        sys.stdout.write(f"{clear}[{dt.datetime.now():%H:%M:%S}] {msg}\n{_status}")
        sys.stdout.flush()


def set_status(line: str) -> None:
    """Draw the progress bar line below the messages; "" removes it."""
    global _status
    with _lock:
        _status = line
        sys.stdout.write("\r\x1b[K" + line)
        sys.stdout.flush()


def setup_console() -> None:
    """UTF-8 output on every platform (a Windows console or file would otherwise use the local code page)."""
    for stream in (sys.stdout, sys.stderr):
        try:
            if (stream.encoding or "").lower().replace("-", "") != "utf8":
                stream.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError):
            pass


def enable_ansi() -> bool:
    """Escape sequences for the progress bar: always on Linux and macOS, switched on in a Windows console."""
    if os.name != "nt":
        return True
    try:
        import ctypes
        kernel32 = ctypes.windll.kernel32
        handle = kernel32.GetStdHandle(-11)  # STD_OUTPUT_HANDLE
        mode = ctypes.c_uint32()
        if not kernel32.GetConsoleMode(handle, ctypes.byref(mode)):
            return False
        return bool(kernel32.SetConsoleMode(handle, mode.value | 0x0004))  # ENABLE_VIRTUAL_TERMINAL_PROCESSING
    except (AttributeError, OSError):
        return False


def write_text(path: Path, text: str) -> None:
    """A text file with "\n" line ends on every platform."""
    with open(path, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(text)


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
# plan (identical in cmd/sourcelens/main.go)
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
    if getattr(o, "dry_run", False):
        types = [t for t in types if t in METADATA_TYPES]
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
                       "--types", ",".join(o.paper_types), "--tiers", o.tiers, "--workers", str(o.workers),
                       *attachment_args(o, formats)))
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
    if getattr(o, "dry_run", False):
        stages.append([Step("dryrun", "--stamp", o.stamp)])
    return [s for s in stages if s]


def attachment_args(o, formats: list[str]) -> list[str]:
    """--attachment-ext / --max-attachment-mb for the fulltext step, when attachments are asked for."""
    if "attachments" not in formats:
        return []
    out = []
    if getattr(o, "ext", ""):
        out += ["--attachment-ext", ",".join(e.strip().lower().lstrip(".") for e in o.ext.split(",") if e.strip())]
    mb = getattr(o, "max_attachment_mb", 100)
    if mb != 100:
        out += ["--max-attachment-mb", str(mb)]
    return out


def run_stamp(t: dt.datetime) -> str:
    """The stamp of a run's reports: YYYY_MM_DD_HH_MM_SS."""
    return t.strftime("%Y_%m_%d_%H_%M_%S")


def command_line() -> str:
    import shlex
    return "sourcelens " + " ".join(shlex.quote(a) for a in sys.argv[1:])


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


BAR_WIDTH = 24
# "340/1200" in a step's last log line: how far that step is
COUNT_RE = re.compile(r"(?:^|[^A-Za-z0-9_/.])(\d+)/(\d+)(?:[^A-Za-z0-9_/.]|$)")


def clock(sec: float) -> str:
    s = int(sec)
    return f"{s // 3600}:{s % 3600 // 60:02d}:{s % 60:02d}" if s >= 3600 else f"{s // 60}:{s % 60:02d}"


def progress_line(done: int, total: int, t0: float, active: list[tuple[str, Path, float]], width: int) -> str:
    """[bar] finished/all steps, elapsed time, then each running step with its count or time."""
    now, frac, parts = time.time(), float(done), []
    for name, log, start in active:
        m = COUNT_RE.search(last_line(log))
        if m and 0 < int(m.group(2)) and int(m.group(1)) <= int(m.group(2)):
            frac += int(m.group(1)) / int(m.group(2))
            parts.append(f"{name} {m.group(1)}/{m.group(2)}")
        else:
            parts.append(f"{name} {clock(now - start)}")
    filled = min(BAR_WIDTH, int(BAR_WIDTH * frac / total + 0.5)) if total else BAR_WIDTH
    line = f"[{'█' * filled}{'░' * (BAR_WIDTH - filled)}] {done}/{total} steps  {clock(now - t0)}"
    if parts:
        line += "  " + ", ".join(parts)
    return line[:max(20, width - 1)]


def run_step(step: Step, log: Path, env: dict) -> tuple[Step, int, float, Path]:
    t0 = time.time()
    say(f"start  {step.name:<15} {step}")
    with open(log, "w", encoding="utf-8") as fh:
        rc = subprocess.call([sys.executable, "-m", "sourcelens", "__step", step.kind, *step.args],
                             stdout=fh, stderr=subprocess.STDOUT, cwd=agelit.RESEARCH, env=env)
    dur = time.time() - t0
    say(f"{'done ' if rc == 0 else 'FAILED'} {step.name:<15} {dur / 60:5.1f} min  {last_line(log)}")
    return step, rc, dur, log


def log_dir(started: dt.datetime) -> Path:
    return rpath("logs", f"cli_{started:%Y-%m-%d_%H%M%S}")


def run_plan(stages: list[list[Step]], o, label: str, started: dt.datetime | None = None) -> int:
    logdir = log_dir(started or dt.datetime.now())
    logdir.mkdir(parents=True, exist_ok=True)
    lines = [f"command {label}", f"version {__version__}", f"window {o.start or 'config start'} .. {o.end or 'today'}",
             f"sources {','.join(o.sources)}", f"types {','.join(o.types)}",
             f"paper-types {','.join(o.paper_types)}"]
    lines += [f"stage {i}: " + " | ".join(str(s) for s in st) for i, st in enumerate(stages, 1)]
    write_text(logdir / "plan.txt", "\n".join(lines) + "\n")
    # the configuration this run used, kept with its logs
    if agelit.CONFIG_FILE.is_file():
        (logdir / "sourcelens.yaml").write_bytes(agelit.CONFIG_FILE.read_bytes())
    say(f"logs in {logdir}")
    env = dict(os.environ)
    env["PYTHONUTF8"] = "1"  # steps read and write UTF-8 on every platform
    if o.start or o.end:
        env["SOURCELENS_SEARCH_START"] = o.start or agelit.query_window()[0]
        env["SOURCELENS_SEARCH_END"] = o.end or dt.date.today().isoformat()

    running: dict[str, Path] = {}
    started: dict[str, float] = {}
    finished, count_lock = [0], threading.Lock()
    total = sum(len(st) for st in stages)
    stop = threading.Event()
    t_all = time.time()
    # a terminal gets one progress bar line; a log file gets heartbeat lines
    bar = o.heartbeat > 0 and sys.stdout.isatty() and os.environ.get("TERM") != "dumb" and enable_ansi()

    def heartbeat():
        while not stop.wait(o.heartbeat):
            for name, log in list(running.items()):
                say(f"  ...  {name:<15} {last_line(log)}")

    kick = threading.Event()  # redraw the bar now: a step started or ended

    def progress():
        while not stop.is_set():
            kick.wait(1)
            kick.clear()
            if stop.is_set():
                break
            active = [(name, log, started[name]) for name, log in list(running.items()) if name in started]
            set_status(progress_line(finished[0], total, t_all, active, shutil.get_terminal_size().columns))

    def start(s: Step, log: Path):
        started[s.name] = time.time()
        kick.set()
        return run_step(s, log, env)

    def end(name: str) -> None:
        with count_lock:
            if running.pop(name, None) is not None:
                finished[0] += 1
        kick.set()

    ticker = None
    if o.heartbeat > 0:
        ticker = threading.Thread(target=progress if bar else heartbeat, daemon=True)
        ticker.start()
    results, n, failed = [], 0, False
    try:
        for i, stage in enumerate(stages, 1):
            say(f"== stage {i}/{len(stages)}: {', '.join(s.name for s in stage)}")
            with cf.ThreadPoolExecutor(max_workers=max(1, o.parallel)) as ex:
                futs = []
                for s in stage:
                    n += 1
                    log = logdir / f"{n:02d}_{s.name}.log"
                    running[s.name] = log
                    fut = ex.submit(start, s, log)
                    # the heartbeat and the bar report running steps only
                    fut.add_done_callback(lambda _f, name=s.name: end(name))
                    futs.append(fut)
                stage_res = [f.result() for f in futs]
            for s, rc, dur, log in stage_res:
                end(s.name)
                results.append((s, rc, dur, log))
                failed = failed or rc != 0
            if failed and o.stop_on_error:
                break
    finally:
        stop.set()
        kick.set()
        if bar and ticker:
            ticker.join(timeout=5)
            set_status("")
    write_text(logdir / "summary.txt", "".join(
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
# estimate (identical in cmd/sourcelens/main.go)
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
    from sourcelens.query.catalog import window
    spec = {**PROJECT_SPEC, "sources": ("all", str), "types": ("all", str),
            "paper-types": (",".join(PAPER_TYPES), str),
            "range": ("", str), "from": ("", str), "to": ("", str), "scope": ("all", str),
            "tiers": ("landmark,core,related", str), "workers": (12, int), "parallel": (5, int),
            "min-stars": (3, int), "heartbeat": (120, int), "stop-on-error": (False, bool),
            "dry-run": (False, bool), "plan": (False, bool), "ext": ("", str), "max-attachment-mb": (100, int)}
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
        p = resolve_project(o.topic, o.dir, not o.plan, lo)
    except ProjectError as exc:
        if o.plan and not exc.project.config.is_file():
            t = o.topic or default_topic()
            print(f"sourcelens update (plan)\n  catalogue: {exc.project.dir} (not created yet)\n"
                  f"  topic    : {t}\n  terms    : {' | '.join(parse_topic(t))}")
            return 0
        return fail(str(exc))
    if p.created:
        say(f'new catalogue for "{p.topic}" in {p.dir}')
    if not o.plan and extend_start(p, lo):
        say(f"catalogue window now starts {lo}")
    o.search_sources = config_search_sources()
    o.start, o.end = lo, hi
    if o.start and not o.end:
        o.end = dt.date.today().isoformat()
    if o.end and not o.start:
        o.start = agelit.query_window()[0]  # the configured start date
    if "github" in o.sources:
        github_token_from_cli()
    started = dt.datetime.now()
    o.stamp = run_stamp(started)
    stages = plan(o)
    shown = [t for t in o.types if t in METADATA_TYPES] if o.dry_run else o.types
    print(f"sourcelens update{' (dry run: metadata only, nothing is downloaded)' if o.dry_run else ''}\n"
          f"  topic  : {p.topic}\n  folder : {p.dir}\n"
          f"  window : {o.start or 'config start'} .. {o.end or 'today'}\n"
          f"  sources: {', '.join(o.sources)}\n  types  : {', '.join(shown)}\n"
          f"  paper types (full text): {', '.join(o.paper_types)}\n"
          f"  scope  : {o.scope}   full-text tiers: {o.tiers}   workers: {o.workers}   parallel steps: {o.parallel}")
    for i, st in enumerate(stages, 1):
        print(f"  stage {i}: " + " | ".join(s.name for s in st))
    if o.plan:
        for i, st in enumerate(stages, 1):
            for s in st:
                print(f"    {i}. {s}")
        formats = [f for f in FT_FORMATS if f in shown and f != "attachments"]
        if formats:
            todo, scope = estimate_fulltext(o, formats)
            print(f"  estimate: {todo} of {scope} catalogued papers in scope would be tried for full text "
                  f"(plus whatever the searches add)")
        return 0
    lock = agelit.acquire_pipeline_lock("sourcelens update")
    if lock is None:
        held = agelit.pipeline_lock_path().read_text(encoding="utf-8").strip() \
            if agelit.pipeline_lock_path().exists() else ""
        return fail(f"another update is running ({held}); try again later")
    label = "dry run" if o.dry_run else "update"
    try:
        rc = run_plan(stages, o, label, started)
    finally:
        lock.close()
    report = write_report(label, started, rc, p.topic)
    if o.dry_run:
        dryrun_summary(o.stamp, topic_flag(o.topic) if not o.dir else f" --dir {o.dir}")
    else:
        newest(10)
    print(f"\ncatalogue: {rpath('progress.csv')}")
    if report:
        print(f"report   : {report}")
    return rc


def write_report(label: str, started: dt.datetime, rc: int, topic: str) -> Path | None:
    """reports/runreport_<stamp>.html for this run; a failed report never fails the run."""
    from sourcelens.buildcatalog import report
    try:
        return report.write(label=label, stamp=run_stamp(started), started=started, logdir=log_dir(started),
                            command=command_line(), topic=topic, rc=rc, impl=IMPL)
    except Exception as exc:  # noqa: BLE001
        say(f"run report not written: {exc.__class__.__name__}: {exc}")
        return None


def dryrun_summary(stamp: str, where: str) -> None:
    """What the dry run found, and how to pick and download from it."""
    from sourcelens.pullliturature import attachments
    path = rpath("reports", f"dryrun_{stamp}.csv")
    rows = read_rows(path)
    if not rows:
        print("\ndry run: no table written (see the log of the dryrun step)")
        return
    papers = [r for r in rows if r.get("resource_type") in PAPER_SET]
    pend = Counter(p.strip() for r in rows for p in (r.get("pending") or "").split(";") if p.strip())
    summ = Counter(r.get("summary_from") or "none" for r in rows)
    files = [a for v in attachments.read_index().values() for a in v if a.get("file")]
    exts = Counter(a.get("ext") or "?" for a in files)
    print("\ndry run: metadata only, nothing was downloaded")
    print(f"  rows        : {len(rows)} ({counts(rows, 'resource_type')})")
    print(f"  full text   : {counts(papers, 'fulltext_status')}   (of {len(papers)} papers; -: not tried)")
    print("  pending     : " + (", ".join(f"{k}: {v}" for k, v in sorted(pend.items(), key=lambda kv: (-kv[1], kv[0])))
                                or "nothing"))
    print("  summaries   : " + ", ".join(f"{k}: {v}" for k, v in sorted(summ.items(), key=lambda kv: (-kv[1], kv[0]))))
    print(f"  attachments : {len(files)} files listed"
          + (" (" + ", ".join(f"{e}: {n}" for e, n in sorted(exts.items(), key=lambda kv: (-kv[1], kv[0]))[:8]) + ")"
             if files else ""))
    print(f"  table       : {path}")
    print("next: pick rows, then download only those")
    print(f'  sourcelens query{where} --in "{path}" --summary "WORDS" --out picked.csv')
    print(f"  sourcelens download{where} {rpath('exports', 'picked.csv')}")


def cmd_retry(argv: list[str]) -> int:
    spec = {**PROJECT_SPEC, "status": ("deferred,partial,error", str), "types": (",".join(FT_FORMATS), str),
            "sources": (",".join(FT_SOURCES), str), "ext": ("", str), "workers": (12, int),
            "plan": (False, bool), "dry-run": (False, bool)}
    try:
        o = parse_flags(argv, spec, RETRY_HELP)
        formats = expand(o.types, FT_FORMATS, {"fulltext": FT_FORMATS[:4], "files": FT_FORMATS,
                                               "attachment": ["attachments"]}, "format")
        fsrc = expand(o.sources, FT_SOURCES, {"fulltext": FT_SOURCES}, "full-text source")
        p = resolve_project(o.topic, o.dir, False)
    except (ValueError, ProjectError) as exc:
        return fail(str(exc))
    want = o.status.split(",")
    n = sum(1 for r in read_rows(rpath("fulltext", "fulltext_index.csv")) if r.get("status") in want)
    print(f"sourcelens retry: {n} records with status {o.status}")
    if o.plan or o.dry_run or n == 0:
        return 0
    o.sources, o.types, o.paper_types = fsrc, formats, PAPER_TYPES
    o.start = o.end = ""
    o.parallel, o.heartbeat, o.stop_on_error = 1, 120, False
    o.max_attachment_mb = 100
    stages = [[Step("fulltext", "--only-status", o.status, "--formats", ",".join(formats),
                    "--sources", ",".join(fsrc), "--workers", str(o.workers), *attachment_args(o, formats))],
              [Step("catalogue-1")], [Step("summary")]]
    lock = agelit.acquire_pipeline_lock("sourcelens retry")
    if lock is None:
        return fail("another update is running; try again later")
    started = dt.datetime.now()
    try:
        rc = run_plan(stages, o, "retry", started)
    finally:
        lock.close()
    report = write_report("retry", started, rc, p.topic)
    if report:
        print(f"report   : {report}")
    return rc


def split_positionals(argv: list[str], spec: dict) -> tuple[list[str], list[str]]:
    """(arguments that are not flags or flag values, the rest)."""
    pos, rest, i = [], [], 0
    while i < len(argv):
        a = argv[i]
        if a.startswith("-") and a != "-":
            rest.append(a)
            name = a.lstrip("-").partition("=")[0]
            if "=" not in a and name in spec and spec[name][1] is not bool and i + 1 < len(argv):
                rest.append(argv[i + 1])
                i += 1
        else:
            pos.append(a)
        i += 1
    return pos, rest


def find_input(path: str) -> Path | None:
    """A file named on the command line: as given, else in the catalogue, else in its exports/."""
    for p in (Path(path), rpath(path), rpath("exports", path)):
        if p.is_file():
            return p.resolve()
    return None


def cmd_download(argv: list[str]) -> int:
    spec = {**PROJECT_SPEC, "types": (",".join(FT_FORMATS), str), "sources": (",".join(FT_SOURCES), str),
            "ext": ("", str), "max-attachment-mb": (100, int), "workers": (12, int), "plan": (False, bool),
            "in": ("", str)}
    pos, rest = split_positionals(argv, spec)
    try:
        o = parse_flags(rest, spec, DOWNLOAD_HELP)
        name = getattr(o, "in") or (pos[0] if pos else "")
        if len(pos) > 1 or (pos and getattr(o, "in")):
            raise ValueError("download takes one file")
        if not name:
            raise ValueError("name the CSV of rows to download (see: sourcelens download --help)")
        formats = expand(o.types, FT_FORMATS, {"fulltext": FT_FORMATS[:4], "files": FT_FORMATS,
                                               "attachment": ["attachments"]}, "format")
        fsrc = expand(o.sources, FT_SOURCES, {"fulltext": FT_SOURCES}, "full-text source")
        p = resolve_project(o.topic, o.dir, False)
    except (ValueError, ProjectError) as exc:
        return fail(str(exc))
    path = find_input(name)
    if path is None:
        return fail(f"cannot find {name}")
    from sourcelens.pullliturature.fetch_fulltext import read_uids_file
    uids = set(read_uids_file(str(path)))
    rows = [r for r in read_rows(rpath("progress.csv")) if r.get("uid") in uids]
    papers = [r for r in rows if r.get("resource_type") in PAPER_SET]
    print(f"sourcelens download: {len(uids)} rows in {path}; {len(papers)} papers in the catalogue")
    if o.plan or not papers:
        return 0
    started = dt.datetime.now()
    logdir = log_dir(started)
    logdir.mkdir(parents=True, exist_ok=True)
    picked = logdir / "selection.txt"
    write_text(picked, "".join(f"{r['uid']}\n" for r in papers))
    o.sources, o.types, o.paper_types = fsrc, formats, PAPER_TYPES
    o.start = o.end = ""
    o.parallel, o.heartbeat, o.stop_on_error = 1, 120, False
    stages = [[Step("fulltext", "--uids-file", str(picked), "--formats", ",".join(formats),
                    "--sources", ",".join(fsrc), "--workers", str(o.workers), *attachment_args(o, formats))],
              [Step("catalogue-1")], [Step("links")], [Step("summary")]]
    lock = agelit.acquire_pipeline_lock("sourcelens download")
    if lock is None:
        return fail("another update is running; try again later")
    try:
        rc = run_plan(stages, o, "download", started)
    finally:
        lock.close()
    report = write_report("download", started, rc, p.topic)
    if report:
        print(f"report   : {report}")
    return rc


def cmd_report(argv: list[str]) -> int:
    try:
        o = parse_flags(argv, PROJECT_SPEC, REPORT_HELP)
        p = resolve_project(o.topic, o.dir, False)
    except (ValueError, ProjectError) as exc:
        return fail(str(exc))
    from sourcelens.buildcatalog import report
    started = dt.datetime.now()
    path = report.write(label="report", stamp=run_stamp(started), started=started, logdir=None,
                        command=command_line(), topic=p.topic, rc=0, impl=IMPL)
    print(f"report: {path}")
    return 0


def cmd_status(argv: list[str]) -> int:
    try:
        o = parse_flags(argv, PROJECT_SPEC, STATUS_HELP)
        p = resolve_project(o.topic, o.dir, False)
    except (ValueError, ProjectError) as exc:
        return fail(str(exc))
    print(f"topic: {p.topic}\nfolder: {p.dir}")
    rows = read_rows(rpath("progress.csv"))
    if not rows:
        print(f"no progress.csv yet; run: sourcelens update{topic_flag(o.topic)}")
        return 0
    print(f"progress.csv: {len(rows)} rows, {rows[0]['date']} .. {rows[-1]['date']}")
    for col in ("resource_type", "tier", "fulltext_status"):
        print(f"  {col:<16}{counts(rows, col)}")
    idx = read_rows(rpath("fulltext", "fulltext_index.csv"))
    if idx:
        print(f"full-text index  {counts(idx, 'status')}")
        seen = {r["uid"] for r in idx}
        waiting = sum(1 for r in rows if r.get("resource_type") in PAPER_SET and r["uid"] not in seen)
        print(f"  papers not yet tried for full text: {waiting}   (try deferred/partial again: sourcelens retry)")
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
            for line in plan_file.read_text(encoding="utf-8").splitlines():
                if line.startswith(("command", "window")):
                    print("  " + line)
        summ = last / "summary.txt"
        if summ.exists():
            for line in summ.read_text(encoding="utf-8").strip().splitlines():
                mark = "ok    " if "rc=0" in line else "FAILED"
                print(f"  {mark} {line.replace(chr(9), '  ')}")
        else:
            print("  (no summary.txt: still running or interrupted)")
    lockf = agelit.pipeline_lock_path()
    if lockf.exists():
        held = agelit.acquire_pipeline_lock("status probe")
        if held is None:
            print(f"update running now: {lockf.read_text(encoding='utf-8').strip()}")
        else:
            held.close()
    return 0


def cmd_sources() -> int:
    from sourcelens.query import refs
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
    print("  groups: fulltext = pdf,md,txt,xml; files = pdf,md,txt,xml,attachments; repos/package = repo; "
          "websites = website")
    print("  attachments: figures and supplementary files; --ext picks extensions (xlsx,csv,pptx,...)")
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
    sys.argv = [f"sourcelens {args[0]}", *args[1:]]
    rc = 0
    try:
        mod.main()
    except SystemExit as exc:
        rc = exc.code if isinstance(exc.code, int) else (0 if exc.code is None else 1)
    # a server that asked for a long wait: the step is incomplete, the next run tries again
    notes = agelit.rate_limit_notes()
    for note in notes:
        agelit.log(note)
    return rc or (1 if notes else 0)


COMMANDS = ["update", "download", "retry", "query", "files", "export", "report", "status", "list", "config",
            "sources", "test", "version", "help"]


def has_help(args: list[str]) -> bool:
    return any(a in ("-h", "--help", "-help") for a in args)


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    setup_console()
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
    if cmd == "download":
        return cmd_download(args)
    if cmd == "report":
        return cmd_report(args)
    if cmd == "files":
        from sourcelens.query import files
        if has_help(args):
            print(files.HELP, end="")
            return 0
        return files.run(args)
    if cmd in ("query", "export"):
        from sourcelens.query import export_refs, search_catalog
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
        from sourcelens.pullliturature.fetch_fulltext import pdf_tools
        return cmd_config(args, pdf_tools())
    if cmd == "sources":
        return cmd_sources()
    if cmd == "test":
        from sourcelens.buildcatalog.test_incremental import run_test
        return run_test()
    if cmd in ("version", "--version", "-v"):
        print(f"sourcelens {__version__} ({IMPL})")
        return 0
    # anything else that is not an option is a topic: sourcelens "CRISPR base editing" [options]
    if not cmd.startswith("-"):
        words, rest = [], []
        for i, a in enumerate(argv):
            if a.startswith("-"):
                rest = argv[i:]
                break
            words.append(a)
        return cmd_update(["--topic", " ".join(words), *rest])
    print(f'sourcelens: unknown option "{cmd}"\n', file=sys.stderr)
    print(MAIN_HELP.format(version=__version__), end="")
    return 2


def entry() -> None:
    """Console script entry point."""
    try:
        code = main()
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        code = 130
    sys.exit(code)
